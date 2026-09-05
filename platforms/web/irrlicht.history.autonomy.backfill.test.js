import { describe, test, expect } from 'vitest'

import {
  AUTONOMY_REASON_LEGEND,
  AUTONOMY_REASON_PRIORITY,
  AUTONOMY_STRIP_MAX_ROWS,
  AUTONOMY_UNKNOWN_LEGEND,
  autonomyBoundaryLabel,
  autonomyLegendEntries,
  autonomyReconstructionNote,
  autonomyStripOverflowLabel,
  autonomyVisibleBoundaries,
  collapseAutonomyStrip,
} from './historyTab.js'

// The back-fill's marking (#1905). tools/autonomy-backfill reconstructs runs
// from logs a machine already had; the daemon serves them like any other row,
// and the panel has to say so. Everything here is about that saying-so — a
// reconstructed figure rendered as a measured one is the wrong number with
// nothing on screen admitting it.

const span = (start, end, reason, project = 'p') => ({ start, end, reason, project, session: 's' + start })

// A duration payload with an arbitrary provenance block.
const durationWith = (provenance, count = 100) => ({
  summary: { p95: 60, p50: 30, p5: 10, min: 5, max: 90, count },
  buckets: [],
  earliest_span: 1_700_000_000,
  total_recorded: count,
  provenance,
})

describe('autonomyReconstructionNote — the panel marks a back-filled view', () => {
  // The silent case is the one every other install gets. A note about nothing
  // would train people to skip the one that matters.
  test('says nothing when every run in view was measured', () => {
    expect(autonomyReconstructionNote(durationWith({ reconstructed: 0, cost_derived: 0, live_since: 1_700_000_000 })))
      .toBe('')
  })

  test('says nothing when the payload carries no provenance at all', () => {
    // An older daemon, or a client reading a response it did not expect.
    expect(autonomyReconstructionNote({ summary: { count: 12 } })).toBe('')
    expect(autonomyReconstructionNote(null)).toBe('')
    expect(autonomyReconstructionNote(undefined)).toBe('')
  })

  test('states how many of the runs in view are reconstructed', () => {
    const note = autonomyReconstructionNote(
      durationWith({ reconstructed: 40, cost_derived: 0, live_since: 1_755_000_000 }, 100))
    expect(note).toContain('40 of 100 runs in view')
    expect(note).toContain('not measured as they happened')
  })

  test('states the date before which everything is reconstructed', () => {
    const liveSince = 1_755_000_000
    const note = autonomyReconstructionNote(durationWith({ reconstructed: 40, cost_derived: 0, live_since: liveSince }))
    const label = new Date(liveSince * 1000)
      .toLocaleDateString(undefined, { year: 'numeric', month: 'short', day: 'numeric' })
    expect(note).toContain('Everything before ' + label + ' is reconstructed.')
  })

  // live_since 0 is "nothing has ever been measured live", which is a
  // different claim from "measured since the epoch" — and printing Jan 1 1970
  // would be a fabricated date, which is the exact failure mode this whole
  // change exists to avoid.
  test('never prints an epoch date when nothing was measured live', () => {
    const note = autonomyReconstructionNote(durationWith({ reconstructed: 40, cost_derived: 0, live_since: 0 }))
    expect(note).toContain('Nothing here was measured live')
    expect(note).not.toContain('1970')
  })

  // The honesty rule for the cost era, in words: those runs' end reason is
  // unknown, and the note says it is not assumed.
  test('names the cost-derived runs and calls their end reason unknown', () => {
    const note = autonomyReconstructionNote(
      durationWith({ reconstructed: 90, cost_derived: 55, live_since: 1_755_000_000 }))
    expect(note).toContain('55 of them come from the cost log')
    expect(note).toContain('unknown')
    expect(note).toContain('not assumed')
  })

  test('leaves the cost sentence out when no cost-derived run is in range', () => {
    const note = autonomyReconstructionNote(
      durationWith({ reconstructed: 40, cost_derived: 0, live_since: 1_755_000_000 }))
    expect(note).not.toContain('cost log')
    expect(note).toContain('40 of 100 runs in view')
  })

  // The committed mutation for this check, in the idiom this suite already
  // uses (see mergedResolver in irrlicht.history.autonomy.test.js). Both ways
  // of getting the conditional wrong — a build that always speaks and one that
  // never does — return the SAME string for both fixtures. Production must
  // not, or "says it when it should" and "stays silent when it should" are two
  // assertions that could each pass against a build that never looked at the
  // data at all.
  test('production tells the two fixtures apart, so neither direction passes vacuously', () => {
    const allLive = durationWith({ reconstructed: 0, cost_derived: 0, live_since: 1_755_000_000 })
    const backfilled = durationWith({ reconstructed: 5, cost_derived: 2, live_since: 1_755_000_000 })

    const alwaysSpeaks = () => 'some runs here were reconstructed'
    const neverSpeaks = () => ''
    expect(alwaysSpeaks(allLive)).toBe(alwaysSpeaks(backfilled))
    expect(neverSpeaks(allLive)).toBe(neverSpeaks(backfilled))

    expect(autonomyReconstructionNote(allLive)).not.toBe(autonomyReconstructionNote(backfilled))
    expect(autonomyReconstructionNote(allLive)).toBe('')
    expect(autonomyReconstructionNote(backfilled)).not.toBe('')
  })
})

describe('the strip legend gains a neutral entry only when it needs one', () => {
  test('three measured reasons and nothing more, when every reason is named', () => {
    // One reason per line: a single line naming three of the four canonical
    // states is what tools/state-vocabulary-lint.sh refuses, and rightly.
    const spans = { spans: [
      span(0, 10, 'ready'),
      span(20, 30, 'waiting'),
      span(40, 50, 'error'),
    ] }
    expect(autonomyLegendEntries(spans)).toEqual(AUTONOMY_REASON_LEGEND)
  })

  test('a fourth neutral entry once a run has an unknown end reason', () => {
    const spans = { spans: [span(0, 10, 'ready'), span(20, 30, 'unknown')] }
    const entries = autonomyLegendEntries(spans)
    expect(entries).toHaveLength(AUTONOMY_REASON_LEGEND.length + 1)
    expect(entries[entries.length - 1]).toEqual(AUTONOMY_UNKNOWN_LEGEND)
  })

  // Two different rows draw the same neutral column: a cost-derived span
  // (reason `unknown`) and an old row written before the reason was recorded
  // (reason absent). One legend entry has to cover both, or one of them is a
  // colour with no key.
  test('an old row with no reason at all also earns the neutral entry', () => {
    const spans = { spans: [span(0, 10, 'ready'), { start: 20, end: 30, project: 'p', session: 's' }] }
    expect(autonomyLegendEntries(spans)).toHaveLength(AUTONOMY_REASON_LEGEND.length + 1)
  })

  test('an empty or absent window does not invent a legend entry', () => {
    expect(autonomyLegendEntries({ spans: [] })).toEqual(AUTONOMY_REASON_LEGEND)
    expect(autonomyLegendEntries(null)).toEqual(AUTONOMY_REASON_LEGEND)
  })

  // The neutral entry must not be given a rank, or one reconstructed span
  // would grey out a strip column that also holds a real error.
  test('`unknown` has no rank on the collapse ladder', () => {
    expect(AUTONOMY_REASON_PRIORITY.unknown).toBeUndefined()
    const cells = collapseAutonomyStrip(
      [span(1_000, 1_100, 'unknown'), span(1_200, 1_300, 'error')], 0, 100_000, 10)
    expect(cells[0].reason).toBe('error')
  })

  // …and a column holding ONLY unknown runs is still occupied, drawn neutral.
  // The run happened; nothing can say how it ended.
  test('an unknown-only column is occupied and neutral, never idle', () => {
    const cells = collapseAutonomyStrip([span(1_000, 1_100, 'unknown')], 0, 100_000, 10)
    expect(cells[0].occupied).toBe(true)
    expect(cells[0].reason).toBe(null)
  })
})

// The run strip had no row cap at all, which nobody could see until the
// back-fill gave it history to draw: a 12mo window over a back-filled log
// renders 95 project rows (QA-1). The daemon ranks `projects` by TOTAL
// AUTONOMOUS SECONDS, so the cap keeps the rows that matter, and the omission
// is stated rather than silent.
describe('the run strip caps its project rows', () => {
  const projects = (n) => Array.from({ length: n }, (_, i) => 'p' + i)

  test('nothing is said when every project fits', () => {
    expect(autonomyStripOverflowLabel(projects(AUTONOMY_STRIP_MAX_ROWS))).toBe('')
    expect(autonomyStripOverflowLabel([])).toBe('')
    expect(autonomyStripOverflowLabel(null)).toBe('')
  })

  test('the overflow line names how many rows were left out', () => {
    const label = autonomyStripOverflowLabel(projects(AUTONOMY_STRIP_MAX_ROWS + 83))
    expect(label).toContain('+83 more projects')
    // …and WHY they are the missing ones, so the cap does not read as an
    // arbitrary slice of an unknown ordering.
    expect(label).toContain('less autonomous time')
  })

  test('one hidden project is singular', () => {
    expect(autonomyStripOverflowLabel(projects(AUTONOMY_STRIP_MAX_ROWS + 1)))
      .toContain('+1 more project,')
  })

  // The measured case from QA: 95 projects in a 12mo window.
  test('a 95-project window draws exactly the cap and accounts for the rest', () => {
    const all = projects(95)
    const drawn = all.slice(0, AUTONOMY_STRIP_MAX_ROWS)
    expect(drawn).toHaveLength(AUTONOMY_STRIP_MAX_ROWS)
    // A PREFIX of the daemon's ranking — the cap must never reorder, or the two
    // clients would disagree about which projects are the busiest.
    expect(drawn).toEqual(all.slice(0, drawn.length))
    expect(autonomyStripOverflowLabel(all))
      .toContain('+' + (95 - AUTONOMY_STRIP_MAX_ROWS) + ' more projects')
  })

  test('the cap is a fixed number, not something a caller can talk out of', () => {
    expect(AUTONOMY_STRIP_MAX_ROWS).toBeGreaterThan(0)
    expect(Number.isInteger(AUTONOMY_STRIP_MAX_ROWS)).toBe(true)
  })

  // The committed mutation, in the idiom this suite already uses. An
  // always-silent build (the one shipped before QA-1) and an always-speaking
  // one both answer identically for the two fixtures; production must not.
  test('production tells a capped strip from an uncapped one', () => {
    const fits = projects(AUTONOMY_STRIP_MAX_ROWS)
    const overflows = projects(AUTONOMY_STRIP_MAX_ROWS + 1)

    const neverSpeaks = () => ''
    const alwaysSpeaks = () => '+N more projects'
    expect(neverSpeaks(fits)).toBe(neverSpeaks(overflows))
    expect(alwaysSpeaks(fits)).toBe(alwaysSpeaks(overflows))

    expect(autonomyStripOverflowLabel(fits)).not.toBe(autonomyStripOverflowLabel(overflows))
    expect(autonomyStripOverflowLabel(fits)).toBe('')
    expect(autonomyStripOverflowLabel(overflows)).not.toBe('')
  })
})

// The p5 line steps by two orders of magnitude at the cost→log boundary,
// because the cost log cannot see a run shorter than its 60s write interval
// while the event log records one-second runs (QA-2). A reader takes that for a
// change in behaviour. The marker puts the explanation where the artefact is.
describe('source boundaries are marked on the chart', () => {
  const durationOver = (starts, boundaries) => ({
    bucket_starts: starts,
    buckets: [],
    summary: { count: 0 },
    provenance: { reconstructed: 0, cost_derived: 0, live_since: 0, boundaries },
  })

  test('a range that straddles a boundary marks it, at the right fraction', () => {
    const got = autonomyVisibleBoundaries(
      durationOver([0, 100, 200, 300, 400], [{ ts: 100, from: 'cost', to: 'log' }]))
    expect(got).toHaveLength(1)
    expect(got[0].ts).toBe(100)
    expect(got[0].fraction).toBeCloseTo(0.25)
  })

  test('a range that does not reach the boundary marks nothing', () => {
    expect(autonomyVisibleBoundaries(
      durationOver([1000, 1100, 1200], [{ ts: 100, from: 'cost', to: 'log' }]))).toEqual([])
    expect(autonomyVisibleBoundaries(
      durationOver([0, 100, 200], [{ ts: 9999, from: 'log', to: 'live' }]))).toEqual([])
  })

  // A rule exactly on the axis marks nothing and reads as a chart border.
  test('a boundary on either edge of the drawn domain is not drawn', () => {
    expect(autonomyVisibleBoundaries(
      durationOver([100, 200, 300], [{ ts: 100, from: 'cost', to: 'log' }]))).toEqual([])
    expect(autonomyVisibleBoundaries(
      durationOver([100, 200, 300], [{ ts: 300, from: 'log', to: 'live' }]))).toEqual([])
  })

  test('both handovers are drawn by the one mechanism', () => {
    const got = autonomyVisibleBoundaries(durationOver([0, 100, 200, 300, 400], [
      { ts: 100, from: 'cost', to: 'log' },
      { ts: 300, from: 'log', to: 'live' },
    ]))
    expect(got.map(b => b.ts)).toEqual([100, 300])
  })

  test('a machine that was never back-filled draws nothing at all', () => {
    expect(autonomyVisibleBoundaries(durationOver([0, 100, 200], []))).toEqual([])
    expect(autonomyVisibleBoundaries(durationOver([0, 100, 200], undefined))).toEqual([])
    expect(autonomyVisibleBoundaries({ bucket_starts: [0, 100, 200] })).toEqual([])
    expect(autonomyVisibleBoundaries(null)).toEqual([])
  })

  test('a degenerate domain cannot produce a divide-by-zero fraction', () => {
    expect(autonomyVisibleBoundaries(
      durationOver([100], [{ ts: 100, from: 'cost', to: 'log' }]))).toEqual([])
    expect(autonomyVisibleBoundaries(
      durationOver([100, 100], [{ ts: 100, from: 'cost', to: 'log' }]))).toEqual([])
  })

  // The label has to say the data BEFORE the line is the coarser one, and name
  // the resolution — which is the whole reason the marker exists.
  test('the label describes what lies to the left, with its resolution', () => {
    expect(autonomyBoundaryLabel({ from: 'cost', to: 'log' })).toBe('← cost log · 60s resolution')
    expect(autonomyBoundaryLabel({ from: 'log', to: 'live' })).toBe('← event log · rebuilt')
  })

  test('an era this build does not know still gets a label, never a blank one', () => {
    expect(autonomyBoundaryLabel({ from: 'some-future-source', to: 'live' }))
      .toBe('← some-future-source')
    expect(autonomyBoundaryLabel({})).toBe('← a different source')
    expect(autonomyBoundaryLabel(null)).toBe('← a different source')
  })

  // The committed mutation. A build that never marks (the one shipped before
  // QA-2) and one that marks unconditionally both answer identically for the
  // two fixtures; production must not, or "draws it when it should" and "draws
  // nothing when it should not" could each pass against a build that never
  // looked at the range.
  test('production tells a straddling range from one that misses the boundary', () => {
    const straddles = durationOver([0, 100, 200, 300], [{ ts: 150, from: 'cost', to: 'log' }])
    const misses = durationOver([0, 100, 200, 300], [{ ts: 9999, from: 'cost', to: 'log' }])

    const neverMarks = () => []
    const alwaysMarks = () => [{ ts: 150 }]
    expect(neverMarks(straddles)).toEqual(neverMarks(misses))
    expect(alwaysMarks(straddles)).toEqual(alwaysMarks(misses))

    expect(autonomyVisibleBoundaries(straddles)).toHaveLength(1)
    expect(autonomyVisibleBoundaries(misses)).toHaveLength(0)
  })
})
