import {
  autonomyBoundaryCaptionShown,
  autonomyBoundaryLabel,
  autonomyMoreProjectsLabel,
  autonomyPanelChoice,
  autonomyProjectOptions,
  autonomyProvenanceLine,
  autonomyVisibleBoundaries,
} from './historyTab.js'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { WEB_DIR } from './shippedFiles.testutil.js'

// The back-fill's marking (#1905). tools/autonomy-backfill reconstructs runs
// from logs a machine already had; the daemon serves them like any other row,
// and the panel has to say so. Everything here is about that saying-so — a
// reconstructed figure rendered as a measured one is the wrong number with
// nothing on screen admitting it.

// A panels payload with an arbitrary provenance block.
const durationWith = (provenance, count = 100) => ({
  panels: [],
  summary: { longest: 90, peak: 2, runs: count, projects: 1 },
  earliest_span: 1_700_000_000,
  total_recorded: count,
  provenance,
})

describe('the provenance line marks a back-filled view', () => {
  // #1905's prose cut deleted the three-sentence reconstruction paragraph and
  // kept its count as a suffix on the line that was already there. What had to
  // survive is the CLAIM, not the paragraph: a machine whose figures were
  // rebuilt from old logs must not read as one that measured them.
  //
  // Two of the paragraph's three facts moved rather than went. The boundary
  // DATE is still on screen — it is the rule and caption the charts draw, which
  // `autonomyBoundaryLabel` and `autonomyVisibleBoundaries` below still pin.
  // The cost-log sentence is the one that is simply gone: it explained why a
  // run's END REASON was unknown, and #1919 deleted the run strip, which was the
  // only thing that ever drew an end reason.

  // The silent case is the one every other install gets — the reader this cut
  // was made for. A caveat about nothing would train people to skip the one
  // that matters, and here it costs that reader zero characters.
  test('says nothing about reconstruction when every run in view was measured', () => {
    const line = autonomyProvenanceLine(
      durationWith({ reconstructed: 0, cost_derived: 0, live_since: 1_700_000_000 }))
    expect(line).not.toContain('reconstructed')
    expect(line).toContain('100 runs recorded')
  })

  test('says nothing about reconstruction when the payload carries no provenance at all', () => {
    // An older daemon, or a client reading a response it did not expect.
    expect(autonomyProvenanceLine({ earliest_span: 1_700_000_000, total_recorded: 12 }))
      .not.toContain('reconstructed')
  })

  test('states how many of the runs in view are reconstructed', () => {
    const line = autonomyProvenanceLine(
      durationWith({ reconstructed: 40, cost_derived: 0, live_since: 1_755_000_000 }, 100))
    expect(line).toContain('40 in view reconstructed')
    // …on the SAME line as the total it qualifies, not a second line below it.
    expect(line).toContain('100 runs recorded · 40 in view reconstructed.')
    expect(line.split('\n')).toHaveLength(1)
  })

  // liveSince is no longer formatted into this sentence at all, which is the
  // strongest possible form of "never prints an epoch date": there is no date
  // in the clause to get wrong.
  test('the reconstruction clause carries no date of its own', () => {
    const line = autonomyProvenanceLine(durationWith({ reconstructed: 40, cost_derived: 40, live_since: 0 }))
    expect(line).toContain('40 in view reconstructed')
    expect(line).not.toContain('1970')
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

    expect(autonomyProvenanceLine(allLive)).not.toBe(autonomyProvenanceLine(backfilled))
    expect(autonomyProvenanceLine(allLive)).not.toContain('reconstructed')
    expect(autonomyProvenanceLine(backfilled)).toContain('5 in view reconstructed')
  })
})

// The run strip was REMOVED, not hidden (#1905 redesign), and it STAYS removed
// through the #1905 restore that brought the percentile band back. Dead code
// that still parses is the thing a later reader restores by accident, so its
// identifiers are checked out of the shipped files — and the band's are checked
// back IN, so a half-finished restore cannot pass as a whole one.
describe('the run strip is gone from the shipped files, and the band is back', () => {
  const js = readFileSync(join(WEB_DIR, 'historyTab.js'), 'utf8')
  const css = readFileSync(join(WEB_DIR, 'irrlicht.css'), 'utf8')
  const html = readFileSync(join(WEB_DIR, 'index.html'), 'utf8')

  test('the files were actually read', () => {
    // Absence of a finding and inability to look must not read the same. A
    // positive control per file, so "not found" below means removed rather
    // than mis-pathed.
    expect(js).toContain('autonomyPanelHeadline')
    expect(css).toContain('.history-autonomy-panel')
    expect(html).toContain('history-autonomy-panels')
  })

  test('the p5–p95 band and its three tokens are back in the shipped files', () => {
    for (const back of ['AUTONOMY_SERIES', 'autonomyBandSegments', 'autonomyChartPoints',
      'sample_floor', 'autonomyAggregateDomain']) {
      expect(js).toContain(back)
    }
    for (const token of ['--autonomy-band', '--autonomy-band-thin', '--autonomy-edge']) {
      expect(css).toContain(token)
    }
  })

  test('no log-scale mapping survives on either axis', () => {
    // The maintainer's explicit call (#1905): linear, and no log path kept
    // alive behind a flag. `Math.log` anywhere in the Autonomy code would be
    // one, so it is checked out of the file rather than merely unused.
    const section = js.slice(js.indexOf('// --- Autonomy (#1905) ---'),
      js.indexOf('// --- Activity matrix'))
    expect(section.length).toBeGreaterThan(1000) // the slice actually found the section
    expect(section).not.toContain('Math.log')
    expect(section).not.toContain('Math.exp')
    expect(section).toContain('autonomyLinearY')
  })

  // The prose #1905's cut deleted, checked out of the file for the same reason
  // the strip's identifiers are: a helper that still parses is the one a later
  // reader wires back up by accident, and these two produced four of the five
  // paragraphs the cut was made to remove.
  test('the deleted prose helpers are gone, not merely unwired', () => {
    for (const gone of ['autonomyCountingLine', 'autonomyReconstructionNote']) {
      expect(js).not.toContain(gone)
    }
    // …and the surviving four lines are all still generated somewhere.
    for (const kept of ['AUTONOMY_CONCURRENCY_CAVEAT', 'autonomyThinNote',
      'autonomyMeasurementNote', 'autonomyProvenanceLine']) {
      expect(js).toContain(kept)
    }
  })

  test('no run strip, end-reason colours, glyphs or legend survive', () => {
    for (const gone of ['collapseAutonomyStrip', 'AUTONOMY_REASON_PRIORITY', 'AUTONOMY_REASON_LEGEND',
      'AUTONOMY_UNKNOWN_LEGEND', 'autonomyLegendEntries', 'autonomyReasonColor',
      'AUTONOMY_STRIP_MAX_ROWS']) {
      expect(js).not.toContain(gone)
    }
    expect(css).not.toContain('history-autonomy-legend')
    expect(html).not.toContain('history-autonomy-strip')
  })
})

// The dropdown offers EVERY project the daemon sent. The daemon sends every
// project in the window up to a high safety cap and says how many it left out;
// the client offers what it was sent and names the rest.
describe('the dropdown offers the projects it was sent, and names the rest', () => {
  const stack = (panelCount, more) => ({
    panels: Array.from({ length: panelCount }, (_, i) => ({ project: 'p' + i, buckets: [] })),
    more_projects: more,
    panel_limit: 200,
  })

  test('every project sent is offered, however many there are', () => {
    expect(autonomyProjectOptions(stack(93, 0), null)).toHaveLength(93)
  })

  test('the overflow line says WHY those are the projects missing', () => {
    // Ranked by total autonomous time, so the tail is what went — not an
    // arbitrary slice, and not the projects with the shortest single run.
    expect(autonomyMoreProjectsLabel({ more_projects: 90 }))
      .toContain('each with less autonomous time')
  })

  test('nothing is said when the cap left nothing out', () => {
    expect(autonomyMoreProjectsLabel(stack(93, 0))).toBe('')
  })

  // The committed mutation: a client that re-decides the list itself. Two
  // surfaces doing that is exactly how the run strip ended up drawing twelve
  // rows on the web against six on macOS from one ranked list — and a picker
  // that caps its own list drops projects the summary beside it still counts.
  test('the client offers what it was sent, never its own slice', () => {
    const clientCap = (data) => data.panels.slice(0, 5)
    const data = stack(93, 0)
    expect(clientCap(data)).toHaveLength(5)
    expect(autonomyProjectOptions(data, null)).toHaveLength(data.panels.length)
  })

  test('an empty window offers nothing and claims no overflow', () => {
    expect(autonomyProjectOptions({ panels: [], more_projects: 0 }, null)).toEqual([])
    expect(autonomyProjectOptions(null, null)).toEqual([])
    expect(autonomyPanelChoice(null, null).panel).toBeNull()
  })
})

describe('source boundaries are marked across the panel stack', () => {
  const durationOver = (starts, boundaries) => ({
    bucket_starts: starts,
    panels: [],
    summary: { runs: 0 },
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

// WITH TWO ELEMENTS THE RULE HAS TO READ ONCE. Both step at the same instant —
// they share one window — so the rule is drawn on both, and the caption is
// written by exactly one. Two stacked copies of "← cost log · 60s resolution"
// are two competing captions where the reader needs one, and at 9px the lower
// one collides with the panel's own header row.
describe('the boundary caption is written once, not once per element', () => {
  test('exactly one of the two elements carries it', () => {
    const carrying = ['aggregate', 'panel'].filter(autonomyBoundaryCaptionShown)
    expect(carrying).toHaveLength(1)
  })

  test('it is the aggregate chart, which is the top element', () => {
    expect(autonomyBoundaryCaptionShown('aggregate')).toBe(true)
    expect(autonomyBoundaryCaptionShown('panel')).toBe(false)
  })

  // The committed mutation: captioning every element. It passes "a caption is
  // drawn" and fails the thing that matters, which is that there is one.
  test('production tells one caption from two', () => {
    const captionEverywhere = () => true
    expect(['aggregate', 'panel'].filter(captionEverywhere)).toHaveLength(2)
    expect(['aggregate', 'panel'].filter(autonomyBoundaryCaptionShown)).toHaveLength(1)
  })
})
