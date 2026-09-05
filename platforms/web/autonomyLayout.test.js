// Autonomy control-placement and key-width contract (#1905 redesign).
//
// TWO DEFECTS, one retired and one still live.
//
// The retired one: Range (which changed the duration chart) and Span (which
// changed the run strip) shipped on ONE control row above BOTH elements, with
// nothing on screen saying which control moved which, and the two vocabularies
// overlapping textually — `30d` was a Range value AND a Span value. The strip
// is gone and Span went with it, so the section has ONE control now. What this
// file keeps checking is that it stays one: a second picker reintroduces the
// same confusion, and a Span leftover in either the markup or the wiring is a
// control that does nothing or a listener on a key no request reads.
//
// The live one: a key label that does not fit the 260px side panel ellipsises
// the very row whose job is to explain a mark.
//
// jsdom has no layout engine, so this cannot assert geometry — every
// getBoundingClientRect here would be zeroes. It asserts the things that CAUSE
// the geometry instead: where the DOM puts each control, and the width budget
// the stylesheet gives a label. The same technique headerLayout.test.js uses,
// for the same reason.
//
// Fail-loud (house rule: a verifier that cannot run must never read as a pass):
// an unreadable or empty file, an index.html with no Autonomy section, and a
// stylesheet with no matching rule each THROW rather than vacuously satisfying
// the assertions below.

import { describe, expect, test } from 'vitest';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { JSDOM } from 'jsdom';
import { WEB_DIR } from './shippedFiles.testutil.js';
import {
  autonomyPanelRows,
  autonomyDuration,
  autonomyYDomain,
  autonomyTickValues,
  autonomyTickPlacement,
  autonomyBarRect,
  autonomyBaselineY,
  AUTONOMY_PANEL,
  AUTONOMY_PANEL_CANVAS_H,
  AUTONOMY_BAR_MIN_H,
} from './historyTab.js';

function read(name) {
  const body = readFileSync(join(WEB_DIR, name), 'utf8');
  if (body.trim().length === 0) {
    throw new Error(`fail-loud: ${name} is empty — nothing below can be trusted`);
  }
  return body;
}

const html = read('index.html');
const css = read('irrlicht.css');
const js = read('historyTab.js');

function doc() {
  const d = new JSDOM(html).window.document;
  if (!d.querySelector('#history-autonomy-panels')) {
    throw new Error('fail-loud: index.html has no #history-autonomy-panels — the parse found no Autonomy section');
  }
  return d;
}

// The declaration block of the first CSS rule whose selector list is exactly
// `selector`. Throws rather than returning '' so a renamed selector fails
// loudly instead of satisfying every assertion.
function ruleBody(selector) {
  const escaped = selector.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  const m = new RegExp('(?:^|\\n)[ \\t]*' + escaped + '\\s*\\{([^}]*)\\}').exec(css);
  if (!m) {
    throw new Error(`fail-loud: irrlicht.css has no \`${selector} { … }\` rule — the parse found nothing to assert on`);
  }
  return m[1];
}

// Node.DOCUMENT_POSITION_FOLLOWING === 4
const follows = (a, b) => Boolean(a.compareDocumentPosition(b) & 4);

// The property under test, as a predicate over any document, so it can be run
// against the mutation fixture below as well as against the shipped markup:
// the Autonomy section offers EXACTLY ONE window control.
function autonomyPickers(d) {
  const rows = d.querySelectorAll('.history-ctl-row');
  if (!rows.length) {
    throw new Error('fail-loud: a document under test has no .history-ctl-row at all');
  }
  return [...d.querySelectorAll('fieldset[id^="history-autonomy-"]')].map((f) => f.id);
}

describe('the Autonomy section offers exactly one window control', () => {
  test('Range is the only Autonomy picker in the tab', () => {
    // `#history-autonomy-sel` is the chart CHOOSER (the segmented button that
    // selects the section), not a window control — so two ids here is one
    // picker plus the chooser, and a third would be a second window.
    const pickers = autonomyPickers(doc());
    expect(pickers).toContain('history-autonomy-range-sel');
    expect(pickers).not.toContain('history-autonomy-span-sel');
    expect(pickers.filter((id) => id.endsWith('-range-sel') || id.endsWith('-span-sel'))).toHaveLength(1);
  });

  test('Range’s row comes before the panels it changes', () => {
    const d = doc();
    const row = d.querySelector('#history-autonomy-range-row');
    expect(row, 'index.html must hold #history-autonomy-range-row').not.toBeNull();
    const layout = d.querySelector('.history-layout');
    expect(layout, 'index.html must hold the chart layout').not.toBeNull();
    expect(follows(row, layout), 'the panels must follow their Range picker').toBe(true);
  });

  test('the panels live inside the chart card, beside the side panel', () => {
    // Not a full-width block below it, which is where the run strip lived when
    // it was a SECOND element. There is one element now.
    const d = doc();
    const panels = d.querySelector('#history-autonomy-panels');
    const wrap = d.querySelector('#history-chart-wrap');
    expect(wrap, 'index.html must hold the chart card').not.toBeNull();
    expect(wrap.contains(panels)).toBe(true);
    expect(d.querySelector('.history-panel'), 'the side panel keeps its place').not.toBeNull();
  });

  // The markup and the code that shows/hides it are two files, and a rename in
  // one is invisible to the other until the row silently stops toggling.
  test('the row toggle and the markup name the same row', () => {
    expect(html).toContain('id="history-autonomy-range-row"');
    expect(js).toContain("getElementById('history-autonomy-range-row')");
    expect(html).toContain('id="history-autonomy-panels"');
    expect(js).toContain("getElementById('history-autonomy-panels')");
  });

  // THE COMMITTED MUTATION: the shipped-before layout, two pickers with two
  // textually-overlapping vocabularies. A check that could not tell it from
  // the one-control layout would pass against the very arrangement this change
  // removes.
  test('the check goes red against the two-picker layout it replaced', () => {
    const twoPickers = new JSDOM(`
      <div class="history-ctl-row" id="history-autonomy-range-row">
        <span class="history-ctl-label">Range</span>
        <fieldset id="history-autonomy-range-sel"></fieldset>
        <span class="history-ctl-label">Span</span>
        <fieldset id="history-autonomy-span-sel"></fieldset>
      </div>
    `).window.document;
    const mutated = autonomyPickers(twoPickers)
      .filter((id) => id.endsWith('-range-sel') || id.endsWith('-span-sel'));
    expect(mutated).toHaveLength(2);
    expect(autonomyPickers(doc())
      .filter((id) => id.endsWith('-range-sel') || id.endsWith('-span-sel'))).toHaveLength(1);
  });

  // …and the fail-loud half: a document with no control rows at all must
  // throw, never quietly report "one picker".
  test('a document with no control rows fails loudly instead of passing', () => {
    const empty = new JSDOM('<div></div>').window.document;
    expect(() => autonomyPickers(empty)).toThrow(/fail-loud/);
  });
});

// The one px value in this file, and it is an ESTIMATE, stated as one: the
// advance of a monospace glyph as a fraction of its font-size. 0.6em is the
// figure for SF Mono, Menlo, Consolas and Cascadia Code — every family in
// --font-mono — and jsdom cannot measure text, so nothing here can produce a
// real metric. It is used only to show a label CLEARS its column with room to
// spare, so a face a few percent wider than the estimate still fits.
const MONO_ADVANCE_EM = 0.6;

// One declaration out of a rule body, as a number of px. Throws rather than
// returning a default: a property this cannot find is one whose value nothing
// below can be trusted to have computed.
function px(body, property, selector) {
  const m = new RegExp('(?:^|[;{\\s])' + property + '\\s*:\\s*([^;]+)').exec(body);
  if (!m) {
    throw new Error(`fail-loud: \`${selector}\` declares no ${property} — the width budget cannot be computed`);
  }
  const value = /(-?\d+(?:\.\d+)?)px/.exec(m[1]);
  if (!value) {
    throw new Error(`fail-loud: \`${selector}\`'s ${property} is "${m[1].trim()}", which is not a px length`);
  }
  return Number(value[1]);
}

// The panel's geometry, read from the stylesheet rather than typed here — so
// narrowing the panel, fattening the swatch or widening the gap fails this
// test instead of silently re-truncating the label.
function panelGeometry() {
  const panel = ruleBody('.history-panel');
  const flex = /flex:\s*\d+\s+\d+\s+(\d+(?:\.\d+)?)px/.exec(panel);
  if (!flex) {
    throw new Error('fail-loud: .history-panel has no fixed px flex-basis — the width budget cannot be computed');
  }
  const content = Number(flex[1]) - 2 * px(panel, 'padding', '.history-panel');
  const row = ruleBody('.history-contrib li');
  const swatch = px(ruleBody('.history-contrib .dot.autonomy-bars'), 'width', '.history-contrib .dot.autonomy-bars');
  const gap = px(row, 'gap', '.history-contrib li');
  return {
    content,
    fontSize: px(row, 'font-size', '.history-contrib li'),
    // A key row's label shares its line with the swatch and one gap; its
    // value has a line to itself, so the value gets the whole content width.
    labelLine: content - swatch - gap,
  };
}

describe('the key rows fit the panel they are rendered in', () => {
  // THE DEFECT: at the shipped width a key row rendered with an ellipsis, so
  // the one row whose whole job is to explain a mark cut off its own
  // explanation. jsdom has no layout engine, so this asserts the two things
  // that CAUSE that geometry — the width budget, and the CSS that decides what
  // happens when something exceeds it.
  const geometry = panelGeometry();
  const width = (text) => text.length * geometry.fontSize * MONO_ADVANCE_EM;
  const unstyled = { getPropertyValue: () => '' };
  const summary = { longest: 41_940, peak: 5, runs: 312, projects: 12 };
  const keyRows = () => autonomyPanelRows(summary, unstyled).filter((r) => r.kind);

  test('the panel is wide enough for every key label on one line', () => {
    expect(keyRows()).toHaveLength(2);
    for (const row of keyRows()) {
      expect(width(row.label), `"${row.label}" (${row.label.length} chars) overruns the `
        + `${geometry.labelLine}px a key row's label gets`).toBeLessThan(geometry.labelLine);
    }
  });

  test('both key rows’ figures clear their line', () => {
    for (const row of keyRows()) {
      expect(width(row.value)).toBeLessThan(geometry.content);
    }
  });

  // …and the widest figure this row can ever carry, so the check is not pinned
  // to one lucky data set: autonomyDuration's longest output is a
  // six-character `NNdNNh`, plus the still-going mark.
  test('even the widest longest-run this formatter can emit fits its line', () => {
    const widest = autonomyDuration(12 * 86400 + 23 * 3600) + ' · still going';
    expect(widest).toBe('12d23h · still going');
    expect(width(widest)).toBeLessThan(geometry.content);
  });

  // THE COMMITTED MUTATION: the label as it once shipped, one character over
  // the column — which is exactly why nobody caught it by eye. It is the string
  // the check has to reject, or the check is decoration.
  test('the check rejects a label one character too long', () => {
    expect(width('p5–p95 · where most runs land')).toBeGreaterThan(geometry.labelLine);
    // …while the ones that ship clear the column with room to spare, so a mono
    // face a little wider than the estimate still fits.
    for (const row of keyRows()) {
      expect(width(row.label)).toBeLessThan(geometry.labelLine * 0.95);
    }
  });

  test('a stylesheet the budget cannot be read from fails loudly', () => {
    expect(() => px('color: red;', 'width', '.nope')).toThrow(/fail-loud/);
    expect(() => px('width: 50%;', 'width', '.nope')).toThrow(/fail-loud/);
    expect(() => ruleBody('.history-contrib li.no-such-key')).toThrow(/fail-loud/);
  });
});

describe('nothing in the panel truncates a key label or splits a figure', () => {
  // The width budget above says the label FITS. These say what happens if it
  // ever stops fitting — it wraps, and the reader still gets the whole
  // sentence. A truncated explanation is unreadable; a wrapped one is merely
  // taller.
  test('a key row’s label wraps rather than ellipsising', () => {
    const body = ruleBody('.history-contrib li.autonomy-key .label');
    expect(body).toMatch(/white-space:\s*normal/);
    expect(body).toMatch(/overflow:\s*visible/);
    expect(body).not.toMatch(/text-overflow:\s*ellipsis/);
  });

  test('no figure in the panel is ever split across lines', () => {
    expect(ruleBody('.history-contrib .val')).toMatch(/white-space:\s*nowrap/);
  });

  // The value gets a line of its own — which is what makes the label's line
  // 204px rather than the 196px it would have to share.
  test('a key row’s value takes a line of its own, right-aligned', () => {
    expect(ruleBody('.history-contrib li.autonomy-key')).toMatch(/flex-wrap:\s*wrap/);
    const val = ruleBody('.history-contrib li.autonomy-key .val');
    expect(val).toMatch(/flex:\s*0\s+0\s+100%/);
    expect(val).toMatch(/text-align:\s*right/);
  });

  // The rows have to CARRY the class the rules above are written against, or
  // every one of them is asserting about markup nothing renders.
  test('the panel actually marks its key rows', () => {
    const list = new JSDOM('<ul id="l"></ul>').window.document.getElementById('l');
    for (const row of autonomyPanelRows({ longest: 60, peak: 1, runs: 5, projects: 2 },
      { getPropertyValue: () => '' })) {
      const li = list.ownerDocument.createElement('li');
      if (row.kind) li.className = 'autonomy-key';
      list.appendChild(li);
    }
    // Two key rows, two unswatched figures — the same split
    // renderAutonomySidePanel makes.
    expect(list.querySelectorAll('li.autonomy-key')).toHaveLength(2);
    expect(js).toMatch(/li\.className\s*=\s*'autonomy-key'/);
  });
});

describe('the two surfaces name the key the same way', () => {
  // Two clients must not explain one chart differently. Reads the Swift source
  // rather than comparing against a hand-typed literal — the same technique
  // sessionError.test.js uses for Tokens.swift, and the twin of macOS's own
  // testBoundaryLabelsMatchTheWebs.
  const swift = readFileSync(
    join(WEB_DIR, '..', 'macos', 'Irrlicht', 'Views', 'HistoryAutonomyView.swift'), 'utf8',
  );

  test('AutonomyPalette.keyEntries carries the web’s two labels verbatim', () => {
    const entries = /static var keyEntries: \[AutonomyKeyEntry\] \{([\s\S]*?)\n    \}/.exec(swift);
    expect(entries, 'fail-loud: no AutonomyPalette.keyEntries found in HistoryAutonomyView.swift').not.toBeNull();
    const labels = [...entries[1].matchAll(/label: "([^"]+)"/g)].map((m) => m[1]);
    expect(labels, 'fail-loud: parsed no labels out of keyEntries').toHaveLength(2);
    const web = autonomyPanelRows(undefined, { getPropertyValue: () => '' })
      .filter((r) => r.kind)
      .map((r) => r.label);
    expect(labels).toEqual(web);
  });
});

// ---------------------------------------------------------------------------
// QA of the shipped web build against the maintainer's real data found three
// layout defects the macOS build does not have. These pin the fixes, and each
// carries the shipped-before geometry as a committed mutation — a check that
// could not tell the two apart would pass against the very layout it replaced.
// ---------------------------------------------------------------------------

describe('QA-1: a panel header and the topmost y tick never share a line', () => {
  // THE DEFECT: tick labels sat in a gutter on the LEFT, drawn centred on their
  // gridline. The top gridline is the plot's own top edge, so half of "14h34m"
  // fell outside the canvas — sliced by the panel above, and landing on that
  // panel's header line, where it overprinted "longest 11h39m · 15 at once".
  //
  // TWO INDEPENDENT HALVES OF THE FIX, and both are needed: the labels moved to
  // the RIGHT gutter (which frees the header row's column), and the canvas
  // gained a top inset (which keeps the topmost label inside the canvas).
  const width = 420;
  const domain = autonomyYDomain([
    { buckets: [{ ts: 0, longest: 52_440, peak: 3 }, { ts: 1, longest: 120, peak: 1 }] },
  ]);

  test('every tick label is drawn in a gutter to the right of the plot', () => {
    // Two claims, and the second is the one that distinguishes a real gutter
    // from a nominal one: the label starts right of the plot AND fits in the
    // room left for it. The widest figure autonomyDuration can emit is a
    // six-character `NNdNNh`, at MONO_ADVANCE_EM per character.
    const widest = autonomyDuration(12 * 86400 + 23 * 3600).length
      * AUTONOMY_PANEL.tickFont * MONO_ADVANCE_EM;
    expect(autonomyTickValues(domain)).toHaveLength(3);
    for (const v of autonomyTickValues(domain)) {
      const at = autonomyTickPlacement(v, domain, width);
      expect(at.x, `${autonomyDuration(v)} is drawn at x=${at.x}, inside the plot`)
        .toBeGreaterThanOrEqual(at.plotRight);
      expect(at.x + widest, `the right gutter is only ${AUTONOMY_PANEL.padR}px — a tick label `
        + `needs ${Math.ceil(widest + AUTONOMY_PANEL.tickGap)}px and would be clipped`)
        .toBeLessThanOrEqual(width);
    }
  });

  test('the topmost label is wholly inside the canvas, not sliced by the panel above', () => {
    const highest = autonomyTickValues(domain).at(-1);
    const at = autonomyTickPlacement(highest, domain, width);
    expect(at.top, 'the top tick label spills above the canvas and lands on the header row')
      .toBeGreaterThanOrEqual(0);
  });

  test('the lowest label is wholly inside the canvas too', () => {
    const lowest = autonomyTickValues(domain)[0];
    const at = autonomyTickPlacement(lowest, domain, width);
    expect(at.bottom).toBeLessThanOrEqual(AUTONOMY_PANEL_CANVAS_H);
  });

  test('the header is a block of its own, above the canvas, not laid over it', () => {
    // A header positioned over the plot would put the two back on one line
    // whatever the tick geometry says.
    const head = ruleBody('.history-autonomy-panel-head');
    expect(head).not.toMatch(/position:\s*absolute/);
    expect(head).toMatch(/line-height:\s*\d/);
    expect(ruleBody('.history-autonomy-panel-canvas')).toMatch(/display:\s*block/);
  });

  // THE COMMITTED MUTATION: the shipped-before geometry — labels on the left,
  // no top inset. Both assertions above have to reject it, or neither is
  // measuring the defect.
  test('the checks go red against the geometry that shipped', () => {
    const before = { ...AUTONOMY_PANEL, padL: 46, padR: 6, padT: 0, lineH: 32 };
    const highest = autonomyTickValues(domain).at(-1);
    const mutated = autonomyTickPlacement(highest, domain, width, before);
    expect(mutated.top, 'the shipped geometry drew the top label half outside the canvas')
      .toBeLessThan(0);
    // …and its label column was the LEFT one, under the header's project name.
    expect(before.padL).toBeGreaterThan(before.padR);
    expect(AUTONOMY_PANEL.padR).toBeGreaterThan(AUTONOMY_PANEL.padL);
  });

  test('the inset is derived from the tick font, not a lucky constant', () => {
    // Half a glyph is what has to clear the edge; a smaller inset re-creates
    // the defect for any larger tick font.
    expect(AUTONOMY_PANEL.padT * 2).toBeGreaterThanOrEqual(AUTONOMY_PANEL.tickFont);
  });
});

describe('QA-2: the axis and the “+N more” line are inside the rendered card', () => {
  // THE DEFECT: the stack sat in a fixed-height card with its own scrollbar, so
  // five panels were compressed AND clipped at once — the x axis was cut off at
  // the bottom edge — while ~250px of page sat empty below the card.
  const PANELS = 5;

  // The stack's intrinsic height, from the same stylesheet and constants the
  // renderer uses. Throws rather than defaulting: a rule this cannot read is
  // one whose value nothing below can be trusted to have computed.
  function stackHeight() {
    const panel = ruleBody('.history-autonomy-panel');
    const head = ruleBody('.history-autonomy-panel-head');
    const perPanel = AUTONOMY_PANEL_CANVAS_H
      + px(head, 'line-height', '.history-autonomy-panel-head')
      + px(panel, 'margin-bottom', '.history-autonomy-panel');
    const axis = px(ruleBody('.history-autonomy-axis'), 'font-size', '.history-autonomy-axis');
    const more = px(ruleBody('.history-autonomy-more'), 'font-size', '.history-autonomy-more');
    return PANELS * perPanel + axis + more;
  }

  test('the card releases its fixed height for this chart', () => {
    const body = ruleBody('.history-chart-wrap.autonomy');
    expect(body).toMatch(/height:\s*auto/);
    // A min-height is a FLOOR (it keeps the empty state's overlay a sensible
    // size). A max-height would be a ceiling, and a ceiling is what clipped the
    // axis in the first place.
    expect(body).not.toMatch(/max-height/);
  });

  test('the stack does not scroll inside the card', () => {
    const body = ruleBody('.history-autonomy-panels');
    expect(body).not.toMatch(/overflow:\s*auto/);
    expect(body).not.toMatch(/overflow:\s*scroll/);
    // …and it is in the flow rather than pinned to the card's box, which is
    // what made the card's height a clip rectangle.
    expect(body).toMatch(/position:\s*static/);
    expect(body).not.toMatch(/inset:/);
  });

  test('the toggle that grows the card is actually wired', () => {
    // A stylesheet rule nothing applies is a fix that never ships.
    expect(js).toMatch(/classList\.toggle\('autonomy', isAutonomy\)/);
  });

  // THE COMMITTED MUTATION: the fixed-height card. The stack is taller than it,
  // which is the whole reason the height had to go — and the two rows a
  // scrollbar cut off first are the last two in the stack.
  test('the stack is taller than the fixed card it used to be clipped by', () => {
    const fixedCard = px(ruleBody('.history-chart-wrap'), 'height', '.history-chart-wrap')
      - 2 * px(ruleBody('.history-chart-wrap'), 'padding', '.history-chart-wrap');
    expect(stackHeight(), `five panels need ${stackHeight()}px; the fixed card offered ${fixedCard}px`)
      .toBeGreaterThan(fixedCard);
  });

  test('the min-height floor cannot clip the stack', () => {
    const floor = px(ruleBody('.history-chart-wrap.autonomy'), 'min-height', '.history-chart-wrap.autonomy');
    expect(floor).toBeLessThan(stackHeight());
  });

  test('the axis is the last thing in the stack, so nothing can hide behind it', () => {
    // Order matters for the clipping claim: the axis and the "+N more" line are
    // appended after every panel, so a ceiling takes them first.
    const render = /function renderAutonomyPanels\(\)[\s\S]*?\n}/.exec(js);
    expect(render, 'fail-loud: renderAutonomyPanels not found in historyTab.js').not.toBeNull();
    expect(render[0].indexOf('history-autonomy-more'))
      .toBeLessThan(render[0].indexOf('buildAutonomyAxis(data)'));
  });
});

describe('QA-3: the concurrency bars share a baseline', () => {
  // THE DEFECT: two- or three-pixel bars with nothing to stand on scanned as
  // dashes scattered under the line rather than as a distribution, and the
  // tallest and the shortest looked nearly identical.
  test('every bar stands on the same baseline', () => {
    const heights = [1, 2, 7, 15];
    const bottoms = new Set(heights.map((n) => autonomyBarRect(n, 15).bottom));
    expect(bottoms.size, 'the bars do not share a foot').toBe(1);
    expect([...bottoms][0]).toBe(autonomyBaselineY());
  });

  test('the baseline sits directly beneath the plot, at the foot of the panel', () => {
    expect(autonomyBaselineY()).toBe(AUTONOMY_PANEL_CANVAS_H);
    // …and immediately under the line plot, not floating below it.
    expect(autonomyBaselineY() - AUTONOMY_PANEL.barsH)
      .toBe(AUTONOMY_PANEL.padT + AUTONOMY_PANEL.lineH + AUTONOMY_PANEL.gap);
  });

  test('a bar of 1 and a bar of 15 are visibly different heights', () => {
    const smallest = autonomyBarRect(1, 15).height;
    const biggest = autonomyBarRect(15, 15).height;
    expect(biggest - smallest,
      `1 draws ${smallest}px and 15 draws ${biggest}px — indistinguishable at a glance`)
      .toBeGreaterThanOrEqual(12);
    expect(biggest).toBe(AUTONOMY_PANEL.barsH);
  });

  test('the smallest real reading is still visible', () => {
    expect(autonomyBarRect(1, 15).height).toBeGreaterThanOrEqual(AUTONOMY_BAR_MIN_H);
    expect(AUTONOMY_BAR_MIN_H).toBeGreaterThan(1);
  });

  // The honesty rule survives the fix: a bucket where nobody worked draws
  // NOTHING. The floor applies only to a bar that is drawn at all.
  test('a bucket with nobody working still draws no bar', () => {
    expect(autonomyBarRect(0, 15)).toBeNull();
    expect(autonomyBarRect(undefined, 15)).toBeNull();
    expect(autonomyBarRect(3, 0)).toBeNull();
  });

  test('the drawn baseline is furniture, not a measurement', () => {
    // It is stroked in the gridline colour across the whole plot, exactly like
    // the y gridlines — never in the bar colour, which would make it read as a
    // row of zero-height bars.
    const draw = /function drawAutonomyBars\([\s\S]*?\n}/.exec(js);
    expect(draw, 'fail-loud: drawAutonomyBars not found in historyTab.js').not.toBeNull();
    expect(draw[0]).toMatch(/strokeStyle = gridColor/);
    expect(draw[0]).not.toMatch(/strokeStyle = color/);
  });

  // THE COMMITTED MUTATION: the shipped-before band height. At 10px a bar of 1
  // and a bar of 15 differed by under 10px and both read as grit.
  test('the check goes red against the band height that shipped', () => {
    const before = { ...AUTONOMY_PANEL, barsH: 10 };
    const spread = autonomyBarRect(15, 15, before).height - autonomyBarRect(1, 15, before).height;
    expect(spread, 'the shipped band spread 1..15 over only ' + spread + 'px').toBeLessThan(12);
    const now = autonomyBarRect(15, 15).height - autonomyBarRect(1, 15).height;
    expect(now).toBeGreaterThanOrEqual(12);
  });
});

describe('the stylesheet and the painter agree on the canvas height', () => {
  // The painter sizes its backing store from AUTONOMY_PANEL_CANVAS_H and lays
  // out against it; a stylesheet that disagreed would scale every panel and put
  // the baseline somewhere other than the foot of the box.
  test('.history-autonomy-panel-canvas is exactly AUTONOMY_PANEL_CANVAS_H tall', () => {
    const css = px(ruleBody('.history-autonomy-panel-canvas'), 'height', '.history-autonomy-panel-canvas');
    expect(css).toBe(AUTONOMY_PANEL_CANVAS_H);
  });

  test('the x-axis bounds sit under the plot, not under the tick gutter', () => {
    const axis = ruleBody('.history-autonomy-axis');
    expect(px(axis, 'padding-left', '.history-autonomy-axis')).toBe(AUTONOMY_PANEL.padL);
    expect(px(axis, 'padding-right', '.history-autonomy-axis')).toBe(AUTONOMY_PANEL.padR);
  });
});
