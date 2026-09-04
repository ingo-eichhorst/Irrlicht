import { describe, test, expect } from 'vitest'

import {
  AUTONOMY_RANGE_LABELS,
  AUTONOMY_SPAN_LABELS,
  AUTONOMY_REASON_PRIORITY,
  autonomyQuery,
  autonomyChartPoints,
  autonomyDuration,
  autonomyProvenanceLine,
  collapseAutonomyStrip,
} from './historyTab.js'
import { historyPriorityForState } from './irrlicht.js'

// The Autonomy section (#1905): the strip's pixel-collapse rule, the
// gap-not-zero rule, the two window vocabularies staying distinct, and the
// wording that keeps an empty view from reading as "you did nothing".

const span = (start, end, reason, project = 'p') => ({ start, end, reason, project, session: 's' + start })

describe('collapseAutonomyStrip — the pixel-collapse rule', () => {
  // Every span draws at a minimum of one column. At 12mo a 40-second run is
  // far under one device pixel; rounding it away would erase exactly the short
  // runs the p5 line is about.
  test('a sub-pixel run still occupies exactly one column', () => {
    const cells = collapseAutonomyStrip([span(50_000, 50_001, 'ready')], 0, 100_000, 10)
    expect(cells).toHaveLength(10)
    const occupied = cells.filter(c => c.occupied)
    expect(occupied).toHaveLength(1)
    expect(occupied[0].reason).toBe('ready')
  })

  test('a shared column takes the highest-priority reason, whatever the order', () => {
    // One state per line: a line naming three of the four canonical states is
    // what tools/state-vocabulary-lint.sh refuses, and rightly — such a list is
    // complete when written and silently stale one state later.
    const spans = [
      span(1_000, 1_100, 'ready'),
      span(1_200, 1_300, 'error'),
      span(1_400, 1_500, 'waiting'),
    ]
    expect(collapseAutonomyStrip(spans, 0, 100_000, 10)[0].reason).toBe('error')
    expect(collapseAutonomyStrip([...spans].reverse(), 0, 100_000, 10)[0].reason).toBe('error')
    // …and without the error, waiting beats ready.
    const noError = [span(1_000, 1_100, 'ready'), span(1_400, 1_500, 'waiting')]
    expect(collapseAutonomyStrip(noError, 0, 100_000, 10)[0].reason).toBe('waiting')
  })

  test('the ladder matches the session-history strip’s', () => {
    // Two hand-written copies of one ORDER (the bar also ranks `working`,
    // which is never an end reason, so only the order can be compared).
    const ordered = Object.keys(AUTONOMY_REASON_PRIORITY)
      .sort((a, b) => AUTONOMY_REASON_PRIORITY[b] - AUTONOMY_REASON_PRIORITY[a])
    expect(ordered[0]).toBe('error')
    expect(ordered[1]).toBe('waiting')
    expect(ordered[2]).toBe('ready')
    expect(ordered).toHaveLength(3)
    for (let i = 1; i < ordered.length; i++) {
      expect(historyPriorityForState(ordered[i - 1]))
        .toBeGreaterThan(historyPriorityForState(ordered[i]))
    }
  })

  test('a long run covers every column it spans', () => {
    const cells = collapseAutonomyStrip([span(0, 50_000, 'waiting')], 0, 100_000, 10)
    expect(cells.filter(c => c.occupied)).toHaveLength(6)
  })

  test('runs outside the window are clipped, not drawn', () => {
    const before = collapseAutonomyStrip([span(-5_000, -1_000, 'ready')], 0, 100_000, 10)
    expect(before.some(c => c.occupied)).toBe(false)
    const straddling = collapseAutonomyStrip([span(-5_000, 10_000, 'ready')], 0, 100_000, 10)
    expect(straddling[0].occupied).toBe(true)
    expect(straddling[5].occupied).toBe(false)
  })

  test('an unnamed reason still occupies its column and never outranks a real one', () => {
    const unknown = span(10, 20, 'martian')
    const cells = collapseAutonomyStrip([unknown], 0, 100, 10)
    expect(cells[1].occupied).toBe(true)
    expect(cells[1].reason).toBe(null)
    expect(collapseAutonomyStrip([unknown, span(11, 19, 'error')], 0, 100, 10)[1].reason).toBe('error')
  })

  test('degenerate inputs produce no columns', () => {
    expect(collapseAutonomyStrip([], 0, 0, 10)).toEqual([])
    expect(collapseAutonomyStrip([], 0, 100, 0)).toEqual([])
  })
})

describe('autonomyChartPoints — an empty bucket is a gap, not a zero', () => {
  // The daemon omits empty buckets; the client must keep them absent so the
  // stroke BREAKS there. Interpolating (or substituting a zero) would pull the
  // line to the axis on a day with no runs, which reads as "runs got shorter".
  test('omitted buckets come back as nulls aligned to the axis', () => {
    const points = autonomyChartPoints({
      bucket_starts: [100, 200, 300, 400],
      buckets: [
        { ts: 100, p95: 90, p50: 50, p5: 10, min: 10, max: 90, count: 30 },
        { ts: 400, p95: 80, p50: 40, p5: 8, min: 8, max: 80, count: 25 },
      ],
    })
    expect(points).toHaveLength(4)
    expect(points[0]).not.toBeNull()
    expect(points[1]).toBeNull()
    expect(points[2]).toBeNull()
    expect(points[3]).not.toBeNull()

    // THE COMMITTED MUTATION: the "just densify with zeros" build. If
    // production ever did this, a quiet day would draw a point at the floor
    // instead of a break in the line — and this expectation is what fails.
    const densified = points.map(b => b || { ts: 0, p95: 0, p50: 0, p5: 0, min: 0, max: 0, count: 0 })
    expect(densified.filter(b => b.count === 0)).toHaveLength(2)
    expect(points.filter(b => b === null)).toHaveLength(2)
  })

  test('a fully populated range has no gaps', () => {
    const points = autonomyChartPoints({
      bucket_starts: [1, 2],
      buckets: [{ ts: 1, p50: 5 }, { ts: 2, p50: 6 }],
    })
    expect(points.every(Boolean)).toBe(true)
  })

  test('a missing payload is an empty axis, not a crash', () => {
    expect(autonomyChartPoints(null)).toEqual([])
    expect(autonomyChartPoints({})).toEqual([])
  })
})

describe('the two window vocabularies stay distinct', () => {
  // The trap #1905 calls out by name: chart=state's granularity keys and the
  // Autonomy windows OVERLAP textually and mean different things — a
  // granularity is a bucket width times a count ('24h' → a 30-DAY window),
  // an autonomy window IS the window.
  test('each element sends its own ?window=, and neither is a granularity', () => {
    const state = { autonomyRange: '1y', autonomySpan: '12mo' }
    expect(autonomyQuery('duration', state)).toBe('chart=autonomy_duration&window=1y')
    expect(autonomyQuery('spans', state)).toBe('chart=autonomy_spans&window=12mo')
  })

  test('the two pickers offer different sets, and neither is the granularity set', () => {
    expect(Object.keys(AUTONOMY_RANGE_LABELS)).toEqual(['30d', '1y'])
    expect(Object.keys(AUTONOMY_SPAN_LABELS)).toEqual(['8h', '24h', '7d', '30d', '12mo'])
    // 12mo is the strip's own key and has no granularity twin; if it ever
    // disappears, the vocabularies may have been merged.
    expect(AUTONOMY_SPAN_LABELS['12mo']).toBeTruthy()
    // The chart's range vocabulary must not silently gain the strip's keys.
    expect(AUTONOMY_RANGE_LABELS['24h']).toBeUndefined()
  })

  test('a strip key is never sent to the duration chart', () => {
    // The daemon 400s this pairing; the client must not be the thing that
    // constructs it in the first place.
    expect(autonomyQuery('duration', { autonomyRange: '30d', autonomySpan: '8h' }))
      .toBe('chart=autonomy_duration&window=30d')
  })
})

describe('“no data” never reads as “you did nothing”', () => {
  test('an empty log says collection just started', () => {
    const line = autonomyProvenanceLine({ earliest_span: 0, total_recorded: 0 })
    expect(line).toMatch(/began measuring/)
    expect(line).not.toMatch(/^0 runs$/)
  })

  test('a seeded log names the date collection started and the total', () => {
    const line = autonomyProvenanceLine({ earliest_span: 1_700_000_000, total_recorded: 312 })
    expect(line).toMatch(/Collecting since/)
    expect(line).toMatch(/312 runs recorded/)
  })
})

describe('autonomyDuration', () => {
  test('formats seconds through days', () => {
    expect(autonomyDuration(41)).toBe('41s')
    expect(autonomyDuration(660)).toBe('11m')
    expect(autonomyDuration(7080)).toBe('1h58m')
    expect(autonomyDuration(86_400)).toBe('1d')
    expect(autonomyDuration(0)).toBe('0s')
  })
})
