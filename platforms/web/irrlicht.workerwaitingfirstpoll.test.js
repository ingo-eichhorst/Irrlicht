// Issue #2145 review: the first rehydrate poll is scheduled when the module
// loads, before the initial /api/v1/sessions fetch lands, so it used to wait
// the idle 30 s even when that fetch listed a worker list. Within that
// half-minute a slot present at the load that turned waiting was seen up to
// 30 s late, and a slot that appeared after the load and turned waiting was
// first seen already waiting — a first sighting, which stays silent. Once the
// tree is in, a poll still pending is re-armed at the orchestrator cadence.
//
// Red-first: fails before rearmRehydratePollAfterLoad existed (only the 30 s
// timer was ever scheduled during load). Own file: the payload is consumed at
// module load, and vitest's per-file isolation gives a fresh module instance.
import { test, expect, beforeAll } from 'vitest'

const payload = {
  groups: [{
    name: 'VM tasks', type: 'workerlist',
    groups: [{ name: 'lead-T100', status: 'operational', unattached_workers: [{ role: 'worker', worker_name: 'T101', worker_state: 'busy' }] }],
  }],
  provider_costs: {},
}

const json = (body) => Promise.resolve({ ok: true, json: () => Promise.resolve(structuredClone(body)) })

// The rehydrate cadences, in the order they were scheduled.
const cadences = []

beforeAll(async () => {
  const realSetTimeout = global.setTimeout
  global.setTimeout = (fn, ms, ...args) => {
    if (ms === 2500 || ms === 30000) cadences.push(ms)
    return realSetTimeout(fn, ms, ...args)
  }
  global.fetch = (url) => {
    const u = String(url)
    if (u.includes('/api/v1/sessions')) return json(payload)
    if (u.includes('/api/v1/agents')) return json([])
    return Promise.resolve({ ok: false, json: () => Promise.resolve(null) })
  }
  await import('./irrlicht.js')
  for (let i = 0; i < 4; i++) await new Promise((r) => realSetTimeout(r, 0))
})

test('a page that loads a worker list polls at 2.5 s from the first poll on', () => {
  expect(cadences[0], 'the load-time poll is no longer the idle one').toBe(30000)
  expect(cadences.slice(1), 'the pending poll was not re-armed once the worker list arrived').toEqual([2500])
})
