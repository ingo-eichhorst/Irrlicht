import { describe, test, expect } from 'vitest'

import { autonomyMeasurementNote } from './historyTab.js'

// The panel's marking for runs whose duration is a FLOOR rather than a
// measurement (#1905 recording).
//
// Two kinds, and they are two different limits:
//
//   - STILL RUNNING — the run has not ended, so its length is unknowable. Shown
//     on the strip, deliberately absent from the percentiles.
//   - STARTED BEFORE IRRLICHT WAS WATCHING — the run has finished, but its
//     start is where Irrlicht began watching. Those ARE samples; dropping them
//     is what left 5 of a day's 35 runs on the record.
//
// A reader who conflated the two would misread the chart in opposite
// directions, which is why the sentence never merges them.

const payload = (measurement) => ({ measurement })

describe('autonomyMeasurementNote', () => {
  // The quiet case, and the one that must stay quiet: a machine whose daemon
  // has been up all day, with every run in view finished and fully measured.
  test('says nothing when every run in view is finished and measured', () => {
    expect(autonomyMeasurementNote(payload({ running: 0, start_lower_bound: 0 }))).toBe('')
    expect(autonomyMeasurementNote({})).toBe('')
    expect(autonomyMeasurementNote(null)).toBe('')
    expect(autonomyMeasurementNote(undefined)).toBe('')
  })

  // A running run is SHOWN and left OUT of the percentiles, and the sentence
  // has to say both — otherwise a reader who can see a 3-hour run on the strip
  // is left wondering why the median did not move.
  test('a running run is named as still going AND as absent from the percentiles', () => {
    const line = autonomyMeasurementNote(payload({ running: 1 }))
    expect(line).toContain('1 run is still going')
    expect(line).toContain('SO FAR')
    expect(line).toContain('left out of the percentiles')
  })

  test('an unmeasured start says which end of the run is the estimate', () => {
    const line = autonomyMeasurementNote(payload({ start_lower_bound: 4 }))
    expect(line).toContain('4 runs already going when Irrlicht started watching')
    expect(line).toContain('not when the run began')
    expect(line).toContain('minimums')
    // It must NOT claim those runs were dropped from the figures: they are
    // finished runs and they are samples.
    expect(line).not.toContain('left out of the percentiles')
  })

  test('both at once read as two separate facts', () => {
    const line = autonomyMeasurementNote(payload({ running: 2, start_lower_bound: 3 }))
    expect(line).toContain('2 runs are still going')
    expect(line).toContain('3 runs already going when Irrlicht started watching')
  })

  test('singular and plural both read as English', () => {
    expect(autonomyMeasurementNote(payload({ running: 1 }))).toContain('1 run is still going')
    expect(autonomyMeasurementNote(payload({ running: 2 }))).toContain('2 runs are still going')
    expect(autonomyMeasurementNote(payload({ start_lower_bound: 1 }))).toContain('that length is a minimum')
    expect(autonomyMeasurementNote(payload({ start_lower_bound: 2 }))).toContain('those lengths are minimums')
  })

  // COMMITTED IN-LANGUAGE MUTANTS. Each is a plausible way to get this wrong,
  // and each passes at least one assertion above on its own.
  test('production tells the two limits apart, and both from silence', () => {
    const running = payload({ running: 3 })
    const bounded = payload({ start_lower_bound: 3 })
    const clean = payload({ running: 0, start_lower_bound: 0 })

    // A mutant that merges the two into one count: a run still going and a run
    // whose start was guessed read identically, so the reader cannot tell which
    // figure to distrust.
    const merged = (p) => `${(p.measurement.running || 0) + (p.measurement.start_lower_bound || 0)} runs are approximate.`
    expect(merged(running)).toBe(merged(bounded))
    expect(autonomyMeasurementNote(running)).not.toBe(autonomyMeasurementNote(bounded))

    // A mutant that never falls silent: a fully measured window carries a
    // caveat it does not need, and the caveat stops meaning anything.
    const always = () => 'Some runs are approximate.'
    expect(always(clean)).toBe(always(running))
    expect(autonomyMeasurementNote(clean)).toBe('')
  })
})
