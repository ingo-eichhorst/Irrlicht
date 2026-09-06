import { describe, test, expect } from 'vitest'

import { autonomyMeasurementNote } from './historyTab.js'

// The panel's marking for runs whose duration is a FLOOR rather than a
// measurement (#1905 recording).
//
// A run that has not ended has no length yet — only how long it has lasted SO
// FAR. That floor COUNTS towards the panel's longest run (the section reports a
// maximum, and "already lasted 3h" is true) and is deliberately NOT a sample
// for the aggregate chart's percentiles, where a floor shortens the longest
// runs hardest.
//
// THE SECOND SENTENCE WENT (#1905 prose cut). It named the runs already going
// when Irrlicht started watching, whose start is a lower bound rather than a
// beginning. That is a real limit, but it is a restart artefact a reader can do
// nothing with — and `start_lower_bound` still ships on the wire, so nothing
// about what the daemon measures changed. The tests that pinned that sentence
// are retargeted below onto the fact that it no longer speaks.

const payload = (measurement) => ({ measurement })

describe('autonomyMeasurementNote', () => {
  // The quiet case, and the one that must stay quiet: a machine between
  // sessions, with nothing running.
  test('says nothing when no run is still going', () => {
    expect(autonomyMeasurementNote(payload({ running: 0, start_lower_bound: 0 }))).toBe('')
    expect(autonomyMeasurementNote({})).toBe('')
    expect(autonomyMeasurementNote(null)).toBe('')
    expect(autonomyMeasurementNote(undefined)).toBe('')
  })

  // A running run's figure is a floor, and the sentence has to say so —
  // otherwise a reader who can see "longest 3h" beside a project is left unsure
  // whether that figure is finished.
  test('a running run is named, and its length called a so-far', () => {
    const line = autonomyMeasurementNote(payload({ running: 1 }))
    expect(line).toBe('1 run still going — length so far.')
  })

  test('the lower-bound sentence is gone, and takes no other line with it', () => {
    // `start_lower_bound` still arrives; it just no longer produces prose. A
    // payload carrying ONLY that is silent, and one carrying both says exactly
    // what the running-only payload says — which is the check that would fail
    // if half the old sentence survived.
    expect(autonomyMeasurementNote(payload({ start_lower_bound: 4 }))).toBe('')
    expect(autonomyMeasurementNote(payload({ running: 2, start_lower_bound: 3 })))
      .toBe(autonomyMeasurementNote(payload({ running: 2 })))
    const both = autonomyMeasurementNote(payload({ running: 2, start_lower_bound: 3 }))
    expect(both).not.toContain('minimum')
    expect(both).not.toContain('watching')
  })

  test('singular and plural both read as English', () => {
    expect(autonomyMeasurementNote(payload({ running: 1 }))).toBe('1 run still going — length so far.')
    expect(autonomyMeasurementNote(payload({ running: 2 }))).toBe('2 runs still going — lengths so far.')
  })

  // COMMITTED IN-LANGUAGE MUTANTS. Each is a plausible way to get this wrong,
  // and each passes at least one assertion above on its own.
  test('production tells a running window from a still one, and counts it', () => {
    const running = payload({ running: 3 })
    const clean = payload({ running: 0, start_lower_bound: 0 })

    // A mutant blind to HOW MANY are running: one long-running session reads
    // exactly like a fleet of them.
    const countBlind = () => 'Some runs are still going.'
    expect(countBlind(running)).toBe(countBlind(payload({ running: 1 })))
    expect(autonomyMeasurementNote(running)).not.toBe(autonomyMeasurementNote(payload({ running: 1 })))

    // A mutant that never falls silent: a window with nothing running carries a
    // caveat it does not need, and the caveat stops meaning anything.
    const always = () => 'Some runs are approximate.'
    expect(always(clean)).toBe(always(running))
    expect(autonomyMeasurementNote(clean)).toBe('')
  })
})
