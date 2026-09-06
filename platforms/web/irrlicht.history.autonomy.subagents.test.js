import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, test, expect } from 'vitest'

import { WEB_DIR } from './shippedFiles.testutil.js'
import { AUTONOMY_CONCURRENCY_CAVEAT, autonomyQuery } from './historyTab.js'

// What the Autonomy section counts (#1905 subagents), retargeted at the FIELD
// after the control was removed (#1905 recording).
//
// The maintainer's decision: every run counts, subagent runs included, because
// Irrlicht recorded them. So there is no mode, no control, and no excluded
// count. The classification survives on every row and on the wire.
//
// THE SENTENCE THAT REPORTED THE CENSUS IS GONE (#1905 prose cut). "Every run
// counts, including 259 subagent runs" is reassurance rather than a caveat, and
// its `unknown` clause described legacy rows a machine installing Irrlicht
// today will never hold. What a reader still needs from the nesting is the one
// place it changes how a NUMBER reads — the concurrency figure — and that is
// the caveat beside it, pinned below.

const state = (over = {}) => ({
  autonomyRange: '30d',
  ...over,
})

describe('autonomyQuery — no run-scope parameter reaches the daemon', () => {
  // Sending the old parameter would leave an OLD daemon serving a filtered
  // payload while this panel's sentence said it counted everything — the exact
  // "wrong number with nothing on screen saying so" the section exists to
  // avoid. So the query is exactly the chart and its window, and nothing else.
  test('each element sends its chart and its window, and nothing else', () => {
    expect(autonomyQuery('projects', state())).toBe('chart=autonomy_projects&window=30d')
    expect(autonomyQuery('duration', state())).toBe('chart=autonomy_duration&window=30d')
  })

  test('no leftover state key can revive the parameter', () => {
    // The mutation this catches: a stale `autonomyRuns` left in local state (a
    // stored preference, a resumed session) silently re-filtering the view.
    const s = state({ autonomyRuns: 'all' })
    for (const element of ['projects', 'duration']) {
      expect(autonomyQuery(element, s)).not.toContain('include_subagents')
      // …and the window survives: the section has one, and it is not one of
      // chart=state's same-looking granularity keys.
      expect(autonomyQuery(element, s)).toContain('window=30d')
    }
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

  // The Span picker went with the run strip it governed (#1905 redesign). Same
  // two-file rule: markup without wiring is a control that does nothing, and
  // wiring without markup is a listener on a state key no request reads.
  test('no Span fieldset, buttons or wiring survive either', () => {
    expect(html).not.toContain('history-autonomy-span-sel')
    expect(html).not.toContain('data-autonomy-span')
    expect(js).not.toContain('history-autonomy-span-sel')
    expect(js).not.toContain('autonomySpan')
    expect(js).not.toContain('AUTONOMY_SPAN_LABELS')
  })
})

describe('what a reader still needs from the nesting is the concurrency caveat', () => {
  // The census sentence is gone. The one consequence of nesting that changes
  // how a figure READS stays, because the `at once` number is otherwise taken
  // for a count of independent agents: a parent is held `working` while its
  // subagents run, so one agent with three subagents overlaps as four.
  test('the caveat names the parent, the subagents and the state', () => {
    expect(AUTONOMY_CONCURRENCY_CAVEAT).toMatch(/parent/i)
    expect(AUTONOMY_CONCURRENCY_CAVEAT).toMatch(/subagents/i)
    expect(AUTONOMY_CONCURRENCY_CAVEAT).toMatch(/working/i)
  })

  // The committed mutation: the census sentence back in the shipped file under
  // any name. A helper that still parses is the one a later reader wires back
  // up, so it is checked out of the file rather than merely left uncalled.
  test('no census sentence survives in the shipped file', () => {
    const js = readFileSync(join(WEB_DIR, 'historyTab.js'), 'utf8')
    expect(js).toContain('AUTONOMY_CONCURRENCY_CAVEAT') // the file was actually read
    expect(js).not.toContain('autonomyCountingLine')
    // Fragments only ever emitted as PROSE, so a code comment restating the
    // decision ("the section counts every run, subagent runs included") does
    // not trip this — only the sentence itself coming back would.
    for (const gone of ['This window holds none', 'top/sub split',
      'happened inside its parent', 'recorded before Irrlicht told']) {
      expect(js).not.toContain(gone)
    }
  })
})
