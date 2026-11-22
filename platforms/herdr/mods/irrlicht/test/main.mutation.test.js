// Committed mutants of main.js (test/mutant.js says how they are loaded): the
// drawing of a failed row, of a row in a state the panel cannot read, and of
// the Gas Town mark. Each mutant breaks one of them; the render contract in
// test/render.js that pins it must then fail.

import { describe, expect, test } from 'vitest';
import { mutantLoader } from './mutant.js';
import { errorRowIsRed, gastownGroupIsMarked, unknownRowIsGrey } from './render.js';

const load = mutantLoader('irrlicht-herdr-main-mutant-');
const loadMain = async (name, anchor, replacement) => (await load(name, 'main.js', anchor, replacement)).default;

const STATE_ICON_HUE = 'return draw(STATE_HUE[state] ?? STATE_HUE.unknown, 12);';

describe('committed mutants of the row drawing', () => {
  test('the unmutated main.js passes every render contract', async () => {
    const Irrlicht = (await import('../main.js')).default;
    errorRowIsRed(Irrlicht);
    unknownRowIsGrey(Irrlicht);
    gastownGroupIsMarked(Irrlicht);
  });

  test('error and unknown icons drawn in the ready hue fail both contracts', async () => {
    const Irrlicht = await loadMain('error-unknown-green', STATE_ICON_HUE,
      'return draw(["error", "unknown"].includes(state) ? STATE_HUE.ready : STATE_HUE[state], 12);');
    expect(() => errorRowIsRed(Irrlicht)).toThrow(/the error row's state glyph/);
    expect(() => unknownRowIsGrey(Irrlicht)).toThrow(/the matched row in a state this panel cannot read/);
  });

  test('the error state drawn with the ready glyph fails the error-row contract', async () => {
    const Irrlicht = await loadMain('error-as-ready-glyph',
      '  error: (hue, size) => glyph("state-error", hue, size),',
      '  error: (hue, size) => glyph("state-ready", hue, size),');
    expect(() => errorRowIsRed(Irrlicht)).toThrow(/the error row's state glyph/);
  });

  test('the unknown state drawn with the ready glyph fails the unknown-row contract', async () => {
    const Irrlicht = await loadMain('unknown-as-ready-glyph',
      '  unknown: (hue, size) => glyph("state-unknown", hue, size),',
      '  unknown: (hue, size) => glyph("state-ready", hue, size),');
    expect(() => unknownRowIsGrey(Irrlicht)).toThrow(/the matched row in a state this panel cannot read/);
  });

  test('a row that drops its error line fails the error-row contract', async () => {
    const Irrlicht = await loadMain('no-error-line',
      '.children(r.notes.map((note) => this.note(t, r, note)));',
      '.children(r.notes.filter((note) => note.kind !== "error").map((note) => this.note(t, r, note)));');
    expect(() => errorRowIsRed(Irrlicht)).toThrow(/no element with id ir-error-t3/);
  });

  test('a group header without the Gas Town mark fails the Gas Town contract', async () => {
    const Irrlicht = await loadMain('no-gastown-mark',
      '      .when(g.gastown, (el) => el.child(div().text_size(10).child("⛽")))\n',
      '');
    expect(() => gastownGroupIsMarked(Irrlicht)).toThrow(/the Gas Town header/);
  });
});
