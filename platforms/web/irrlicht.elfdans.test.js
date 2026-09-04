import { describe, test, expect, beforeAll, vi } from 'vitest'
import { compoundSessionId } from './sessionIdentity.js'
import { toggleGroupCollapsed } from './collapsedGroups.js'

// Where a notification tap RESOLVES (docs/mobile-notifications-arc42.md R6,
// §8.5). This is the obstacle that made the deep link a slice rather than a
// line: a push payload names the relay's BARE session id (Payload in
// core/domain/notify/notify.go carries no daemon id), while a relay-sourced
// row is keyed by the compound `<daemon>\0<id>` that normalizeSourcedFrame
// folds in (#537) — and that compound id is what reaches `data-session-id`.
//
// The dashboard is driven the way index.html does: settings seeded in
// localStorage BEFORE the import, then real frames through the mock socket, so
// the ids under test are produced by the production folding path rather than
// spelled out here.

const mocks = vi.hoisted(() => ({
  initElfdans: vi.fn(async () => {}),
  publishLedgerSnapshot: vi.fn(async () => true),
  ledgerEntry: vi.fn(async () => null),
}))

// elfdans.js's own wiring is exercised in elfdans.test.js and
// elfdans.deeplink.test.js; here it is replaced so the two seams the dashboard
// uses are observable and initElfdans's feature-detection fetch stays out of
// the way.
vi.mock('./elfdans.js', async (importOriginal) => ({ ...(await importOriginal()), ...mocks }))

const DAEMON = 'daemon-a'
const BARE = 'proc-1'
const COMPOUND = compoundSessionId(DAEMON, BARE)

let irrlicht = null
let ws = null

function sessionFrame(session) {
  return { type: 'push', source: DAEMON, msg: { type: 'session_update', session } }
}

function rowFor(sessionId) {
  for (const el of document.querySelectorAll('#session-list .session-row')) {
    if (el.dataset.sessionId === sessionId) return el
  }
  return null
}

const settle = async () => { for (let i = 0; i < 4; i++) await new Promise((r) => setTimeout(r, 0)) }

beforeAll(async () => {
  localStorage.setItem('irrlicht_settings', JSON.stringify({
    // Relay only: the phone's shape, and it keeps `lastMockWebSocket` pointing
    // at the socket these tests drive.
    enableLocalSource: false,
    enableRelaySource: true,
    relayUrl: 'https://relay.example',
    relayToken: 'dev-tok',
  }))
  global.fetch = (url) => {
    const u = String(url)
    if (u.includes('/api/v1/sessions')) return Promise.resolve({ ok: true, json: () => Promise.resolve({ groups: [] }) })
    if (u.includes('/api/v1/agents')) return Promise.resolve({ ok: true, json: () => Promise.resolve([]) })
    return Promise.resolve({ ok: false, json: () => Promise.resolve(null) })
  }
  irrlicht = await import('./irrlicht.js')
  await settle()
  ws = global.lastMockWebSocket
  ws.simulateOpen()
  ws.simulateMessage(sessionFrame({
    session_id: BARE, state: 'waiting', project_name: 'irrlicht', adapter: 'claude-code',
  }))
  await settle()
})

describe('a bare relay session id resolves to the row the dashboard actually keys (R6)', () => {
  test('the row under test is keyed by the compound id — the bug this slice fixes', () => {
    // The pin on the failure, stated as a fact about the DOM rather than as a
    // claim about the resolver: a deep link that queried the payload's own id
    // would select nothing at all, silently, for exactly the sessions
    // notifications are about.
    expect(COMPOUND).not.toBe(BARE)
    expect(rowFor(COMPOUND), 'the relay frame produced no row').toBeTruthy()
    expect(rowFor(BARE), 'a bare-id lookup found a row — this test can no longer see the bug').toBeNull()
    expect(document.querySelector(`#session-list .session-row[data-session-id="${BARE}"]`)).toBeNull()
  })

  test('the tap selects that row, visibly', () => {
    irrlicht.focusSessionFromNotification(BARE)
    const row = rowFor(COMPOUND)
    expect(row.classList.contains('elfdans-focus')).toBe(true)
    expect(document.getElementById('elfdans-notice'), 'a resolved tap has nothing to report').toBeNull()
  })

  test('the selection survives the next render, which rewrites every className', () => {
    // reconcile repaints a row's class list on every update, so the selection
    // is re-applied from state rather than set once on the element.
    ws.simulateMessage(sessionFrame({ session_id: BARE, state: 'ready', project_name: 'irrlicht' }))
    expect(rowFor(COMPOUND).classList.contains('elfdans-focus')).toBe(true)
  })
})

describe('a tap whose session is not in the list says so (R6)', () => {
  test('with nothing in the ledger, it names the session and the reason', async () => {
    mocks.ledgerEntry.mockResolvedValueOnce(null)
    irrlicht.focusSessionFromNotification('proc-gone')
    await settle()
    const notice = document.getElementById('elfdans-notice')
    expect(notice, 'a tap that resolved to nothing left no trace at all').toBeTruthy()
    expect(notice.textContent).toMatch(/not in the list/i)
    expect(mocks.ledgerEntry).toHaveBeenCalledWith('proc-gone')
    // And the previous selection is dropped — leaving it would point at the
    // wrong session while the notice talks about another.
    expect(rowFor(COMPOUND).classList.contains('elfdans-focus')).toBe(false)
  })

  test('the notice is dismissible, and the next resolved tap clears it', async () => {
    document.getElementById('elfdans-notice').click()
    expect(document.getElementById('elfdans-notice')).toBeNull()

    irrlicht.focusSessionFromNotification('proc-gone-2')
    await settle()
    expect(document.getElementById('elfdans-notice')).toBeTruthy()
    irrlicht.focusSessionFromNotification(BARE)
    expect(document.getElementById('elfdans-notice')).toBeNull()
  })

  test('with a ledger row, the last-known state IS the answer — stated "as of", never as now', async () => {
    mocks.ledgerEntry.mockResolvedValueOnce({
      state: 'waiting', label: 'codex', project: 'webapp', at: 1755000000,
    })
    irrlicht.focusSessionFromNotification('proc-elsewhere')
    await settle()
    const text = document.getElementById('elfdans-notice').textContent
    expect(text).toContain('codex')
    expect(text).toContain('webapp')
    expect(text).toMatch(/last known waiting/)
    expect(text).toMatch(/as of \d{1,2}:\d{2}/)
  })
})

describe('missingSessionText (arc42 §8.5)', () => {
  test('a ledger entry is reported as of its own time', () => {
    const t = irrlicht.missingSessionText('proc-9', {
      state: 'waiting', label: 'claude-code', project: 'irrlicht', at: 1755000000,
    })
    expect(t).toContain('claude-code · irrlicht')
    expect(t).toMatch(/last known waiting as of \d{1,2}:\d{2}/)
    expect(t).toMatch(/not in the list/i)
  })

  test('an entry with an unusable timestamp says so rather than printing 1970', () => {
    const t = irrlicht.missingSessionText('proc-9', { state: 'ready', at: 0 })
    expect(t).toContain('at an unknown time')
    expect(t).not.toContain('1970')
  })

  test('no entry at all is a different sentence, not a blank one', () => {
    const t = irrlicht.missingSessionText('proc-abcdefghij', null)
    expect(t).toContain('proc-abc')
    expect(t).toMatch(/kept no last-known state/i)
  })
})

describe('a session the dashboard holds but is not painting', () => {
  test('a collapsed group is expanded rather than reported missing', () => {
    // Reporting "not in the list" about a row a chevron would reveal is a lie,
    // and the honest move is the one the user would make.
    ws.simulateMessage(sessionFrame({
      session_id: 'proc-2', state: 'working', project_name: 'webapp', adapter: 'codex',
    }))
    expect(document.querySelectorAll('#session-list .group-hdr').length).toBeGreaterThan(1)

    toggleGroupCollapsed('irrlicht')
    ws.simulateMessage(sessionFrame({ session_id: 'proc-2', state: 'ready', project_name: 'webapp' }))
    expect(rowFor(COMPOUND), 'the collapse did not hide the row, so this test proves nothing').toBeNull()

    irrlicht.focusSessionFromNotification(BARE)
    expect(rowFor(COMPOUND)).toBeTruthy()
    expect(rowFor(COMPOUND).classList.contains('elfdans-focus')).toBe(true)
    expect(document.getElementById('elfdans-notice')).toBeNull()
  })
})

describe('a bare id two daemons both use', () => {
  test('is reported as ambiguous rather than silently picking one', async () => {
    // The ambiguity the compound key exists to keep apart, and the one a
    // notification cannot resolve: a push payload names no daemon (Payload in
    // core/domain/notify/notify.go), and `proc-<pid>` collides readily.
    ws.simulateMessage({
      type: 'push',
      source: 'daemon-b',
      msg: { type: 'session_update', session: { session_id: BARE, state: 'waiting', project_name: 'irrlicht' } },
    })
    expect(rowFor(compoundSessionId('daemon-b', BARE)), 'the second daemon produced no row').toBeTruthy()

    irrlicht.focusSessionFromNotification(BARE)
    const notice = document.getElementById('elfdans-notice')
    expect(notice, 'two rows matched and the app said nothing about it').toBeTruthy()
    expect(notice.textContent).toMatch(/share that id/i)
    // One of them IS selected — a tap that reports ambiguity and then shows
    // nothing is the silent failure wearing a longer sentence.
    expect(document.querySelectorAll('#session-list .session-row.elfdans-focus')).toHaveLength(1)
  })
})
