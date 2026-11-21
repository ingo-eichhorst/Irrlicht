// The panel's colours against their sources in this repository, in the style
// of address.parity.test.js: a Herdr mod folder is installed on its own and
// cannot read the design system at run time, so lib/palette.js carries a copy
// and this file keeps the copy from drifting.
//   - Every hue in BRAND is the token of that name in
//     tools/irrlicht-design-system/colors_and_type.css.
//   - STATE_HUE and STATE_LABEL name every state the daemon declares
//     (canonicalStates in core/domain/session/session.go), plus `unknown`.
// palette.mutation.test.js runs the same checks against committed mutants
// and sees them fail. Both run only inside this repository, so
// tools/preflight.sh's Herdr gate also triggers on the two files read here.

import { readFileSync } from 'node:fs';
import { describe, expect, test } from 'vitest';
import { BRAND, connectionHue, pressureHue, track, wash, withAlpha } from '../lib/palette.js';
import { STATE_HUE, STATE_LABEL, herdrState, irrlichtState } from '../lib/states.js';
import { DESIGN_CSS, SESSION_GO, brandMatchesCss, canonicalStates, coversCanonicalStates, cssTokens } from './contracts.js';

const css = readFileSync(DESIGN_CSS, 'utf8');
const go = readFileSync(SESSION_GO, 'utf8');

describe('the palette against the design system', () => {
  test('every brand hue is the colors_and_type.css token of the same name', () => {
    brandMatchesCss(BRAND, css);
  });

  test('a key with no token of its name fails rather than being skipped', () => {
    expect(() => brandMatchesCss({ ...BRAND, 'working-ish': '#8B5CF6' }, css)).toThrow(/--working-ish is not a token/);
  });

  test('a CSS file with no tokens fails rather than reading as one that agrees', () => {
    expect(() => cssTokens('body { color: red; }')).toThrow(/no --token declarations/);
    expect(() => brandMatchesCss(BRAND, '')).toThrow(/no --token declarations/);
  });
});

describe('the state maps against the daemon\'s vocabulary', () => {
  test('canonicalStates is read from session.go, not typed here', () => {
    expect(canonicalStates(go)).toContain('error');
  });

  test('a session.go the reader cannot follow fails rather than reading as no states', () => {
    expect(() => canonicalStates('package session\n')).toThrow(/no canonicalStates literal/);
    expect(() => canonicalStates('var canonicalStates = []string{StateA, StateB, StateC, StateD}\nStateA = "a"\n'))
      .toThrow(/StateB has no string value/);
    expect(() => canonicalStates('var canonicalStates = []string{StateA}\nStateA = "a"\n')).toThrow(/fewer than four/);
  });

  test('every daemon state, and unknown, has a hue and a label', () => {
    coversCanonicalStates(STATE_HUE, go);
    coversCanonicalStates(STATE_LABEL, go);
  });

  test('the unknown state takes the grey of --cancelled, never a state\'s hue', () => {
    expect(STATE_HUE.unknown).toBe(BRAND.cancelled);
    expect(irrlichtState('paused')).toBe('unknown');
    expect(irrlichtState(undefined)).toBe('unknown');
    expect(irrlichtState('toString')).toBe('unknown');
  });
});

describe('Herdr\'s status read onto Irrlicht\'s states, for agents no session matched', () => {
  test.each([
    ['working', 'working'],
    ['blocked', 'waiting'],
    ['done', 'ready'],
    ['idle', 'ready'],
    [null, 'unknown'],
    ['sleeping', 'unknown'],
  ])('%s is %s', (status, want) => {
    expect(herdrState(status)).toBe(want);
  });
});

describe('derived colours', () => {
  test('each pressure level has its own token, an unknown level none', () => {
    expect([pressureHue('safe'), pressureHue('caution'), pressureHue('warning'), pressureHue('critical')])
      .toEqual([BRAND['pressure-low'], BRAND['pressure-medium'], BRAND['pressure-high'], BRAND['pressure-critical']]);
    expect(pressureHue('unknown')).toBeNull();
  });

  test('the connection dot is green when the daemon answers, orange while looking, red otherwise', () => {
    expect(connectionHue('ok')).toBe(BRAND['ws-connected']);
    expect(connectionHue('starting')).toBe(BRAND.waiting);
    for (const phase of ['unreachable', 'stalled', 'unexpected', 'error']) expect(connectionHue(phase)).toBe(BRAND['ws-disconnected']);
  });

  test('washes are 12% of the hue, the track 8% of the theme\'s text colour', () => {
    expect(wash('#FF3B30')).toBe('#FF3B301F');
    expect(track('#abb2bf')).toBe('#abb2bf14');
    expect(withAlpha('#abc', 0.5)).toBe('#aabbcc80');
    expect(withAlpha('#11223344', 1)).toBe('#112233FF');
    expect(withAlpha('red', 0.5)).toBe('red');
  });
});
