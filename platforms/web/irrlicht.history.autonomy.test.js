import { describe, test, expect } from 'vitest'

import {
  AUTONOMY_RANGE_LABELS,
  AUTONOMY_SPAN_LABELS,
  AUTONOMY_REASON_PRIORITY,
  AUTONOMY_SERIES,
  autonomyQuery,
  autonomyChartPoints,
  autonomyDuration,
  autonomyProvenanceLine,
  autonomyAxisLabel,
  autonomySeriesColor,
  autonomyPanelRows,
  buildAutonomyStripHeader,
  buildAutonomyStripAxis,
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

describe('the chart names its three lines', () => {
  // QA found three unlabelled curves on the web: the panel built p95/p50/p5
  // rows and then blanked every swatch (`background = 'transparent'`), so the
  // only way to tell the lines apart was vertical order. These pin the key.
  //
  // `cs` here returns '' for every custom property, which is what an unstyled
  // document (and jsdom) gives — so this exercises the fallback branch, the
  // one a stylesheet-less render actually takes.
  const unstyled = { getPropertyValue: () => '' }

  test('every drawn line resolves to a non-empty colour', () => {
    for (const [key] of AUTONOMY_SERIES) {
      expect(autonomySeriesColor(key, unstyled)).toBeTruthy()
    }
  })

  test('the three line colours are distinct', () => {
    const colors = AUTONOMY_SERIES.map(([key]) => autonomySeriesColor(key, unstyled))
    expect(new Set(colors).size).toBe(AUTONOMY_SERIES.length)
  })

  test('the series table covers exactly the three drawn lines', () => {
    expect(AUTONOMY_SERIES.map(([key]) => key)).toEqual(['p95', 'p50', 'p5'])
  })

  test('a key that is not a drawn line gets no colour', () => {
    // longest/shortest/runs are FIGURES, not lines. A swatch for one of them
    // would claim a curve the chart deliberately does not draw.
    expect(autonomySeriesColor('longest', unstyled)).toBe('')
    expect(autonomySeriesColor('p90', unstyled)).toBe('')
  })

  test('a themed document wins over the fallback', () => {
    const themed = { getPropertyValue: (name) => (name === '--ready' ? ' #123456 ' : '') }
    expect(autonomySeriesColor('p95', themed)).toBe('#123456')
    expect(autonomySeriesColor('p50', themed)).toBe('#8B5CF6')
  })

  // THE DEFECT ITSELF. The panel used to build these rows and then set every
  // dot to `transparent`, so the key existed in the markup and said nothing.
  // Asserting on the resolver alone would not have caught that; these assert
  // on the rows the panel actually renders.
  const summary = { p95: 3600, p50: 600, p5: 30, min: 8, max: 7200, count: 312 }
  const unstyledCS = { getPropertyValue: () => '' }

  test('the three line rows carry non-empty, distinct swatches', () => {
    const rows = autonomyPanelRows(summary, unstyledCS)
    const lines = rows.filter(r => r.series)
    expect(lines.map(r => r.series)).toEqual(['p95', 'p50', 'p5'])
    for (const row of lines) {
      expect(row.swatch).toBeTruthy()
      expect(row.swatch).not.toBe('transparent')
    }
    expect(new Set(lines.map(r => r.swatch)).size).toBe(3)
  })

  test('the figures stay unswatched — they are not lines', () => {
    const figures = autonomyPanelRows(summary, unstyledCS).filter(r => !r.series)
    expect(figures.map(r => r.label)).toEqual(['longest', 'shortest', 'runs'])
    for (const row of figures) expect(row.swatch).toBe('transparent')
  })

  test('every line row names its percentile in words', () => {
    for (const row of autonomyPanelRows(summary, unstyledCS).filter(r => r.series)) {
      expect(row.label).toContain(row.series)
      expect(row.label.length).toBeGreaterThan(row.series.length)
    }
  })

  test('an empty summary still produces the full key', () => {
    const rows = autonomyPanelRows(undefined, unstyledCS)
    expect(rows).toHaveLength(6)
    expect(rows[5].value).toBe('0')
  })
})

describe('the strip labels its value column and its window', () => {
  test('the header names the value column', () => {
    const head = buildAutonomyStripHeader()
    expect(head.textContent).toContain('longest')
    // Same grid as a data row, so the header lands over the column it names.
    expect(head.className).toContain('history-autonomy-row')
    expect(head.children).toHaveLength(3)
  })

  test('the axis states where the window starts and that it ends now', () => {
    const now = Math.floor(Date.UTC(2026, 0, 9) / 1000)
    const start = now - 7 * 86400
    const axis = buildAutonomyStripAxis({ start, end: now })
    const bounds = axis.querySelector('.history-autonomy-axis-bounds')
    expect(bounds).toBeTruthy()
    expect(bounds.children).toHaveLength(2)
    expect(bounds.children[1].textContent).toBe('now')
    // The left bound must be a HUMAN label, not the raw epoch seconds — the
    // whole point is that a mark can be placed in time by reading it.
    expect(bounds.children[0].textContent).toBe(autonomyAxisLabel(start, now - start))
    expect(bounds.children[0].textContent).not.toContain(String(start))
  })
})

describe('the strip states its own time bounds', () => {
  // Without them the strip is texture: at 30d and 12mo a mark cannot be
  // placed in time at all.
  const jan2 = Date.UTC(2026, 0, 2, 15, 30) / 1000

  test('a short window labels a time of day, a long one a date', () => {
    expect(autonomyAxisLabel(jan2, 8 * 3600)).toMatch(/\d/)
    expect(autonomyAxisLabel(jan2, 8 * 3600)).not.toMatch(/Jan/)
    expect(autonomyAxisLabel(jan2, 7 * 86400)).toMatch(/Jan/)
  })

  test('a year-long window coarsens to a month, not a day', () => {
    const label = autonomyAxisLabel(jan2, 365 * 86400)
    expect(label).toMatch(/Jan/)
    expect(label).toMatch(/2026/)
  })

  test('a missing timestamp still renders something', () => {
    expect(autonomyAxisLabel(undefined, 86400)).toBeTruthy()
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
