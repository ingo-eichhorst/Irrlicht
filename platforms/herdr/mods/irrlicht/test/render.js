// Drawing main.js under node and reading back what it asked for, shared by
// main.render.test.js (the real main.js) and main.mutation.test.js
// (committed mutants of it). The stand-ins in test/stubs record every builder
// call; these helpers walk that record. The contracts at the bottom take the
// view class under test, so the same checks run against both.

import { expect } from 'vitest';
import { BRAND, wash } from '../lib/palette.js';
import { readSessions } from '../lib/sessions.js';
import { world } from './stubs/herdr.js';
import { CLAUDE_SESSION, LIVE_AGENTS, LIVE_TERMINALS, livePayload } from './fixtures.js';

// A dark theme as Herdr reports one: the six-digit colours main.js reads.
export const THEME = {
  chrome: '#1b1d23', foreground: '#abb2bf', muted: '#8a909c', dim: '#5c6370',
  border: '#2c313a', hover: '#2c313a', selected: '#323844',
};

// resetWorld is Herdr as the live run saw it: the Claude Code and Codex agents
// and their terminals, the theme above, and no command run yet.
export function resetWorld() {
  world.agents = LIVE_AGENTS;
  world.terminals = LIVE_TERMINALS;
  world.theme = THEME;
  world.ran = [];
}

export const kids = (node) => (node && typeof node === 'object' ? node.kids : []);

export function walk(node, visit) {
  if (!node || typeof node !== 'object') return;
  visit(node);
  for (const c of kids(node)) walk(c, visit);
}

// texts is every string child under `node`, in drawing order.
export function texts(node) {
  const out = [];
  const go = (n) => {
    if (typeof n === 'string') out.push(n);
    for (const c of kids(n)) go(c);
  };
  go(node);
  return out;
}

export const call = (node, name) => node.calls.find((c) => c[0] === name);
export const arg = (node, name) => call(node, name)?.[1];

export function byId(root, id) {
  let found = null;
  walk(root, (n) => {
    if (arg(n, 'id') === id) found = found || n;
  });
  expect(found, `no element with id ${id}`).not.toBeNull();
  return found;
}

// svgs is every icon file drawn under `node`, with its tint.
export function svgs(node) {
  const out = [];
  walk(node, (n) => {
    if (n.kind === 'svg') out.push([n.arg, arg(n, 'text_color')]);
  });
  return out;
}

// draw is a fresh copy of `View` (main.js's default export) in `state`.
export function draw(View, state) {
  const view = new View();
  view.init({}, { timer: { every() {} }, spawn() {} });
  if (state) view.state = state;
  return view;
}

export const ok = (payload = livePayload()) => ({ phase: 'ok', where: 'localhost:7837', sessions: readSessions(payload) });

// withClaude is the live payload with the Claude Code session (group 1)
// changed by `edit`.
export function withClaude(edit) {
  const payload = livePayload();
  payload.groups[1] = { ...payload.groups[1], agents: [edit(CLAUDE_SESSION)] };
  return payload;
}

// groupHeader is the header drawn for the group named `name`.
function groupHeader(root, name) {
  let found = null;
  walk(byId(root, 'ir-list'), (n) => {
    if (String(arg(n, 'id')).startsWith('ir-group-') && texts(n).includes(name)) found = found || n;
  });
  expect(found, `no header for group ${name}`).not.toBeNull();
  return found;
}

// A failed session's row is drawn in the error hue: the disc-mark glyph, and
// the error line on the error wash after a triangle in that hue.
export function errorRowIsRed(View) {
  resetWorld();
  const failed = withClaude((s) => ({ ...s, state: 'error', metrics: { ...s.metrics, session_error: { class: 'auth', message: 'Invalid API key' } } }));
  const row = byId(draw(View, ok(failed)).render(), 'ir-t3');
  expect(svgs(byId(row, 'ir-state-t3')), 'the error row\'s state glyph').toEqual([['mods/irrlicht/icons/state-error.svg', BRAND.error]]);
  const line = byId(row, 'ir-error-t3');
  expect(texts(line), 'the error line\'s text').toEqual(['Invalid API key']);
  expect(arg(line, 'bg'), 'the error line\'s wash').toBe(wash(BRAND.error));
  expect(svgs(line), 'the error line\'s triangle').toEqual([['mods/irrlicht/icons/alert.svg', BRAND.error]]);
}

// A state the panel cannot read, matched or not, is the grey ring with a
// question mark, never green (states.js irrlichtState; the macOS app's #1797
// rule).
export function unknownRowIsGrey(View) {
  resetWorld();
  const unreadable = withClaude((s) => ({ ...s, state: 'hibernating' }));
  // t9 has no terminal, so no session matches it, and no Herdr status.
  world.agents = [...LIVE_AGENTS, { ...LIVE_AGENTS[0], address: 't9', status: null }];
  const root = draw(View, ok(unreadable)).render();
  const unknown = [['mods/irrlicht/icons/state-unknown.svg', BRAND.cancelled]];
  expect(svgs(byId(root, 'ir-state-t3')), 'the matched row in a state this panel cannot read').toEqual(unknown);
  expect(svgs(byId(root, 'ir-state-t9')), 'the unmatched row with no Herdr status').toEqual(unknown);
  expect(BRAND.cancelled).not.toBe(BRAND.ready);
}

// The header of a Gas Town group carries ⛽ before its name; a project's does
// not (GroupView.swift:80-83).
export function gastownGroupIsMarked(View) {
  resetWorld();
  const payload = livePayload();
  payload.groups[1] = { ...payload.groups[1], name: 'gastown', type: 'gastown' };
  const root = draw(View, ok(payload)).render();
  expect(texts(groupHeader(root, 'gastown')), 'the Gas Town header').toContain('⛽');
  expect(texts(groupHeader(root, 'codex')), 'a project header').not.toContain('⛽');
}
