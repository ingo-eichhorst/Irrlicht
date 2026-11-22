// Committed mutants of lib/palette.js and lib/states.js (test/mutant.js says
// how they are loaded): the checks in palette.parity.test.js must fail on a
// hue that drifted from the design system, on a state map that lost a state
// the daemon declares, and on a state painted in another state's hue.

import { readFileSync } from 'node:fs';
import { describe, expect, test } from 'vitest';
import { BRAND } from '../lib/palette.js';
import { DESIGN_CSS, SESSION_GO, brandMatchesCss, coversCanonicalStates, stateHuesAreTheirTokens } from './contracts.js';
import { mutantLoader } from './mutant.js';

const loadMutant = mutantLoader('irrlicht-herdr-palette-mutant-');
const css = readFileSync(DESIGN_CSS, 'utf8');
const go = readFileSync(SESSION_GO, 'utf8');

describe('committed mutants of the palette', () => {
  test('a working hue one step off the design system fails the parity check', async () => {
    const { BRAND: drifted } = await loadMutant('working-drifted', 'lib/palette.js', "working: '#8B5CF6',", "working: '#8B5CF7',");
    expect(() => brandMatchesCss(drifted, css)).toThrow(/--working/);
  });

  test('a pressure hue swapped for its neighbour fails the parity check', async () => {
    const { BRAND: swapped } = await loadMutant('critical-as-high', 'lib/palette.js', "'pressure-critical': '#D70015',", "'pressure-critical': '#FF3B30',");
    expect(() => brandMatchesCss(swapped, css)).toThrow(/--pressure-critical/);
  });

  test('a state map without the error state fails the vocabulary check', async () => {
    const { STATE_HUE } = await loadMutant('no-error-hue', 'lib/states.js', '  error: BRAND.error,\n', '');
    expect(() => coversCanonicalStates(STATE_HUE, go)).toThrow(/keyed by the daemon's states plus unknown/);
  });

  test('the error state painted with the ready hue fails the state-hue check', async () => {
    const { STATE_HUE } = await loadMutant('error-in-ready-hue', 'lib/states.js', '  error: BRAND.error,\n', '  error: BRAND.ready,\n');
    expect(() => stateHuesAreTheirTokens(STATE_HUE, BRAND)).toThrow(/the state hues/);
  });
});
