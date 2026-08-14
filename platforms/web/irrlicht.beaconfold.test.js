import { describe, test, expect, beforeAll, vi } from 'vitest'

// The live fold the dashboard publishes into the phone's ledger
// (docs/mobile-notifications-arc42.md §8.5, ADR-9) — the half that lets the
// badge FALL. §8.4 never pushes on `* → working` and iOS forbids a silent
// push, so a backgrounded phone only ever learns that sessions NEED attention.
// What the dashboard can see while open is the correcting signal.
//
// A file of its own because the guard against republishing an unchanged set is
// module state, and the case that matters most is the FIRST publish: a phone
// opened after every session ended connects, renders zero rows, and that
// nothing is precisely the news. Ordering is reproduced rather than asserted
// about — the socket opens BEFORE the initial /api/v1/sessions fetch resolves,
// which is the ordering a relay on a warm connection actually produces.

const mocks = vi.hoisted(() => ({
  initBeacon: vi.fn(async () => {}),
  publishLedgerSnapshot: vi.fn(async () => true),
  ledgerEntry: vi.fn(async () => null),
}))
vi.mock('./beacon.js', async (importOriginal) => ({ ...(await importOriginal()), ...mocks }))

const DAEMON = 'daemon-a'
const BARE = 'proc-1'
const DEBOUNCE_MS = 700 // beacon publish debounce (500ms) plus slack

let ws = null

function sessionFrame(session) {
  return { type: 'push', source: DAEMON, msg: { type: 'session_update', session } }
}

function deleteFrame(sessionId) {
  return { type: 'push', source: DAEMON, msg: { type: 'session_deleted', session: { session_id: sessionId } } }
}

const settle = async () => { for (let i = 0; i < 4; i++) await new Promise((r) => setTimeout(r, 0)) }
const afterDebounce = () => new Promise((r) => setTimeout(r, DEBOUNCE_MS))

beforeAll(async () => {
  localStorage.setItem('irrlicht_settings', JSON.stringify({
    enableLocalSource: false,
    enableRelaySource: true,
    relayUrl: 'https://relay.example',
    relayToken: 'dev-tok',
  }))
  // One deferred sessions response, shared by every caller (the rehydrate
  // poll included), so the initial render can be held until after the socket
  // is up.
  let releaseSessions = null
  const sessions = new Promise((r) => {
    releaseSessions = () => r({ ok: true, json: () => Promise.resolve({ groups: [] }) })
  })
  global.fetch = (url) => {
    const u = String(url)
    if (u.includes('/api/v1/sessions')) return sessions
    if (u.includes('/api/v1/agents')) return Promise.resolve({ ok: true, json: () => Promise.resolve([]) })
    return Promise.resolve({ ok: false, json: () => Promise.resolve(null) })
  }
  await import('./irrlicht.js')
  await settle()
  ws = global.lastMockWebSocket
  ws.simulateOpen()
  releaseSessions()
  await settle()
})

describe('the first snapshot (arc42 §8.5)', () => {
  test('an empty one is published — it is what takes the last stale row out of the ledger', async () => {
    // Without this the ledger keeps whatever the last push wrote. The user
    // answered the agent at the Mac, the session ended, and the phone still
    // reads "1" on its home screen with nothing anywhere able to lower it.
    await afterDebounce()
    expect(mocks.publishLedgerSnapshot).toHaveBeenCalled()
    expect(mocks.publishLedgerSnapshot.mock.calls[0][0]).toEqual([])
  })
})

describe('what the fold carries (arc42 §8.5)', () => {
  test('sessions reach the ledger under the BARE id — the key space push writes', async () => {
    // A relay-sourced row is keyed by the compound `<daemon>\0<id>` (#537),
    // but a push payload names only the bare id. Both folds must share one key
    // space or the badge would count the same session twice, under two
    // spellings, and never come down.
    mocks.publishLedgerSnapshot.mockClear()
    ws.simulateMessage(sessionFrame({
      session_id: BARE, state: 'waiting', project_name: 'irrlicht', adapter: 'claude-code',
    }))
    await afterDebounce()
    expect(mocks.publishLedgerSnapshot).toHaveBeenCalled()
    const rows = mocks.publishLedgerSnapshot.mock.calls.at(-1)[0]
    expect(rows).toHaveLength(1)
    // Mirrors what the relay composes into a push payload (push_observer.go:
    // Label = adapter, Project = project name), so the two folds write the
    // same fields rather than two dialects.
    expect(rows[0]).toMatchObject({
      session_id: BARE, state: 'waiting', label: 'claude-code', project: 'irrlicht',
    })
    expect(rows[0].at).toBeGreaterThan(0)
    expect(rows[0].session_id).not.toContain('\0')
  })

  test('a subagent is not in it — §8.4 never notifies about one', async () => {
    // Counting a subagent in the badge would claim attention nobody was asked
    // for: the parent's own notification already covers it.
    mocks.publishLedgerSnapshot.mockClear()
    ws.simulateMessage(sessionFrame({
      session_id: 'proc-child', parent_session_id: BARE, state: 'waiting', project_name: 'irrlicht',
    }))
    await afterDebounce()
    const rows = mocks.publishLedgerSnapshot.mock.calls.at(-1)?.[0] ?? []
    expect(rows.map((r) => r.session_id)).not.toContain('proc-child')
  })

  test('an unchanged set is not republished', async () => {
    // A metrics tick is not news to the ledger, and a phone should not post
    // one message per row update.
    mocks.publishLedgerSnapshot.mockClear()
    ws.simulateMessage(sessionFrame({
      session_id: BARE, state: 'waiting', project_name: 'irrlicht', adapter: 'claude-code',
    }))
    await afterDebounce()
    expect(mocks.publishLedgerSnapshot).not.toHaveBeenCalled()
  })

  test('a state change is', async () => {
    mocks.publishLedgerSnapshot.mockClear()
    ws.simulateMessage(sessionFrame({
      session_id: BARE, state: 'working', project_name: 'irrlicht', adapter: 'claude-code',
    }))
    await afterDebounce()
    expect(mocks.publishLedgerSnapshot.mock.calls.at(-1)[0].find((r) => r.session_id === BARE).state)
      .toBe('working')
  })

  test('and so is the last session ending', async () => {
    mocks.publishLedgerSnapshot.mockClear()
    ws.simulateMessage(deleteFrame(BARE))
    ws.simulateMessage(deleteFrame('proc-child'))
    await afterDebounce()
    expect(mocks.publishLedgerSnapshot.mock.calls.at(-1)[0]).toEqual([])
  })
})

describe('a disconnected dashboard publishes nothing (arc42 §8.5)', () => {
  test('its list is not a smaller truth, it is no truth at all', async () => {
    // Publishing it would delete the ledger §8.5 keeps for exactly that
    // moment — the "as of 14:32" list a phone with no connection shows.
    mocks.publishLedgerSnapshot.mockClear()
    ws.close()
    ws.simulateMessage(sessionFrame({ session_id: 'proc-9', state: 'waiting', project_name: 'irrlicht' }))
    await afterDebounce()
    expect(mocks.publishLedgerSnapshot).not.toHaveBeenCalled()
  })
})
