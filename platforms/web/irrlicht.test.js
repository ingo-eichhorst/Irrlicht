import { describe, test, expect, beforeEach } from 'vitest'
import {
  resolvedTheme,
  rowLabel,
  maybeNotifyOnUpdate,
  formatCost,
  formatUsageCost,
  pressureClass,
  taskEtaPresentation,
  historyPriorityForState,
  lastNotifiedPressure,
  relayFrameKind,
  aggregateConnState,
  relayWsUrl,
  compoundSessionId,
  displaySessionId,
  sessionOrigin,
  sourceIdOf,
  localBareIds,
  isShadowedRemote,
  daemonSessionIds,
} from './irrlicht.js'

describe('resolvedTheme', () => {
  beforeEach(() => localStorage.clear())

  test('returns stored preference when set', () => {
    localStorage.setItem('irrlicht_theme', 'light')
    expect(resolvedTheme()).toBe('light')
  })

  test('falls back to dark when no stored pref and matchMedia reports dark', () => {
    // setup stub returns matches:false for light → resolves to 'dark'
    expect(resolvedTheme()).toBe('dark')
  })
})

describe('rowLabel', () => {
  test('formats project · branch when both present', () => {
    expect(rowLabel({ project_name: 'myapp', git_branch: 'feat/x' }))
      .toBe('myapp · feat/x')
  })

  test('falls back to project name when branch is absent', () => {
    expect(rowLabel({ project_name: 'myapp' })).toBe('myapp')
  })

  test('falls back to branch when project is absent', () => {
    expect(rowLabel({ git_branch: 'main' })).toBe('main')
  })

  test('falls back to truncated session_id when both absent', () => {
    expect(rowLabel({ session_id: 'abcdef123456' })).toBe('abcdef12')
  })

  test('returns "session" when all fields are absent', () => {
    expect(rowLabel({})).toBe('session')
  })
})

describe('maybeNotifyOnUpdate', () => {
  beforeEach(() => lastNotifiedPressure.clear())

  test('does nothing when next is null', () => {
    maybeNotifyOnUpdate(null, null)
    expect(lastNotifiedPressure.size).toBe(0)
  })

  test('records pressure level when session enters high-pressure state', () => {
    const prev = { state: 'working', session_id: 's1', metrics: { pressure_level: 'none' } }
    const next = { state: 'working', session_id: 's1', metrics: { pressure_level: 'warning' } }
    maybeNotifyOnUpdate(prev, next)
    expect(lastNotifiedPressure.get('s1')).toBe('warning')
  })

  test('updates pressure record when level escalates from warning to critical', () => {
    lastNotifiedPressure.set('s1', 'warning')
    const prev = { state: 'working', session_id: 's1', metrics: { pressure_level: 'warning' } }
    const next = { state: 'working', session_id: 's1', metrics: { pressure_level: 'critical' } }
    maybeNotifyOnUpdate(prev, next)
    expect(lastNotifiedPressure.get('s1')).toBe('critical')
  })

  test('clears pressure record when session pressure drops', () => {
    lastNotifiedPressure.set('s1', 'warning')
    const prev = { state: 'working', session_id: 's1', metrics: { pressure_level: 'warning' } }
    const next = { state: 'ready',   session_id: 's1', metrics: { pressure_level: 'none' } }
    maybeNotifyOnUpdate(prev, next)
    expect(lastNotifiedPressure.has('s1')).toBe(false)
  })
})

describe('formatCost', () => {
  test('returns empty string for zero or falsy', () => {
    expect(formatCost(0)).toBe('')
    expect(formatCost(null)).toBe('')
  })

  test('formats positive values with dollar sign and two decimals', () => {
    expect(formatCost(1.5)).toBe('$1.50')
    expect(formatCost(0.123)).toBe('$0.12')
  })
})

describe('pressureClass', () => {
  test('maps critical to "critical"', () => {
    expect(pressureClass('critical')).toBe('critical')
  })

  test('maps warning and high to "high"', () => {
    expect(pressureClass('warning')).toBe('high')
    expect(pressureClass('high')).toBe('high')
  })

  test('maps caution and medium to "medium"', () => {
    expect(pressureClass('caution')).toBe('medium')
    expect(pressureClass('medium')).toBe('medium')
  })

  test('returns empty string for unknown levels', () => {
    expect(pressureClass('low')).toBe('')
    expect(pressureClass('')).toBe('')
  })
})

describe('historyPriorityForState', () => {
  test('waiting has highest priority (2)', () => {
    expect(historyPriorityForState('waiting')).toBe(2)
  })

  test('working is 1, ready is 0, unknown is -1', () => {
    expect(historyPriorityForState('working')).toBe(1)
    expect(historyPriorityForState('ready')).toBe(0)
    expect(historyPriorityForState('unknown')).toBe(-1)
  })
})

describe('formatUsageCost', () => {
  // Windowed usage chip headline (#386). Zero renders "$0" (a windowed zero
  // is honest) to match the macOS chip, not "$0.00".
  test('zero and falsy render "$0"', () => {
    expect(formatUsageCost(0)).toBe('$0')
    expect(formatUsageCost(undefined)).toBe('$0')
    expect(formatUsageCost(-5)).toBe('$0')
  })

  test('sub-cent renders "<$0.01"', () => {
    expect(formatUsageCost(0.004)).toBe('<$0.01')
  })

  test('normal costs render with two decimals', () => {
    expect(formatUsageCost(1.2)).toBe('$1.20')
    expect(formatUsageCost(0.5)).toBe('$0.50')
  })

  test('>= $100 drops to integer dollars', () => {
    expect(formatUsageCost(105.3)).toBe('$105')
  })
})

describe('relayFrameKind', () => {
  test('classifies relay envelope frames', () => {
    expect(relayFrameKind({ type: 'push', msg: {} })).toBe('push')
    expect(relayFrameKind({ type: 'snapshot', daemons: [] })).toBe('snapshot')
    expect(relayFrameKind({ type: 'daemon_status' })).toBe('daemon_status')
    expect(relayFrameKind({ type: 'hello_ack' })).toBe('hello_ack')
  })

  test('treats raw daemon frames (and junk) as raw', () => {
    expect(relayFrameKind({ type: 'session_updated', session: {} })).toBe('raw')
    expect(relayFrameKind({ type: 'history_tick' })).toBe('raw')
    expect(relayFrameKind(null)).toBe('raw')
    expect(relayFrameKind({})).toBe('raw')
  })
})

describe('aggregateConnState', () => {
  test('connected wins over any other source state', () => {
    expect(aggregateConnState(['connected', 'reconnecting'])).toBe('connected')
    expect(aggregateConnState(['disconnected', 'connected'])).toBe('connected')
  })

  test('falls through connecting → reconnecting → disconnected', () => {
    expect(aggregateConnState(['connecting', 'reconnecting'])).toBe('connecting')
    expect(aggregateConnState(['reconnecting', 'disconnected'])).toBe('reconnecting')
    expect(aggregateConnState(['disconnected'])).toBe('disconnected')
  })

  test('no sources reads as disconnected', () => {
    expect(aggregateConnState([])).toBe('disconnected')
  })
})

describe('relayWsUrl', () => {
  test('empty input yields empty', () => {
    expect(relayWsUrl('')).toBe('')
    expect(relayWsUrl('   ')).toBe('')
  })

  test('bare host gets ws:// scheme and the stream path', () => {
    expect(relayWsUrl('localhost:7839')).toBe('ws://localhost:7839/api/v1/sessions/stream')
  })

  test('http(s) is rewritten to ws(s)', () => {
    expect(relayWsUrl('http://relay.example:7839')).toBe('ws://relay.example:7839/api/v1/sessions/stream')
    expect(relayWsUrl('https://relay.example')).toBe('wss://relay.example/api/v1/sessions/stream')
  })

  test('an explicit stream path is preserved (not doubled)', () => {
    expect(relayWsUrl('ws://localhost:7839/api/v1/sessions/stream'))
      .toBe('ws://localhost:7839/api/v1/sessions/stream')
  })
})

describe('compoundSessionId', () => {
  test('prefixes a relay daemon_id so colliding session_ids stay distinct', () => {
    const a = compoundSessionId('daemon-A', 'proc-1234')
    const b = compoundSessionId('daemon-B', 'proc-1234')
    expect(a).not.toBe(b)            // two daemons sharing a session_id never merge
    expect(a).toContain('proc-1234')
  })

  test('local / un-sourced frames keep the bare session_id', () => {
    expect(compoundSessionId('', 'proc-1234')).toBe('proc-1234')
    expect(compoundSessionId(undefined, 'proc-1234')).toBe('proc-1234')
  })

  test('delimiter cannot collide with a label-style daemon_id', () => {
    // daemon_ids can be arbitrary labels; a printable delimiter could split
    // wrong. The NUL delimiter keeps the boundary unambiguous.
    const id = compoundSessionId('my:weird/daemon|id', 'proc-1')
    expect(displaySessionId(id)).toBe('proc-1')
  })
})

describe('displaySessionId', () => {
  test('recovers the daemon-local id for display', () => {
    expect(displaySessionId(compoundSessionId('daemon-A', 'proc-1234'))).toBe('proc-1234')
  })

  test('passes bare (local) ids through unchanged', () => {
    expect(displaySessionId('proc-1234')).toBe('proc-1234')
    expect(displaySessionId('')).toBe('')
  })
})

describe('compound keying keeps two daemons distinct in an index', () => {
  test('same session_id + different daemon_id → two map entries / render keys', () => {
    const idx = new Map()
    const k1 = compoundSessionId('daemon-A', 'proc-1234')
    const k2 = compoundSessionId('daemon-B', 'proc-1234')
    idx.set(k1, { daemon: 'A' })
    idx.set(k2, { daemon: 'B' })
    expect(idx.size).toBe(2)                  // would be 1 under bare session_id keying
    expect('a:' + k1).not.toBe('a:' + k2)     // distinct reconciliation render keys
  })
})

describe('sessionOrigin / sourceIdOf (#538 origin glyph)', () => {
  test('a compound (relay) id is remote; a bare (local) id is local', () => {
    expect(sessionOrigin({ session_id: compoundSessionId('daemon-A', 'proc-1') })).toBe('remote')
    expect(sessionOrigin({ session_id: 'proc-1' })).toBe('local')
    expect(sessionOrigin({})).toBe('local')
    expect(sessionOrigin(null)).toBe('local')
  })

  test('sourceIdOf recovers the daemon id from a compound id, else empty', () => {
    expect(sourceIdOf(compoundSessionId('daemon-A', 'proc-1'))).toBe('daemon-A')
    expect(sourceIdOf(compoundSessionId('my:weird/label', 'proc-9'))).toBe('my:weird/label')
    expect(sourceIdOf('proc-1')).toBe('')
    expect(sourceIdOf('')).toBe('')
  })
})

describe('local-wins shadowing (#538)', () => {
  const groups = [{
    name: 'proj',
    agents: [
      { session_id: 'proc-1' },                                      // local
      { session_id: compoundSessionId('daemon-A', 'proc-1') },       // relay dup of the local one
      { session_id: compoundSessionId('daemon-B', 'proc-9') },       // relay-only
    ],
  }]

  test('localBareIds collects only local-origin ids', () => {
    expect(localBareIds(groups)).toEqual(new Set(['proc-1']))
  })

  test('a relay session whose bare id is local is shadowed (collapses to local)', () => {
    const localIds = localBareIds(groups)
    expect(isShadowedRemote({ session_id: compoundSessionId('daemon-A', 'proc-1') }, localIds)).toBe(true)
  })

  test('a relay-only session and a plain local session are not shadowed', () => {
    const localIds = localBareIds(groups)
    expect(isShadowedRemote({ session_id: compoundSessionId('daemon-B', 'proc-9') }, localIds)).toBe(false)
    expect(isShadowedRemote({ session_id: 'proc-1' }, localIds)).toBe(false)
  })

  test('two different-daemon remotes sharing a session_id both survive (neither local)', () => {
    const g = [{ name: 'p', agents: [
      { session_id: compoundSessionId('daemon-A', 'proc-7') },
      { session_id: compoundSessionId('daemon-B', 'proc-7') },
    ]}]
    const localIds = localBareIds(g)
    expect(localIds.size).toBe(0)
    expect(isShadowedRemote(g[0].agents[0], localIds)).toBe(false)
    expect(isShadowedRemote(g[0].agents[1], localIds)).toBe(false)
  })
})

describe('daemonSessionIds (#540 drop a disconnected daemon\'s rows)', () => {
  const groups = [{
    name: 'proj',
    agents: [
      { session_id: 'proc-1' },                                 // local — untouched
      { session_id: compoundSessionId('daemon-A', 'proc-2') },  // daemon A
      { session_id: compoundSessionId('daemon-A', 'proc-3') },  // daemon A
      { session_id: compoundSessionId('daemon-B', 'proc-4') },  // daemon B
    ],
  }]

  test('returns only the target daemon\'s session ids', () => {
    expect(daemonSessionIds(groups, 'daemon-A')).toEqual([
      compoundSessionId('daemon-A', 'proc-2'),
      compoundSessionId('daemon-A', 'proc-3'),
    ])
    expect(daemonSessionIds(groups, 'daemon-B')).toEqual([
      compoundSessionId('daemon-B', 'proc-4'),
    ])
  })

  test('never matches local sessions, and is empty for an unknown/empty daemon', () => {
    expect(daemonSessionIds(groups, 'daemon-Z')).toEqual([])
    expect(daemonSessionIds(groups, '')).toEqual([])
    expect(daemonSessionIds(groups, undefined)).toEqual([])
  })
})

describe('taskEtaPresentation', () => {
  const now = 1769000000
  const metricsFor = (over) => ({
    task_estimate: { total_rounds: 10, completed_rounds: 6, updated_at: now - 30, ...((over && over.est) || {}) },
    task_completion_eta: now + 720,
    ...((over && over.metrics) || {}),
  })

  test('point estimate once half the rounds are done', () => {
    const info = taskEtaPresentation(metricsFor(), 'working', now)
    expect(info).not.toBeNull()
    expect(info.text).toBe('~12m left')
    expect(info.stale).toBe(false)
    expect(info.title).toContain('6/10 rounds')
  })

  test('range when completed fraction is below half', () => {
    const info = taskEtaPresentation(metricsFor({ est: { completed_rounds: 2 } }), 'working', now)
    expect(info.text).toBe('~12m–18m left')
  })

  test('stale when the last marker is older than 3 minutes', () => {
    const info = taskEtaPresentation(metricsFor({ est: { updated_at: now - 200 } }), 'working', now)
    expect(info.stale).toBe(true)
  })

  test('suppressed when not working', () => {
    expect(taskEtaPresentation(metricsFor(), 'waiting', now)).toBeNull()
    expect(taskEtaPresentation(metricsFor(), 'ready', now)).toBeNull()
  })

  test('suppressed without estimate, eta, or reported progress', () => {
    expect(taskEtaPresentation({}, 'working', now)).toBeNull()
    expect(taskEtaPresentation(metricsFor({ metrics: { task_completion_eta: undefined } }), 'working', now)).toBeNull()
    expect(taskEtaPresentation(metricsFor({ est: { completed_rounds: 0 } }), 'working', now)).toBeNull()
  })

  test('eta in the past clamps to <1m, never negative', () => {
    const info = taskEtaPresentation(metricsFor({ metrics: { task_completion_eta: now - 5 } }), 'working', now)
    expect(info.text).toBe('~<1m left')
  })

  test('hour-scale remaining uses h+m resolution', () => {
    const info = taskEtaPresentation(metricsFor({ metrics: { task_completion_eta: now + 5400 } }), 'working', now)
    expect(info.text).toBe('~1h30m left')
  })
})
