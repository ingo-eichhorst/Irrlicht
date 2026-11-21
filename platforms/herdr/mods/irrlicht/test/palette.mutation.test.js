// Committed mutants of lib/palette.js and lib/states.js (test/mutant.js says
// how they are loaded): the parity checks in palette.parity.test.js must fail
// on a hue that drifted from the design system and on a state map that lost a
// state the daemon declares.

import { readFileSync } from 'node:fs';
import { describe, expect, test } from 'vitest';
import { DESIGN_CSS, SESSION_GO, brandMatchesCss, coversCanonicalStates } from './contracts.js';
import { mutantLoader } from './mutant.js';

const loadMutant = mutantLoader('irrlicht-herdr-palette-mutant-');
const css = readFileSync(DESIGN_CSS, 'utf8');
const go = readFileSync(SESSION_GO, 'utf8');

describe('committed mutants of the palette', () => {
  test('a working hue one step off the design system fails the parity check', async () => {
    const { BRAND } = await loadMutant('working-drifted', 'palette.js', "working: '#8B5CF6',", "working: '#8B5CF7',");
    expect(() => brandMatchesCss(BRAND, css)).toThrow(/--working/);
  });

  test('a pressure hue swapped for its neighbour fails the parity check', async () => {
    const { BRAND } = await loadMutant('critical-as-high', 'palette.js', "'pressure-critical': '#D70015',", "'pressure-critical': '#FF3B30',");
    expect(() => brandMatchesCss(BRAND, css)).toThrow(/--pressure-critical/);
  });

  test('a state map without the error state fails the vocabulary check', async () => {
    const { STATE_HUE } = await loadMutant('no-error-hue', 'states.js', '  error: BRAND.error,\n', '');
    expect(() => coversCanonicalStates(STATE_HUE, go)).toThrow();
  });
});
