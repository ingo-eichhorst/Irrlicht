import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, test, expect } from 'vitest'

import { WEB_DIR } from './shippedFiles.testutil.js'
import { autonomyCountingLine, autonomyQuery } from './historyTab.js'

// What the Autonomy section counts (#1905 subagents), retargeted at the FIELD
// after the control was removed (#1905 recording).
//
// The maintainer's decision: every run counts, subagent runs included, because
// Irrlicht recorded them. So there is no mode, no control, and no excluded
// count. The classification survives on every row, and the panel still
// describes a window's MAKEUP — "42 runs" reads differently once you know how
// many of them were nested inside another.

const state = (over = {}) => ({
  autonomyRange: '30d',
  autonomySpan: '24h',
  ...over,
})

const kinds = (over = {}) => ({ top_level: 10, subagent: 0, unknown: 0, ...over })

describe('autonomyQuery — no run-scope parameter reaches the daemon', () => {
  // Sending the old parameter would leave an OLD daemon serving a filtered
  // payload while this panel's sentence said it counted everything — the exact
  // "wrong number with nothing on screen saying so" the section exists to
  // avoid. So the query is exactly the chart and its window, and nothing else.
  test('the query is the chart and its window, and nothing else', () => {
    expect(autonomyQuery('duration', state())).toBe('chart=autonomy_duration&window=30d')
    expect(autonomyQuery('spans', state())).toBe('chart=autonomy_spans&window=24h')
  })

  test('no leftover state key can revive the parameter', () => {
    // The mutation this catches: a stale `autonomyRuns` left in local state (a
    // stored preference, a resumed session) silently re-filtering the view.
    const s = state({ autonomyRuns: 'all' })
    expect(autonomyQuery('duration', s)).not.toContain('include_subagents')
    expect(autonomyQuery('spans', s)).not.toContain('include_subagents')
    // …and neither element loses its own window: the two vocabularies are
    // different sets and neither endpoint accepts the other's keys.
    expect(autonomyQuery('duration', s)).toContain('window=30d')
    expect(autonomyQuery('spans', s)).toContain('window=24h')
  })
})

// The control is GONE from both files. Two files, and a leftover in either one
// is its own failure: markup without wiring is a control that does nothing,
// wiring without markup is a listener attached to a state key no request reads.
describe('the Runs control is gone from the markup and the wiring', () => {
  const html = readFileSync(join(WEB_DIR, 'index.html'), 'utf8')
  const js = readFileSync(join(WEB_DIR, 'historyTab.js'), 'utf8')

  test('the files were actually read', () => {
    // A file that failed to load would make every assertion below vacuous —
    // absence of a finding and inability to look must not read the same.
    expect(html.length).toBeGreaterThan(1000)
    expect(js.length).toBeGreaterThan(1000)
    // A positive control: the row the Runs picker used to sit in is still
    // there, so "not found" below means removed rather than mis-pathed.
    expect(html).toContain('history-autonomy-range-row')
    expect(js).toContain("wireAutonomyPicker('history-autonomy-range-sel'")
  })

  test('no Runs fieldset, buttons or wiring survive', () => {
    expect(html).not.toContain('history-autonomy-runs-sel')
    expect(html).not.toContain('data-autonomy-runs')
    expect(js).not.toContain('history-autonomy-runs-sel')
    expect(js).not.toContain('autonomyRuns')
    expect(js).not.toContain('include_subagents')
  })
})

describe('autonomyCountingLine — the panel states what it counted', () => {
  test('says nothing when the payload carries no census', () => {
    // An older daemon. Absence is "this response never said", which is not the
    // same claim as "there were none".
    expect(autonomyCountingLine({})).toBe('')
    expect(autonomyCountingLine(null)).toBe('')
    expect(autonomyCountingLine(undefined)).toBe('')
  })

  test('a window with no subagent runs says so', () => {
    const line = autonomyCountingLine({ kinds: kinds() })
    expect(line).toContain('Counting every run')
    expect(line).toContain('holds none')
  })

  test('it says how many of the runs were subagents', () => {
    const line = autonomyCountingLine({ kinds: kinds({ subagent: 37 }) })
    expect(line).toContain('37 subagent runs')
    expect(line).toContain('inside its parent')
    // The word that has to be gone: nothing is excluded any more, and a
    // sentence still claiming so would describe a filter that no longer exists.
    expect(line).not.toContain('excluded')
  })

  test('singular and plural both read as English', () => {
    expect(autonomyCountingLine({ kinds: kinds({ subagent: 1 }) })).toContain('1 subagent run —')
    expect(autonomyCountingLine({ kinds: kinds({ subagent: 2 }) })).toContain('2 subagent runs —')
  })

  // THE TRAP THIS CLAUSE EXISTS FOR. A row written before Irrlicht told the two
  // apart carries no classification. It is counted like the rest — and counting
  // it in SILENCE would let the panel imply a classification nobody made.
  test('unknown-kind runs are named', () => {
    const line = autonomyCountingLine({ kinds: kinds({ subagent: 3, unknown: 8148 }) })
    expect(line).toContain('8148 runs were recorded before Irrlicht told')
    expect(line).toContain('counted either way')
  })

  test('a window with no unknown runs says nothing about them', () => {
    expect(autonomyCountingLine({ kinds: kinds({ subagent: 3 }) })).not.toContain('unknown')
  })

  test('one unknown run reads as singular', () => {
    expect(autonomyCountingLine({ kinds: kinds({ unknown: 1 }) })).toContain('1 run was recorded before')
  })

  // COMMITTED IN-LANGUAGE MUTANTS. Each is a plausible way to get the sentence
  // wrong and each passes at least one assertion above on its own, so the suite
  // has to be shown to tell them apart from production.
  test('production tells the census cases apart', () => {
    const withSubs = { kinds: kinds({ subagent: 5, unknown: 9 }) }
    const noSubs = { kinds: kinds({ subagent: 0, unknown: 9 }) }
    const noUnknown = { kinds: kinds({ subagent: 5, unknown: 0 }) }

    // A mutant blind to the subagent count: a window full of nested runs reads
    // exactly like one with none.
    const subBlind = () => 'Counting every run.'
    expect(subBlind(withSubs)).toBe(subBlind(noSubs))
    expect(autonomyCountingLine(withSubs)).not.toBe(autonomyCountingLine(noSubs))

    // A mutant that drops the unknown clause: the silent classification.
    const unknownBlind = (p) => `Counting every run, including ${p.kinds.subagent}.`
    expect(unknownBlind(withSubs)).toBe(unknownBlind(noUnknown))
    expect(autonomyCountingLine(withSubs)).not.toBe(autonomyCountingLine(noUnknown))
  })
})
