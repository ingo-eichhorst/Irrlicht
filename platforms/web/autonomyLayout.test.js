// Autonomy control-placement contract (#1905 redesign).
//
// The defect this pins: Range (which changes the duration chart) and Span
// (which changes the run strip) shipped on ONE control row above BOTH
// elements. Nothing on screen said which control moved which, and the two
// vocabularies overlap textually — `30d` is a Range value AND a Span value —
// so the row read as one zoom control with an odd set of steps. Each control
// now sits directly above the element it changes: Range in its own row above
// the chart card, Span in the strip's own header.
//
// jsdom has no layout engine, so this cannot assert geometry — every
// getBoundingClientRect here would be zeroes. It asserts the two things that
// CAUSE the geometry instead: the DOM puts each picker inside the block it
// governs, and the picker precedes the element it labels in document order.
// The same technique headerLayout.test.js uses, for the same reason.
//
// Fail-loud (house rule: a verifier that cannot run must never read as a
// pass): an unreadable or empty file, an index.html with no Autonomy section,
// and a stylesheet with no matching rule each THROW rather than vacuously
// satisfying the assertions below.

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
  if (!d.querySelector('#history-autonomy-strip')) {
    throw new Error('fail-loud: index.html has no #history-autonomy-strip — the parse found no Autonomy section');
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
// against the mutation fixture below as well as against the shipped markup.
function pickersAreSeparated(d) {
  const range = d.querySelector('#history-autonomy-range-sel');
  const span = d.querySelector('#history-autonomy-span-sel');
  if (!range || !span) {
    throw new Error('fail-loud: a document under test is missing one of the two Autonomy pickers');
  }
  const sharedRow = range.closest('.history-ctl-row');
  return Boolean(sharedRow) && sharedRow !== span.closest('.history-ctl-row');
}

describe('each Autonomy control sits with the element it changes', () => {
  test('Range has a control row of its own, and Span is not on it', () => {
    const d = doc();
    const range = d.querySelector('#history-autonomy-range-sel');
    expect(range, 'the Range picker must exist').not.toBeNull();
    const row = range.closest('.history-ctl-row');
    expect(row, 'Range belongs in a .history-ctl-row above the chart').not.toBeNull();
    expect(row.querySelector('#history-autonomy-span-sel'),
      'Span must not share Range’s row — that is the confusion this fixes').toBeNull();
    expect(pickersAreSeparated(d)).toBe(true);
  });

  test('Range’s row comes before the chart it changes', () => {
    const d = doc();
    const row = d.querySelector('#history-autonomy-range-row');
    expect(row, 'index.html must hold #history-autonomy-range-row').not.toBeNull();
    const chart = d.querySelector('.history-layout');
    expect(chart, 'index.html must hold the chart layout').not.toBeNull();
    expect(follows(row, chart), 'the chart must follow its Range picker').toBe(true);
  });

  test('Span lives inside the run strip, above the rows it changes', () => {
    const d = doc();
    const span = d.querySelector('#history-autonomy-span-sel');
    const strip = d.querySelector('#history-autonomy-strip');
    expect(strip.contains(span), 'Span must live inside the strip section it governs').toBe(true);
    // …and not in the control block at the top of the tab, which is what
    // "above both elements" meant.
    expect(span.closest('.history-ctl-row'), 'Span must not be in a top-of-tab control row').toBeNull();
    const rows = d.querySelector('#history-autonomy-rows');
    expect(rows, 'the strip must hold its rows container').not.toBeNull();
    expect(follows(span, rows), 'the strip’s rows must follow the Span picker').toBe(true);
  });

  test('the strip’s header keeps the title and the picker on one row', () => {
    const d = doc();
    const header = d.querySelector('.history-autonomy-header');
    expect(header, 'the strip needs a header row for its title + picker').not.toBeNull();
    expect(header.querySelector('#history-autonomy-strip-title')).not.toBeNull();
    expect(header.querySelector('#history-autonomy-span-sel')).not.toBeNull();
    expect(ruleBody('.history-autonomy-header')).toMatch(/display:\s*flex/);
    // It wraps rather than compressing: five segmented buttons plus a title
    // do not fit a narrow window on one line, and squeezing them is worse
    // than stacking them.
    expect(ruleBody('.history-autonomy-header')).toMatch(/flex-wrap:\s*wrap/);
  });

  // The markup and the code that shows/hides it are two files, and a rename in
  // one is invisible to the other until the row silently stops toggling. The
  // shared row's old id is gone from BOTH, and the new one is in both.
  test('the row toggle and the markup name the same row', () => {
    expect(html).toContain('id="history-autonomy-range-row"');
    expect(js).toContain("getElementById('history-autonomy-range-row')");
    expect(html).not.toContain('id="history-autonomy-row"');
    expect(js).not.toContain("getElementById('history-autonomy-row')");
  });

  // THE COMMITTED MUTATION: the shipped-before layout, both pickers on one
  // row. A check that could not tell it from the split layout would pass
  // against the very arrangement this change exists to remove.
  test('the check goes red against the one-row layout it replaced', () => {
    const merged = new JSDOM(`
      <div class="history-ctl-row" id="history-autonomy-row">
        <span class="history-ctl-label">Range</span>
        <fieldset id="history-autonomy-range-sel"></fieldset>
        <span class="history-ctl-label">Span</span>
        <fieldset id="history-autonomy-span-sel"></fieldset>
      </div>
    `).window.document;
    expect(pickersAreSeparated(merged)).toBe(false);
    expect(pickersAreSeparated(doc())).toBe(true);
  });

  // …and the fail-loud half: a document with no pickers at all must throw,
  // never quietly report "separated".
  test('a document missing a picker fails loudly instead of passing', () => {
    const empty = new JSDOM('<div></div>').window.document;
    expect(() => pickersAreSeparated(empty)).toThrow(/fail-loud/);
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
  const swatch = px(ruleBody('.history-contrib .dot.autonomy-band'), 'width', '.history-contrib .dot.autonomy-band');
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
  // THE DEFECT: at the shipped width the band row rendered as
  // `p5–p95 · where most runs…` with an ellipsis, and its value broke across
  // two lines as `6s –` / `23m46s`. So the one row whose whole job is to
  // explain the band cut off its own explanation, and a single span read as
  // two numbers. jsdom has no layout engine, so this asserts the two things
  // that CAUSE that geometry — the width budget, and the CSS that decides
  // what happens when something exceeds it.
  const geometry = panelGeometry();
  const width = (text) => text.length * geometry.fontSize * MONO_ADVANCE_EM;
  const unstyled = { getPropertyValue: () => '' };
  const keyRows = () => autonomyPanelRows(
    { p95: 1426, p50: 210, p5: 6, min: 4, max: 8040, count: 312 }, unstyled,
  ).filter((r) => r.kind);

  test('the panel is wide enough for every key label on one line', () => {
    expect(keyRows()).toHaveLength(2);
    for (const row of keyRows()) {
      expect(width(row.label), `"${row.label}" (${row.label.length} chars) overruns the `
        + `${geometry.labelLine}px a key row's label gets`).toBeLessThan(geometry.labelLine);
    }
  });

  // The QA case verbatim: p5 6s, p95 23m46s. It is the value that broke, and
  // it breaks again the moment a label is allowed to crowd it.
  test('the reported row fits: label and value each clear their line', () => {
    const band = keyRows()[1];
    expect(band.value).toBe('6s – 23m46s');
    expect(width(band.label)).toBeLessThan(geometry.labelLine);
    expect(width(band.value)).toBeLessThan(geometry.content);
  });

  // …and the widest figure this formatter can ever produce, so the check is
  // not pinned to one lucky data set. autonomyDuration's longest output is a
  // six-character `NNdNNh`, twice, with the separator between.
  test('even the widest range this formatter can emit fits its line', () => {
    const widest = autonomyDuration(12 * 86400 + 23 * 3600) + ' – ' + autonomyDuration(12 * 86400 + 23 * 3600);
    expect(widest).toBe('12d23h – 12d23h');
    expect(width(widest)).toBeLessThan(geometry.content);
  });

  // THE COMMITTED MUTATION: the label as it shipped. It is one character over
  // the column, which is exactly why nobody caught it by eye — and it is the
  // string the check has to reject, or the check is decoration.
  test('the check rejects the label that shipped truncated', () => {
    expect(width('p5–p95 · where most runs land')).toBeGreaterThan(geometry.labelLine);
    // …while the one that replaced it clears the column with room to spare,
    // so a mono face a little wider than the estimate still fits.
    expect(width('p5–p95 · the usual spread')).toBeLessThan(geometry.labelLine * 0.95);
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
  // taller, and this row explains the largest area of ink on the chart.
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
    for (const row of autonomyPanelRows({ p95: 60, p50: 30, p5: 10, min: 1, max: 99, count: 5 },
      { getPropertyValue: () => '' })) {
      const li = list.ownerDocument.createElement('li');
      if (row.kind) li.className = 'autonomy-key';
      list.appendChild(li);
    }
    // Two key rows, three unswatched figures — the same split renderAutonomyPanel makes.
    expect(list.querySelectorAll('li.autonomy-key')).toHaveLength(2);
    expect(js).toMatch(/li\.className\s*=\s*'autonomy-key'/);
  });
});

describe('the two surfaces name the key the same way', () => {
  // Two clients must not explain one chart differently. Reads the Swift
  // source rather than comparing against a hand-typed literal — the same
  // technique sessionError.test.js uses for Tokens.swift, and the twin of
  // macOS's own testBoundaryLabelsMatchTheWebs.
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
