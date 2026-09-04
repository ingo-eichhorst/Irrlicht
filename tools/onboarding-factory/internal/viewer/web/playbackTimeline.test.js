import { describe, test, expect } from 'vitest'
import {
  isPresession, eventStyle, computeStateBand, computeEventDots, computeTurns,
  computeExpectedLane, deriveEventOffsets, findOffsetBefore, findOffsetAfter,
  resolveDashboardIframeUrl,
} from './playbackTimeline.js'

describe('isPresession', () => {
  test('proc-<pid> ids are pre-sessions', () => {
    expect(isPresession('proc-1234')).toBe(true)
  })
  test('UUID ids are real sessions', () => {
    expect(isPresession('abc-123-def')).toBe(false)
  })
  test('nullish / non-string → false', () => {
    expect(isPresession(null)).toBe(false)
    expect(isPresession(undefined)).toBe(false)
    expect(isPresession(1234)).toBe(false)
  })
})

describe('eventStyle', () => {
  test('transcript_new disambiguates pre-session vs real session', () => {
    expect(eventStyle({ kind: 'transcript_new', session_id: 'proc-1' }).label)
      .toBe('Process detected — waiting for transcript')
    expect(eventStyle({ kind: 'transcript_new', session_id: 'uuid-1' }).label)
      .toBe('Session started — transcript created')
  })
  test('pid_discovered on a UUID is a handoff re-bind', () => {
    expect(eventStyle({ kind: 'pid_discovered', session_id: 'proc-1' }).label)
      .toBe('PID identified for pre-session')
    expect(eventStyle({ kind: 'pid_discovered', session_id: 'uuid-1' }).label)
      .toBe('PID re-bound to UUID session (handoff)')
  })
  test('transcript_removed reads as handoff vs end depending on id', () => {
    expect(eventStyle({ kind: 'transcript_removed', session_id: 'proc-1' }).label)
      .toBe('Pre-session transcript dropped')
    expect(eventStyle({ kind: 'transcript_removed', session_id: 'uuid-1' }).label)
      .toBe('Session ended — transcript closed')
  })
  test('state_transition embeds the new state', () => {
    expect(eventStyle({ kind: 'state_transition', new_state: 'working' }).label)
      .toBe('State changed → working')
    expect(eventStyle({ kind: 'state_transition' }).label).toBe('State changed')
    expect(eventStyle({ kind: 'state_transition', new_state: 'working' }).color).toBe('#8b5cf6')
  })
  test('unknown kind falls back to a small slate dot labelled with the kind', () => {
    const st = eventStyle({ kind: 'mystery_event' })
    expect(st).toEqual({ color: '#94a3b8', size: 7, opacity: 0.5, label: 'mystery_event' })
  })
})

describe('computeStateBand', () => {
  test('no duration → no segments', () => {
    expect(computeStateBand([], 0)).toEqual([])
    expect(computeStateBand([{ session_id: 's1', kind: 'transcript_new', offset_ms: 0 }], undefined)).toEqual([])
  })

  test('events without a session_id contribute no segments', () => {
    expect(computeStateBand([{ kind: 'file_event', offset_ms: 50 }], 100)).toEqual([])
  })

  test('single session: ready → working → ready across its lifetime', () => {
    const events = [
      { session_id: 's1', kind: 'transcript_new', offset_ms: 0 },
      { session_id: 's1', kind: 'state_transition', new_state: 'working', offset_ms: 100 },
      { session_id: 's1', kind: 'state_transition', new_state: 'ready', offset_ms: 300 },
      { session_id: 's1', kind: 'process_exited', offset_ms: 500 },
    ]
    const band = computeStateBand(events, 600)
    expect(band.map(s => s.state)).toEqual(['ready', 'working', 'ready'])
    expect(band[0]).toMatchObject({ start: 0, end: 100, color: '#4ade80', leftPct: 0 })
    expect(band[1]).toMatchObject({ start: 100, end: 300, color: '#8b5cf6' })
    expect(band[2].leftPct).toBe(50) // 300 / 600
    expect(band[0].tip).toBe('ready\n+0.00s → +0.10s (0.10s)')
  })

  test('overlapping sessions aggregate to the highest-priority state', () => {
    const events = [
      { session_id: 's1', kind: 'transcript_new', offset_ms: 0 },
      { session_id: 's1', kind: 'state_transition', new_state: 'working', offset_ms: 0 },
      { session_id: 's2', kind: 'transcript_new', offset_ms: 0 },
      { session_id: 's2', kind: 'state_transition', new_state: 'ready', offset_ms: 0 },
    ]
    const band = computeStateBand(events, 100)
    expect(band).toHaveLength(1)
    expect(band[0]).toMatchObject({ start: 0, end: 100, state: 'working' })
  })
})

describe('computeEventDots', () => {
  test('no duration / no events → empty', () => {
    expect(computeEventDots([], 100)).toEqual([])
    expect(computeEventDots([{ kind: 'x', offset_ms: 1 }], 0)).toEqual([])
  })

  test('positions a dot and appends a session line for non-bookkeeping events', () => {
    const [dot] = computeEventDots([{ kind: 'transcript_new', session_id: 'proc-1', offset_ms: 50 }], 100)
    expect(dot).toMatchObject({ leftPct: 50, size: 14, color: '#3b82f6', opacity: 1 })
    expect(dot.tip).toBe('Process detected — waiting for transcript\n+0.05s\nsession: proc-1')
  })

  test('bookkeeping events (debounce_coalesced) omit the session line', () => {
    const [dot] = computeEventDots([{ kind: 'debounce_coalesced', session_id: 's1', offset_ms: 10 }], 100)
    expect(dot.tip).toBe('Bookkeeping — multiple updates coalesced\n+0.01s')
  })

  test('out-of-range offset clamps to 0..100', () => {
    expect(computeEventDots([{ kind: 'x', offset_ms: 200 }], 100)[0].leftPct).toBe(100)
    expect(computeEventDots([{ kind: 'x', offset_ms: -20 }], 100)[0].leftPct).toBe(0)
  })
})

describe('computeTurns', () => {
  test('no duration / no turns → empty', () => {
    expect(computeTurns([], 100)).toEqual([])
    expect(computeTurns([{ role: 'user', offset_ms: 1 }], 0)).toEqual([])
    expect(computeTurns(undefined, 100)).toEqual([])
  })

  test('user ticks pin to the top, assistant ticks to the bottom', () => {
    const [u] = computeTurns([{ role: 'user', offset_ms: 250, text: 'hi' }], 1000)
    expect(u).toEqual({ leftPct: 25, color: '#2563eb', top: '1px', tip: 'User\n+0.25s\nhi' })
    const [a] = computeTurns([{ role: 'assistant', offset_ms: 500, text: 'reply' }], 1000)
    expect(a).toEqual({ leftPct: 50, color: '#0d9488', top: '11px', tip: 'Assistant\n+0.50s\nreply' })
  })

  test('missing text renders an empty last line', () => {
    expect(computeTurns([{ role: 'user', offset_ms: 0 }], 1000)[0].tip).toBe('User\n+0.00s\n')
  })
})

describe('computeExpectedLane', () => {
  const start = '2024-01-01T00:00:00.000Z'

  test('no duration → null', () => {
    expect(computeExpectedLane({ phases: [{}] }, 0)).toBeNull()
  })

  test('no expected.jsonl → the "not configured" note', () => {
    expect(computeExpectedLane(null, 100)).toEqual({ note: 'expected: not configured', markers: [] })
    expect(computeExpectedLane({ phases: [] }, 100)).toEqual({ note: 'expected: not configured', markers: [] })
    expect(computeExpectedLane({}, 100)).toEqual({ note: 'expected: not configured', markers: [] })
  })

  test('matched state phase → positioned circle marker', () => {
    const rep = {
      recording_start: start,
      phases: [{ phase: 'p1', pass: true, matched_ts: '2024-01-01T00:00:00.500Z' }],
      definitions: [{ expected_state: 'working', text: 'should be working', max_delay_ms: 1000 }],
    }
    const { markers } = computeExpectedLane(rep, 1000)
    expect(markers).toHaveLength(1)
    expect(markers[0]).toMatchObject({ type: 'state', pos: 50, baseColor: '#8b5cf6', rimColor: '#22c55e' })
    expect(markers[0].tip).toBe('p1 — PASS\nshould be working\n+500 ms from recording start (target ≤ 1000 ms from anchor)')
  })

  test('failed, unmatched phase → left-pinned unmatched marker with the FAIL rim', () => {
    const rep = {
      recording_start: start,
      phases: [{ phase: 'p2', pass: false }],
      definitions: [{ expected_state: 'waiting' }],
    }
    const { markers } = computeExpectedLane(rep, 1000)
    expect(markers[0]).toMatchObject({ type: 'unmatched', pos: null, rimColor: '#dc2626' })
    expect(markers[0].tip).toBe('p2 — FAIL')
  })

  test('lifecycle phase (no expected_state) → tag marker labelled with the kind', () => {
    const rep = {
      recording_start: start,
      phases: [{ phase: 'lc', pass: true, matched_ts: '2024-01-01T00:00:00.250Z' }],
      definitions: [{ kind: 'transcript_new' }],
    }
    const { markers } = computeExpectedLane(rep, 1000)
    expect(markers[0]).toMatchObject({ type: 'lifecycle', label: 'TRA', baseColor: '#3b82f6', pos: 25 })
    expect(markers[0].tip).toBe('lc — PASS\n+250 ms from recording start')
  })
})

describe('deriveEventOffsets', () => {
  test('dedupes and sorts ascending', () => {
    expect(deriveEventOffsets([{ offset_ms: 300 }, { offset_ms: 100 }, { offset_ms: 100 }, { offset_ms: 200 }]))
      .toEqual([100, 200, 300])
  })
  test('empty → empty', () => {
    expect(deriveEventOffsets([])).toEqual([])
  })
})

describe('findOffsetBefore / findOffsetAfter', () => {
  const sorted = [100, 200, 300]
  test('before: greatest strictly-less, else null', () => {
    expect(findOffsetBefore(sorted, 250)).toBe(200)
    expect(findOffsetBefore(sorted, 300)).toBe(200)
    expect(findOffsetBefore(sorted, 400)).toBe(300)
    expect(findOffsetBefore(sorted, 100)).toBeNull()
    expect(findOffsetBefore([], 50)).toBeNull()
  })
  test('after: smallest strictly-greater, else null', () => {
    expect(findOffsetAfter(sorted, 150)).toBe(200)
    expect(findOffsetAfter(sorted, -1)).toBe(100)
    expect(findOffsetAfter(sorted, 300)).toBeNull()
    expect(findOffsetAfter([], 50)).toBeNull()
  })
})

describe('resolveDashboardIframeUrl', () => {
  const origin = 'http://localhost:5173'
  test('relative same-origin path resolves and gets a pb cache-buster', () => {
    expect(resolveDashboardIframeUrl('/dashboard', 'pb-1', origin))
      .toBe('http://localhost:5173/dashboard?pb=pb-1')
  })
  test('absolute same-origin URL with an existing query string keeps it and adds pb', () => {
    expect(resolveDashboardIframeUrl('http://localhost:5173/dashboard?foo=bar', 'pb-2', origin))
      .toBe('http://localhost:5173/dashboard?foo=bar&pb=pb-2')
  })
  test('javascript: scheme is rejected', () => {
    expect(resolveDashboardIframeUrl('javascript:alert(1)', 'pb-3', origin)).toBeNull()
  })
  test('data: scheme is rejected', () => {
    expect(resolveDashboardIframeUrl('data:text/html,<script>alert(1)</script>', 'pb-4', origin)).toBeNull()
  })
  test('cross-origin http(s) URL is rejected', () => {
    expect(resolveDashboardIframeUrl('https://evil.example/dashboard', 'pb-5', origin)).toBeNull()
  })
  test('protocol-relative URL pointing off-origin is rejected', () => {
    expect(resolveDashboardIframeUrl('//evil.example/dashboard', 'pb-6', origin)).toBeNull()
  })
  test('unparseable URL is rejected', () => {
    expect(resolveDashboardIframeUrl('http://', 'pb-7', origin)).toBeNull()
  })
  test('nullish or empty dashboard_url is rejected', () => {
    expect(resolveDashboardIframeUrl(null, 'pb-8', origin)).toBeNull()
    expect(resolveDashboardIframeUrl(undefined, 'pb-8', origin)).toBeNull()
    expect(resolveDashboardIframeUrl('', 'pb-8', origin)).toBeNull()
  })
  test('whitespace-only dashboard_url is rejected, not silently resolved to the origin root', () => {
    expect(resolveDashboardIframeUrl('   ', 'pb-9', origin)).toBeNull()
  })
})
