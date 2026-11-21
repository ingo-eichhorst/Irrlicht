import { describe, test, expect, beforeAll } from 'vitest'
import { compoundSessionId } from './sessionIdentity.js'

// Issue #2149, phase 1: the web keeps relay sessions in the same
// `dashboardGroups` tree it replaces with the local daemon's REST groups.
// Before the fix, both replacing paths dropped every relay row:
//   · the initial load, when the first /api/v1/sessions response landed after
//     relay frames had already arrived (rebuildSources() runs beside that
//     fetch, so the relay replay can win the race);
//   · every rehydrate poll, because a relay row made structureSignature of
//     the current tree differ from the payload's, so the poll took its
//     "adopt" branch with an unchanged payload.
// A dropped row came back on its daemon's next push through insertNewSession,
// which notifies on first sight — so a `waiting` relay session notified again
// after each wipe.
//
// Red-first on 8d6279df7 (the parent branch): every test below fails there
// except the one marked LOCK and the one marked PINNED (which pins a cost of
// this fix rather than a defect, and so passes there by construction).
//
// Own test file: the payload is consumed at module load, and vitest's
// per-file isolation gives this file a fresh module instance.

const DAEMON = 'box-d1'
const REMOTE_ID = compoundSessionId(DAEMON, 'proc-7')

const localRow = { session_id: 'loc-1', state: 'working', project_name: 'irrlicht', adapter: 'claude-code', metrics: {} }

// No orchestrator: the defect does not need one.
const payload = (extraGroups = []) => ({
  groups: [{ name: 'irrlicht', agents: [localRow] }, ...extraGroups],
  provider_costs: {},
})

let notifications = 0
let releaseFirstSessionsFetch
let currentPayload = payload()

// A fresh copy per response, as a real fetch parses one: the dashboard adopts
// the payload's arrays as its own tree and later inserts into them, so a
// shared object would make the next "unchanged" payload carry those inserts.
const json = (body) => Promise.resolve({ ok: true, json: () => Promise.resolve(structuredClone(body)) })

const relayPush = (state) => ({
  type: 'push',
  source: DAEMON,
  msg: { type: 'session_updated', session: { session_id: 'proc-7', state, project_name: 'container-proj', adapter: 'claude-code', first_seen: 100, metrics: {} } },
})

const rowFor = (id) => [...document.querySelectorAll('#session-list .session-row')].find((el) => el.dataset.sessionId === id) || null
const groupNames = () => [...document.querySelectorAll('#session-list .group-hdr .group-name')].map((el) => el.textContent)
const settle = async () => { for (let i = 0; i < 4; i++) await new Promise((r) => setTimeout(r, 0)) }

let irr
let ws

beforeAll(async () => {
  localStorage.setItem('irrlicht_settings', JSON.stringify({
    // Relay only, so `lastMockWebSocket` is the relay socket these tests
    // drive. The initial load and rehydratePoll still read same-origin REST,
    // which the stubbed fetch serves as the local daemon's payload.
    enableLocalSource: false,
    enableRelaySource: true,
    relayUrl: 'https://relay.example',
    notifyOnWaiting: true,
  }))
  // tryNotify's other three gates: permission, a hidden tab, a constructor.
  global.Notification = class {
    static permission = 'granted'
    static requestPermission() { return Promise.resolve('granted') }
    constructor() { notifications++ }
  }
  Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => 'hidden' })

  let first = true
  global.fetch = (url) => {
    const u = String(url)
    if (u.includes('/api/v1/sessions')) {
      if (first) {
        // Hold the first response until the relay frames are in.
        first = false
        return new Promise((resolve) => { releaseFirstSessionsFetch = () => resolve(json(currentPayload)) })
      }
      return json(currentPayload)
    }
    if (u.includes('/api/v1/agents')) return json([])
    return Promise.resolve({ ok: false, json: () => Promise.resolve(null) })
  }

  irr = await import('./irrlicht.js')
  await settle()
  ws = global.lastMockWebSocket
  ws.simulateOpen()
  ws.simulateMessage({ type: 'snapshot', daemons: [{ daemon_id: DAEMON, daemon_label: 'box-1', status: 'connected' }] })
  ws.simulateMessage(relayPush('waiting'))
  await settle()
})

describe('relay rows survive a replaced local tree (#2149 phase 1)', () => {
  test('LOCK: the relay push renders and notifies once', () => {
    expect(rowFor(REMOTE_ID), 'the relay frame produced no row').not.toBeNull()
    expect(notifications).toBe(1)
  })

  test('the first sessions response landing after the relay frames keeps the relay row', async () => {
    releaseFirstSessionsFetch()
    await settle()
    expect(rowFor('loc-1'), 'precondition: the local payload rendered').not.toBeNull()
    expect(rowFor(REMOTE_ID), 'the initial load dropped the relay row').not.toBeNull()
  })

  test('a rehydrate poll with an unchanged payload keeps the relay row', async () => {
    // Re-pushed first, so this test sees the poll on its own even where the
    // initial load already dropped the row (on the parent, that re-insert
    // notifies; the next test counts it).
    ws.simulateMessage(relayPush('waiting'))
    expect(rowFor(REMOTE_ID), 'precondition: the row is there before the poll').not.toBeNull()
    await irr.rehydratePoll()
    expect(rowFor(REMOTE_ID), 'the rehydrate poll dropped the relay row').not.toBeNull()
    expect(groupNames()).toEqual(['irrlicht', 'container-proj'])
  })

  test('the next push of the same waiting session does not notify again', () => {
    ws.simulateMessage(relayPush('waiting'))
    expect(rowFor(REMOTE_ID)).not.toBeNull()
    expect(notifications, 'a relay row re-inserted after a wipe notified a second time').toBe(1)
  })

  test('an unchanged poll leaves live local rows as the websocket left them', async () => {
    // What adopting on every poll cost besides the relay rows: each local row
    // was replaced by its REST copy, so a live websocket update reverted on
    // the next poll. dispatchRawFrame stands in for the local socket.
    irr.dispatchRawFrame({ type: 'session_updated', session: { ...localRow, git_branch: 'live-branch' } })
    expect(rowFor(REMOTE_ID), 'precondition: a relay row is in the tree').not.toBeNull()
    await irr.rehydratePoll()
    expect(rowFor('loc-1').querySelector('.row-branch').textContent, 'an unchanged poll replaced the live local row').toBe('live-branch')
  })

  test('a real structure change adopts the new tree and carries the relay row across', async () => {
    currentPayload = payload([{ name: 'second', agents: [{ session_id: 'loc-2', state: 'ready', project_name: 'second', metrics: {} }] }])
    await irr.rehydratePoll()
    expect(rowFor('loc-2'), 'the new local group was not adopted').not.toBeNull()
    expect(rowFor(REMOTE_ID), 'adopting a changed tree dropped the relay row').not.toBeNull()
    expect(groupNames()).toEqual(['irrlicht', 'second', 'container-proj'])
    ws.simulateMessage(relayPush('waiting'))
    expect(notifications).toBe(1)
  })

  // #2148's stopgap left a websocket-inserted session whose project name
  // equals an orchestrator group's name inside that group until the next
  // rehydrate. A relay row is now carried rather than wiped, so it stays where
  // insertNewSession put it — the same-named group — across polls. Keying
  // top-level groups by type plus name is #2148's named follow-up, not this.
  test('a relay row named like an orchestrator group lands in it and stays there', async () => {
    // The daemon omits an orchestrator group's empty `agents` (omitempty).
    currentPayload = payload([{ name: 'pool', type: 'workerlist', groups: [{ name: 'team', unattached_workers: [{ worker_name: 'slot-1', worker_state: 'idle' }] }] }])
    await irr.rehydratePoll()
    ws.simulateMessage({ type: 'push', source: DAEMON, msg: { type: 'session_updated', session: { session_id: 'proc-8', state: 'working', project_name: 'pool', first_seen: 100, metrics: {} } } })
    const id = compoundSessionId(DAEMON, 'proc-8')
    expect(rowFor(id), 'a relay push into a group adopted without `agents` was lost').not.toBeNull()
    await irr.rehydratePoll()
    expect(rowFor(id)).not.toBeNull()
    expect(groupNames()).toEqual(['irrlicht', 'pool', 'team', 'container-proj'])
  })

  // Pins a cost of dropping a closed socket's rows rather than proving a
  // defect: the reconnect replay re-inserts them through insertNewSession,
  // which notifies on first sight, so a session still `waiting` notifies
  // again after a reconnect. (macOS does not: its relay path notifies only on
  // a state change against the row it held.)
  test('PINNED: a closed relay socket drops its rows, and the replay notifies a waiting one again', async () => {
    const before = notifications
    ws.close()
    expect(rowFor(REMOTE_ID), 'a closed relay socket left its row behind').toBeNull()
    expect(rowFor('loc-1'), 'a local row went with the relay socket').not.toBeNull()

    const closed = ws
    const deadline = Date.now() + 3000
    while (global.lastMockWebSocket === closed && Date.now() < deadline) await new Promise((r) => setTimeout(r, 50))
    expect(global.lastMockWebSocket, 'no reconnect within 3 s').not.toBe(closed)
    ws = global.lastMockWebSocket
    ws.simulateOpen()
    ws.simulateMessage({ type: 'snapshot', daemons: [{ daemon_id: DAEMON, daemon_label: 'box-1', status: 'connected' }] })
    ws.simulateMessage(relayPush('waiting'))
    expect(rowFor(REMOTE_ID)).not.toBeNull()
    expect(notifications).toBe(before + 1)
  })
})
