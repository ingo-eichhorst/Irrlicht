import { describe, test, expect } from 'vitest'

import {
  AUTONOMY_REASON_LEGEND,
  AUTONOMY_REASON_PRIORITY,
  AUTONOMY_UNKNOWN_LEGEND,
  autonomyLegendEntries,
  autonomyReconstructionNote,
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
