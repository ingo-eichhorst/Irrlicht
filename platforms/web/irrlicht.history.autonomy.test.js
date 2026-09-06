import { describe, test, expect } from 'vitest'

import {
  AUTONOMY_RANGE_LABELS,
  AUTONOMY_MARK_TOKENS,
  AUTONOMY_BOUNDARY_CAPTION_ON,
  AUTONOMY_CONCURRENCY_CAVEAT,
  autonomyQuery,
  autonomyPanelPoints,
  autonomyLineSegments,
  autonomyYDomain,
  autonomyAggregateDomain,
  autonomyYAt,
  autonomyTickValues,
  autonomyPeakScale,
  autonomyPanelChoice,
  autonomyProjectOptions,
  autonomyEmptyProjectNote,
  autonomyBucketAtX,
  autonomyPanelTooltip,
  autonomyBarAxisLabels,
  autonomyGutterLabels,
  autonomyBaselineY,
  AUTONOMY_PANEL,
  AUTONOMY_PANEL_CANVAS_H,
  autonomyConcurrencyLabel,
  autonomyPanelHeadline,
  autonomyMoreProjectsLabel,
  autonomyBoundaryCaptionShown,
  autonomyBoundaryLabel,
  autonomyVisibleBoundaries,
  autonomyDuration,
  autonomyProvenanceLine,
  autonomyAxisLabel,
  autonomyKeyColor,
  autonomyKeyEntries,
  autonomyPanelRows,
  buildAutonomyAxis,
} from './historyTab.js'

// The Autonomy section (#1905): an AGGREGATE percentile chart over every
// project, and ONE per-project panel under it — a longest-run line over a
// concurrency histogram — picked from a dropdown.
//
// What these pin is the set of claims the section makes about data it cannot
// show in full — the gap rule on every mark, the split that is only ever shown
// where the data supports one, the project the picker keeps when a range does
// not hold it, and the wording that keeps an empty view from reading as "you
// did nothing".

// bucket builds one panel bucket. `longest` 0 means "no run ended here";
// `peak` 0 means "nobody was working here".
const bucket = (ts, longest, peak, extra = {}) => ({ ts, longest, peak, peak_top: 0, peak_sub: 0, ...extra })
const panel = (project, buckets, extra = {}) => ({
  project, buckets, longest: 0, total_seconds: 0, runs: buckets.length,
  peak: 0, peak_top: 0, peak_sub: 0, ...extra,
})

describe('a bucket with no runs is a gap, not a zero', () => {
  // The honesty rule, and it now binds TWO marks rather than one: the line has
  // to break, and the histogram has to draw nothing. A zero-height bar sitting
  // on the axis looks measured; a zero point pulls the line down to it.
  const starts = [100, 200, 300, 400, 500]

  test('an omitted bucket becomes a null, aligned to bucket_starts', () => {
    const points = autonomyPanelPoints(panel('p', [bucket(100, 60, 1), bucket(400, 90, 2)]), starts)
    expect(points).toHaveLength(5)
    expect(points[0].longest).toBe(60)
    expect(points[1]).toBeNull()
    expect(points[2]).toBeNull()
    expect(points[3].longest).toBe(90)
    expect(points[4]).toBeNull()
  })

  test('the line breaks at a gap instead of spanning it', () => {
    const points = autonomyPanelPoints(panel('p', [bucket(100, 60, 1), bucket(400, 90, 2)]), starts)
    // The MUTATION this refuses: `points.filter(Boolean)`, a dense list in
    // which the gap simply is not there and the stroke runs straight across.
    const dense = points.filter(Boolean)
    expect(autonomyLineSegments(dense)).toEqual([{ from: 0, to: 1 }])
    expect(autonomyLineSegments(points)).toEqual([{ from: 0, to: 0 }, { from: 3, to: 3 }])
  })

  test('a bar is drawn only where somebody was working', () => {
    const points = autonomyPanelPoints(panel('p', [bucket(100, 60, 2), bucket(400, 90, 0)]), starts)
    const drawn = points.map(p => (p && Number(p.peak) > 0 ? p.peak : null))
    expect(drawn).toEqual([2, null, null, null, null])
  })

  test('a run that passed through breaks the line but keeps its bar', () => {
    // A bucket a long run merely crossed: something WAS working, nothing
    // finished. Two true facts, and neither may be borrowed for the other.
    const points = autonomyPanelPoints(
      panel('p', [bucket(100, 60, 1), bucket(200, 0, 3), bucket(300, 90, 1)]), starts)
    expect(autonomyLineSegments(points)).toEqual([{ from: 0, to: 0 }, { from: 2, to: 2 }])
    expect(points[1].peak).toBe(3)
  })

  test('an empty panel has no segments at all', () => {
    expect(autonomyLineSegments(autonomyPanelPoints(panel('p', []), starts))).toEqual([])
    expect(autonomyLineSegments(null)).toEqual([])
  })
})

describe('the panel\u2019s Y axis is that project\u2019s own, and LINEAR', () => {
  // Its own, because exactly one panel is drawn: there is nothing left to be
  // comparable WITH, and a domain stretched to fit projects that are not on
  // screen would flatten the one that is.
  //
  // Linear is the maintainer\u2019s explicit call, and these tests record BOTH
  // halves of it — that the mapping is linear, and what linear costs.
  const big = panel('big', [bucket(1, 41_940, 5)])   // 11h39m
  const small = panel('small', [bucket(1, 120, 1)])  // 2m

  test('the domain is the drawn panel\u2019s own, not every panel\u2019s', () => {
    // The MUTATION this refuses: the shared domain the five-panel stack used.
    // Under it `small` is plotted against big\u2019s 11h39m and is a flat line on
    // the floor; with its own domain its 2m fills the plot.
    const own = autonomyYDomain(small)
    const shared = { lo: 0, hi: 41_940 * 1.1 }
    expect(own.hi).toBeLessThan(shared.hi)
    expect(autonomyYAt(120, own, 100)).not.toBeCloseTo(autonomyYAt(120, shared, 100))
    expect(autonomyYAt(120, own, 100)).toBeLessThan(10) // near the TOP of its own plot
    expect(autonomyYAt(120, shared, 100)).toBeGreaterThan(99) // on the floor of the shared one
  })

  test('the mapping is linear, not logarithmic', () => {
    // The MUTATION this refuses: the log mapping that shipped before. Halfway
    // up a LINEAR plot is half the domain; on a log plot it is its geometric
    // mean, which for 0\u2026100 is a different number entirely.
    const domain = { lo: 0, hi: 100 }
    expect(autonomyYAt(50, domain, 100)).toBeCloseTo(50)
    const logY = (v) => 100 * (1 - (Math.log(Math.max(1, v)) - Math.log(1)) / (Math.log(100) - Math.log(1)))
    expect(logY(50)).not.toBeCloseTo(autonomyYAt(50, domain, 100))
  })

  test('the axis starts at zero, which the log axis could not', () => {
    // A linear axis whose origin is not zero exaggerates every difference above
    // it, and the "a log scale cannot plot 0" reason for lifting it is gone.
    expect(autonomyYDomain(big).lo).toBe(0)
    expect(autonomyYAt(0, autonomyYDomain(big), 100)).toBe(100)
  })

  test('the ticks are evenly spaced in VALUE, not in ratio', () => {
    const ticks = autonomyTickValues({ lo: 0, hi: 100 }, 2)
    expect(ticks).toEqual([0, 50, 100])
  })

  test('the cost of linear is real, and this is what it looks like', () => {
    // Recorded rather than hidden: on a linear axis a project whose longest day
    // is 11h39m plots its typical 10m runs in the bottom ~1.5% of the plot.
    const domain = autonomyYDomain(big)
    const y = autonomyYAt(600, domain, 110)
    expect(y).toBeGreaterThan(110 * 0.98)
  })

  test('an empty panel has no domain rather than a fabricated one', () => {
    expect(autonomyYDomain(null)).toBeNull()
    expect(autonomyYDomain(panel('p', [bucket(1, 0, 2)]))).toBeNull()
  })

  test('the histogram scale is the panel\u2019s own, and absent when nothing overlapped', () => {
    expect(autonomyPeakScale(big)).toBe(5)
    expect(autonomyPeakScale(panel('p', [bucket(1, 60, 0)]))).toBe(0)
  })
})

describe('the aggregate chart\u2019s Y axis is linear too', () => {
  const pts = [{ ts: 1, p95: 1200, p50: 200, p5: 60, min: 60, max: 1500, count: 30 }]

  test('the domain covers the drawn p95s and the true max, from zero', () => {
    const domain = autonomyAggregateDomain(pts)
    expect(domain.lo).toBe(0)
    expect(domain.hi).toBeGreaterThanOrEqual(1500)
  })

  test('a window with nothing drawn has no domain', () => {
    expect(autonomyAggregateDomain([])).toBeNull()
    expect(autonomyAggregateDomain([null, null])).toBeNull()
  })
})

describe('the concurrency figure, and the split it may not invent', () => {
  test('a derivable split is spelled out beside the total', () => {
    expect(autonomyConcurrencyLabel(5, 3, 2, true)).toBe('5 at once (3 + 2 sub)')
  })

  test('an underivable split shows the total alone', () => {
    // Most rows before 18 Aug 2026 carry `kind: unknown`; there is no way to
    // recover which they were, and "5 (5 + 0 sub)" would be an invented answer.
    expect(autonomyConcurrencyLabel(5, 0, 0, false)).toBe('5 at once')
    expect(autonomyConcurrencyLabel(5, 0, 0, false)).not.toMatch(/sub/)
  })

  test('a window nobody overlapped in says nothing at all', () => {
    expect(autonomyConcurrencyLabel(0, 0, 0, true)).toBe('')
  })

  test('the caveat is on screen, and says what the number is not', () => {
    // The number is otherwise misread: a parent is held `working` while its
    // subagents run, so one agent with three subagents reads as four at once.
    expect(AUTONOMY_CONCURRENCY_CAVEAT).toMatch(/parent/i)
    expect(AUTONOMY_CONCURRENCY_CAVEAT).toMatch(/subagents/i)
    expect(AUTONOMY_CONCURRENCY_CAVEAT).toMatch(/not four independent agents/i)
  })
})

describe('a panel states its own two figures in its header', () => {
  test('longest and at-once, in the order the sketch has them', () => {
    expect(autonomyPanelHeadline({ longest: 41_940, peak: 5, peak_top: 3, peak_sub: 2, peak_split: true }))
      .toBe('longest 11h39m · 5 at once (3 + 2 sub)')
  })

  test('a still-running longest run is marked, not dropped', () => {
    const head = autonomyPanelHeadline({ longest: 10_800, longest_running: true, peak: 1, peak_split: false })
    expect(head).toMatch(/longest 3h/)
    expect(head).toMatch(/still going/)
  })

  test('a project nobody overlapped in still states its longest', () => {
    expect(autonomyPanelHeadline({ longest: 600, peak: 0 })).toBe('longest 10m')
  })
})

describe('the payload cap names what it left out', () => {
  test('the overflow line says how many projects and why they are the ones missing', () => {
    const label = autonomyMoreProjectsLabel({ more_projects: 7 })
    expect(label).toMatch(/^\+7 more projects past the payload cap/)
    expect(label).toMatch(/less autonomous time/)
  })

  test('one hidden project is singular', () => {
    expect(autonomyMoreProjectsLabel({ more_projects: 1 })).toMatch(/^\+1 more project past/)
  })

  test('nothing is said when every project fits', () => {
    expect(autonomyMoreProjectsLabel({ more_projects: 0 })).toBe('')
    expect(autonomyMoreProjectsLabel(undefined)).toBe('')
  })
})

describe('the source-boundary rule reads once across the stack', () => {
  const data = {
    bucket_starts: [1000, 2000, 3000, 4000],
    provenance: { boundaries: [{ ts: 2500, from: 'cost', to: 'log' }] },
  }

  test('a boundary inside the drawn domain gets its x fraction', () => {
    const visible = autonomyVisibleBoundaries(data)
    expect(visible).toHaveLength(1)
    expect(visible[0].fraction).toBeCloseTo(0.5)
  })

  test('a boundary on the axis itself is not drawn', () => {
    // A rule on the first or last bucket marks nothing and reads as a border.
    expect(autonomyVisibleBoundaries({
      ...data, provenance: { boundaries: [{ ts: 1000, from: 'cost', to: 'log' }] },
    })).toEqual([])
    expect(autonomyVisibleBoundaries({
      ...data, provenance: { boundaries: [{ ts: 4000, from: 'cost', to: 'log' }] },
    })).toEqual([])
  })

  test('the caption is written by exactly one of the two elements', () => {
    // The rule is drawn on BOTH — it has to be, or it annotates the aggregate
    // line and leaves the panel below it stepping for no stated reason — but
    // two stacked copies of "← cost log · 60s resolution" would be two
    // competing captions where the reader needs one.
    const shown = ['aggregate', 'panel'].filter(autonomyBoundaryCaptionShown)
    expect(shown).toEqual([AUTONOMY_BOUNDARY_CAPTION_ON])
    expect(shown).toHaveLength(1)
  })

  test('it is the aggregate chart, which is the TOP element', () => {
    // On the panel the caption would collide with the header row that carries
    // the project picker and its figures.
    expect(AUTONOMY_BOUNDARY_CAPTION_ON).toBe('aggregate')
    expect(autonomyBoundaryCaptionShown('panel')).toBe(false)
  })

  test('the caption describes the era to the LEFT of the rule', () => {
    // The arrow is load-bearing: without it the caption reads as a label for
    // the line rather than for the data before it.
    expect(autonomyBoundaryLabel({ from: 'cost' })).toBe('← cost log · 60s resolution')
    expect(autonomyBoundaryLabel({ from: 'log' })).toMatch(/^← event log/)
    expect(autonomyBoundaryLabel({ from: 'nonsense' })).toBe('← nonsense')
    expect(autonomyBoundaryLabel({})).toMatch(/different source/)
  })
})

describe('the window vocabulary stays distinct from chart=state’s', () => {
  // The trap #1905 calls out by name: chart=state's granularity keys and the
  // Autonomy windows OVERLAP textually and mean different things — a
  // granularity is a bucket width times a count ('24h' → a 30-DAY window), an
  // autonomy window IS the window.
  test('both elements send the SAME window, which is what makes one Range control honest', () => {
    expect(autonomyQuery('duration', { autonomyRange: '1y' })).toBe('chart=autonomy_duration&window=1y')
    expect(autonomyQuery('projects', { autonomyRange: '1y' })).toBe('chart=autonomy_projects&window=1y')
    expect(autonomyQuery('duration', { autonomyRange: '30d' })).toBe('chart=autonomy_duration&window=30d')
    expect(autonomyQuery('projects', { autonomyRange: '30d' })).toBe('chart=autonomy_projects&window=30d')
    // The MUTATION this refuses: a per-element window, the shape the run
    // strip's Span picker had. Two windows under one control is how a reader
    // ends up comparing a month against a year with nothing saying so.
    const split = (el) => (el === 'projects' ? 'window=24h' : 'window=30d')
    expect(split('duration')).not.toBe(split('projects'))
  })

  test('the picker offers two windows, neither of them a granularity key', () => {
    expect(Object.keys(AUTONOMY_RANGE_LABELS)).toEqual(['30d', '1y'])
    // The run strip's own vocabulary went with the strip. If any of its keys
    // reappears here, two textually-overlapping sets are back on one screen.
    for (const stripKey of ['8h', '24h', '7d', '12mo']) {
      expect(AUTONOMY_RANGE_LABELS[stripKey]).toBeUndefined()
    }
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

describe('the panels are one hue in two weights', () => {
  // `cs` here returns '' for every custom property, which is what an unstyled
  // document (and jsdom) gives — so this exercises the fallback branch, the one
  // a stylesheet-less render actually takes.
  const unstyled = { getPropertyValue: () => '' }

  // rgb triple of a '#rrggbb' or 'rgba(r, g, b, a)' literal, as a string.
  // Throws on anything else rather than returning a value nothing can trust: a
  // colour this cannot parse is the last place to drop the comparison.
  const rgbOf = (color) => {
    const hex = /^#([0-9a-f]{2})([0-9a-f]{2})([0-9a-f]{2})$/i.exec(color)
    if (hex) return [1, 2, 3].map(i => parseInt(hex[i], 16)).join(',')
    const rgba = /^rgba?\(\s*(\d+)\s*,\s*(\d+)\s*,\s*(\d+)/.exec(color)
    if (rgba) return [1, 2, 3].map(i => Number(rgba[i])).join(',')
    throw new Error('unparseable colour: ' + color)
  }

  test('the bars are the line hue, not a second colour', () => {
    expect(rgbOf(autonomyKeyColor('bars', unstyled))).toBe(rgbOf(autonomyKeyColor('line', unstyled)))
  })

  test('the bars are quieter than the line', () => {
    // A histogram at the line's full strength competes with the line above it;
    // the bars are a second reading of the same activity, not a second subject.
    const alpha = /rgba\([^)]*,\s*([\d.]+)\s*\)/.exec(AUTONOMY_MARK_TOKENS.bars[1])
    expect(alpha).toBeTruthy()
    expect(Number(alpha[1])).toBeLessThan(1)
  })

  test('every mark the section draws is the same hue', () => {
    // One hue throughout: the band, its edges and the bars are all the line's
    // hue at lower alphas, because they are further readings of the same
    // activity and not further subjects.
    const line = rgbOf(autonomyKeyColor('line', unstyled))
    for (const mark of ['p50', 'band', 'bandThin', 'edge', 'bars']) {
      expect(rgbOf(autonomyKeyColor(mark, unstyled))).toBe(line)
    }
  })

  test('a mark the section does not draw has no colour to give it', () => {
    expect(autonomyKeyColor('p99', unstyled)).toBe('')
    expect(autonomyKeyColor('', unstyled)).toBe('')
  })

  test('a live stylesheet wins over the fallback', () => {
    const styled = { getPropertyValue: (n) => (n === '--working' ? ' #123456 ' : '') }
    expect(autonomyKeyColor('line', styled)).toBe('#123456')
  })
})

describe('the key has four entries, and each stands for a mark on screen', () => {
  const unstyled = { getPropertyValue: () => '' }

  test('two lines, one plane and a row of bars, in the order they appear', () => {
    const entries = autonomyKeyEntries(unstyled)
    expect(entries.map(e => e.kind)).toEqual(['p50', 'band', 'line', 'bars'])
    for (const e of entries) expect(e.color).toBeTruthy()
  })

  test('the aggregate chart\u2019s two marks are named, and so are the panel\u2019s', () => {
    const labels = autonomyKeyEntries(unstyled).map(e => e.label).join(' ')
    expect(labels).toMatch(/p50/)
    expect(labels).toMatch(/spread/)
    expect(labels).toMatch(/longest run/)
    expect(labels).toMatch(/at once/)
  })

  test('only the band has a fill \u2014 a line has no area', () => {
    const entries = autonomyKeyEntries(unstyled)
    const withFill = entries.filter(e => e.fill)
    expect(withFill.map(e => e.kind)).toEqual(['band'])
  })
})

describe('the side panel carries the window’s figures', () => {
  const unstyled = { getPropertyValue: () => '' }
  const both = (durationSummary, projectsSummary) => ({
    duration: { summary: durationSummary },
    projects: { summary: projectsSummary },
  })

  test('the four key rows carry both elements\u2019 headline numbers', () => {
    const rows = autonomyPanelRows(
      both({ p95: 7080, p50: 660, p5: 41, min: 6, max: 41_940, count: 312 },
           { longest: 41_940, peak: 5, runs: 312, projects: 12 }), unstyled)
    expect(rows[0].value).toBe('11m')                 // p50
    expect(rows[1].value).toBe('41s \u2013 1h58m')        // p5 \u2013 p95
    expect(rows[2].value).toBe('11h39m')              // the panel\u2019s longest
    expect(rows[3].value).toBe('5')                   // peak at once
    expect(rows.map(r => r.label)).toContain('runs')
    expect(rows.map(r => r.label)).toContain('projects')
  })

  test('every key row has a real swatch colour', () => {
    // The defect this pins: rows built and then every dot blanked, leaving
    // marks on the canvas with no key at all.
    for (const row of autonomyPanelRows(both({ p50: 60, p5: 6, p95: 90 }, { longest: 60, peak: 1, runs: 1, projects: 1 }), unstyled)) {
      if (row.kind) expect(row.swatch).toBeTruthy()
    }
  })

  test('a still-running longest run says so here too', () => {
    const rows = autonomyPanelRows(both({}, { longest: 10_800, longest_running: true, runs: 1, projects: 1 }), unstyled)
    expect(rows[2].value).toMatch(/still going/)
  })

  test('the figure rows stay unswatched', () => {
    // A swatch would claim ink the section deliberately does not lay down.
    const rows = autonomyPanelRows(both({}, { longest: 60, peak: 1, runs: 4, projects: 2 }), unstyled)
    for (const row of rows.filter(r => !r.kind)) expect(row.swatch).toBe('transparent')
  })
})

describe('the panel states its own time bounds', () => {
  // Without them the panel is texture: at 30d and 1y a mark cannot be placed
  // in time at all.
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

  test('the axis names the window start on the left and now on the right', () => {
    const now = Math.floor(Date.UTC(2026, 0, 9) / 1000)
    const axis = buildAutonomyAxis({ start: now - 30 * 86400, end: now })
    const bounds = [...axis.querySelectorAll('i')].map(i => i.textContent)
    expect(bounds).toHaveLength(2)
    expect(bounds[1]).toBe('now')
    expect(bounds[0]).toMatch(/Dec/)
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

// --- One panel at a time, and the picker that chooses it (#1905) -------------

describe('the panel draws ONE project, chosen from the dropdown', () => {
  const data = {
    panels: [panel('irrlicht', [bucket(1, 600, 2)]), panel('articles', [bucket(1, 300, 1)])],
    bucket_starts: [1],
  }

  test('nothing selected draws rank 1, the busiest project', () => {
    const choice = autonomyPanelChoice(data, null)
    expect(choice.project).toBe('irrlicht')
    expect(choice.panel).toBe(data.panels[0])
    expect(choice.missing).toBe(false)
  })

  test('a selection this range holds draws that project', () => {
    const choice = autonomyPanelChoice(data, 'articles')
    expect(choice.project).toBe('articles')
    expect(choice.panel).toBe(data.panels[1])
    expect(choice.missing).toBe(false)
  })

  test('a selection this range does NOT hold is kept, and marked', () => {
    // The MUTATION this refuses: `panels.find(...) || panels[0]`, the silent
    // fall back to rank 1. Under it a Range change looks like a click the
    // reader never made, and the project they were looking at is gone with
    // nothing on screen saying where it went.
    const silentFallback = data.panels.find(p => p.project === 'besenkammer') || data.panels[0]
    expect(silentFallback.project).toBe('irrlicht')

    const choice = autonomyPanelChoice(data, 'besenkammer')
    expect(choice.project).toBe('besenkammer')
    expect(choice.panel).toBeNull()
    expect(choice.missing).toBe(true)
  })

  test('an empty window chooses nothing rather than inventing a project', () => {
    const choice = autonomyPanelChoice({ panels: [] }, null)
    expect(choice.panel).toBeNull()
    expect(choice.project).toBe('')
  })

  test('the dropdown offers every project the window holds, ranked', () => {
    expect(autonomyProjectOptions(data, null).map(o => o.value)).toEqual(['irrlicht', 'articles'])
  })

  test('…and keeps an absent selection in the list, last and labelled', () => {
    const opts = autonomyProjectOptions(data, 'besenkammer')
    expect(opts.map(o => o.value)).toEqual(['irrlicht', 'articles', 'besenkammer'])
    // Last by the SAME rule the rest follow — the order is most autonomous
    // time first, and a project with no runs in this range has none of it.
    expect(opts[2].missing).toBe(true)
    expect(opts[2].label).toMatch(/no runs in this range/)
  })

  test('an absent selection gets a sentence, not an empty frame', () => {
    // An empty plot with axes on it is the same picture a broken request
    // draws, and the reader cannot tell which they are looking at.
    expect(autonomyEmptyProjectNote('besenkammer')).toBe('no runs for besenkammer in this range')
    expect(autonomyEmptyProjectNote('')).toMatch(/this project/)
  })
})

// --- The concurrency figure is legible: an axis, and a tooltip ---------------

describe('the histogram labels its own y axis', () => {
  test('the band’s full-height value sits at its top, and 0 on the baseline', () => {
    const labels = autonomyBarAxisLabels(15)
    expect(labels.map(l => l.text)).toEqual(['15', '0'])
    expect(labels[1].y).toBe(autonomyBaselineY())
    expect(labels[0].y).toBe(autonomyBaselineY() - AUTONOMY_PANEL.barsH)
  })

  test('the axis goes red against the blank gutter that shipped', () => {
    // The MUTATION, which is what shipped in #1919 on both surfaces: a gutter
    // kept for alignment and deliberately left EMPTY. That is what "the number
    // of agents running in parallel is not visible" was about — the bars
    // carried the only figure on the panel with no scale of any kind.
    const blankGutter = []
    expect(blankGutter).not.toEqual(autonomyBarAxisLabels(15))
    expect(autonomyBarAxisLabels(15)).toHaveLength(2)
  })

  test('nothing overlapped anywhere means no axis at all', () => {
    // An axis labelled 0 to 0 would be furniture claiming a measurement.
    expect(autonomyBarAxisLabels(0)).toEqual([])
    expect(autonomyBarAxisLabels(undefined)).toEqual([])
  })

  test('both labels are inside the canvas, in the line chart’s own gutter', () => {
    // padB exists for the baseline label: it is drawn CENTRED on the baseline,
    // so without an inset half of it would fall off the bottom of the canvas.
    const half = AUTONOMY_PANEL.tickFont / 2
    for (const l of autonomyBarAxisLabels(9)) {
      expect(l.y - half).toBeGreaterThanOrEqual(0)
      expect(l.y + half).toBeLessThanOrEqual(AUTONOMY_PANEL_CANVAS_H)
    }
  })
})

describe('the tooltip names the bucket the pointer is actually over', () => {
  // n = 5 buckets across a 200px plot starting at padL.
  const geo = { padL: 4, plotW: 200, n: 5 }
  // The painter's own mapping, copied here so the inverse can be checked
  // against it rather than against a second guess at it.
  const xAt = (i) => geo.padL + geo.plotW * (i / (geo.n - 1))

  test('every bucket’s own x hits that bucket', () => {
    for (let i = 0; i < geo.n; i++) expect(autonomyBucketAtX(xAt(i), geo)).toBe(i)
  })

  test('the hit test goes red against arithmetic of its own', () => {
    // The MUTATION: a plausible-looking hit test that divides the plot into n
    // equal COLUMNS, instead of inverting xAt — which spaces n POINTS across
    // the plot, so the gap between them is plotW/(n-1) and the nearest point to
    // a pixel is a rounding, not a flooring.
    //
    // It happens to agree at the bucket centres, which is exactly why the sweep
    // below is over the whole plot: a tooltip that is right only where the
    // pointer lands dead on a gridline is a tooltip that is wrong nearly
    // everywhere, and it would name a different bucket from the one drawn under
    // the pointer with nothing on screen saying so.
    const ownArithmetic = (x) => Math.min(geo.n - 1, Math.floor(((x - geo.padL) / geo.plotW) * geo.n))
    let disagreements = 0
    for (let x = geo.padL; x <= geo.padL + geo.plotW; x++) {
      if (ownArithmetic(x) !== autonomyBucketAtX(x, geo)) disagreements++
    }
    // ~40 of the plot's 201 pixel columns, i.e. roughly a fifth of it.
    expect(disagreements).toBeGreaterThan(geo.plotW / 10)
  })

  test('the gutters report nothing rather than the end buckets', () => {
    expect(autonomyBucketAtX(0, geo)).toBeNull()
    expect(autonomyBucketAtX(geo.padL + geo.plotW + 20, geo)).toBeNull()
  })

  test('a single-bucket window is bucket 0, not a division by zero', () => {
    expect(autonomyBucketAtX(4, { padL: 4, plotW: 200, n: 1 })).toBe(0)
  })

  test('a degenerate plot hits nothing', () => {
    expect(autonomyBucketAtX(10, { padL: 4, plotW: 0, n: 5 })).toBeNull()
    expect(autonomyBucketAtX(10, { padL: 4, plotW: 200, n: 0 })).toBeNull()
  })
})

describe('what the tooltip says', () => {
  const day = Date.UTC(2026, 0, 9) / 1000
  const month = 30 * 86400

  test('the bucket’s date, its longest run and how many ran at once', () => {
    const text = autonomyPanelTooltip(bucket(day, 41_940, 15), day, month)
    expect(text).toMatch(/Jan/)
    expect(text).toContain('longest 11h39m')
    expect(text).toContain('15 at once')
  })

  test('the split rides along wherever the daemon says it is derivable', () => {
    const text = autonomyPanelTooltip(
      bucket(day, 600, 5, { peak_top: 3, peak_sub: 2, peak_split: true }), day, month)
    expect(text).toContain('5 at once (3 + 2 sub)')
  })

  test('…and never where it is not', () => {
    const text = autonomyPanelTooltip(bucket(day, 600, 5, { peak_top: 3, peak_sub: 2 }), day, month)
    expect(text).toContain('5 at once')
    expect(text).not.toContain('sub)')
  })

  test('a bucket a run merely crossed says so rather than claiming a zero run', () => {
    // longest 0 with a bar: something WAS working, nothing finished. "longest
    // 0s" would claim a run of no length.
    const text = autonomyPanelTooltip(bucket(day, 0, 3), day, month)
    expect(text).toContain('nothing finished')
    expect(text).not.toContain('0s')
    expect(text).toContain('3 at once')
  })

  test('a still-going longest run is marked here too', () => {
    expect(autonomyPanelTooltip(bucket(day, 10_800, 1, { running: true }), day, month))
      .toContain('(still going)')
  })

  test('a bucket the daemon omitted says nothing at all', () => {
    // It draws nothing, so it has nothing to report — and a tooltip over a gap
    // would be the one place the section claimed a measurement it does not have.
    expect(autonomyPanelTooltip(null, day, month)).toBe('')
  })
})

// --- QA-4: two axes, one gutter (#1905) ------------------------------------

describe('the two y axes share one gutter without overprinting', () => {
  // THE DEFECT, caught by QA of this very change in a browser: adding the
  // histogram's own axis put a second set of labels in the gutter the line
  // axis already used, and at the 3px gap the five-panel stack had — where the
  // bar band carried NO labels — the line axis's floor tick "0s" and the
  // histogram's peak "15" were drawn 3px apart and overprinted into a smudge.
  // A wrong figure with nothing on screen saying so, in the gutter this change
  // added precisely to make the concurrency figure legible.
  const domain = { lo: 0, hi: 41_940 }
  const width = 420

  const overlaps = (labels) => {
    const bad = []
    for (let i = 1; i < labels.length; i++) {
      if (labels[i].top < labels[i - 1].bottom) bad.push([labels[i - 1].text, labels[i].text])
    }
    return bad
  }

  test('every label in the gutter is listed, from both axes', () => {
    // Fail-loud: an empty or single-axis list would satisfy the overlap check
    // vacuously, which is the one thing a geometry check must not do.
    const labels = autonomyGutterLabels(domain, 15, width)
    expect(labels.length).toBeGreaterThanOrEqual(5) // 3 line ticks + 2 bar labels
    expect(new Set(labels.map(l => l.axis))).toEqual(new Set(['line', 'bars']))
  })

  test('no two of them overlap', () => {
    expect(overlaps(autonomyGutterLabels(domain, 15, width))).toEqual([])
  })

  test('the check goes red against the gap that shipped', () => {
    // THE COMMITTED MUTATION: the 3px gap, which is what the browser showed.
    const before = { ...AUTONOMY_PANEL, gap: 3 }
    const bad = overlaps(autonomyGutterLabels(domain, 15, width, before))
    expect(bad.length, 'the shipped gap did not overprint, so this fixture cannot show the fix')
      .toBeGreaterThan(0)
    expect(bad[0]).toEqual(['0s', '15'])
  })

  test('the gap is derived from the tick font, not a lucky constant', () => {
    // The two labels nearest each other are the line axis's floor and the
    // histogram's peak, one at each end of `gap`. Clearing one whole glyph is
    // what keeps them apart for any larger tick font.
    expect(AUTONOMY_PANEL.gap).toBeGreaterThanOrEqual(AUTONOMY_PANEL.tickFont)
  })

  test('a panel nobody overlapped in still lays its line axis out cleanly', () => {
    // peakMax 0 draws no histogram axis at all, so the gutter is the line
    // axis's alone — and must still not overlap itself.
    const labels = autonomyGutterLabels(domain, 0, width)
    expect(labels.every(l => l.axis === 'line')).toBe(true)
    expect(overlaps(labels)).toEqual([])
  })
})
