import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, test, expect } from 'vitest'

import { WEB_DIR } from './shippedFiles.testutil.js'
import { autonomyModeLine, autonomyQuery } from './historyTab.js'

// Which runs the Autonomy section counts (#1905 subagents).
//
// The daemon holds a parent `working` while its children run, so a subagent's
// span is a NESTED INTERVAL inside its parent's. Counting both counts one
// stretch of wall clock twice and — because subagent runs are short and
// numerous — drags the headline median down. Top-level runs only is therefore
// the default, and the panel has to SAY which mode produced the numbers above
// it, because "42 runs" means two different things under the two modes.

const state = (over = {}) => ({
  autonomyRange: '30d',
  autonomySpan: '24h',
  autonomyRuns: 'top',
  ...over,
})

const kinds = (over = {}) => ({ mode: 'top_level', top_level: 10, subagent: 0, unknown: 0, ...over })

describe('autonomyQuery — the mode reaches the daemon', () => {
  // The daemon's own default is top-level runs, so the default view sends
  // NOTHING extra. That is what makes an older daemon serving a newer dashboard
  // behave identically instead of silently including runs the panel says it
  // excluded.
  test('the default mode adds no parameter at all', () => {
    expect(autonomyQuery('duration', state())).toBe('chart=autonomy_duration&window=30d')
    expect(autonomyQuery('spans', state())).toBe('chart=autonomy_spans&window=24h')
  })

  test('including subagents is sent on BOTH elements', () => {
    const s = state({ autonomyRuns: 'all' })
    expect(autonomyQuery('duration', s)).toContain('include_subagents=true')
    expect(autonomyQuery('spans', s)).toContain('include_subagents=true')
    // …and neither element loses its own window doing it: the two vocabularies
    // are different sets and neither endpoint accepts the other's keys.
    expect(autonomyQuery('duration', s)).toContain('window=30d')
    expect(autonomyQuery('spans', s)).toContain('window=24h')
  })
})

// The control has to exist in the markup AND be read by the wiring. Those are
// two files, and a rename in either one produces a control that looks right and
// does nothing — which is indistinguishable, on screen, from a window that
// genuinely holds no subagent runs.
describe('the control and the wiring name the same thing', () => {
  const html = readFileSync(join(WEB_DIR, 'index.html'), 'utf8')
  const js = readFileSync(join(WEB_DIR, 'historyTab.js'), 'utf8')

  test('the markup was actually read', () => {
    // A file that failed to load would make every assertion below vacuous —
    // absence of a finding and inability to look must not read the same.
    expect(html.length).toBeGreaterThan(1000)
    expect(js.length).toBeGreaterThan(1000)
    expect(html).toContain('history-autonomy-range-row')
  })

  test('both modes are offered, with top-level pre-selected', () => {
    expect(html).toContain('id="history-autonomy-runs-sel"')
    expect(html).toContain('data-autonomy-runs="top"')
    expect(html).toContain('data-autonomy-runs="all"')
    // The default has to be the pre-selected button too, or the panel would
    // say "top-level only" beside a control showing the other choice.
    expect(html).toMatch(/data-autonomy-runs="top"[^>]*class="active"/)
  })

  test('the wiring reads that exact attribute into that exact state key', () => {
    expect(js).toContain("wireAutonomyPicker('history-autonomy-runs-sel', 'autonomy-runs', 'autonomyRuns', 'autonomyRuns')")
  })
})

describe('autonomyModeLine — the panel states what it counted', () => {
  test('says nothing when the payload never stated a mode', () => {
    // An older daemon. Absence is not "it counted top-level runs" — that is a
    // claim this payload cannot make.
    expect(autonomyModeLine({})).toBe('')
    expect(autonomyModeLine(null)).toBe('')
    expect(autonomyModeLine(undefined)).toBe('')
  })

  test('the default mode names itself even when nothing was excluded', () => {
    const line = autonomyModeLine({ kinds: kinds() })
    expect(line).toContain('top-level runs only')
    expect(line).toContain('no subagent runs')
  })

  test('the default mode says HOW MANY runs it left out', () => {
    const line = autonomyModeLine({ kinds: kinds({ subagent: 37 }) })
    expect(line).toContain('37 subagent runs')
    expect(line).toContain('excluded')
  })

  test('singular and plural both read as English', () => {
    expect(autonomyModeLine({ kinds: kinds({ subagent: 1 }) })).toContain('1 subagent run excluded')
    expect(autonomyModeLine({ kinds: kinds({ subagent: 2 }) })).toContain('2 subagent runs excluded')
  })

  test('the including mode says what it added', () => {
    const line = autonomyModeLine({ kinds: kinds({ mode: 'all', subagent: 37 }) })
    expect(line).toContain('Counting every run')
    expect(line).toContain('37 subagent runs')
    expect(line).not.toContain('excluded')
  })

  // THE TRAP THIS SENTENCE EXISTS FOR. A row written before Irrlicht told the
  // two apart carries no classification. It is COUNTED — excluding it would
  // delete most of a back-filled history on a guess — and counting it in
  // SILENCE is the failure #1905 exists to prevent: the view would claim to
  // exclude subagent runs while including every historical one.
  test('unknown-kind runs are named, in both modes', () => {
    for (const mode of ['top_level', 'all']) {
      const line = autonomyModeLine({ kinds: kinds({ mode, subagent: 3, unknown: 8148 }) })
      expect(line).toContain('8148 runs were recorded before Irrlicht told')
      expect(line).toContain('counted either way')
    }
  })

  test('a window with no unknown runs says nothing about them', () => {
    expect(autonomyModeLine({ kinds: kinds({ subagent: 3 }) })).not.toContain('unknown')
  })

  test('one unknown run reads as singular', () => {
    expect(autonomyModeLine({ kinds: kinds({ unknown: 1 }) })).toContain('1 run was recorded before')
  })

  // COMMITTED IN-LANGUAGE MUTANTS. Each of these is a plausible way to get the
  // sentence wrong, and each passes at least one of the assertions above on its
  // own — so the suite has to be shown to tell them apart from production.
  test('production tells the modes and the unknown case apart', () => {
    const topOnly = { kinds: kinds({ subagent: 5, unknown: 9 }) }
    const withSubs = { kinds: kinds({ mode: 'all', subagent: 5, unknown: 9 }) }
    const noUnknown = { kinds: kinds({ subagent: 5, unknown: 0 }) }

    // A mutant that ignores the mode: both modes read identically, so a reader
    // could not tell which produced the figures.
    const modeBlind = (p) => `${p.kinds.subagent} subagent runs.`
    expect(modeBlind(topOnly)).toBe(modeBlind(withSubs))
    expect(autonomyModeLine(topOnly)).not.toBe(autonomyModeLine(withSubs))

    // A mutant that drops the unknown clause: the silent inclusion.
    const unknownBlind = (p) => `Counting top-level runs only · ${p.kinds.subagent} excluded.`
    expect(unknownBlind(topOnly)).toBe(unknownBlind(noUnknown))
    expect(autonomyModeLine(topOnly)).not.toBe(autonomyModeLine(noUnknown))

    // A mutant that always speaks the same sentence.
    const constant = () => 'Counting runs.'
    expect(constant(topOnly)).toBe(constant(withSubs))
  })
})
