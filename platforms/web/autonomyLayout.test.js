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
import { autonomyPanelRows, autonomyDuration } from './historyTab.js';

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

  test('the stack scrolls rather than clipping its explanation', () => {
    // Five panels plus the "+N more" line and the axis run a little past a
    // 360px card. Clipping is the one option that silently removes the row
    // that says what was left out.
    const body = ruleBody('.history-autonomy-panels');
    expect(body).toMatch(/overflow:\s*auto/);
    expect(body).toMatch(/position:\s*absolute/);
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
