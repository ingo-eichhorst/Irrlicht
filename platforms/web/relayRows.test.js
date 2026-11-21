import { describe, test, expect } from 'vitest'
import { compoundSessionId } from './sessionIdentity.js'
import { withoutRemoteSessions, remoteRootSessions } from './relayRows.js'
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
