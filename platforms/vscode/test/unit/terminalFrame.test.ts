import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { describe, expect, it } from 'vitest';
import { STATES } from '../../src/states';
import {
  EDITOR_BORDER_KEYS, FRAME_DEBOUNCE_MS, FrameSync, OWNED_KEYS, PANEL_BORDER_KEYS, STATE_COLORS, TINT_AMOUNT, TINT_BASE, URGENCY,
  blend, desiredFrame, mostUrgent, normalizeMemo, parseFrameMode, planFrame, resolveStateColor, tabEdgesDrawn, themeKindOf,
} from '../../src/terminalFrame';
import type { EditorView, FrameInput, FrameIO, FrameMemo, FramePlan, FrameTimers } from '../../src/terminalFrame';

const pkg = JSON.parse(readFileSync(join(import.meta.dirname, '../../package.json'), 'utf8'));

type Planner = typeof planFrame;
type Obj = Record<string, unknown>;

const PANEL_COLOR = '#8B5CF6';
const panelDesired = (color: string): Record<string, string> =>
  Object.fromEntries(PANEL_BORDER_KEYS.map((k) => [k, color]));
const editorDesired = (color: string): Record<string, string> =>
  Object.fromEntries(EDITOR_BORDER_KEYS.map((k) => [k, color]));

/** Applies one planner step the way the controller would: the plan's value and memo become the new state. */
interface World { current: unknown; memo: FrameMemo | undefined }
function step(plan: Planner, world: World, desired: Record<string, string>): World & { plan: FramePlan } {
  const p = plan(world.current, world.memo, desired);
  return { current: p.next, memo: p.memo, plan: p };
}

// ---------------------------------------------------------------------------
// Checkers: each runs against the real planFrame (must pass) and a committed
// mutant (must throw). See the mutant describe block at the bottom.
// ---------------------------------------------------------------------------
const USER_SETTINGS = (): Obj => ({
  'statusBar.background': '#222222',
  'panel.border': '#123456',
  '[Default Dark Modern]': { 'panel.border': '#ABCDEF', 'editor.background': '#000000' },
});

function checkKeepsNonOwnedKeys(plan: Planner): void {
  let w: World = { current: USER_SETTINGS(), memo: undefined };
  const keep = (label: string): void => {
    const cur = w.current as Obj;
    expect(cur['statusBar.background'], `${label}: statusBar`).toBe('#222222');
    expect(cur['[Default Dark Modern]'], `${label}: theme block`).toEqual({ 'panel.border': '#ABCDEF', 'editor.background': '#000000' });
  };
  w = step(plan, w, panelDesired(PANEL_COLOR)); keep('apply');
  w = step(plan, w, panelDesired('#34C759')); keep('recolour');
  w = step(plan, w, {}); keep('restore');
}

function checkKeepsThemeBlocks(plan: Planner): void {
  let w: World = { current: USER_SETTINGS(), memo: undefined };
  w = step(plan, w, panelDesired(PANEL_COLOR));
  expect(Object.keys(w.current as Obj), 'theme block present while applied').toContain('[Default Dark Modern]');
  w = step(plan, w, {});
  expect(Object.keys(w.current as Obj), 'theme block present after restore').toContain('[Default Dark Modern]');
}

function checkNoReSnapshot(plan: Planner): void {
  let w: World = { current: { 'panel.border': '#123456' }, memo: undefined };
  w = step(plan, w, panelDesired('#8B5CF6'));
  w = step(plan, w, panelDesired('#FF9500'));
  expect((w.current as Obj)['panel.border']).toBe('#FF9500');
  w = step(plan, w, {});
  expect(w.current).toEqual({ 'panel.border': '#123456' });
  expect(w.memo).toBeUndefined();
}

function checkRestoresOwnedKeys(plan: Planner): void {
  let w: World = { current: { 'panel.border': '#123456', 'statusBar.background': '#222222' }, memo: undefined };
  w = step(plan, w, panelDesired(PANEL_COLOR));
  expect((w.current as Obj)['terminal.border']).toBe(PANEL_COLOR);
  w = step(plan, w, {});
  expect(w.current).toEqual({ 'panel.border': '#123456', 'statusBar.background': '#222222' });
}

// ---------------------------------------------------------------------------
describe('parseFrameMode', () => {
  it('accepts each valid mode', () => {
    for (const m of ['off', 'border', 'border-and-tint'] as const) expect(parseFrameMode(m)).toBe(m);
  });
  it('maps junk and undefined to border', () => {
    for (const v of [undefined, null, '', 'tint', 'OFF', 3, {}, true]) expect(parseFrameMode(v)).toBe('border');
  });
});

describe('themeKindOf', () => {
  it('maps the vscode ColorThemeKind values', () => {
    expect(themeKindOf(1)).toBe('light');
    expect(themeKindOf(2)).toBe('dark');
    expect(themeKindOf(3)).toBe('dark');
    expect(themeKindOf(4)).toBe('light');
  });
  it('treats an unknown kind as dark', () => {
    expect(themeKindOf(0)).toBe('dark');
    expect(themeKindOf(99)).toBe('dark');
  });
});

describe('STATE_COLORS', () => {
  it('equals package.json contributes.colors for every canonical state', () => {
    for (const s of STATES) {
      const entry = pkg.contributes.colors.find((c: { id: string }) => c.id === `irrlicht.state.${s}`);
      expect(entry, `no irrlicht.state.${s} in package.json contributes.colors`).toBeDefined();
      const d = entry.defaults;
      const up = (v: string): string => v.toUpperCase();
      expect([STATE_COLORS[s].dark, up(d.dark), up(d.highContrast)], `${s} dark`).toEqual([up(d.dark), up(d.dark), up(d.dark)]);
      expect([STATE_COLORS[s].light, up(d.light), up(d.highContrastLight)], `${s} light`).toEqual([up(d.light), up(d.light), up(d.light)]);
    }
  });
});

describe('resolveStateColor', () => {
  it('defaults to STATE_COLORS per theme kind', () => {
    for (const s of STATES) {
      expect(resolveStateColor(s, 'dark')).toBe(STATE_COLORS[s].dark);
      expect(resolveStateColor(s, 'light', {})).toBe(STATE_COLORS[s].light);
    }
  });
  it('a valid override wins and is normalised to uppercase #RRGGBB', () => {
    expect(resolveStateColor('working', 'dark', { 'irrlicht.state.working': '#abc' })).toBe('#AABBCC');
    expect(resolveStateColor('ready', 'light', { 'irrlicht.state.ready': '#0a1B2c' })).toBe('#0A1B2C');
    expect(resolveStateColor('error', 'dark', { 'irrlicht.state.error': '#FF0000' })).toBe('#FF0000');
  });
  it('invalid overrides are ignored', () => {
    const bad: unknown[] = ['red', '#12345678', '#1234', '123456', 42, null, '', ' #123456'];
    for (const v of bad) {
      expect(resolveStateColor('waiting', 'dark', { 'irrlicht.state.waiting': v }), String(v)).toBe(STATE_COLORS.waiting.dark);
    }
  });
  it('a theme-scoped block entry is ignored', () => {
    const c = { '[Default Dark Modern]': { 'irrlicht.state.working': '#123456' } };
    expect(resolveStateColor('working', 'dark', c)).toBe(STATE_COLORS.working.dark);
  });
  it('non-object customizations are ignored', () => {
    for (const c of ['x', 5, null, [], true]) expect(resolveStateColor('working', 'light', c)).toBe(STATE_COLORS.working.light);
  });
});

describe('blend', () => {
  it('matches the contract examples exactly', () => {
    expect(blend('#181818', '#8B5CF6', 0.14)).toBe('#282237');
    expect(blend('#F8F8F8', '#6526F3', 0.1)).toBe('#E9E3F8');
  });
  it('amount 0 gives base and amount 1 gives over, uppercase', () => {
    expect(blend('#181818', '#8b5cf6', 0)).toBe('#181818');
    expect(blend('#181818', '#8b5cf6', 1)).toBe('#8B5CF6');
  });
});

const C = (state: keyof typeof STATE_COLORS): string => STATE_COLORS[state].dark;
const NO_EDITOR: EditorView = { focused: [], unfocused: [], groupCount: 1, tabEdges: true };
const BASE: FrameInput = { mode: 'border', kind: 'dark', panel: [], editor: NO_EDITOR };
const withEditor = (editor: Partial<EditorView>, rest: Partial<FrameInput> = {}): FrameInput =>
  ({ ...BASE, ...rest, editor: { ...NO_EDITOR, ...editor } });

/** Checker: several visible agent terminals in one area show the most urgent state. */
type Aggregate = (states: readonly unknown[]) => unknown;
function checkMostUrgentWins(agg: Aggregate): void {
  expect(agg(['ready', 'waiting']), 'waiting beats ready').toBe('waiting');
  expect(agg(['waiting', 'ready']), 'order does not matter').toBe('waiting');
  expect(agg(['working', 'ready']), 'working beats ready').toBe('working');
  expect(agg(['working', 'waiting']), 'waiting beats working').toBe('waiting');
  expect(agg(['waiting', 'error', 'ready', 'working']), 'error beats all').toBe('error');
}

/** Checker: the panel and the editor area are framed at the same time, each in its own state. */
type Desire = (input: FrameInput) => Record<string, string>;
function checkBothAreasAtOnce(desire: Desire): void {
  const out = desire(withEditor({ focused: ['ready'] }, { panel: ['waiting'] }));
  for (const k of PANEL_BORDER_KEYS) expect(out[k], `panel ${k}`).toBe(C('waiting'));
  expect(out['editor.border'], 'editor box').toBe(C('ready'));
  expect(out['tab.activeBorderTop'], 'editor tab').toBe(C('ready'));
}

/** Checker: with two or more editor groups and tab edges drawn, nothing boxes every group. */
function checkNoBoxAcrossGroups(desire: Desire): void {
  const out = desire(withEditor({ focused: ['waiting'], groupCount: 2 }));
  expect(Object.keys(out), 'editor.border spans every group').not.toContain('editor.border');
  expect(Object.keys(out), 'editorGroup.border paints every separator').not.toContain('editorGroup.border');
  expect(out['tab.activeBorderTop']).toBe(C('waiting'));
}

describe('mostUrgent', () => {
  it('URGENCY is the canonical vocabulary, most urgent first', () => {
    expect([...URGENCY].sort()).toEqual([...STATES].sort());
    expect(URGENCY).toEqual(['error', 'waiting', 'working', 'ready']);
  });
  it('most urgent wins: error > waiting > working > ready', () => {
    checkMostUrgentWins(mostUrgent);
  });
  it('every permutation of the four states gives error', () => {
    const perms = (xs: string[]): string[][] => xs.length <= 1 ? [xs] : xs.flatMap((x, i) => perms([...xs.slice(0, i), ...xs.slice(i + 1)]).map((p) => [x, ...p]));
    for (const p of perms([...STATES])) expect(mostUrgent(p), p.join(',')).toBe('error');
  });
  it('ignores anything that is not a canonical state; nothing left gives undefined', () => {
    expect(mostUrgent([])).toBeUndefined();
    expect(mostUrgent(['cancelled', undefined, 7, null])).toBeUndefined();
    expect(mostUrgent(['cancelled', 'ready'])).toBe('ready');
  });
});

describe('tabEdgesDrawn', () => {
  it('the classic layout draws tab edges in every version', () => {
    for (const v of ['1.90.0', '1.140.0', '1.147.0']) {
      expect(tabEdgesDrawn(v, false), v).toBe(true);
      expect(tabEdgesDrawn(v, undefined), `${v}, setting not registered`).toBe(true);
    }
  });
  it('the modern layout draws them from 1.147 on (1.140 does not: spike, #2175)', () => {
    expect(tabEdgesDrawn('1.140.0', true)).toBe(false);
    expect(tabEdgesDrawn('1.146.9', true)).toBe(false);
    expect(tabEdgesDrawn('1.147.0', true)).toBe(true);
    expect(tabEdgesDrawn('1.148.0-insider', true)).toBe(true);
    expect(tabEdgesDrawn('1.150.2', true)).toBe(true);
    expect(tabEdgesDrawn('2.0.0', true)).toBe(true);
  });
  it('an unreadable version in the modern layout falls back to the box', () => {
    expect(tabEdgesDrawn('', true)).toBe(false);
    expect(tabEdgesDrawn('nightly', true)).toBe(false);
  });
});

describe('desiredFrame (per area)', () => {
  it('is {} when the mode is off, even with agents in both areas', () => {
    expect(desiredFrame(withEditor({ focused: ['waiting'] }, { mode: 'off', panel: ['error'] }))).toEqual({});
  });
  it('is {} when no area shows a canonical state', () => {
    expect(desiredFrame(BASE)).toEqual({});
    expect(desiredFrame(withEditor({ focused: ['cancelled'] }, { panel: [undefined, 7] }))).toEqual({});
  });
  it('panel: the most urgent state among the panel agent terminals (split panes)', () => {
    expect(desiredFrame({ ...BASE, panel: ['ready', 'waiting'] })).toEqual(panelDesired(C('waiting')));
  });
  it('panel and editor area are framed at the same time, each by its own terminals', () => {
    checkBothAreasAtOnce(desiredFrame);
  });
  it('one editor group: the box and the active-tab edge, never the unfocused-tab key or the separators', () => {
    expect(desiredFrame(withEditor({ focused: ['working'] }))).toEqual({
      'editor.border': C('working'), 'tab.activeBorderTop': C('working'),
    });
  });
  it('two or more groups with tab edges drawn: only the tab edges, each in its group bucket', () => {
    checkNoBoxAcrossGroups(desiredFrame);
    expect(desiredFrame(withEditor({ focused: ['ready'], unfocused: ['waiting'], groupCount: 2 }))).toEqual({
      'tab.activeBorderTop': C('ready'), 'tab.unfocusedActiveBorderTop': C('waiting'),
    });
    expect(desiredFrame(withEditor({ unfocused: ['ready'], groupCount: 2 }))).toEqual({
      'tab.unfocusedActiveBorderTop': C('ready'),
    });
  });
  it('several agent terminals in unfocused groups: the most urgent wins on the unfocused key', () => {
    const out = desiredFrame(withEditor({ unfocused: ['ready', 'error', 'working', 'waiting'], groupCount: 4 }));
    expect(out).toEqual({ 'tab.unfocusedActiveBorderTop': C('error') });
  });
  it('two or more groups without tab edges (modern layout before 1.147): the box, in the most urgent editor state', () => {
    expect(desiredFrame(withEditor({ focused: ['ready'], unfocused: ['waiting'], groupCount: 2, tabEdges: false }))).toEqual({
      'editor.border': C('waiting'), 'tab.activeBorderTop': C('ready'), 'tab.unfocusedActiveBorderTop': C('waiting'),
    });
  });
  it('never writes editorGroup.border (it paints every separator); it stays owned so an old value is restored', () => {
    expect(OWNED_KEYS).toContain('editorGroup.border');
    for (const groupCount of [1, 2, 3]) {
      for (const tabEdges of [true, false]) {
        const out = desiredFrame(withEditor({ focused: ['waiting'], unfocused: ['ready'], groupCount, tabEdges }, { mode: 'border-and-tint', panel: ['ready'] }));
        expect(Object.keys(out)).not.toContain('editorGroup.border');
      }
    }
  });
  it('tint: one tint for the window, from the most urgent framed state; panel.background only when the panel is framed', () => {
    const tintOf = (s: keyof typeof STATE_COLORS): string => blend(TINT_BASE.dark, C(s), TINT_AMOUNT.dark);
    const both = desiredFrame(withEditor({ focused: ['waiting'] }, { mode: 'border-and-tint', panel: ['ready'] }));
    expect(both['terminal.background']).toBe(tintOf('waiting'));
    expect(both['panel.background']).toBe(tintOf('waiting'));
    expect(both['panel.border']).toBe(C('ready'));
    const editorOnly = desiredFrame(withEditor({ focused: ['working'] }, { mode: 'border-and-tint' }));
    expect(editorOnly['terminal.background']).toBe(tintOf('working'));
    expect(Object.keys(editorOnly)).not.toContain('panel.background');
  });
  it('the light theme blends over the light base', () => {
    const out = desiredFrame({ ...BASE, mode: 'border-and-tint', kind: 'light', panel: ['working'] });
    expect(out['panel.border']).toBe('#6526F3');
    expect(out['terminal.background']).toBe(blend(TINT_BASE.light, '#6526F3', TINT_AMOUNT.light));
  });
  it('honours an irrlicht.state.<state> override', () => {
    expect(desiredFrame({ ...BASE, panel: ['error'], customizations: { 'irrlicht.state.error': '#abcdef' } }))
      .toEqual(panelDesired('#ABCDEF'));
  });
  it('only returns owned keys', () => {
    for (const mode of ['border', 'border-and-tint'] as const) {
      for (const groupCount of [1, 2]) {
        for (const tabEdges of [true, false]) {
          const out = desiredFrame(withEditor({ focused: ['ready'], unfocused: ['error', 'working'], groupCount, tabEdges }, { mode, panel: ['waiting'] }));
          for (const k of Object.keys(out)) expect(OWNED_KEYS).toContain(k);
        }
      }
    }
  });
});

describe('normalizeMemo', () => {
  const good: FrameMemo = { prev: { 'panel.border': '#123456' }, written: { 'panel.border': '#8B5CF6' }, hadObject: false };
  it('returns a valid memo unchanged', () => {
    expect(normalizeMemo(good)).toEqual(good);
    const withPending = { ...good, pending: { 'panel.border': '#FF9500' } };
    expect(normalizeMemo(withPending)).toEqual(withPending);
  });
  it('migrates the old shape by using applied as written', () => {
    const old = { prev: good.prev, applied: good.written, hadObject: false };
    expect(normalizeMemo(old)).toEqual(good);
  });
  it('drops a pending that is not an object of strings, but keeps the memo', () => {
    expect(normalizeMemo({ ...good, pending: 'x' })).toEqual(good);
    expect(normalizeMemo({ ...good, pending: { a: 1 } })).toEqual(good);
    expect(normalizeMemo({ ...good, pending: [] })).toEqual(good);
  });
  it('returns undefined for junk', () => {
    const junk: unknown[] = [
      undefined, null, 'x', 7, true, [], [good],
      {}, { prev: {}, hadObject: true }, { written: {}, hadObject: true }, { prev: {}, written: {} },
      { ...good, prev: 'x' }, { ...good, prev: [] }, { ...good, prev: null },
      { ...good, written: 'x' }, { ...good, written: [] }, { ...good, written: { a: 1 } }, { ...good, written: { a: null } },
      { ...good, hadObject: 'yes' }, { ...good, hadObject: 1 },
      { prev: {}, applied: { a: 1 }, hadObject: true },
    ];
    for (const j of junk) expect(normalizeMemo(j), JSON.stringify(j)).toBeUndefined();
  });
});

describe('planFrame', () => {
  const desiredA = panelDesired('#8B5CF6');
  const desiredB = panelDesired('#FF9500');

  it('rule 1: a defined non-object current is invalid and nothing is written', () => {
    const memo: FrameMemo = { prev: {}, written: desiredA, hadObject: true };
    for (const bad of ['text', 5, [1, 2], null, true]) {
      const p = planFrame(bad, memo, desiredB);
      expect(p).toEqual({ invalid: true, changed: false, next: bad, memo });
    }
  });

  it('rule 2: desired keys outside OWNED_KEYS are ignored', () => {
    const p = planFrame({ a: 1 }, undefined, { 'statusBar.background': '#FFFFFF', 'panel.border': '#111111' });
    expect(p.next).toEqual({ a: 1, 'panel.border': '#111111' });
    expect(p.memo?.written).toEqual({ 'panel.border': '#111111' });
    const none = planFrame({ a: 1 }, undefined, { 'statusBar.background': '#FFFFFF' });
    expect(none).toEqual({ invalid: false, changed: false, next: { a: 1 }, memo: undefined });
  });

  it('rule 3/4: non-owned keys, theme blocks and a user statusBar colour survive apply, recolour and restore', () => {
    checkKeepsNonOwnedKeys(planFrame);
    checkKeepsThemeBlocks(planFrame);
  });

  it('rule 3/4: the user own value of an owned key comes back; a key the user did not have is deleted', () => {
    let w: World = { current: { 'panel.border': '#123456' }, memo: undefined };
    w = step(planFrame, w, desiredA);
    expect(w.memo?.prev).toEqual({ 'panel.border': '#123456' });
    expect(w.current).toEqual(desiredA);
    w = step(planFrame, w, {});
    expect(w.current).toEqual({ 'panel.border': '#123456' });
    expect(Object.keys(w.current as Obj)).not.toContain('terminal.border');
  });

  it('rule 4: switching panel to editor restores the panel keys to the user values', () => {
    let w: World = { current: { 'panel.border': '#123456', 'terminal.border': '#654321' }, memo: undefined };
    w = step(planFrame, w, panelDesired('#8B5CF6'));
    w = step(planFrame, w, editorDesired('#8B5CF6'));
    const cur = w.current as Obj;
    expect(cur['panel.border']).toBe('#123456');
    expect(cur['terminal.border']).toBe('#654321');
    for (const k of PANEL_BORDER_KEYS.filter((x) => x !== 'panel.border' && x !== 'terminal.border')) {
      expect(Object.keys(cur), k).not.toContain(k);
    }
    for (const k of EDITOR_BORDER_KEYS) expect(cur[k]).toBe('#8B5CF6');
  });

  it('rule 3: a user edit to an owned key while a frame is applied is kept and survives the restore', () => {
    let w: World = { current: { 'panel.border': '#123456' }, memo: undefined };
    w = step(planFrame, w, desiredA);
    const edited = { ...(w.current as Obj), 'panel.border': '#00FF00' };
    w = step(planFrame, { current: edited, memo: w.memo }, desiredB);
    expect(w.memo?.prev['panel.border']).toBe('#00FF00');
    w = step(planFrame, w, {});
    expect(w.current).toEqual({ 'panel.border': '#00FF00' });
  });

  it('rule 3: stale memo with our colours present and desired {} gives the user originals back and clears the memo', () => {
    const memo: FrameMemo = { prev: { 'panel.border': '#123456' }, written: desiredA, hadObject: true };
    const crashed = { 'statusBar.background': '#222222', ...desiredA };
    const p = planFrame(crashed, memo, {});
    expect(p.next).toEqual({ 'statusBar.background': '#222222', 'panel.border': '#123456' });
    expect(p.memo).toBeUndefined();
    expect(p.changed).toBe(true);
  });

  it('rule 3: re-applying with a memo never re-snapshots our own colours as the user values', () => {
    checkNoReSnapshot(planFrame);
  });

  it('a key equal to the pending colour is ours, equal to the written colour is ours, anything else is the user value', () => {
    const memo: FrameMemo = {
      prev: { 'panel.border': '#123456', 'terminal.border': '#654321' },
      written: { 'panel.border': '#8B5CF6', 'terminal.border': '#8B5CF6', 'panelTitle.activeBorder': '#8B5CF6' },
      pending: { 'panel.border': '#FF9500', 'modernPanel.border': '#FF9500' },
      hadObject: true,
    };
    const disk = {
      'panel.border': '#FF9500',
      'terminal.border': '#8B5CF6',
      'modernPanel.border': '#FF9500',
      'panelTitle.activeBorder': '#00FF00',
      'terminal.tab.activeBorder': '#8B5CF6',
    };
    const p = planFrame(disk, memo, {});
    expect(p.next).toEqual({
      'panel.border': '#123456',
      'terminal.border': '#654321',
      'panelTitle.activeBorder': '#00FF00',
      'terminal.tab.activeBorder': '#8B5CF6',
    });
  });

  it('rule 4: restore puts owned keys back', () => {
    checkRestoresOwnedKeys(planFrame);
  });

  it('rule 5: the memo records prev, written and hadObject (hadObject sticks across passes)', () => {
    const first = planFrame(undefined, undefined, desiredA);
    expect(first.memo).toEqual({ prev: {}, written: desiredA, hadObject: false });
    const second = planFrame(first.next, first.memo, desiredB);
    expect(second.memo).toEqual({ prev: {}, written: desiredB, hadObject: false });
    const withObj = planFrame({ x: 1 }, undefined, desiredA);
    expect(withObj.memo?.hadObject).toBe(true);
  });

  it('rule 6: restore when the workspace had no colorCustomizations gives undefined, not {}', () => {
    let w: World = { current: undefined, memo: undefined };
    w = step(planFrame, w, desiredA);
    expect(w.current).toEqual(desiredA);
    w = step(planFrame, w, {});
    expect(w.current).toBeUndefined();
    expect(w.memo).toBeUndefined();
    expect(Object.keys(w)).toContain('current');
  });

  it('rule 6: restore keeps an object when the workspace had {} or other keys', () => {
    let w: World = { current: {}, memo: undefined };
    w = step(planFrame, w, desiredA);
    w = step(planFrame, w, {});
    expect(w.current).toEqual({});
    let other: World = { current: { x: 1 }, memo: undefined };
    other = step(planFrame, other, desiredA);
    other = step(planFrame, other, {});
    expect(other.current).toEqual({ x: 1 });
  });

  it('rule 7: unchanged desired gives changed:false', () => {
    const first = planFrame({ x: 1 }, undefined, desiredA);
    const again = planFrame(first.next, first.memo, desiredA);
    expect(again.changed).toBe(false);
    expect(again.memo).toEqual(first.memo);
  });

  it('rule 7: key order differences alone give changed:false', () => {
    const first = planFrame({ x: 1 }, undefined, desiredA);
    const reordered = Object.fromEntries(Object.entries(first.next as Obj).reverse());
    expect(planFrame(reordered, first.memo, desiredA).changed).toBe(false);
  });

  it('rule 7: undefined and {} are different values', () => {
    const memo: FrameMemo = { prev: {}, written: desiredA, hadObject: true };
    const p = planFrame(desiredA, memo, {});
    expect(p.next).toEqual({});
    expect(p.changed).toBe(true);
  });

  it('rule 8: no memo and desired {} is a no-op', () => {
    for (const cur of [undefined, {}, { x: 1 }, { 'panel.border': '#123456' }]) {
      expect(planFrame(cur, undefined, {})).toEqual({ invalid: false, changed: false, next: cur, memo: undefined });
    }
  });

  it('does not mutate its inputs', () => {
    const current = USER_SETTINGS();
    const snapshot = JSON.parse(JSON.stringify(current));
    const first = planFrame(current, undefined, desiredA);
    const memoSnapshot = JSON.parse(JSON.stringify(first.memo));
    planFrame(first.next, first.memo, {});
    expect(current).toEqual(snapshot);
    expect(first.memo).toEqual(memoSnapshot);
  });
});

// ---------------------------------------------------------------------------
// FrameSync with fake IO and fake timers
// ---------------------------------------------------------------------------
class FakeTimers implements FrameTimers {
  private next = 1;
  readonly pending = new Map<number, { fn: () => void; ms: number }>();
  set(fn: () => void, ms: number): unknown {
    const id = this.next++;
    this.pending.set(id, { fn, ms });
    return id;
  }
  clear(handle: unknown): void {
    this.pending.delete(handle as number);
  }
  get count(): number { return this.pending.size; }
  get delays(): number[] { return [...this.pending.values()].map((t) => t.ms); }
  fire(): void {
    const all = [...this.pending.values()];
    this.pending.clear();
    for (const t of all) t.fn();
  }
}

/** Lets pending promise continuations run; a macrotask turn, not a timed wait. */
const flush = (): Promise<void> => new Promise((r) => setImmediate(r));

interface Deferred { resolve(): void; reject(e: Error): void }

class FakeIO implements FrameIO {
  current: unknown;
  memo: FrameMemo | undefined;
  events: string[] = [];
  writes: unknown[] = [];
  saves: Array<FrameMemo | undefined> = [];
  logs: string[] = [];
  inFlight = 0;
  maxInFlight = 0;
  manual = false;
  pendingWrites: Deferred[] = [];
  failWrites = 0;
  constructor(current: unknown = undefined, memo: FrameMemo | undefined = undefined) {
    this.current = current;
    this.memo = memo;
  }
  read(): unknown { return this.current; }
  loadMemo(): FrameMemo | undefined { return this.memo; }
  log(m: string): void { this.logs.push(m); }
  saveMemo(m: FrameMemo | undefined): Promise<void> {
    this.events.push(m === undefined ? 'saveMemo:clear' : 'saveMemo:set');
    this.saves.push(m);
    this.memo = m;
    return Promise.resolve();
  }
  write(next: unknown): Promise<void> {
    this.events.push('write');
    this.writes.push(next);
    this.inFlight++;
    this.maxInFlight = Math.max(this.maxInFlight, this.inFlight);
    return new Promise<void>((resolve, reject) => {
      const finish = (error?: Error): void => {
        this.inFlight--;
        if (error) { reject(error); return; }
        this.current = next;
        this.events.push('write:done');
        resolve();
      };
      if (this.failWrites > 0) { this.failWrites--; queueMicrotask(() => finish(new Error('boom'))); return; }
      if (!this.manual) { queueMicrotask(() => finish()); return; }
      this.pendingWrites.push({ resolve: () => finish(), reject: (e) => finish(e) });
    });
  }
}

/** Applies A, then asks for `failures` different colours whose writes are all rejected. */
async function applyThenFailRecolours(sync: FrameSync, io: FakeIO, failures: number): Promise<void> {
  sync.request(panelDesired('#8B5CF6'));
  await sync.settle();
  io.failWrites = failures;
  const colours = ['#FF9500', '#34C759', '#FF3B30'];
  for (let i = 0; i < failures; i++) {
    sync.request(panelDesired(colours[i]));
    await sync.settle();
  }
}

describe('FrameSync', () => {
  const A = panelDesired('#8B5CF6');
  const B = panelDesired('#FF9500');
  const make = (io: FrameIO) => {
    const timers = new FakeTimers();
    return { sync: new FrameSync(io, timers), timers };
  };

  it('debounces: several requests in the window give one write with the latest', async () => {
    const io = new FakeIO();
    const { sync, timers } = make(io);
    sync.request(A);
    sync.request(B);
    sync.request(A);
    sync.request(B);
    expect(timers.count).toBe(1);
    expect(timers.delays).toEqual([FRAME_DEBOUNCE_MS]);
    expect(io.writes).toHaveLength(0);
    timers.fire();
    await sync.settle();
    expect(io.writes).toEqual([B]);
  });

  it('serialises: a request during a pending write causes exactly one more pass, never a parallel write', async () => {
    const io = new FakeIO();
    io.manual = true;
    const { sync, timers } = make(io);
    sync.request(A);
    const settled = sync.settle();
    await flush();
    expect(io.writes).toEqual([A]);
    sync.request(B);
    timers.fire();
    await flush();
    expect(io.writes).toHaveLength(1);
    io.pendingWrites[0].resolve();
    await flush();
    expect(io.writes).toHaveLength(2);
    expect(io.writes[1]).toEqual(B);
    io.pendingWrites[1].resolve();
    await settled;
    expect(io.maxInFlight).toBe(1);
    expect(io.writes).toHaveLength(2);
  });

  it('saves the memo before the apply write and clears it only after the restoring write resolved', async () => {
    const io = new FakeIO({ 'panel.border': '#123456' });
    const { sync } = make(io);
    sync.request(A);
    await sync.settle();
    expect(io.events).toEqual(['saveMemo:set', 'write', 'write:done', 'saveMemo:set']);
    expect(io.saves[0]?.pending).toEqual(A);
    expect(io.saves[0]?.written).toEqual({});
    expect(io.saves[1]?.pending).toBeUndefined();
    expect(io.saves[1]?.written).toEqual(A);
    io.events.length = 0;
    io.manual = true;
    sync.request({});
    const settled = sync.settle();
    await flush();
    expect(io.events).toEqual(['write']);
    expect(io.memo).toBeDefined();
    io.pendingWrites[0].resolve();
    await settled;
    expect(io.events).toEqual(['write', 'write:done', 'saveMemo:clear']);
    expect(io.memo).toBeUndefined();
  });

  it('a rejected write is logged without throwing, and the next request retries', async () => {
    const io = new FakeIO();
    io.failWrites = 1;
    const { sync } = make(io);
    sync.request(A);
    await expect(sync.settle()).resolves.toBeUndefined();
    expect(io.logs).toHaveLength(1);
    expect(io.logs[0]).toContain('boom');
    sync.request(A);
    await sync.settle();
    expect(io.current).toEqual(A);
    expect(io.writes).toHaveLength(2);
  });

  it('logs the same failure message only once', async () => {
    const io = new FakeIO();
    io.failWrites = 2;
    const { sync } = make(io);
    sync.request(A);
    await sync.settle();
    sync.request(A);
    await sync.settle();
    expect(io.logs).toHaveLength(1);
  });

  it('a non-object workspace value is logged once and nothing is written', async () => {
    const io = new FakeIO('not an object');
    const { sync } = make(io);
    sync.request(A);
    await sync.settle();
    sync.request(B);
    await sync.settle();
    expect(io.writes).toHaveLength(0);
    expect(io.saves).toHaveLength(0);
    expect(io.logs).toEqual(['workspace workbench.colorCustomizations is not an object; terminal frame left alone']);
  });

  it('settle resolves only when idle', async () => {
    const io = new FakeIO();
    io.manual = true;
    const { sync } = make(io);
    sync.request(A);
    let done = false;
    const settled = sync.settle().then(() => { done = true; });
    await flush();
    expect(done).toBe(false);
    io.pendingWrites[0].resolve();
    await settled;
    expect(done).toBe(true);
  });

  it('settle with nothing requested resolves without any write', async () => {
    const io = new FakeIO();
    const { sync } = make(io);
    await sync.settle();
    expect(io.events).toEqual([]);
  });

  it('settle cancels the pending timer and applies now', async () => {
    const io = new FakeIO();
    const { sync, timers } = make(io);
    sync.request(A);
    expect(timers.count).toBe(1);
    await sync.settle();
    expect(timers.count).toBe(0);
    expect(io.writes).toEqual([A]);
  });

  it('restore puts the user values back and clears the memo', async () => {
    const io = new FakeIO({ 'panel.border': '#123456', 'statusBar.background': '#222222' });
    const { sync } = make(io);
    sync.request(A);
    await sync.settle();
    expect((io.current as Obj)['terminal.border']).toBe('#8B5CF6');
    await sync.restore();
    expect(io.current).toEqual({ 'panel.border': '#123456', 'statusBar.background': '#222222' });
    expect(io.memo).toBeUndefined();
  });

  it('restore also finishes a leftover from a previous run (stale memo)', async () => {
    const memo: FrameMemo = { prev: {}, written: A, hadObject: false };
    const io = new FakeIO({ ...A }, memo);
    const { sync } = make(io);
    await sync.restore();
    expect(io.current).toBeUndefined();
    expect(io.memo).toBeUndefined();
  });

  it('requests after dispose are ignored, and dispose does not restore', async () => {
    const io = new FakeIO();
    const { sync, timers } = make(io);
    sync.request(A);
    sync.dispose();
    expect(timers.count).toBe(0);
    sync.request(B);
    expect(timers.count).toBe(0);
    await sync.settle();
    expect(io.writes).toHaveLength(0);
  });

  it('a settle or restore landing as the pass loop finishes still runs its pass (any microtask tick)', async () => {
    const failing: number[] = [];
    for (let ticks = 0; ticks <= 20; ticks++) {
      const io = new FakeIO({ 'panel.border': '#123456' });
      let release: () => void = () => undefined;
      const gate = new Promise<void>((r) => { release = r; });
      let firstSave = true;
      io.saveMemo = (m) => {
        io.memo = m;
        if (!firstSave) return Promise.resolve();
        firstSave = false;
        return gate;
      };
      const sync = new FrameSync(io, new FakeTimers());
      sync.request(A);
      const first = sync.settle();
      await Promise.resolve();
      let chain: Promise<void> = Promise.resolve();
      for (let i = 0; i < ticks; i++) chain = chain.then(() => undefined);
      const restored = chain.then(() => sync.restore());
      release();
      await first;
      await restored;
      if ((io.current as Obj)['panel.border'] !== '#123456' || Object.keys(io.current as Obj).length !== 1) failing.push(ticks);
    }
    expect(failing, `restore lost at microtask ticks ${failing.join(',')}`).toEqual([]);
  });

  it('dispose right after restore() does not discard the restore while an apply write is in flight', async () => {
    const io = new FakeIO({ 'panel.border': '#123456', 'statusBar.background': '#222222' });
    io.manual = true;
    const { sync } = make(io);
    sync.request(A);
    const applying = sync.settle();
    await flush();
    expect(io.pendingWrites).toHaveLength(1);
    const restoring = sync.restore();
    sync.dispose();
    io.pendingWrites[0].resolve();
    await flush();
    io.pendingWrites[1].resolve();
    await applying;
    await restoring;
    expect(io.current).toEqual({ 'panel.border': '#123456', 'statusBar.background': '#222222' });
    expect(io.memo).toBeUndefined();
  });

  it('dispose drops a request whose debounce timer had not fired', async () => {
    const io = new FakeIO();
    const { sync, timers } = make(io);
    sync.request(A);
    sync.dispose();
    expect(timers.count).toBe(0);
    await sync.settle();
    expect(io.events).toEqual([]);
  });

  it('a rejected recolour write leaves the old frame recognisable: restore brings the user values back', async () => {
    const io = new FakeIO({ 'panel.border': '#123456' });
    const { sync } = make(io);
    await applyThenFailRecolours(sync, io, 1);
    await sync.restore();
    expect(io.current).toEqual({ 'panel.border': '#123456' });
    expect(io.memo).toBeUndefined();
  });

  it('a chain of rejected recolour writes still recognises the last confirmed frame as ours', async () => {
    const io = new FakeIO({ 'panel.border': '#123456' });
    const { sync } = make(io);
    await applyThenFailRecolours(sync, io, 2);
    await sync.restore();
    expect(io.current).toEqual({ 'panel.border': '#123456' });
    expect(io.memo).toBeUndefined();
  });

  it('a crash between the pre-write memo save and a recolour write is repaired by the next run', async () => {
    const first = new FakeIO({ 'panel.border': '#123456' });
    const { sync } = make(first);
    sync.request(A);
    await sync.settle();
    first.manual = true;
    sync.request(editorDesired('#FF9500'));
    void sync.settle();
    await flush();
    expect(first.pendingWrites).toHaveLength(1);
    const reborn = new FakeIO(first.current, first.memo);
    await make(reborn).sync.restore();
    expect(reborn.current).toEqual({ 'panel.border': '#123456' });
    expect(reborn.memo).toBeUndefined();
  });

  it('a rejected restoring write leaves the loaded memo intact', async () => {
    const io = new FakeIO({ 'panel.border': '#123456' });
    const { sync } = make(io);
    sync.request(A);
    await sync.settle();
    const before = io.memo;
    io.failWrites = 1;
    await sync.restore();
    expect(io.memo).toEqual(before);
    await sync.restore();
    expect(io.current).toEqual({ 'panel.border': '#123456' });
    expect(io.memo).toBeUndefined();
  });

  it('an old-shape memo (applied instead of written) is migrated: restore brings the user values back', async () => {
    const old = { prev: { 'panel.border': '#123456' }, applied: A, hadObject: true } as unknown as FrameMemo;
    const io = new FakeIO({ ...A }, old);
    const { sync } = make(io);
    await sync.restore();
    expect(io.current).toEqual({ 'panel.border': '#123456' });
    expect(io.memo).toBeUndefined();
    expect(io.logs).toEqual([]);
  });

  it('an unreadable memo is ignored with one log line, never throws, and is dropped', async () => {
    const io = new FakeIO({ 'statusBar.background': '#222222' }, 'junk' as unknown as FrameMemo);
    const { sync } = make(io);
    sync.request(A);
    await sync.settle();
    await sync.restore();
    expect(io.logs).toEqual(['terminal frame: ignoring an unreadable memo']);
    expect(io.current).toEqual({ 'statusBar.background': '#222222' });
    expect(io.memo).toBeUndefined();
  });

  it('an unreadable memo is cleared even when there is nothing to restore', async () => {
    const io = new FakeIO({ 'statusBar.background': '#222222' }, [1] as unknown as FrameMemo);
    const { sync } = make(io);
    await sync.restore();
    expect(io.memo).toBeUndefined();
    expect(io.writes).toHaveLength(0);
  });

  it('a landed but unconfirmed write followed by a refused one still leaves the frame recognisable', async () => {
    const memo: FrameMemo = { prev: { 'panel.border': '#123456' }, written: A, pending: B, hadObject: true };
    const io = new FakeIO({ ...B }, memo);
    const { sync } = make(io);
    io.failWrites = 1;
    sync.request(panelDesired('#34C759'));
    await sync.settle();
    await sync.restore();
    expect(io.current).toEqual({ 'panel.border': '#123456' });
    expect(io.memo).toBeUndefined();
  });

  it('writes nothing when nothing changed', async () => {
    const io = new FakeIO();
    const { sync } = make(io);
    sync.request(A);
    await sync.settle();
    io.events.length = 0;
    sync.request(A);
    await sync.settle();
    expect(io.events).toEqual([]);
    sync.request({});
    await sync.restore();
    io.events.length = 0;
    sync.request({});
    await sync.settle();
    expect(io.events).toEqual([]);
  });
});

// ---------------------------------------------------------------------------
// Committed in-language mutants: each checker passes on the real planFrame and
// throws on a hand-written mutant. A checker that stayed green on its mutant
// would be a claim, not evidence.
// ---------------------------------------------------------------------------
const isRealOwned = (k: string): boolean => OWNED_KEYS.includes(k);

/** Mutant 1: forgets every key that is not owned. */
const dropsNonOwned: Planner = (current, memo, desired) => {
  const p = planFrame(current, memo, desired);
  if (p.next === null || typeof p.next !== 'object') return p;
  const kept = Object.fromEntries(Object.entries(p.next as Obj).filter(([k]) => isRealOwned(k)));
  return { ...p, next: kept };
};

/** Mutant 2: removes "[Theme Name]" blocks. */
const dropsThemeBlocks: Planner = (current, memo, desired) => {
  const p = planFrame(current, memo, desired);
  if (p.next === null || typeof p.next !== 'object') return p;
  const kept = Object.fromEntries(Object.entries(p.next as Obj).filter(([k]) => !k.startsWith('[')));
  return { ...p, next: kept };
};

/** Mutant 3: re-snapshots the user's values from current even when a memo exists. */
const resnapshots: Planner = (current, memo, desired) =>
  Object.keys(desired).length === 0 ? planFrame(current, memo, desired) : planFrame(current, undefined, desired);

/** Mutant 4: never restores; with nothing desired it leaves the owned keys coloured. */
const skipsRestore: Planner = (current, memo, desired) =>
  Object.keys(desired).length === 0 && memo !== undefined
    ? { invalid: false, changed: false, next: current, memo }
    : planFrame(current, memo, desired);

describe('committed mutants', () => {
  it('the checkers pass on the real planFrame', () => {
    checkKeepsNonOwnedKeys(planFrame);
    checkKeepsThemeBlocks(planFrame);
    checkNoReSnapshot(planFrame);
    checkRestoresOwnedKeys(planFrame);
  });
  it('a planner that drops non-owned keys is caught', () => {
    expect(() => checkKeepsNonOwnedKeys(dropsNonOwned)).toThrow();
  });
  it('a planner that removes a [Theme] block is caught', () => {
    expect(() => checkKeepsThemeBlocks(dropsThemeBlocks)).toThrow();
    expect(() => checkKeepsNonOwnedKeys(dropsThemeBlocks)).toThrow();
  });
  it('a planner that re-snapshots prev while a memo exists is caught', () => {
    expect(() => checkNoReSnapshot(resnapshots)).toThrow();
  });
  it('a planner that skips the restore is caught', () => {
    expect(() => checkRestoresOwnedKeys(skipsRestore)).toThrow();
    expect(() => checkNoReSnapshot(skipsRestore)).toThrow();
  });
});

// ---------------------------------------------------------------------------
// FrameSync-level mutant: persists the after-success memo BEFORE the write (the original
// ordering). A refused recolour then leaves a memo claiming colours the disk does not hold.
// ---------------------------------------------------------------------------
interface Syncer {
  request(desired: Record<string, string>): void;
  settle(): Promise<void>;
  restore(): Promise<void>;
}
type SyncerFactory = (io: FakeIO) => Syncer;

class SavesMemoBeforeWrite implements Syncer {
  private latest: Record<string, string> = {};
  constructor(private readonly io: FakeIO) {}
  request(desired: Record<string, string>): void { this.latest = desired; }
  async settle(): Promise<void> {
    const loaded = this.io.loadMemo();
    const plan = planFrame(this.io.read(), loaded, this.latest);
    try {
      if (plan.memo !== undefined) await this.io.saveMemo(plan.memo);
      if (plan.changed) await this.io.write(plan.next);
      if (plan.memo === undefined && loaded !== undefined) await this.io.saveMemo(undefined);
    } catch {
      // swallowed, as FrameSync does
    }
  }
  async restore(): Promise<void> {
    this.request({});
    await this.settle();
  }
}

/** Apply A, fail the recolour to B, restore: the user's value must be back and no frame key may remain. */
async function checkRefusedRecolourRestores(factory: SyncerFactory): Promise<void> {
  const io = new FakeIO({ 'panel.border': '#123456' });
  const sync = factory(io);
  sync.request(panelDesired('#8B5CF6'));
  await sync.settle();
  io.failWrites = 1;
  sync.request(panelDesired('#FF9500'));
  await sync.settle();
  await sync.restore();
  expect(io.current).toEqual({ 'panel.border': '#123456' });
  expect(io.memo).toBeUndefined();
}

describe('committed FrameSync mutant', () => {
  it('the real FrameSync passes the refused-recolour checker', async () => {
    await checkRefusedRecolourRestores((io) => new FrameSync(io, new FakeTimers()));
  });
  it('a sync that saves the after-success memo before the write is caught', async () => {
    await expect(checkRefusedRecolourRestores((io) => new SavesMemoBeforeWrite(io))).rejects.toThrow();
  });
});

// ---------------------------------------------------------------------------
// Committed mutants for the per-area frame (#2175): each checker passes on the real code
// and throws on a mutant that brings back one of the reported behaviours.
// ---------------------------------------------------------------------------
/** Mutant: the least urgent state wins (URGENCY read backwards). */
const leastUrgent: Aggregate = (states) => {
  const canonical = states.filter((s): s is string => typeof s === 'string' && (URGENCY as readonly string[]).includes(s));
  return [...URGENCY].reverse().find((u) => canonical.includes(u));
};
/** Mutant: one area per window, as before #2175: the panel wins and the editor area is dropped. */
const oneAreaOnly: Desire = (input) =>
  input.panel.length > 0 ? desiredFrame({ ...input, editor: NO_EDITOR }) : desiredFrame(input);
/** Mutant: the editor box is written whatever the group count, as before #2175. */
const alwaysBox: Desire = (input) => {
  const out = desiredFrame(input);
  const editorState = mostUrgent([...input.editor.focused, ...input.editor.unfocused]);
  if (editorState && input.mode !== 'off') { out['editor.border'] = C(editorState); out['editorGroup.border'] = C(editorState); }
  return out;
};

describe('committed per-area mutants', () => {
  it('a least-urgent-wins aggregate is caught', () => {
    expect(() => checkMostUrgentWins(leastUrgent)).toThrow();
  });
  it('a one-area-per-window frame is caught', () => {
    expect(() => checkBothAreasAtOnce(oneAreaOnly)).toThrow();
  });
  it('a frame that boxes every editor group is caught', () => {
    expect(() => checkNoBoxAcrossGroups(alwaysBox)).toThrow();
  });
});
