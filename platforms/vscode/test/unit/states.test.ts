import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { describe, expect, it } from 'vitest';
import { DEFAULT_DAEMON_URL } from '../../src/config';
import { STATES, isStateName, stateColorId, stateIcon } from '../../src/states';

const REPO = join(import.meta.dirname, '../../../..');
const read = (...p: string[]): string => readFileSync(join(REPO, ...p), 'utf8');
const pkg = JSON.parse(readFileSync(join(import.meta.dirname, '../../package.json'), 'utf8'));

/** session.go's `canonicalStates = []string{...}`, each identifier resolved to its string. Throws when unparseable. */
function canonicalStates(goSource: string): string[] {
  const literal = /canonicalStates\s*=\s*\[\]string\{([^}]*)\}/.exec(goSource);
  if (!literal) throw new Error('no canonicalStates literal in core/domain/session/session.go');
  const names = literal[1].split(',').map((x) => x.trim()).filter(Boolean).map((ident) => {
    const value = new RegExp(String.raw`^\s*${ident}\s*=\s*"([a-z_]+)"`, 'm').exec(goSource);
    if (!value) throw new Error(`${ident} has no string value in session.go`);
    return value[1];
  });
  if (names.length < 4) throw new Error(`canonicalStates parsed to ${names.length} states`);
  return names;
}

/** The text between a block's opening `{` and its closing `}` (blocks used here contain no nested braces). */
function cssBlock(css: string, header: RegExp): string {
  const m = header.exec(css);
  if (!m) throw new Error(`css block ${header} not found in platforms/web/irrlicht.css`);
  const start = m.index + m[0].length;
  const end = css.indexOf('}', start);
  if (end < 0) throw new Error(`css block ${header} is not closed`);
  return css.slice(start, end);
}

function cssToken(block: string, name: string): string {
  const m = new RegExp(String.raw`--${name}\s*:\s*(#[0-9A-Fa-f]{6})\s*;`).exec(block);
  if (!m) throw new Error(`--${name} missing from css block`);
  return m[1].toUpperCase();
}

describe('states', () => {
  it('STATES equals the daemon vocabulary in core/domain/session/session.go', () => {
    expect([...STATES]).toEqual(canonicalStates(read('core/domain/session/session.go')));
  });

  it('the canonicalStates parser fails loudly on input it cannot read', () => {
    expect(() => canonicalStates('package session')).toThrow(/no canonicalStates literal/);
    expect(() => canonicalStates('var canonicalStates = []string{StateGone}')).toThrow(/StateGone/);
  });

  it('isStateName accepts each state and rejects others', () => {
    for (const s of STATES) expect(isStateName(s)).toBe(true);
    expect(isStateName('cancelled')).toBe(false);
    expect(isStateName(undefined)).toBe(false);
  });

  it('stateColorId derives the theme colour id', () => {
    expect(stateColorId('waiting')).toBe('irrlicht.state.waiting');
  });

  it('stateIcon draws the macOS app glyphs (SessionStateIcon.swift) in the state colour', () => {
    const ids = Object.fromEntries(STATES.map((s) => [s, stateIcon(s).id]));
    expect(ids).toEqual({ working: 'circle-large-filled', waiting: 'debug-pause', ready: 'pass-filled', error: 'error' });
    for (const s of STATES) expect(stateIcon(s).colorId, s).toBe(stateColorId(s));
  });

  it('stateIcon gives an unrecognised state the neutral question glyph, never a state colour', () => {
    for (const v of ['cancelled', '', undefined]) expect(stateIcon(v)).toEqual({ id: 'question' });
  });

  it('manifest colours are exactly the STATES colour ids', () => {
    const ids: string[] = pkg.contributes.colors.map((c: { id: string }) => c.id);
    expect(ids).toEqual(STATES.map(stateColorId));
  });

  it('manifest colour defaults equal the dashboard palette in platforms/web/irrlicht.css', () => {
    const css = read('platforms/web/irrlicht.css');
    const dark = cssBlock(css, /(^|\n)\s*:root\s*\{/);
    const light = cssBlock(css, /:root\[data-theme="light"\]\s*\{/);
    for (const s of STATES) {
      const c = pkg.contributes.colors.find((x: { id: string }) => x.id === stateColorId(s));
      expect(c, `no colour for ${s}`).toBeDefined();
      const d = c.defaults;
      expect([d.dark, d.highContrast].map((v: string) => v.toUpperCase()), `${s} dark`).toEqual([cssToken(dark, s), cssToken(dark, s)]);
      expect([d.light, d.highContrastLight].map((v: string) => v.toUpperCase()), `${s} light`).toEqual([cssToken(light, s), cssToken(light, s)]);
    }
  });

  it('the css token reader fails loudly when a token is missing', () => {
    expect(() => cssToken('--working: #FFFFFF;', 'waiting')).toThrow(/--waiting missing/);
    expect(() => cssBlock(':root { }', /nothing/)).toThrow(/not found/);
  });

  it('the manifest daemonUrl default is DEFAULT_DAEMON_URL', () => {
    expect(pkg.contributes.configuration.properties['irrlicht.daemonUrl'].default).toBe(DEFAULT_DAEMON_URL);
  });
});
