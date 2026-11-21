import { describe, test, expect, beforeAll } from 'vitest'
import { compoundSessionId } from './sessionIdentity.js'
import { isGroupCollapsed, toggleGroupCollapsed } from './collapsedGroups.js'

// Issue #2149, phase 2: a session that runs in a container and reaches the
// dashboard through the relay is shown in its worker's row. The join key is
// the forwarding daemon's label (the container hostname), which equals the
// worker's name: exactly one worker in the local tree has that name and it
// has no session, the daemon is connected, and the session is a relay root
// that no local copy shadows.
//
// Red-first on 8d6279df7 (the parent branch): every test except the one
// marked LOCK fails there.
//
// Own test file: the payload is consumed at module load, and vitest's
// per-file isolation gives this file a fresh module instance.

const slot = (name, extra = {}) => ({
  role: 'worker', icon: '🔧', description: 'Pool worker slot', worker_name: name, worker_state: 'idle', ...extra,
})

const payload = {
  groups: [
    { name: 'irrlicht', agents: [{ session_id: 'loc-1', state: 'working', project_name: 'irrlicht', metrics: {} }] },
    {
      name: 'Build pool', type: 'workerlist',
      groups: [{
        name: 'team-a', status: 'operational',
        unattached_workers: [slot('team-a-m1-1', { worker_id: 'proj-a1b2c3', worker_state: 'busy' }), slot('team-a-m1-2')],
      }],
    },
  ],
  provider_costs: {},
}

const D1 = 'daemon-uuid-1'
const D2 = 'daemon-uuid-2'
const D3 = 'daemon-uuid-3'
const JOINED = compoundSessionId(D1, 'proc-1')

const push = (source, id, extra = {}) => ({
  type: 'push', source,
  msg: { type: 'session_updated', session: { session_id: id, state: 'working', project_name: 'container-proj', adapter: 'claude-code', first_seen: 100, metrics: { model_name: 'claude-opus' }, ...extra } },
})

// A fresh copy per response, as a real fetch parses one.
const json = (body) => Promise.resolve({ ok: true, json: () => Promise.resolve(structuredClone(body)) })

const rows = () => [...document.querySelectorAll('#session-list > *')]
const rowFor = (id) => rows().find((el) => el.classList.contains('session-row') && el.dataset.sessionId === id) || null
const workerNames = () => rows().filter((el) => el.classList.contains('worker-row')).map((el) => el.querySelector('.row-worker-name').textContent)
const headerNames = () => rows().filter((el) => el.classList.contains('group-hdr')).map((el) => el.querySelector('.group-name').textContent)
const header = (name) => rows().find((el) => el.classList.contains('group-hdr') && el.querySelector('.group-name').textContent === name)
// The group a row renders under: the nearest header above it.
const headerAbove = (el) => {
  let p = el.previousElementSibling
  while (p && !p.classList.contains('group-hdr')) p = p.previousElementSibling
  return p?.querySelector('.group-name').textContent
}
const settle = async () => { for (let i = 0; i < 4; i++) await new Promise((r) => setTimeout(r, 0)) }

let irr
let ws

beforeAll(async () => {
  localStorage.setItem('irrlicht_settings', JSON.stringify({
    // Relay only, so `lastMockWebSocket` is the relay socket; the stubbed
    // same-origin fetch still serves the local daemon's payload.
    enableLocalSource: false,
    enableRelaySource: true,
    relayUrl: 'https://relay.example',
  }))
  global.fetch = (url) => {
    const u = String(url)
    if (u.includes('/api/v1/sessions')) return json(payload)
    if (u.includes('/api/v1/agents')) return json([])
    return Promise.resolve({ ok: false, json: () => Promise.resolve(null) })
  }
  // vitest.setup.js's body has no Settings form; the one toggle the last
  // test flips is added before the import, which is when its listener binds.
  document.body.insertAdjacentHTML('beforeend', '<input type="checkbox" data-setting="enableRelaySource">')
  irr = await import('./irrlicht.js')
  await settle()
  ws = global.lastMockWebSocket
  ws.simulateOpen()
  ws.simulateMessage({ type: 'snapshot', daemons: [
    { daemon_id: D1, daemon_label: 'team-a-m1-1', status: 'connected' },
    { daemon_id: D2, daemon_label: 'nobody-here', status: 'connected' },
  ] })
  ws.simulateMessage(push(D1, 'proc-1', { metrics: { model_name: 'claude-opus', estimated_cost_usd: 1.25 } }))
  ws.simulateMessage(push(D2, 'proc-2'))
  await settle()
})

describe('a relayed session joins the worker row its daemon is named after (#2149)', () => {
  test('it renders under the worker\'s rig, as that worker', () => {
    const row = rowFor(JOINED)
    expect(row, 'the relay frame produced no row').not.toBeNull()
    expect(headerAbove(row)).toBe('team-a')
    expect(row.querySelector('.row-worker-name').textContent).toBe('team-a-m1-1')
    expect(row.querySelector('.row-worker-id').title).toBe('proj-a1b2c3')
    expect(row.querySelector('.row-role-badge').textContent).toBe('🔧 worker')
    // Everything else is the relayed session's own: state, origin glyph.
    expect(row.dataset.state).toBe('working')
    expect(row.querySelector('.row-origin').style.display).not.toBe('none')
  })

  test('the worker leaves the free-worker rows and the session leaves its project group', () => {
    expect(workerNames()).toEqual(['team-a-m1-2'])
    // container-proj still has proc-2 (no worker named "nobody-here").
    expect(headerAbove(rowFor(compoundSessionId(D2, 'proc-2')))).toBe('container-proj')
    expect(header('team-a').querySelector('.group-count').textContent).toBe('1 session')
    expect(header('container-proj').querySelector('.group-count').textContent).toBe('1 session')
  })

  test('a project group the join empties renders no header', () => {
    ws.simulateMessage({ type: 'push', source: D2, msg: { type: 'session_deleted', session: { session_id: 'proc-2' } } })
    expect(headerNames()).toEqual(['irrlicht', 'Build pool', 'team-a'])
  })

  test('LOCK: the stored session is not decorated', () => {
    // A derived view: the join never writes into the stored session, so
    // leaving the join leaves no worker name behind (the merge in
    // updateExistingSession keeps a worker name once set).
    const flat = irr.collectFlatSessions().find((a) => a.session_id === JOINED)
    expect(flat.worker_name).toBeUndefined()
    expect(flat.role).toBeUndefined()
  })

  test('it stays joined across an unchanged rehydrate and a metrics push', async () => {
    await irr.rehydratePoll()
    ws.simulateMessage(push(D1, 'proc-1', { state: 'waiting' }))
    const row = rowFor(JOINED)
    expect(headerAbove(row)).toBe('team-a')
    expect(row.dataset.state).toBe('waiting')
    expect(workerNames()).toEqual(['team-a-m1-2'])
  })

  // Guards the label rule: the snapshot and daemon_status arms store a
  // daemon's id as its label when it sent no daemon_label, and
  // connectedDaemonLabel must not hand that id to the join. Letting it through
  // turns this test red (mutation J1 in the PR).
  test('a daemon that sent no label is not taken to be named after its id', () => {
    ws.simulateMessage({ type: 'daemon_status', daemon_id: 'team-a-m1-2', status: 'connected' })
    ws.simulateMessage(push('team-a-m1-2', 'proc-9'))
    expect(headerAbove(rowFor(compoundSessionId('team-a-m1-2', 'proc-9')))).toBe('container-proj')
    expect(workerNames()).toEqual(['team-a-m1-2'])
    ws.simulateMessage({ type: 'push', source: 'team-a-m1-2', msg: { type: 'session_deleted', session: { session_id: 'proc-9' } } })
  })

  test('a daemon label that arrives after the session joins it with no further push', () => {
    ws.simulateMessage(push(D3, 'proc-3'))
    expect(headerAbove(rowFor(compoundSessionId(D3, 'proc-3')))).toBe('container-proj')
    ws.simulateMessage({ type: 'daemon_status', daemon_id: D3, daemon_label: 'team-a-m1-2', status: 'connected' })
    expect(headerAbove(rowFor(compoundSessionId(D3, 'proc-3')))).toBe('team-a')
    expect(workerNames()).toEqual([])
  })

  test('a snapshot re-names the connected daemons and the join follows with no push', () => {
    const p3 = compoundSessionId(D3, 'proc-3')
    ws.simulateMessage({ type: 'snapshot', daemons: [{ daemon_id: D1, daemon_label: 'team-a-m1-1', status: 'connected' }] })
    expect(headerAbove(rowFor(p3)), 'a snapshot that no longer names D3 left its session joined').toBe('container-proj')
    expect(workerNames()).toEqual(['team-a-m1-2'])
    ws.simulateMessage({ type: 'snapshot', daemons: [
      { daemon_id: D1, daemon_label: 'team-a-m1-1', status: 'connected' },
      { daemon_id: D3, daemon_label: 'team-a-m1-2', status: 'connected' },
    ] })
    expect(headerAbove(rowFor(p3))).toBe('team-a')
    expect(workerNames()).toEqual([])
  })

  test('a notification tap on a joined session in a collapsed rig expands the rig', () => {
    const key = 'Build pool/team-a'
    if (!isGroupCollapsed(key)) toggleGroupCollapsed(key)
    try {
      irr.dispatchRawFrame({ type: 'session_updated', session: { session_id: 'loc-1', state: 'working', project_name: 'irrlicht', metrics: {} } })
      expect(rowFor(JOINED), 'precondition: the collapsed rig hides the row').toBeNull()
      irr.focusSessionFromNotification('proc-1')
      expect(isGroupCollapsed(key), 'the tap expanded some other group').toBe(false)
      expect(rowFor(JOINED)?.classList.contains('elfdans-focus')).toBe(true)
    } finally {
      // Never leave the rig collapsed for the tests below.
      if (isGroupCollapsed(key)) toggleGroupCollapsed(key)
    }
  })

  test('the daemon disconnecting gives the worker its row back', () => {
    ws.simulateMessage({ type: 'daemon_status', daemon_id: D1, status: 'disconnected' })
    // The web drops a disconnected daemon's rows (#540).
    expect(rowFor(JOINED)).toBeNull()
    expect(workerNames()).toEqual(['team-a-m1-1'])
  })

  test('the relay socket closing drops its rows and gives the workers back', () => {
    // A closed link leaves the remote set unknown, so the rows the socket
    // delivered go (as macOS clears relaySessionMap on link-down); the
    // relay's reconnect replay re-adds the survivors.
    ws.close()
    expect(rowFor(compoundSessionId(D3, 'proc-3')), 'a closed relay socket left its row behind').toBeNull()
    expect(headerNames()).toEqual(['irrlicht', 'Build pool', 'team-a'])
    expect(workerNames()).toEqual(['team-a-m1-1', 'team-a-m1-2'])
  })

  test('turning the relay source off drops its rows and gives the workers back', async () => {
    // The closed socket reconnects after its 1 s backoff; wait for the new one
    // rather than faking timers (that deadlocks on irrlicht.js's own loops).
    const closed = ws
    const deadline = Date.now() + 3000
    while (global.lastMockWebSocket === closed && Date.now() < deadline) await new Promise((r) => setTimeout(r, 50))
    expect(global.lastMockWebSocket, `no reconnect within ${Date.now() - deadline + 3000} ms`).not.toBe(closed)
    ws = global.lastMockWebSocket
    ws.simulateOpen()
    // The relay's connect sequence: the snapshot, then the replay.
    ws.simulateMessage({ type: 'snapshot', daemons: [{ daemon_id: D3, daemon_label: 'team-a-m1-2', status: 'connected' }] })
    ws.simulateMessage(push(D3, 'proc-3'))
    const p3 = compoundSessionId(D3, 'proc-3')
    expect(headerAbove(rowFor(p3)), 'precondition: joined again').toBe('team-a')

    const toggle = document.querySelector('[data-setting="enableRelaySource"]')
    toggle.checked = false
    toggle.dispatchEvent(new Event('change'))
    await settle()
    expect(rowFor(p3), 'a relay source turned off left its row behind').toBeNull()
    expect(workerNames()).toEqual(['team-a-m1-1', 'team-a-m1-2'])
  })
})
