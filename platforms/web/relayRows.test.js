import { describe, test, expect } from 'vitest'
import { compoundSessionId } from './sessionIdentity.js'
import { withoutRemoteSessions, remoteRootSessions, joinRelayWorkers } from './relayRows.js'
import { structureSignature } from './irrlicht.js'

// Pure helpers behind #2149's relay-row fixes (relayRows.js).

const remote = (daemon, id, extra = {}) => ({ session_id: compoundSessionId(daemon, id), state: 'working', ...extra })
const local = (id, extra = {}) => ({ session_id: id, state: 'working', ...extra })

// The local daemon's payload: a project, an orchestrator with no direct
// agents (omitempty), and an all-idle rig holding only a free worker.
const localTree = () => [
  { name: 'irrlicht', agents: [local('loc-1', { children: [local('loc-1-sub')] })] },
  {
    name: 'gt', type: 'gastown', agents: [],
    groups: [{ name: 'quiet', agents: [], unattached_workers: [{ worker_name: 'free-1', worker_state: 'idle' }] }],
  },
]

// The same tree as the dashboard holds it after relay pushes: a relay row in
// a local project group, a relay-only project group, and a relay row whose
// project name matched the orchestrator group's (#2148's stopgap).
const withRelay = () => {
  const t = localTree()
  t[0].agents.push(remote('d1', 'r-1', { children: [remote('d1', 'r-1-sub')] }))
  t[1].agents.push(remote('d1', 'r-2'))
  t.push({ name: 'container-proj', agents: [remote('d2', 'r-3'), remote('d2', 'r-4')] })
  return t
}

describe('withoutRemoteSessions (#2149)', () => {
  test('a tree with relay rows compares equal to the payload it came from', () => {
    expect(structureSignature(withoutRemoteSessions(withRelay()))).toBe(structureSignature(localTree()))
  })

  test('LOCK: the comparison still sees a real local change', () => {
    const changed = localTree()
    changed[0].agents.push(local('loc-2'))
    expect(structureSignature(withoutRemoteSessions(withRelay()))).not.toBe(structureSignature(changed))
  })

  test('drops a group only when the removal emptied it', () => {
    expect(withoutRemoteSessions(withRelay()).map(g => g.name)).toEqual(['irrlicht', 'gt'])
    // Already empty in the payload: kept, whatever its type.
    expect(withoutRemoteSessions([{ name: 'p', agents: [] }]).map(g => g.name)).toEqual(['p'])
    // Emptied, but an orchestrator type, sub-groups or free workers keep it.
    expect(withoutRemoteSessions([{ name: 'pool', type: 'workerlist', agents: [remote('d1', 'x')] }])).toHaveLength(1)
    expect(withoutRemoteSessions([{ name: 'g', agents: [remote('d1', 'x')], groups: [{ name: 'r', agents: [local('l')] }] }])).toHaveLength(1)
    expect(withoutRemoteSessions([{ name: 'r', agents: [remote('d1', 'x')], unattached_workers: [{ worker_name: 'w' }] }])).toHaveLength(1)
  })

  test('never mutates its input', () => {
    const t = withRelay()
    const before = JSON.stringify(t)
    withoutRemoteSessions(t)
    expect(JSON.stringify(t)).toBe(before)
  })
})

describe('remoteRootSessions (#2149)', () => {
  test('lists the relay rows that sit in a group, in tree order, with their children', () => {
    const roots = remoteRootSessions(withRelay())
    expect(roots.map(a => a.session_id)).toEqual([
      compoundSessionId('d1', 'r-1'), compoundSessionId('d1', 'r-2'),
      compoundSessionId('d2', 'r-3'), compoundSessionId('d2', 'r-4'),
    ])
    expect(roots[0].children.map(c => c.session_id)).toEqual([compoundSessionId('d1', 'r-1-sub')])
  })

  test('a tree with no relay rows carries nothing', () => {
    expect(remoteRootSessions(localTree())).toEqual([])
  })
})

describe('joinRelayWorkers (#2149)', () => {
  const free = (name, extra = {}) => ({ role: 'worker', icon: '🔧', description: 'slot', worker_name: name, worker_state: 'idle', ...extra })
  // A pool rig with free worker X, plus a project group holding a relay row.
  const tree = ({ rigWorkers = [free('X', { worker_id: 'job-1' })], rigAgents, project = [remote('d1', 'r-1', { first_seen: 10 })], more = [] } = {}) => [
    { name: 'pool', type: 'workerlist', agents: [], groups: [{ name: 'team', agents: rigAgents, unattached_workers: rigWorkers }] },
    { name: 'container-proj', agents: project },
    ...more,
  ]
  const labels = (map) => (id) => map[id] || ''
  const rig = (gs) => gs[0].groups[0]
  const ids = (arr) => (arr || []).map(a => a.session_id)

  test('a relay root from the daemon labelled X joins X\'s row, decorated as X', () => {
    const t = tree()
    const shown = joinRelayWorkers(t, labels({ d1: 'X' }))
    expect(ids(rig(shown).agents)).toEqual([compoundSessionId('d1', 'r-1')])
    const row = rig(shown).agents[0]
    expect([row.role, row.icon, row.description, row.worker_name, row.worker_id]).toEqual(['worker', '🔧', 'slot', 'X', 'job-1'])
    expect(rig(shown).unattached_workers).toEqual([])
    expect(shown.map(g => g.name), 'the emptied project group is dropped').toEqual(['pool'])
    // A copy: the stored row and tree are untouched.
    expect(t[1].agents[0].worker_name).toBeUndefined()
    expect(rig(t).unattached_workers).toHaveLength(1)
  })

  test('joins after the worker group\'s own session rows; a project group with other rows stays', () => {
    const t = tree({ rigAgents: [local('loc-9', { worker_name: 'Y' })], project: [remote('d1', 'r-1'), remote('d9', 'r-9')] })
    const shown = joinRelayWorkers(t, labels({ d1: 'X' }))
    expect(ids(rig(shown).agents)).toEqual(['loc-9', compoundSessionId('d1', 'r-1')])
    expect(ids(shown[1].agents)).toEqual([compoundSessionId('d9', 'r-9')])
  })

  test('nothing to join returns the tree itself', () => {
    const t = tree()
    expect(joinRelayWorkers(t, labels({}))).toBe(t)                       // no label
    expect(joinRelayWorkers(t, labels({ d1: 'nobody' }))).toBe(t)         // no such worker
    expect(joinRelayWorkers(t, labels({ d2: 'X' }))).toBe(t)              // another daemon's label
  })

  test('two workers named X, attached or not, join nothing', () => {
    const twoFree = tree({ more: [{ name: 'gt', type: 'gastown', agents: [], groups: [{ name: 'rig', agents: [], unattached_workers: [free('X')] }] }] })
    expect(joinRelayWorkers(twoFree, labels({ d1: 'X' }))).toBe(twoFree)
    const oneBusy = tree({ more: [{ name: 'gt', type: 'gastown', agents: [], groups: [{ name: 'rig', agents: [local('pole', { worker_name: 'X' })] }] }] })
    expect(joinRelayWorkers(oneBusy, labels({ d1: 'X' }))).toBe(oneBusy)
  })

  test('LOCK: a worker with a local session is not free, so its local session wins', () => {
    const t = tree({ rigWorkers: [], rigAgents: [local('loc-x', { worker_name: 'X', role: 'worker' })] })
    expect(joinRelayWorkers(t, labels({ d1: 'X' }))).toBe(t)
  })

  test('of several candidates the newest first_seen joins; a tie goes to the lower id', () => {
    const newer = tree({ project: [remote('d1', 'old', { first_seen: 10 }), remote('d2', 'new', { first_seen: 20 })] })
    const shown = joinRelayWorkers(newer, labels({ d1: 'X', d2: 'X' }))
    expect(ids(rig(shown).agents)).toEqual([compoundSessionId('d2', 'new')])
    expect(ids(shown[1].agents)).toEqual([compoundSessionId('d1', 'old')])

    const tie = tree({ project: [remote('d2', 'b', { first_seen: 10 }), remote('d1', 'a', { first_seen: 10 })] })
    const tied = joinRelayWorkers(tie, labels({ d1: 'X', d2: 'X' }))
    expect(ids(rig(tied).agents)).toEqual([compoundSessionId('d1', 'a')])
  })

  test('a shadowed relay row and a relay child never join', () => {
    const shadowed = tree({ project: [remote('d1', 'loc-1')], more: [{ name: 'irrlicht', agents: [local('loc-1')] }] })
    expect(joinRelayWorkers(shadowed, labels({ d1: 'X' }))).toBe(shadowed)
    const child = tree({ project: [remote('d1', 'sub', { parent_session_id: compoundSessionId('d1', 'gone') })] })
    expect(joinRelayWorkers(child, labels({ d1: 'X' }))).toBe(child)
  })

  test('a relay row in a same-named orchestrator group leaves it without dropping the group', () => {
    const t = tree({ project: [] })
    t[0].agents.push(remote('d1', 'r-1'))
    const shown = joinRelayWorkers(t, labels({ d1: 'X' }))
    expect(shown[0].name).toBe('pool')
    expect(ids(shown[0].agents)).toEqual([])
    expect(ids(rig(shown).agents)).toEqual([compoundSessionId('d1', 'r-1')])
  })

  test('never mutates its input', () => {
    const t = tree({ project: [remote('d1', 'r-1'), remote('d2', 'r-2')] })
    const before = JSON.stringify(t)
    joinRelayWorkers(t, labels({ d1: 'X' }))
    expect(JSON.stringify(t)).toBe(before)
  })
})
