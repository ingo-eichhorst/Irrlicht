import { describe, test, expect, beforeAll } from 'vitest'

// Issue #2140: an orchestrator worker with no session (a free Gas Town
// polecat, an idle pool slot) arrives in its rig group's `unattached_workers`
// and gets a row of its own; a session row with a role shows the worker's
// name and work item next to the role badge, as macOS already does
// (SessionRowView.swift's workerName / workerID block).
//
// Own test file: the payload is consumed at module load, and vitest's
// per-file isolation gives this file a fresh module instance.

const busyRow = {
  session_id: 'pole-1',
  state: 'working',
  project_name: 'irrlicht',
  role: 'polecat',
  icon: '👷',
  description: 'Worker agent',
  worker_name: 'busy-1',
  worker_id: 'proj-a1b2c3d4e5',
  metrics: {},
}

const free = (name, id, state = 'idle') => ({
  role: 'polecat', icon: '👷', description: 'Worker agent',
  worker_name: name, worker_id: id, worker_state: state,
})

// The daemon omits empty `agents` (json omitempty): the orchestrator group
// has none of its own, and the all-idle "quiet" rig has none at all.
const payload = (irrlichtWorkers) => ({
  groups: [
    {
      name: 'gt',
      type: 'gastown',
      groups: [
        { name: 'irrlicht', status: 'operational', agents: [busyRow], unattached_workers: irrlichtWorkers },
        { name: 'quiet', status: 'operational', unattached_workers: [free('free-3', 'q-77')] },
      ],
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

const workerRows = () => [...document.querySelectorAll('#session-list .worker-row')]
const workerNames = () => workerRows().map((el) => el.querySelector('.row-worker-name')?.textContent)

let irr

beforeAll(async () => {
  global.fetch = sessionsFetch(payload([free('free-2', 'proj-zz99yy88')]))
  irr = await import('./irrlicht.js')
  await new Promise((r) => setTimeout(r, 0))
})

describe('unattached orchestrator workers (#2140)', () => {
  test('a session row with a role shows its worker name and short work item', () => {
    const row = document.querySelector('#session-list .session-row[data-session-id="pole-1"]')
    expect(row, 'the polecat session row did not render').not.toBeNull()
    const name = row.querySelector('.row-worker-name')
    const id = row.querySelector('.row-worker-id')
    expect(name?.textContent).toBe('busy-1')
    expect(name.style.display).not.toBe('none')
    expect(id?.textContent).toBe('proj-a1b')
    expect(id.title).toBe('proj-a1b2c3d4e5')
  })

  test('each free worker gets its own row after its rig\'s sessions', () => {
    expect(workerNames()).toEqual(['free-2', 'free-3'])
    const [w2] = workerRows()
    expect(w2.querySelector('.row-worker-id').textContent).toBe('proj-zz9')
    expect(w2.querySelector('.row-worker-id').title).toBe('proj-zz99yy88')
    expect(w2.querySelector('.worker-row-state').textContent).toBe('idle')
    expect(w2.querySelector('.worker-row-icon').textContent).toBe('👷')
    expect(w2.classList.contains('nested')).toBe(true)

    // Placement: directly beneath the busy polecat's row, before the next rig.
    const busy = document.querySelector('#session-list .session-row[data-session-id="pole-1"]')
    expect(w2.previousElementSibling).toBe(busy)
    expect(w2.nextElementSibling.classList.contains('group-hdr')).toBe(true)
  })

  test('an all-idle rig with no agents key still renders its header and rows', () => {
    // The orchestrator title carries a glyph prefix (updateGroupHeader), so
    // compare the trailing names only.
    const headers = [...document.querySelectorAll('#session-list .group-hdr .group-name')]
      .map((el) => el.textContent.split(/\s/).pop())
    expect(headers).toEqual(['gt', 'irrlicht', 'quiet'])
    const quietRow = workerRows()[1]
    expect(quietRow.previousElementSibling.querySelector('.group-name').textContent).toBe('quiet')
  })

  test('a worker row is never counted or indexed as a session', () => {
    expect(document.querySelectorAll('#session-list .session-row')).toHaveLength(1)
    for (const el of workerRows()) {
      expect(el.classList.contains('session-row')).toBe(false)
      expect(el.dataset.sessionId).toBeUndefined()
    }
    // Header state summary: one session, so one icon.
    expect(document.querySelectorAll('#app-state-icons .app-state-icon')).toHaveLength(1)
    // Header counts stay session counts: the orchestrator header counts only
    // its own direct agents (none here), each rig its own sessions.
    const counts = [...document.querySelectorAll('#session-list .group-hdr')]
      .map((el) => el.querySelector('.group-count').textContent)
    expect(counts).toEqual(['0 sessions', '1 session', '0 sessions'])
  })

  test('structureSignature changes when a free worker appears, leaves or changes', () => {
    const base = payload([free('free-2', 'proj-zz99yy88')]).groups
    const same = payload([free('free-2', 'proj-zz99yy88')]).groups
    const gone = payload([]).groups
    const added = payload([free('free-2', 'proj-zz99yy88'), free('free-4', 'p-4')]).groups
    const newBead = payload([free('free-2', 'proj-other')]).groups
    const newState = payload([free('free-2', 'proj-zz99yy88', 'stuck')]).groups
    expect(irr.structureSignature(same)).toBe(irr.structureSignature(base))
    expect(irr.structureSignature(gone)).not.toBe(irr.structureSignature(base))
    expect(irr.structureSignature(added)).not.toBe(irr.structureSignature(base))
    expect(irr.structureSignature(newBead)).not.toBe(irr.structureSignature(base))
    expect(irr.structureSignature(newState)).not.toBe(irr.structureSignature(base))
  })

  test('the rehydrate poll adopts a worker change that came with no websocket event', async () => {
    // rehydratePoll is driven directly rather than by advancing its timer:
    // faking timers deadlocks on irrlicht.js's own setInterval loops (see
    // sessionErrorReachability.test.js).
    global.fetch = sessionsFetch(payload([free('free-4', 'p-4')]))
    await irr.rehydratePoll()
    expect(workerNames()).toEqual(['free-4', 'free-3'])
    expect(document.querySelectorAll('#session-list .session-row')).toHaveLength(1)
  })
})
