import { describe, test, expect, beforeAll, vi } from 'vitest'

// The two socket EDGES the live fold hangs off (arc42 §8.5, ADR-9). Its
// sibling irrlicht.elfdansfold.test.js grades what a publish carries; this file
// grades whether one happens at all, which is a different question and was the
// one that failed: the worker can lower the badge, but only if the page tells
// it to.
//
// Both cases below reach the badge through code the fold's own tests never
// execute, which is the same shape as the observer's production-path gap
// (§8.7): a component proven in isolation, wired by something nobody drove.
//
// The sessions fetch here NEVER resolves. That is the point — it is what
// removes render() from the picture, leaving the socket edge as the only thing
// that could publish.

const mocks = vi.hoisted(() => ({
  initElfdans: vi.fn(async () => {}),
  publishLedgerSnapshot: vi.fn(async () => true),
  ledgerEntry: vi.fn(async () => null),
}))
vi.mock('./elfdans.js', async (importOriginal) => ({ ...(await importOriginal()), ...mocks }))

const DAEMON = 'daemon-a'
const DEBOUNCE_MS = 700 // the 500ms publish debounce plus slack

let ws = null
const settle = async () => { for (let i = 0; i < 4; i++) await new Promise((r) => setTimeout(r, 0)) }
const afterDebounce = () => new Promise((r) => setTimeout(r, DEBOUNCE_MS))

function sessionFrame(session) {
  return { type: 'push', source: DAEMON, msg: { type: 'session_update', session } }
}

beforeAll(async () => {
  localStorage.setItem('irrlicht_settings', JSON.stringify({
    enableLocalSource: false,
    enableRelaySource: true,
    relayUrl: 'https://relay.example',
    relayToken: 'dev-tok',
  }))
  global.fetch = (url) => {
    const u = String(url)
    // Never resolves: no initial render, ever. A relay that answers slowly (or
    // not at all) is an ordinary condition, and it must not be the difference
    // between a badge that clears and one that does not.
    if (u.includes('/api/v1/sessions')) return new Promise(() => {})
    if (u.includes('/api/v1/agents')) return Promise.resolve({ ok: true, json: () => Promise.resolve([]) })
    return Promise.resolve({ ok: false, json: () => Promise.resolve(null) })
  }
  await import('./irrlicht.js')
  await settle()
  ws = global.lastMockWebSocket
})

describe('the connect edge (arc42 §8.5)', () => {
  test('opening the socket publishes, with no render anywhere in the story', async () => {
    // The case the whole live-view decision exists for: a phone opened after
    // every session ended. There are no sessions, so no frame ever arrives to
    // trigger a render — and if the publish rode only on render(), the push
    // fold's last `waiting` row and its badge would stay up forever with
    // nothing able to lower them.
    expect(mocks.publishLedgerSnapshot).not.toHaveBeenCalled()
    ws.simulateOpen()
    await afterDebounce()
    expect(mocks.publishLedgerSnapshot).toHaveBeenCalled()
    expect(mocks.publishLedgerSnapshot.mock.calls[0][0]).toEqual([])
  })
})

describe('the disconnect edge (arc42 §8.5)', () => {
  test('a reconnect republishes an unchanged set — the worker did not stand still', async () => {
    // While the page is away, sw.js's push fold keeps writing the ledger. So
    // "the live set has not changed since I last published" says nothing about
    // what the ledger now holds, and suppressing the republish leaves a
    // `waiting` row the push fold added with nothing left to correct it.
    ws.simulateMessage(sessionFrame({
      session_id: 'proc-1', state: 'ready', project_name: 'irrlicht', adapter: 'claude-code',
    }))
    await afterDebounce()
    mocks.publishLedgerSnapshot.mockClear()

    // Same set, republished only because the socket went away and came back.
    ws.close()
    await settle()
    ws.simulateOpen()
    await afterDebounce()

    expect(mocks.publishLedgerSnapshot).toHaveBeenCalled()
    expect(mocks.publishLedgerSnapshot.mock.calls.at(-1)[0].map((r) => r.session_id)).toEqual(['proc-1'])
  })
})

describe('a publish that did not happen is not remembered as one', () => {
  test('a failed publish leaves the next identical set publishable', async () => {
    // `elfdansPublishedSignature = published ? signature : null` — recording the
    // signature unconditionally would mark an unpaired-or-failed attempt as
    // delivered, and the retry that a pairing a minute later should produce
    // would be suppressed as "unchanged".
    // The dashboard's own reconnect timer fires during the test above and
    // builds a fresh socket, so the one this file opened is no longer the
    // source's. Take the current one and open it, or the publish is skipped
    // for want of a connected source rather than for the reason under test.
    ws = global.lastMockWebSocket
    ws.simulateOpen()
    await settle()

    mocks.publishLedgerSnapshot.mockClear()
    mocks.publishLedgerSnapshot.mockResolvedValueOnce(false)
    ws.simulateMessage(sessionFrame({
      session_id: 'proc-2', state: 'waiting', project_name: 'irrlicht', adapter: 'codex',
    }))
    await afterDebounce()
    expect(mocks.publishLedgerSnapshot).toHaveBeenCalledTimes(1)

    // The signature is `id:state`, so a frame that changes only the project
    // re-renders without changing it — which is precisely the suppression
    // case. Only the previous attempt's failure makes this second publish
    // correct.
    mocks.publishLedgerSnapshot.mockClear()
    ws = global.lastMockWebSocket
    ws.simulateOpen()
    await settle()
    ws.simulateMessage(sessionFrame({
      session_id: 'proc-2', state: 'waiting', project_name: 'rewrite-spring', adapter: 'codex',
    }))
    await afterDebounce()
    expect(mocks.publishLedgerSnapshot).toHaveBeenCalled()
  })
})
