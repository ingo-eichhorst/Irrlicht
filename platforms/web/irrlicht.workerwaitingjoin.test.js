// Issue #2145 review: a worker-list slot joined by a relayed session (#2149)
// shows that session's row, and the session notifies for itself. Its own
// edge into waiting must not raise a second "Worker waiting for input"; a
// slot nobody joined still does.
//
// Red-first: the joined-slot test failed while adoptLocalTree diffed the raw
// tree instead of the displayed one. Own file: the payload is consumed at
// module load, and vitest's per-file isolation gives a fresh module instance.
import { test, expect, beforeAll } from 'vitest'

const slot = (name, state) => ({ role: 'worker', icon: '🔧', description: 'Pool worker slot', worker_name: name, worker_state: state })

const payload = (m1, m2) => ({
  groups: [{
    name: 'VM tasks', type: 'workerlist',
    groups: [{ name: 'lead-T100', status: 'operational', unattached_workers: [slot('T101', m1), slot('T102', m2)] }],
  }],
  provider_costs: {},
})

const json = (body) => Promise.resolve({ ok: true, json: () => Promise.resolve(structuredClone(body)) })
const serve = (body) => {
  global.fetch = (url) => {
    const u = String(url)
    if (u.includes('/api/v1/sessions')) return json(body)
    if (u.includes('/api/v1/agents')) return json([])
    return Promise.resolve({ ok: false, json: () => Promise.resolve(null) })
  }
}
const settle = async () => { for (let i = 0; i < 4; i++) await new Promise((r) => setTimeout(r, 0)) }

const D1 = 'daemon-uuid-1'
const calls = []
let irr

beforeAll(async () => {
  localStorage.setItem('irrlicht_settings', JSON.stringify({
    // Relay only, so `lastMockWebSocket` is the relay socket; the stubbed
    // same-origin fetch still serves the local daemon's payload.
    enableLocalSource: false,
    enableRelaySource: true,
    relayUrl: 'https://relay.example',
    notifyOnWaiting: true,
  }))
  global.Notification = class {
    static permission = 'granted'
    static requestPermission() { return Promise.resolve('granted') }
    constructor(title, opts) { calls.push({ title, body: opts?.body }) }
  }
  Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => 'hidden' })
  serve(payload('busy', 'busy'))
  irr = await import('./irrlicht.js')
  await settle()
  const ws = global.lastMockWebSocket
  ws.simulateOpen()
  ws.simulateMessage({ type: 'snapshot', daemons: [{ daemon_id: D1, daemon_label: 'T101', status: 'connected' }] })
  // A working session: entering it notifies nothing on its own.
  ws.simulateMessage({ type: 'push', source: D1, msg: { type: 'session_updated', session: {
    session_id: 'proc-1', state: 'working', project_name: 'vm-proj', adapter: 'claude-code', first_seen: 100, metrics: {},
  } } })
  await settle()
})

test('a slot joined by a relayed session raises no worker notification; an unjoined one does', async () => {
  const joined = [...document.querySelectorAll('#session-list .session-row')]
    .some((el) => el.querySelector('.row-worker-name')?.textContent === 'T101')
  expect(joined, 'precondition: the relayed session joined T101').toBe(true)
  const before = calls.length
  serve(payload('waiting', 'waiting'))
  await irr.rehydratePoll()
  const workerCalls = calls.slice(before).filter((c) => c.title === 'Worker waiting for input')
  expect(workerCalls).toEqual([{ title: 'Worker waiting for input', body: 'lead-T100 · T102' }])
})
