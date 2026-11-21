import { test, expect, beforeAll } from 'vitest'
import { compoundSessionId } from './sessionIdentity.js'

// Issue #2149 review finding: a dashboard served BY a relay. Its Local source
// is the relay's own socket, so REST /api/v1/sessions answers with bare ids
// while the socket's `push` frames fold to compound ids — and local-wins
// (#538) hides the compound rows behind the bare ones. Those bare rows have no
// live socket of their own: only the rehydrate's adopt refreshes them.
// Leaving every relay row out of the rehydrate's comparison stopped that adopt
// and froze them at the first load.
//
// Red on 352b310fb (this branch before the fix). Passes on 8d6279df7 (the
// parent), where every poll adopted — it pins that refresh rather than
// proving a defect the parent had.
//
// Own test file: the payload is consumed at module load, and vitest's
// per-file isolation gives this file a fresh module instance.

let restState = 'working'
const payload = () => ({ groups: [{ name: 'p', agents: [{ session_id: 'proc-1', state: restState, project_name: 'p', metrics: {} }] }] })
// A fresh copy per response, as a real fetch parses one.
const json = (b) => Promise.resolve({ ok: true, json: () => Promise.resolve(structuredClone(b)) })
const settle = async () => { for (let i = 0; i < 4; i++) await new Promise((r) => setTimeout(r, 0)) }
const rowFor = (id) => [...document.querySelectorAll('#session-list .session-row')].find((el) => el.dataset.sessionId === id) || null
const push = (state) => ({ type: 'push', source: 'd1', msg: { type: 'session_updated', session: { session_id: 'proc-1', state, project_name: 'p', metrics: {} } } })

let irr
let ws

beforeAll(async () => {
  // The defaults' Local source, which on a relay-served page is the relay.
  localStorage.setItem('irrlicht_settings', JSON.stringify({ enableLocalSource: true, enableRelaySource: false }))
  global.fetch = (url) => {
    const u = String(url)
    if (u.includes('/api/v1/sessions')) return json(payload())
    if (u.includes('/api/v1/agents')) return json([])
    return Promise.resolve({ ok: false, json: () => Promise.resolve(null) })
  }
  irr = await import('./irrlicht.js')
  await settle()
  ws = global.lastMockWebSocket
  ws.simulateOpen()
  ws.simulateMessage({ type: 'snapshot', daemons: [{ daemon_id: 'd1', daemon_label: 'box', status: 'connected' }] })
})

test('the visible bare row follows the session through each poll', async () => {
  expect(rowFor('proc-1')?.dataset.state).toBe('working')
  ws.simulateMessage(push('waiting'))
  restState = 'waiting'
  expect(rowFor(compoundSessionId('d1', 'proc-1')), 'precondition: local-wins hides the pushed row').toBeNull()
  await irr.rehydratePoll()
  expect(rowFor('proc-1')?.dataset.state, 'the poll did not refresh the visible row').toBe('waiting')

  ws.simulateMessage(push('ready'))
  restState = 'ready'
  await irr.rehydratePoll()
  expect(rowFor('proc-1')?.dataset.state).toBe('ready')
})
