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
