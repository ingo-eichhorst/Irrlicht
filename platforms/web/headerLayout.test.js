// Header layout contract. The quota strip must stay on a row of its OWN,
// under the title/controls row.
//
// The defect this pins: the chips shipped inside `.header-left`, on the same
// non-wrapping flex row as the History/Context/theme/settings controls. The
// chips are the one header element whose width grows with how many providers
// are signed in, and `.header-right` is `flex-shrink: 0`, so past a certain
// width the two simply painted on top of each other — "56% / 40%" underneath
// the History button, "6 sessions" underneath the gear. Reported against
// v0.6.2 at roughly 870px wide.
//
// jsdom has no layout engine, so this cannot assert geometry — every
// getBoundingClientRect here would be zeroes. It asserts the two things that
// CAUSE the geometry instead, both of which a future edit could undo without
// noticing: the DOM places the strip outside the control row, and the CSS
// makes the header a column so that placement means "below" rather than
// "beside". The geometry itself was verified in a real browser at 1137 / 870 /
// 500 / 420 / 360 px — chips below the row, no overlap, no horizontal page
// scroll at any of them.
//
// Fail-loud (house rule: a verifier that cannot run must never read as a
// pass): an unreadable file, an index.html with no <header>, and a stylesheet
// with no `header {` block each FAIL rather than vacuously satisfying the
// assertions below.

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

function headerElement() {
  const header = new JSDOM(html).window.document.querySelector('header');
  if (!header) {
    throw new Error('fail-loud: index.html has no <header> — the parse found nothing to assert on');
  }
  return header;
}

// The declaration block of the first CSS rule whose selector list is exactly
// `selector`. Throws rather than returning '' so a renamed selector fails
// loudly instead of satisfying every "does not contain" assertion.
function ruleBody(selector) {
  // Anchored to a line start so `header` cannot match inside `.header-row`,
  // and so a rule introduced by a comment block (rather than by the previous
  // rule's `}`) is still found.
  const escaped = selector.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  const m = new RegExp('(?:^|\\n)[ \\t]*' + escaped + '\\s*\\{([^}]*)\\}').exec(css);
  if (!m) {
    throw new Error(`fail-loud: irrlicht.css has no \`${selector} { … }\` rule — the parse found nothing to assert on`);
  }
  return m[1];
}

describe('the quota strip sits on its own header row', () => {
  test('index.html puts #quota-chips outside the title/controls row', () => {
    const header = headerElement();
    const chips = header.querySelector('#quota-chips');
    expect(chips, '#quota-chips must live inside <header>').not.toBeNull();

    // The whole point: not inside the row that holds the controls.
    expect(chips.closest('.header-row'), '#quota-chips must not be inside .header-row').toBeNull();
    expect(chips.closest('.header-left'), '#quota-chips must not be inside .header-left').toBeNull();
    expect(chips.closest('.header-right'), '#quota-chips must not be inside .header-right').toBeNull();
    expect(chips.parentElement.tagName.toLowerCase()).toBe('header');
  });

  test('the strip comes after the control row, so a column puts it below', () => {
    const header = headerElement();
    const row = header.querySelector('.header-row');
    expect(row, '<header> must hold a .header-row').not.toBeNull();
    const chips = header.querySelector('#quota-chips');
    // Node.DOCUMENT_POSITION_FOLLOWING === 4
    expect(row.compareDocumentPosition(chips) & 4).toBeTruthy();
  });

  test('the controls still share one row with the title', () => {
    const row = headerElement().querySelector('.header-row');
    expect(row.querySelector('.header-left'), '.header-left belongs in .header-row').not.toBeNull();
    expect(row.querySelector('.header-right'), '.header-right belongs in .header-row').not.toBeNull();
  });

  test('the header stacks its rows, so "after" renders as "below"', () => {
    expect(ruleBody('header')).toMatch(/flex-direction:\s*column/);
    expect(ruleBody('.header-row')).toMatch(/display:\s*flex/);
  });

  test('the title is no longer hidden to make room for the chips', () => {
    // The chips used to REPLACE the version line, because there was only one
    // row to put either in. With a row each, both are visible at once.
    expect(css).not.toMatch(/header\.has-quota-chips\s+\.app-title\s*\{[^}]*display:\s*none/);
  });

  test('a long left side shrinks rather than overflowing into the controls', () => {
    // A flex item's default min-width is its content, which is what let the
    // two halves overlap instead of clip.
    expect(ruleBody('.header-left')).toMatch(/min-width:\s*0/);
    expect(ruleBody('.header-right')).toMatch(/min-width:\s*0/);
  });

  test('more chips than fit scroll inside the strip, never widening the header', () => {
    expect(ruleBody('.quota-chips')).toMatch(/overflow-x:\s*auto/);
  });
});
