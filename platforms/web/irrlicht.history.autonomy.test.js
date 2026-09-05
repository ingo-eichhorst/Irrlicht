import { describe, test, expect } from 'vitest'

import {
  AUTONOMY_RANGE_LABELS,
  AUTONOMY_MARK_TOKENS,
  AUTONOMY_BOUNDARY_CAPTION_PANEL,
  AUTONOMY_CONCURRENCY_CAVEAT,
  autonomyQuery,
  autonomyPanelPoints,
  autonomyLineSegments,
  autonomyYDomain,
  autonomyYAt,
  autonomyPeakScale,
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

// The Autonomy section (#1905), redesigned: five per-project panels, each a
// longest-run line over a concurrency histogram.
//
// What these pin is the set of claims the panels make about data they cannot
// show in full — the gap rule on BOTH marks, the shared axis that makes five
// panels comparable, the split that is only ever shown where the data supports
// one, and the wording that keeps an empty view from reading as "you did
// nothing".

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

describe('the five panels share one log Y axis', () => {
  // Shared, so the projects are comparable — that is the point of picking
  // five. Log, because a project whose best is two minutes would otherwise be
  // a flat line under one whose best is eleven hours.
  const panels = [
    panel('big', [bucket(1, 41_940, 5)]),   // 11h39m
    panel('small', [bucket(1, 120, 1)]),    // 2m
  ]

  test('the domain spans every panel, not just the one being drawn', () => {
    const domain = autonomyYDomain(panels)
    expect(domain.lo).toBeLessThanOrEqual(120)
    expect(domain.hi).toBeGreaterThanOrEqual(41_940)
  })

  test('the same duration lands at the same height in every panel', () => {
    // The MUTATION this refuses: a per-panel domain. Under it `small`'s own
    // 120s would sit at the top of its plot while `big`'s 120s sat near the
    // bottom, and the five panels would not be comparable at all.
    const shared = autonomyYDomain(panels)
    const perPanel = autonomyYDomain([panels[1]])
    expect(autonomyYAt(120, shared, 32)).toBeCloseTo(autonomyYAt(120, shared, 32))
    expect(autonomyYAt(120, perPanel, 32)).not.toBeCloseTo(autonomyYAt(120, shared, 32))
  })

  test('a log axis keeps a two-minute project off the floor', () => {
    const domain = autonomyYDomain(panels)
    const y = autonomyYAt(120, domain, 32)
    // Linear, 120 of 41 940 would be 0.3% of the plot height — indistinguishable
    // from the axis itself.
    expect(y).toBeLessThan(32 * 0.997)
    expect(y).toBeGreaterThan(0)
  })

  test('an empty window has no domain rather than a fabricated one', () => {
    expect(autonomyYDomain([])).toBeNull()
    expect(autonomyYDomain([panel('p', [bucket(1, 0, 2)])])).toBeNull()
  })

  test('the histogram scale is shared too, and absent when nothing overlapped', () => {
    expect(autonomyPeakScale(panels)).toBe(5)
    expect(autonomyPeakScale([panel('p', [bucket(1, 60, 0)])])).toBe(0)
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

describe('the five-panel stack names what it left out', () => {
  test('the overflow line says how many projects and why they are the ones missing', () => {
    const label = autonomyMoreProjectsLabel({ more_projects: 7 })
    expect(label).toBe('+7 more projects, each with less autonomous time')
  })

  test('one hidden project is singular', () => {
    expect(autonomyMoreProjectsLabel({ more_projects: 1 })).toMatch(/^\+1 more project,/)
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

  test('the caption is drawn on exactly one panel', () => {
    // The rule runs through all five — it has to, or it annotates one project's
    // line and leaves the four below it stepping for no stated reason — but
    // five stacked copies of "← cost log · 60s resolution" would be five
    // competing captions where the reader needs one.
    const shown = [0, 1, 2, 3, 4].filter(autonomyBoundaryCaptionShown)
    expect(shown).toEqual([AUTONOMY_BOUNDARY_CAPTION_PANEL])
    expect(shown).toHaveLength(1)
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
  test('the section sends one chart and one window', () => {
    expect(autonomyQuery({ autonomyRange: '1y' })).toBe('chart=autonomy_projects&window=1y')
    expect(autonomyQuery({ autonomyRange: '30d' })).toBe('chart=autonomy_projects&window=30d')
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

  test('a mark the panels do not draw has no colour to give it', () => {
    expect(autonomyKeyColor('band', unstyled)).toBe('')
    expect(autonomyKeyColor('p50', unstyled)).toBe('')
  })

  test('a live stylesheet wins over the fallback', () => {
    const styled = { getPropertyValue: (n) => (n === '--working' ? ' #123456 ' : '') }
    expect(autonomyKeyColor('line', styled)).toBe('#123456')
  })
})

describe('the key has two entries, and both stand for a mark on the panel', () => {
  const unstyled = { getPropertyValue: () => '' }

  test('one line and one row of bars, and nothing else', () => {
    const entries = autonomyKeyEntries(unstyled)
    expect(entries.map(e => e.kind)).toEqual(['line', 'bars'])
    for (const e of entries) expect(e.color).toBeTruthy()
  })

  test('the p50/band key went with the percentiles', () => {
    const labels = autonomyKeyEntries(unstyled).map(e => e.label).join(' ')
    expect(labels).not.toMatch(/p50|p95|p5\b|spread/)
    expect(labels).toMatch(/longest run/)
    expect(labels).toMatch(/at once/)
  })
})

describe('the side panel carries the window’s figures', () => {
  const unstyled = { getPropertyValue: () => '' }

  test('the two key rows carry the two headline numbers', () => {
    const rows = autonomyPanelRows({ longest: 41_940, peak: 5, runs: 312, projects: 12 }, unstyled)
    expect(rows[0].value).toBe('11h39m')
    expect(rows[1].value).toBe('5')
    expect(rows.map(r => r.label)).toContain('runs')
    expect(rows.map(r => r.label)).toContain('projects')
  })

  test('every key row has a real swatch colour', () => {
    // The defect this pins: rows built and then every dot blanked, leaving
    // marks on the canvas with no key at all.
    for (const row of autonomyPanelRows({ longest: 60, peak: 1, runs: 1, projects: 1 }, unstyled)) {
      if (row.kind) expect(row.swatch).toBeTruthy()
    }
  })

  test('a still-running longest run says so here too', () => {
    const rows = autonomyPanelRows({ longest: 10_800, longest_running: true, runs: 1, projects: 1 }, unstyled)
    expect(rows[0].value).toMatch(/still going/)
  })

  test('the figure rows stay unswatched', () => {
    // A swatch would claim ink the panels deliberately do not lay down.
    const rows = autonomyPanelRows({ longest: 60, peak: 1, runs: 4, projects: 2 }, unstyled)
    for (const row of rows.filter(r => !r.kind)) expect(row.swatch).toBe('transparent')
  })
})

describe('the stack states its own time bounds', () => {
  // Without them the panels are texture: at 30d and 1y a mark cannot be placed
  // in time at all. ONE axis under the whole stack, because the five panels
  // share one x domain and five copies would be five statements of one fact.
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
