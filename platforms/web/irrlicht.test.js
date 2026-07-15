import { describe, test, expect, beforeEach, afterEach } from 'vitest'
import { formatCO2, co2TierTitle } from './formatters.js'
import {
  pendingWizardAgents,
  stillPendingForAgents,
  buildPermissionAnswers,
  resolvedTheme,
  rowLabel,
  maybeNotifyOnUpdate,
  formatCost,
  costCellDisplay,
  formatUsageCost,
  pressureClass,
  taskEtaPresentation,
  historyPriorityForState,
  lastNotifiedPressure,
  relayFrameKind,
  seqGap,
  aggregateConnState,
  relayWsUrl,
  compoundSessionId,
  displaySessionId,
  sessionOrigin,
  sourceIdOf,
  localBareIds,
  isShadowedRemote,
  daemonSessionIds,
  structureSignature,
  historyQuery,
  histTokens,
  histCount,
  histCO2,
  histDoraPerWeek,
  histDoraPercent,
  histDoraHours,
  CHART_LABELS,
  DRILL_NEXT,
  historyRunningSum,
  CO2_EQUIVALENTS,
  pickCO2Equivalents,
  stateCellCounts,
  stateCellTotal,
  stateMatrixMaxTotal,
  stateBucketLabel,
  setActivityChartEnabled,
} from './irrlicht.js'
// The gate's reset path needs the module's private historyState to actually be
// on chart=state, which only a real click can do — so this suite drives the
// wired-up control rather than a state literal. Same module instance irrlicht.js
// imported (ESM caches), so historyState is shared.
import { initHistoryTab } from './historyTab.js'

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

describe('formatCO2', () => {
  test('returns empty string for zero or falsy', () => {
    expect(formatCO2(0)).toBe('')
    expect(formatCO2(null)).toBe('')
    expect(formatCO2(undefined)).toBe('')
  })

  test('formats sub-gram values in milligrams', () => {
    expect(formatCO2(0.03)).toBe('30mg CO2e')
  })

  test('formats gram-range values with one decimal', () => {
    expect(formatCO2(1.5)).toBe('1.5g CO2e')
    expect(formatCO2(158.7)).toBe('158.7g CO2e')
  })

  test('formats kilogram-range values with two decimals', () => {
    expect(formatCO2(2850)).toBe('2.85kg CO2e')
  })
})

describe('co2TierTitle', () => {
  test('names the provider disclosure for provider_disclosed tier', () => {
    expect(co2TierTitle('provider_disclosed')).toMatch(/provider-published/)
  })

  test('discloses the fallback approximation for any other tier', () => {
    expect(co2TierTitle('fallback')).toMatch(/cross-model fallback/)
    expect(co2TierTitle(undefined)).toMatch(/cross-model fallback/)
  })
})

describe('costCellDisplay', () => {
  const metrics = { estimated_cost_usd: 1.5, estimated_co2_grams: 30, co2_tier: 'provider_disclosed' }

  test('shows cost by default', () => {
    expect(costCellDisplay(metrics, 'cost')).toEqual({ text: '$1.50', title: 'Click to show CO2 estimate' })
  })

  test('shows CO2 with its tier tooltip in co2 mode', () => {
    const { text, title } = costCellDisplay(metrics, 'co2')
    expect(text).toBe('30.0g CO2e')
    expect(title).toMatch(/provider-published/)
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

describe('seqGap', () => {
  test('gaps when a stamped seq skips ahead of the cursor', () => {
    expect(seqGap(5, 7)).toBe(true)
    expect(seqGap(1, 100)).toBe(true)
  })

  test('contiguous delivery never gaps', () => {
    expect(seqGap(5, 6)).toBe(false)
    expect(seqGap(5, 5)).toBe(false) // duplicate
  })

  test('first stamped message after a fresh cursor never gaps', () => {
    expect(seqGap(0, 42)).toBe(false)
  })

  test('backward jump (daemon restart) is not a gap', () => {
    expect(seqGap(100, 3)).toBe(false)
  })

  test('unstamped frames (older daemons, snapshots) never gap', () => {
    expect(seqGap(5, 0)).toBe(false)
    expect(seqGap(5, undefined)).toBe(false)
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

  test('missing completed_rounds falls back to the zero-rounds "estimating…" chip, not a broken undefined/N label', () => {
    // Regression: `completed_rounds <= 0` is false for undefined (unlike the
    // intended `!(completed_rounds > 0)`), which would otherwise fall through
    // to the rounds-only/projected paths and render "undefined/10 rounds".
    const info = taskEtaPresentation(metricsFor({ est: { completed_rounds: undefined, updated_at: undefined }, metrics: { task_completion_eta: undefined } }), 'working', now)
    expect(info).not.toBeNull()
    expect(info.text).toBe('estimating…')
  })

  test('missing total_rounds (alongside zero completed_rounds) returns null instead of a broken 0/undefined label', () => {
    // Same regression class as the completed_rounds case above, in
    // zeroRoundsEtaPresentation's own total_rounds guard.
    const info = taskEtaPresentation(metricsFor({ est: { completed_rounds: 0, total_rounds: undefined } }), 'working', now)
    expect(info).toBeNull()
  })

  test('point estimate at the marker once half the rounds are done', () => {
    const info = taskEtaPresentation(metricsFor({ est: { updated_at: now } }), 'working', now)
    expect(info).not.toBeNull()
    expect(info.text).toBe('~12m left')
    expect(info.stale).toBe(false)
    expect(info.title).toContain('6/10 rounds')
  })

  test('point estimate widens into a range between markers — high pinned, no bare countdown (#616)', () => {
    const m = metricsFor({ est: { updated_at: now } })
    expect(taskEtaPresentation(m, 'working', now).text).toBe('~12m left')
    expect(taskEtaPresentation(m, 'working', now + 120).text).toBe('~10m–12m left')
    expect(taskEtaPresentation(m, 'working', now + 300).text).toBe('~7m–12m left')
  })

  test('no marker timestamp at/above half keeps the bare point countdown', () => {
    // Nothing to pin a high bound to (pre-#604 daemon) — old behavior.
    const m = metricsFor({ est: { updated_at: 0 } })
    expect(taskEtaPresentation(m, 'working', now).text).toBe('~12m left')
    expect(taskEtaPresentation(m, 'working', now + 120).text).toBe('~10m left')
  })

  test('range when completed fraction is below half — high pinned at the marker', () => {
    // high = 1.5 × (eta − updated_at) = 1.5 × 750s = 1125s → 19m
    const info = taskEtaPresentation(metricsFor({ est: { completed_rounds: 2 } }), 'working', now)
    expect(info.text).toBe('~12m–19m left')
  })

  test('low bound counts down between markers; high stays pinned', () => {
    const m = metricsFor({ est: { completed_rounds: 2 } })
    expect(taskEtaPresentation(m, 'working', now).text).toBe('~12m–19m left')
    expect(taskEtaPresentation(m, 'working', now + 120).text).toBe('~10m–19m left')
  })

  test('range with sub-minute low collapses to its upper bound — one sign', () => {
    const m = metricsFor({ est: { completed_rounds: 2 }, metrics: { task_completion_eta: now + 30 } })
    // high = 1.5 × (30 + 30) = 90s → 2m; low <1m → "<2m left", never "~<1m–2m"
    expect(taskEtaPresentation(m, 'working', now).text).toBe('<2m left')
  })

  test('stale when the last marker is older than 3 minutes', () => {
    const info = taskEtaPresentation(metricsFor({ est: { updated_at: now - 200 } }), 'working', now)
    expect(info.stale).toBe(true)
  })

  test('suppressed when not working', () => {
    expect(taskEtaPresentation(metricsFor(), 'waiting', now)).toBeNull()
    expect(taskEtaPresentation(metricsFor(), 'ready', now)).toBeNull()
  })

  test('suppressed without estimate', () => {
    expect(taskEtaPresentation({}, 'working', now)).toBeNull()
    // Progress but no eta is no longer suppressed — it renders a
    // rounds-only chip (#626), covered below.
  })

  test('zero completed rounds without a projection falls back to "estimating…" (#602)', () => {
    // No measured rate AND no prior projection (e.g. a subagent aggregate) —
    // keep the progress-only "estimating…" chip.
    const m = metricsFor({ est: { completed_rounds: 0 }, metrics: { task_completion_eta: undefined } })
    const info = taskEtaPresentation(m, 'working', now)
    expect(info).not.toBeNull()
    expect(info.text).toBe('estimating…')
    expect(info.stale).toBe(false)
    expect(info.title).toContain('0/10 rounds')
  })

  test('zero completed rounds with a prior eta renders a wide range (#753)', () => {
    // The daemon projects from a corpus prior at the first marker, so a real
    // number appears immediately — shown as a deliberately wide (2×) range.
    const m = metricsFor({ est: { completed_rounds: 0, updated_at: now }, metrics: { task_completion_eta: now + 600 } })
    const info = taskEtaPresentation(m, 'working', now)
    expect(info).not.toBeNull()
    expect(info.text).toBe('~10m–20m left')
    expect(info.stale).toBe(false)
    expect(info.title).toContain('0/10 rounds')
    expect(info.title).toContain('rough prior')
  })

  test('zero completed rounds: stale past 3min, hidden without total or while not working', () => {
    const zero = (over) => metricsFor({ est: { completed_rounds: 0, ...(over || {}) }, metrics: { task_completion_eta: undefined } })
    expect(taskEtaPresentation(zero({ updated_at: now - 200 }), 'working', now).stale).toBe(true)
    expect(taskEtaPresentation(zero({ total_rounds: 0 }), 'working', now)).toBeNull()
    expect(taskEtaPresentation(zero(), 'ready', now)).toBeNull()
  })

  test('progress without a projection renders a rounds-only chip (#626)', () => {
    const m = metricsFor({ est: { source: 'subagents' }, metrics: { task_completion_eta: null } })
    const info = taskEtaPresentation(m, 'working', now)
    expect(info).not.toBeNull()
    expect(info.text).toBe('6/10')
    expect(info.stale).toBe(false)
    expect(info.title).toContain('from subagents')
    expect(info.title).toContain('6/10 rounds')
  })

  test('tooltip attributes the estimate source (#604)', () => {
    expect(taskEtaPresentation(metricsFor(), 'working', now).title).toContain('agent-reported')
    const tasks = metricsFor({ est: { source: 'tasks' } })
    expect(taskEtaPresentation(tasks, 'working', now).title).toContain('from task list')
    const subagents = metricsFor({ est: { source: 'subagents' } })
    expect(taskEtaPresentation(subagents, 'working', now).title).toContain('from subagents')
  })

  test('eta in the past clamps to <1m, never negative — one sign only', () => {
    const info = taskEtaPresentation(metricsFor({ metrics: { task_completion_eta: now - 5 } }), 'working', now)
    expect(info.text).toBe('<1m left')
  })

  test('long overdue: upper bound is the at-marker projection, not a stuck <1m (#616)', () => {
    // Marker 10min ago projected 5min of work; the eta passed 5min ago.
    // Worst case the full projected 5m still lies ahead — never a
    // confident "<1m left" while the agent may be stuck.
    const m = metricsFor({ est: { updated_at: now - 600 }, metrics: { task_completion_eta: now - 300 } })
    expect(taskEtaPresentation(m, 'working', now).text).toBe('<5m left')
  })

  test('hour-scale remaining uses h+m resolution', () => {
    const info = taskEtaPresentation(metricsFor({ est: { updated_at: now }, metrics: { task_completion_eta: now + 5400 } }), 'working', now)
    expect(info.text).toBe('~1h30m left')
  })
})

describe('group traversal recurses Gas Town rig sub-groups (#559)', () => {
  // A gastown group with a global agent plus two nested rigs whose worker
  // sessions live in `groups[].agents`, not the top-level `agents`.
  const groups = [{
    name: 'Gas Town',
    type: 'gastown',
    agents: [{ session_id: 'mayor-1' }],                          // local global agent
    groups: [
      { name: 'rig-1', agents: [
        { session_id: 'polecat-1' },                              // local
        { session_id: compoundSessionId('daemon-A', 'witness-1') }, // relay
      ]},
      { name: 'rig-2', agents: [{ session_id: 'crew-1' }] },      // local
    ],
  }]

  test('localBareIds includes rig worker ids nested under sub-groups', () => {
    expect(localBareIds(groups)).toEqual(new Set(['mayor-1', 'polecat-1', 'crew-1']))
  })

  test('daemonSessionIds finds a relay session nested in a rig', () => {
    expect(daemonSessionIds(groups, 'daemon-A')).toEqual([
      compoundSessionId('daemon-A', 'witness-1'),
    ])
  })
})

describe('structureSignature (#559 — detects nesting/role changes the WS deltas miss)', () => {
  const base = () => [{
    name: 'gt', type: 'gastown',
    agents: [{ session_id: 'mayor-1', role: 'mayor', icon: '🎩' }],
    groups: [{ name: 'rig-1', status: 'operational', agents: [
      { session_id: 'pc-1', role: 'polecat', icon: '👷', worker_id: 'bead-1' },
    ]}],
  }]

  test('identical trees produce identical signatures', () => {
    expect(structureSignature(base())).toBe(structureSignature(base()))
  })

  test('metric-only changes do NOT change the signature (no needless rebuild)', () => {
    const a = base()
    const b = base()
    b[0].agents[0].metrics = { estimated_cost_usd: 9.99, context_utilization_percentage: 80 }
    b[0].costs = { day: 5 }
    expect(structureSignature(b)).toBe(structureSignature(a))
  })

  test('a session gaining a role/icon changes the signature (emoji fix)', () => {
    const before = [{ name: 'gt', type: 'gastown', agents: [{ session_id: 's1' }] }]
    const after = [{ name: 'gt', type: 'gastown', agents: [{ session_id: 's1', role: 'witness', icon: '🦉' }] }]
    expect(structureSignature(after)).not.toBe(structureSignature(before))
  })

  test('re-nesting a session under a rig changes the signature (nesting fix)', () => {
    const flat = [
      { name: 'gt', type: 'gastown', agents: [] },
      { name: 'rig-1', agents: [{ session_id: 's1', role: 'refinery' }] },
    ]
    const nested = [
      { name: 'gt', type: 'gastown', agents: [], groups: [
        { name: 'rig-1', agents: [{ session_id: 's1', role: 'refinery' }] },
      ]},
    ]
    expect(structureSignature(nested)).not.toBe(structureSignature(flat))
  })
})

describe('permission wizard (#570)', () => {
  const snap = (overrides = {}) => ({
    mode: 'ask',
    agents: [
      {
        name: 'claude-code', display_name: 'Claude Code', detected: true,
        permissions: [
          { key: 'transcripts', kind: 'observe', state: 'pending', title: 'Read transcripts' },
          { key: 'hooks', kind: 'modify', state: 'pending', title: 'Install hooks' },
        ],
      },
      {
        name: 'codex', display_name: 'Codex', detected: false,
        permissions: [{ key: 'transcripts', kind: 'observe', state: 'pending' }],
      },
      {
        name: 'pi', display_name: 'Pi', detected: true,
        permissions: [{ key: 'transcripts', kind: 'observe', state: 'granted' }],
      },
    ],
    ...overrides,
  })

  test('pendingWizardAgents: only detected agents with pending permissions', () => {
    const got = pendingWizardAgents(snap())
    expect(got.map(a => a.name)).toEqual(['claude-code'])
  })

  test('pendingWizardAgents: empty in grant-all mode (wizard suppressed)', () => {
    expect(pendingWizardAgents(snap({ mode: 'grant-all' }))).toEqual([])
  })

  test('pendingWizardAgents: tolerates null/missing fields', () => {
    expect(pendingWizardAgents(null)).toEqual([])
    expect(pendingWizardAgents({})).toEqual([])
    expect(pendingWizardAgents({ mode: 'ask', agents: [{ name: 'x', detected: true }] })).toEqual([])
  })

  test('pendingWizardAgents: an upgrade-added pending item re-triggers for an answered agent', () => {
    const s = snap()
    s.agents[2].permissions.push({ key: 'newthing', kind: 'observe', state: 'pending' })
    expect(pendingWizardAgents(s).map(a => a.name)).toEqual(['claude-code', 'pi'])
  })

  test('buildPermissionAnswers: auto mode answers every displayed pending item explicitly', () => {
    const draft = {
      'claude-code/transcripts': true,
      'claude-code/hooks': false,
    }
    const got = buildPermissionAnswers(snap(), draft, true)
    expect(got).toEqual([
      { agent: 'claude-code', permission: 'transcripts', grant: true },
      { agent: 'claude-code', permission: 'hooks', grant: false },
    ])
  })

  test('buildPermissionAnswers: auto mode never touches already-answered items', () => {
    const draft = { 'pi/transcripts': false } // answered (granted) — not in the auto wizard
    expect(buildPermissionAnswers(snap(), draft, true)).toEqual([])
  })

  test('buildPermissionAnswers: review mode submits only changes', () => {
    const draft = {
      'pi/transcripts': false,        // granted → off: revoke
      'claude-code/hooks': true,      // pending → answered grant
      'codex/transcripts': false,     // pending → answered deny
    }
    const got = buildPermissionAnswers(snap(), draft, false)
    expect(got).toContainEqual({ agent: 'pi', permission: 'transcripts', grant: false })
    expect(got).toContainEqual({ agent: 'claude-code', permission: 'hooks', grant: true })
    expect(got).toContainEqual({ agent: 'codex', permission: 'transcripts', grant: false })
    expect(got).toHaveLength(3)
  })

  test('buildPermissionAnswers: review mode skips unchanged grants', () => {
    const draft = { 'pi/transcripts': true } // granted → on: unchanged
    expect(buildPermissionAnswers(snap(), draft, false)).toEqual([])
  })

  test('buildPermissionAnswers: ignores draft keys not in the snapshot', () => {
    const draft = { 'ghost/agent': true }
    expect(buildPermissionAnswers(snap(), draft, false)).toEqual([])
    expect(buildPermissionAnswers(null, draft, false)).toEqual([])
  })
})

describe('stillPendingForAgents (#570 locked-set dismissal)', () => {
  const snap = (state) => ({
    mode: 'ask',
    agents: [
      { name: 'claude-code', detected: false, // process exited mid-decision
        permissions: [{ key: 'transcripts', state }] },
      { name: 'codex', detected: true,
        permissions: [{ key: 'transcripts', state: 'pending' }] },
    ],
  })

  test('locked agent still pending keeps the wizard up even when undetected', () => {
    expect(stillPendingForAgents(snap('pending'), ['claude-code'])).toBe(true)
  })

  test('locked agent fully answered dismisses (other agents do not count)', () => {
    // codex is still pending but was NOT locked into this wizard.
    expect(stillPendingForAgents(snap('granted'), ['claude-code'])).toBe(false)
    expect(stillPendingForAgents(snap('denied'), ['claude-code'])).toBe(false)
  })

  test('tolerates null/missing input', () => {
    expect(stillPendingForAgents(null, ['x'])).toBe(false)
    expect(stillPendingForAgents({}, ['x'])).toBe(false)
    expect(stillPendingForAgents(snap('pending'), null)).toBe(false)
  })
})

describe('histTokens', () => {
  test('formats millions, thousands, and units', () => {
    expect(histTokens(2_000_000)).toBe('2.0M')
    expect(histTokens(1500)).toBe('1.5k')
    expect(histTokens(970)).toBe('970')
    expect(histTokens(0)).toBe('0')
  })
})

describe('historyQuery (#750 chart/group/scope params)', () => {
  const base = { range: 'day', chart: 'cost', group: 'project', forecast: true, start: null, end: null, scope: null }

  test('emits chart, group, forecast, range', () => {
    const q = new URLSearchParams(historyQuery(base))
    expect(q.get('chart')).toBe('cost')
    expect(q.get('group')).toBe('project')
    expect(q.get('forecast')).toBe('true')
    expect(q.get('range')).toBe('day')
    expect(q.get('scope')).toBeNull()
  })

  test('includes scope when drilled down', () => {
    const q = new URLSearchParams(historyQuery({ ...base, chart: 'tokens', group: 'branch', scope: { field: 'project', value: 'irrlicht' } }))
    expect(q.get('chart')).toBe('tokens')
    expect(q.get('group')).toBe('branch')
    expect(q.get('scope')).toBe('project:irrlicht')
  })

  test('a custom range sends start/end instead of range', () => {
    const q = new URLSearchParams(historyQuery({ ...base, range: 'custom', start: 900, end: 2000 }))
    expect(q.get('start')).toBe('900')
    expect(q.get('end')).toBe('2000')
    expect(q.get('range')).toBeNull()
  })
})

describe('historyQuery cross-filters (#750 faceted)', () => {
  const base = {
    range: 'day', chart: 'tokens', group: 'project', forecast: true, start: null, end: null, scope: null,
    filters: { provider: ['anthropic'], token_type: ['input', 'output'], project: ['x'] },
  }

  test('emits non-grouped filters and drops the grouped dimension', () => {
    const q = new URLSearchParams(historyQuery(base))
    expect(q.get('provider')).toBe('anthropic')
    expect(q.get('token_type')).toBe('input,output')
    expect(q.get('project')).toBeNull() // project is the active group
  })

  test('token_type filter is omitted unless the tokens metric is active', () => {
    const q = new URLSearchParams(historyQuery({ ...base, chart: 'cost' }))
    expect(q.get('token_type')).toBeNull()
    expect(q.get('provider')).toBe('anthropic')
  })

  test('empty filter sets emit nothing', () => {
    const q = new URLSearchParams(historyQuery({ ...base, filters: { provider: [], token_type: [], project: [] } }))
    expect(q.get('provider')).toBeNull()
    expect(q.get('token_type')).toBeNull()
  })

  test('a missing filters field is tolerated (back-compat)', () => {
    const q = new URLSearchParams(historyQuery({ range: 'day', chart: 'cost', group: 'project', forecast: true, start: null, end: null, scope: null }))
    expect(q.get('provider')).toBeNull()
  })
})

describe('historyQuery dora project (#951)', () => {
  const base = { range: 'day', chart: 'dora', group: 'project', forecast: true, start: null, end: null, scope: null }

  test('emits project when chart=dora and a project is selected', () => {
    const q = new URLSearchParams(historyQuery({ ...base, doraProject: 'irrlicht' }))
    expect(q.get('chart')).toBe('dora')
    expect(q.get('project')).toBe('irrlicht')
  })

  test('omits project when none selected', () => {
    const q = new URLSearchParams(historyQuery({ ...base, doraProject: null }))
    expect(q.get('project')).toBeNull()
  })

  test('omits project for non-dora charts even if doraProject is set', () => {
    const q = new URLSearchParams(historyQuery({ ...base, chart: 'cost', doraProject: 'irrlicht' }))
    expect(q.get('project')).toBeNull()
  })
})

describe('historyRunningSum (cumulative chart)', () => {
  test('produces a monotonic running total', () => {
    expect(historyRunningSum([1, 0, 2, 0, 3])).toEqual([1, 1, 3, 3, 6])
  })
  test('tolerates empty/nullish input', () => {
    expect(historyRunningSum([])).toEqual([])
    expect(historyRunningSum(null)).toEqual([])
    expect(historyRunningSum([1, null, 2])).toEqual([1, 1, 3])
  })
})

describe('DRILL_NEXT (drilldown axis order)', () => {
  test('project → branch → session; provider/model → session; session is a leaf', () => {
    expect(DRILL_NEXT.project).toBe('branch')
    expect(DRILL_NEXT.branch).toBe('session')
    expect(DRILL_NEXT.provider).toBe('model')
    expect(DRILL_NEXT.model).toBe('session')
    expect(DRILL_NEXT.session).toBeUndefined()
  })
})

describe('agents chart (#751 Phase 3)', () => {
  test('chart=agents serializes with project grouping', () => {
    const q = new URLSearchParams(historyQuery({ range: 'day', chart: 'agents', group: 'project', forecast: true, start: null, end: null, scope: null }))
    expect(q.get('chart')).toBe('agents')
    expect(q.get('group')).toBe('project')
  })

  test('CHART_LABELS includes Agents', () => {
    expect(CHART_LABELS.agents).toBe('Agents')
  })

  test('histCount renders an integer agent count', () => {
    expect(histCount(0)).toBe('0')
    expect(histCount(2)).toBe('2')
    expect(histCount(2.6)).toBe('3')
    expect(histCount(undefined)).toBe('0')
  })
})

describe('Activity chart beta gate (#1075)', () => {
  const btn = (chart) => document.querySelector(`#history-chart-sel button[data-chart="${chart}"]`)
  const isActive = (chart) => btn(chart).classList.contains('active')
  let host

  beforeEach(() => {
    // The real chart selector, trimmed to the three buttons this suite drives.
    // Activity ships hidden in index.html so a default-off gate never flashes it.
    host = document.createElement('div')
    host.innerHTML = `
      <fieldset id="history-chart-sel">
        <button data-chart="cost" class="active">Cost</button>
        <button data-chart="agents">Agents</button>
        <button data-chart="state" hidden>Activity</button>
      </fieldset>`
    document.body.append(host)
    initHistoryTab() // wires the click handler onto this fresh fieldset
  })
  afterEach(() => host.remove())

  test('the Activity button is hidden until the toggle turns it on', () => {
    expect(btn('state').hidden).toBe(true)
    setActivityChartEnabled(true)
    expect(btn('state').hidden).toBe(false)
    setActivityChartEnabled(false)
    expect(btn('state').hidden).toBe(true)
  })

  test('turning the toggle off while Activity is the live chart falls back to Cost', () => {
    setActivityChartEnabled(true)
    btn('state').click()
    expect(isActive('state')).toBe(true)

    setActivityChartEnabled(false)
    // Otherwise the view strands on a chart the setting says is off, with the
    // range row hidden and the granularity row showing.
    expect(isActive('state')).toBe(false)
    expect(isActive('cost')).toBe(true)
  })

  test('turning the toggle off leaves a non-Activity chart selected', () => {
    setActivityChartEnabled(true)
    btn('agents').click()
    setActivityChartEnabled(false)
    expect(isActive('agents')).toBe(true)
  })
})

describe('activity matrix (chart=state, #981)', () => {
  test('chart=state serializes with project grouping and a granularity param, not range', () => {
    const q = new URLSearchParams(historyQuery({ chart: 'state', group: 'project', granularity: '8h', forecast: true, scope: null, filters: {} }))
    expect(q.get('chart')).toBe('state')
    expect(q.get('group')).toBe('project')
    expect(q.get('granularity')).toBe('8h')
    expect(q.get('range')).toBeNull()
  })

  test('CHART_LABELS includes Activity', () => {
    expect(CHART_LABELS.state).toBe('Activity')
  })

  const sampleData = {
    projects: ['projA', 'projB'],
    bucket_starts: [1000, 2000, 3000],
    by_state: {
      working: { projA: [1, 2, 0] },
      waiting: { projA: [0, 1, 1], projB: [3, 0, 0] },
      ready: { projA: [0, 0, 1] },
    },
  }

  test('stateCellCounts defaults missing states/projects to 0', () => {
    expect(stateCellCounts(sampleData, 'projA', 1)).toEqual({ working: 2, waiting: 1, ready: 0 })
    expect(stateCellCounts(sampleData, 'projB', 0)).toEqual({ working: 0, waiting: 3, ready: 0 })
    expect(stateCellCounts(sampleData, 'unknownProject', 0)).toEqual({ working: 0, waiting: 0, ready: 0 })
  })

  test('stateCellTotal sums working+waiting+ready for one cell', () => {
    expect(stateCellTotal(sampleData, 'projA', 1)).toBe(3)
    expect(stateCellTotal(sampleData, 'projA', 2)).toBe(2)
  })

  test('stateMatrixMaxTotal finds the busiest cell across the whole grid', () => {
    // projA totals: 1, 3, 2 — projB totals: 3, 0, 0. Busiest is 3.
    expect(stateMatrixMaxTotal(sampleData)).toBe(3)
  })

  test('stateMatrixMaxTotal is 0 for an empty grid', () => {
    expect(stateMatrixMaxTotal({ projects: [], bucket_starts: [], by_state: {} })).toBe(0)
    expect(stateMatrixMaxTotal(null)).toBe(0)
  })

  test('stateBucketLabel coarsens format as granularity widens', () => {
    const ts = Math.floor(new Date('2026-03-15T14:30:00Z').getTime() / 1000)
    expect(stateBucketLabel(ts, '1y')).toBe('2026')
    expect(stateBucketLabel(ts, '1mo')).toMatch(/Mar.*26/)
    expect(stateBucketLabel(ts, '24h')).toMatch(/Mar/)
    // Fine granularities (minutes/hours) render a time-of-day, not a date.
    expect(stateBucketLabel(ts, '60m')).not.toMatch(/Mar|2026/)
  })
})

describe('co2 chart (issue #829)', () => {
  test('chart=co2 serializes with project grouping', () => {
    const q = new URLSearchParams(historyQuery({ range: 'day', chart: 'co2', group: 'project', forecast: true, start: null, end: null, scope: null }))
    expect(q.get('chart')).toBe('co2')
    expect(q.get('group')).toBe('project')
  })

  test('CHART_LABELS includes CO2', () => {
    expect(CHART_LABELS.co2).toBe('CO2')
  })

  test('histCO2 is unit-adaptive and always renders a value', () => {
    expect(histCO2(0)).toBe('0mg')
    expect(histCO2(0.03)).toBe('30mg')
    expect(histCO2(158.7)).toBe('158.7g')
    expect(histCO2(2850)).toBe('2.85kg')
    expect(histCO2(undefined)).toBe('0mg')
  })
})

describe('dora chart (#951)', () => {
  test('chart=dora serializes with the selected project', () => {
    const q = new URLSearchParams(historyQuery({
      range: 'day', chart: 'dora', group: 'project', forecast: true, start: null, end: null, scope: null, doraProject: 'irrlicht',
    }))
    expect(q.get('chart')).toBe('dora')
    expect(q.get('project')).toBe('irrlicht')
  })

  test('CHART_LABELS includes DORA', () => {
    expect(CHART_LABELS.dora).toBe('DORA')
  })

  test('histDoraPerWeek renders one decimal place', () => {
    expect(histDoraPerWeek(2.551)).toBe('2.6/week')
    expect(histDoraPerWeek(undefined)).toBe('0.0/week')
  })

  test('histDoraPercent rounds to a whole percent', () => {
    expect(histDoraPercent(42.9)).toBe('43%')
    expect(histDoraPercent(0)).toBe('0%')
  })

  test('histDoraHours is unit-adaptive: hours below a day, days at or above', () => {
    expect(histDoraHours(8)).toBe('8 hours')
    expect(histDoraHours(23.9)).toBe('24 hours')
    expect(histDoraHours(24)).toBe('1.0 days')
    expect(histDoraHours(48)).toBe('2.0 days')
    expect(histDoraHours(undefined)).toBe('0 hours')
  })
})

describe('CO2 equivalents (issue #952)', () => {
  test('CO2_EQUIVALENTS is ascending by grams with no duplicate ids', () => {
    for (let i = 1; i < CO2_EQUIVALENTS.length; i++) {
      expect(CO2_EQUIVALENTS[i].grams).toBeGreaterThan(CO2_EQUIVALENTS[i - 1].grams)
    }
    expect(new Set(CO2_EQUIVALENTS.map(e => e.id)).size).toBe(CO2_EQUIVALENTS.length)
  })

  test('pickCO2Equivalents returns nothing for a zero or negative axis', () => {
    expect(pickCO2Equivalents(0)).toEqual([])
    expect(pickCO2Equivalents(-5)).toEqual([])
    expect(pickCO2Equivalents(undefined)).toEqual([])
  })

  test('a tiny axis (below the smallest equivalent) draws no lines', () => {
    expect(pickCO2Equivalents(0.05)).toEqual([])
  })

  test('every pick sits under the axis ceiling and none repeat', () => {
    const picks = pickCO2Equivalents(2_000_000)
    expect(picks.length).toBeGreaterThan(0)
    for (const eq of picks) expect(eq.grams).toBeLessThan(2_000_000 * 0.98)
    expect(new Set(picks.map(e => e.id)).size).toBe(picks.length)
  })

  test('picks are sorted ascending by grams', () => {
    const picks = pickCO2Equivalents(1_000_000)
    for (let i = 1; i < picks.length; i++) expect(picks[i].grams).toBeGreaterThan(picks[i - 1].grams)
  })

  test('a small axis only surfaces small-scale equivalents (no flights for a few grams)', () => {
    const picks = pickCO2Equivalents(100)
    expect(picks.every(eq => eq.grams < 100)).toBe(true)
    expect(picks.some(eq => eq.id.startsWith('flight'))).toBe(false)
  })

  test('a large axis is capped at 3 reference lines', () => {
    expect(pickCO2Equivalents(5_000_000).length).toBeLessThanOrEqual(3)
  })

  test('the list is dense across ~100g to ~100 tonnes, not just the original 10 entries', () => {
    expect(CO2_EQUIVALENTS.length).toBeGreaterThanOrEqual(17)
    expect(CO2_EQUIVALENTS[CO2_EQUIVALENTS.length - 1].grams).toBeGreaterThanOrEqual(100_000_000)
  })

  test('a 50kg axis picks well-spread equivalents, not clustered ones', () => {
    const picks = pickCO2Equivalents(50_000)
    expect(picks.map(e => e.id)).toEqual(['petrol-liter', 'running-shoes', 'flight-short'])
    expect(picks.map(e => e.grams)).toEqual([2350, 9500, 43800])
  })

  test('a ~10kg axis (issue #980) no longer clusters two picks together', () => {
    // Regression test for the reported bug: at this axis scale the original
    // sparse table picked grid-kwh (460g) and laundry (1500g) together, only
    // ~9% of the axis height apart — enough for their labels to overlap.
    const picks = pickCO2Equivalents(11_000)
    expect(picks.map(e => e.id)).toEqual(['grid-kwh', 'petrol-liter', 'running-shoes'])
    for (let i = 1; i < picks.length; i++) {
      const fractionalGap = (picks[i].grams - picks[i - 1].grams) / 11_000
      expect(fractionalGap).toBeGreaterThan(0.15)
    }
  })

  test('no two adjacent picks land within 5% of the axis height of each other, across the full range', () => {
    // Sweeps maxY across ~9 orders of magnitude (log-spaced) so a future edit
    // that reintroduces a sparse region gets caught here instead of shipping
    // as another overlapping-label bug.
    for (let exp = 0; exp <= 8.2; exp += 0.05) {
      const maxY = Math.pow(10, exp)
      const picks = pickCO2Equivalents(maxY)
      for (let i = 1; i < picks.length; i++) {
        const fractionalGap = (picks[i].grams - picks[i - 1].grams) / maxY
        expect(fractionalGap).toBeGreaterThan(0.04)
      }
    }
  })
})
