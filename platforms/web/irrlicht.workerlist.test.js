// Issue #2148: a worker-list pool (type "workerlist") renders beside Gas Town
// through #2140's worker rows, and a slot's row shows two row-only
// indications — "stale" (heartbeat too old) and "failed · exit N" (last run
// exited non-zero). Neither is a session state or a counted row.
//
// Red-first on 796a07307 (the parent branch): the tag, signature and
// rehydrate tests fail there; the tests marked LOCK pass there by
// construction and pin what must not change.
import { describe, test, expect, beforeAll } from 'vitest'

const gasTown = {
  name: 'gt',
  type: 'gastown',
  groups: [{
    name: 'irrlicht',
    status: 'operational',
    unattached_workers: [{ role: 'polecat', icon: '👷', worker_name: 'free-1', worker_id: 'GH-1', worker_state: 'idle' }],
  }],
}

const slot = (name, extra = {}) => ({
  role: 'worker', icon: '🔧', description: 'Pool worker slot', worker_name: name, worker_state: 'idle', ...extra,
})

const initialSlots = () => [
  slot('team-a-m1-1', { worker_id: 'proj-a1b2', worker_state: 'busy', stale: true, last_exit: 2 }),
  slot('team-a-m1-2', { last_exit: 0 }),
  slot('team-a-m1-3'),
  slot('team-a-m1-4', { worker_id: 'proj-c3d4', worker_state: 'busy' }),
]

const payload = (slots) => ({
  groups: [
    gasTown,
    { name: 'Build pool', type: 'workerlist', groups: [{ name: 'team-a', status: 'operational', unattached_workers: slots }] },
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

// visibleTag returns the tag's text when it is shown, else null.
const visibleTag = (row, cls) => {
  const el = row.querySelector(cls)
  return el && el.style.display !== 'none' ? el.textContent : null
}

let irr

beforeAll(async () => {
  global.fetch = sessionsFetch(payload(initialSlots()))
  irr = await import('./irrlicht.js')
  await new Promise((r) => setTimeout(r, 0))
})

describe('worker-list pool rows (#2148)', () => {
  test('a stale busy slot whose last run failed shows both tags', () => {
    const row = workerRow('team-a-m1-1')
    expect(visibleTag(row, '.worker-row-stale')).toBe('stale')
    expect(visibleTag(row, '.worker-row-failed')).toBe('failed · exit 2')
    expect(row.querySelector('.worker-row-failed').title).toContain('exit code 2')
  })

  test('LOCK: a passed last run (exit 0) or no completed run shows neither tag', () => {
    for (const name of ['team-a-m1-2', 'team-a-m1-3', 'team-a-m1-4']) {
      const row = workerRow(name)
      expect(visibleTag(row, '.worker-row-stale'), name).toBeNull()
      expect(visibleTag(row, '.worker-row-failed'), name).toBeNull()
    }
  })

  test('LOCK: slot rows are never counted as sessions', () => {
    expect(document.querySelectorAll('#session-list .session-row')).toHaveLength(0)
    expect(document.querySelectorAll('#app-state-icons .app-state-icon')).toHaveLength(0)
    const counts = [...document.querySelectorAll('#session-list .group-hdr .group-count')].map((el) => el.textContent)
    expect(counts.every((c) => c === '0 sessions'), counts.join(', ')).toBe(true)
  })

  test('LOCK: the pool header carries no ⛽ glyph; Gas Town keeps it', () => {
    const names = [...document.querySelectorAll('#session-list .group-hdr .group-name')].map((el) => el.textContent)
    expect(names).toContain('Build pool')
    expect(names).toContain('⛽\u202fgt') // the glyph is joined by a narrow no-break space
  })

  test('structureSignature changes when a slot turns stale or its last run changes', () => {
    const base = payload(initialSlots()).groups
    const sig = (slots) => irr.structureSignature(payload(slots).groups)
    const changed = (i, extra) => {
      const slots = initialSlots()
      slots[i] = { ...slots[i], ...extra }
      return sig(slots)
    }
    expect(sig(initialSlots())).toBe(irr.structureSignature(base))
    expect(changed(2, { stale: true })).not.toBe(irr.structureSignature(base))
    expect(changed(0, { stale: false })).not.toBe(irr.structureSignature(base))
    expect(changed(2, { last_exit: 1 })).not.toBe(irr.structureSignature(base))
    expect(changed(1, { last_exit: 3 })).not.toBe(irr.structureSignature(base))
  })

  test('the rehydrate poll adopts a busy slot turning stale with nothing else changed', async () => {
    // rehydratePoll is driven directly rather than by advancing its timer:
    // faking timers deadlocks on irrlicht.js's own setInterval loops (see
    // sessionErrorReachability.test.js).
    const slots = initialSlots()
    slots[3] = { ...slots[3], stale: true }
    global.fetch = sessionsFetch(payload(slots))
    await irr.rehydratePoll()
    expect(visibleTag(workerRow('team-a-m1-4'), '.worker-row-stale')).toBe('stale')
  })
})
