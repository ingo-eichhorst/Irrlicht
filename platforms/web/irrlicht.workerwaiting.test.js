// Issue #2145: a worker-list slot whose agent waits for a person arrives with
// `waiting: true` (and worker_state "waiting" as its text). Its row is painted
// in the session waiting colour with a filled dot, and a slot that turns
// waiting raises the same browser notification a waiting session does — once
// per edge, never on a first sighting, and never for a worker without the flag
// (Gas Town passes gt's own state strings through in worker_state).
//
// Red-first: every test except the LOCK failed before the change (no
// `waiting` class, no filled dot, no notification); the Gas Town test failed
// while the paint keyed on the state text (#2145 review). Mutations run by
// hand (tools/mutate.sh refuses irrlicht.js, which holds a NUL byte): reading
// the state text as waiting reddened the Gas Town test; letting
// waitingWorkerEdges count a worker absent from prev reddened the first-load
// test.
import { describe, test, expect, beforeAll } from 'vitest'

// A slot as the daemon sends it: `waiting` set exactly when its state is.
const slot = (name, state, extra = {}) => ({
  role: 'worker', icon: '🔧', description: 'Pool worker slot', worker_name: name, worker_state: state,
  ...(state === 'waiting' ? { waiting: true } : {}), ...extra,
})

// polecatState is Gas Town's free polecat; its state is gt's own text, passed
// through, and never carries the waiting flag.
const payload = ({ m1 = 'busy', m2 = 'idle', m3 = 'waiting', polecatState = 'idle' } = {}) => ({
  groups: [
    {
      name: 'gt',
      type: 'gastown',
      groups: [{
        name: 'irrlicht',
        status: 'operational',
        unattached_workers: [{ role: 'polecat', icon: '👷', worker_name: 'free-1', worker_id: 'GH-1', worker_state: polecatState }],
      }],
    },
    {
      name: 'VM tasks',
      type: 'workerlist',
      groups: [{
        name: 'lead-T100',
        status: 'operational',
        unattached_workers: [
          slot('T101', m1, { worker_id: 'T101' }),
          slot('T102', m2),
          slot('T103', m3, { worker_id: 'T103' }),
        ],
      }],
    },
  ],
  provider_costs: {},
})

const sessionsFetch = (body) => (url) => {
  const u = String(url)
  if (u.includes('/api/v1/sessions')) {
    return Promise.resolve({ ok: true, json: () => Promise.resolve(body) })
  }
  if (u.includes('/api/v1/agents')) {
    return Promise.resolve({ ok: true, json: () => Promise.resolve([]) })
  }
  return Promise.resolve({ ok: false, json: () => Promise.resolve(null) })
}

const workerRow = (name) => {
  const row = [...document.querySelectorAll('#session-list .worker-row')]
    .find((el) => el.querySelector('.row-worker-name')?.textContent === name)
  expect(row, `no worker row for ${name}`).toBeDefined()
  return row
}

const glyph = (row) => row.querySelector('.worker-row-glyph').textContent

// One fresh payload per poll: rehydratePoll adopts the response object itself.
const poll = async (opts) => {
  global.fetch = sessionsFetch(payload(opts))
  await irr.rehydratePoll()
}

const calls = []
let irr

beforeAll(async () => {
  localStorage.setItem('irrlicht_settings', JSON.stringify({ notifyOnWaiting: true }))
  // tryNotify's other three gates: permission, a hidden tab, a constructor.
  global.Notification = class {
    static permission = 'granted'
    static requestPermission() { return Promise.resolve('granted') }
    constructor(title, opts) { calls.push({ title, body: opts?.body }) }
  }
  Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => 'hidden' })
  // T103 is already waiting at the first load.
  global.fetch = sessionsFetch(payload())
  irr = await import('./irrlicht.js')
  await new Promise((r) => setTimeout(r, 0))
})

describe('waiting worker row (#2145)', () => {
  test('a slot already waiting at the first load is painted waiting but does not notify', () => {
    const row = workerRow('T103')
    expect(row.classList.contains('waiting')).toBe(true)
    expect(glyph(row)).toBe('●')
    expect(calls).toEqual([])
  })

  test('LOCK: busy and free slots keep the hollow placeholder and no waiting class', () => {
    for (const name of ['T101', 'T102']) {
      const row = workerRow(name)
      expect(row.classList.contains('waiting'), name).toBe(false)
      expect(glyph(row), name).toBe('○')
    }
  })

  test('a busy slot turning waiting notifies once, with its team and name', async () => {
    await poll({ m1: 'waiting' })
    expect(calls).toEqual([{ title: 'Worker waiting for input', body: 'lead-T100 · T101' }])
    const row = workerRow('T101')
    expect(row.classList.contains('waiting')).toBe(true)
    expect(glyph(row)).toBe('●')
  })

  test('the same payload again notifies nothing', async () => {
    await poll({ m1: 'waiting' })
    await poll({ m1: 'waiting' })
    expect(calls).toHaveLength(1)
  })

  test('back to busy clears the paint, and a new waiting edge notifies again', async () => {
    await poll({ m1: 'busy' })
    const row = workerRow('T101')
    expect(row.classList.contains('waiting')).toBe(false)
    expect(glyph(row)).toBe('○')
    expect(calls).toHaveLength(1)

    await poll({ m1: 'waiting' })
    expect(calls).toHaveLength(2)
    expect(calls[1]).toEqual({ title: 'Worker waiting for input', body: 'lead-T100 · T101' })
  })

  test('a worker whose text reads waiting without the flag is neither painted nor notified', async () => {
    const before = calls.length
    await poll({ m1: 'waiting', polecatState: 'busy' })
    await poll({ m1: 'waiting', polecatState: 'waiting' })
    expect(calls).toHaveLength(before)
    const row = workerRow('free-1')
    expect(row.querySelector('.worker-row-state').textContent).toBe('waiting')
    expect(row.classList.contains('waiting')).toBe(false)
    expect(glyph(row)).toBe('○')
  })
})
