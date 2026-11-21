// main.js drawn under node, against the recording stand-ins in test/stubs
// (vitest.config.js aliases Herdr's module names to them). `herdr-app check`
// renders only a first frame with no agents and no daemon, so this is where
// the drawing of groups, rows, state icons, the context bar and the notes is
// run at all before someone looks at a window. It reads back what main.js
// asked for (texts, colours, icon files, handlers); it cannot see layout.

import { beforeEach, describe, expect, test } from 'vitest';
import Irrlicht from '../main.js';
import { BRAND, track, wash } from '../lib/palette.js';
import { readSessions } from '../lib/sessions.js';
import { world } from './stubs/herdr.js';
import { CLAUDE_SESSION, LIVE_AGENTS, LIVE_TERMINALS, livePayload } from './fixtures.js';

// A dark theme as Herdr reports one: the six-digit colours main.js reads.
const THEME = {
  chrome: '#1b1d23', foreground: '#abb2bf', muted: '#8a909c', dim: '#5c6370',
  border: '#2c313a', hover: '#2c313a', selected: '#323844',
};

const kids = (node) => (node && typeof node === 'object' ? node.kids : []);

function walk(node, visit) {
  if (!node || typeof node !== 'object') return;
  visit(node);
  for (const c of kids(node)) walk(c, visit);
}

// texts is every string child under `node`, in drawing order.
function texts(node) {
  const out = [];
  const go = (n) => {
    if (typeof n === 'string') out.push(n);
    for (const c of kids(n)) go(c);
  };
  go(node);
  return out;
}

const call = (node, name) => node.calls.find((c) => c[0] === name);
const arg = (node, name) => call(node, name)?.[1];

function byId(root, id) {
  let found = null;
  walk(root, (n) => {
    if (arg(n, 'id') === id) found = found || n;
  });
  expect(found, `no element with id ${id}`).not.toBeNull();
  return found;
}

// svgs is every icon file drawn under `node`, with its tint.
function svgs(node) {
  const out = [];
  walk(node, (n) => {
    if (n.kind === 'svg') out.push([n.arg, arg(n, 'text_color')]);
  });
  return out;
}

function panel(state) {
  const view = new Irrlicht();
  view.init({}, { timer: { every() {} }, spawn() {} });
  if (state) view.state = state;
  return view;
}

const ok = (payload = livePayload()) => ({ phase: 'ok', where: 'localhost:7837', sessions: readSessions(payload) });

beforeEach(() => {
  world.agents = LIVE_AGENTS;
  world.terminals = LIVE_TERMINALS;
  world.theme = THEME;
  world.ran = [];
});

describe('the drawn panel, daemon answering', () => {
  test('groups by the session\'s project in payload order, numbering rows as drawn', () => {
    const list = byId(panel(ok()).render(), 'ir-list');
    const words = texts(list);
    // codex comes first in the payload, so its group and row come first.
    expect(words.filter((w) => ['codex', 'claude'].includes(w))).toEqual(['codex', 'claude']);
    expect(words.filter((w) => /^\d+ sessions?$/.test(w))).toEqual(['1 session', '1 session']);
    expect(texts(byId(list, 'ir-t4'))).toContain('1');
    expect(texts(byId(list, 'ir-t3'))).toContain('2');
    expect(arg(byId(list, 'ir-t4'), 'tooltip')).toMatch(/^1\. herdr-demo · Create DNS resolution notes \| codex · Ready · /);
  });

  test('each row shows its session\'s state in Irrlicht\'s hue, its model and its context bar', () => {
    const root = panel(ok()).render();
    const codex = byId(root, 'ir-t4');
    const claude = byId(root, 'ir-t3');
    // The Codex session is ready: the disc-check glyph in --ready.
    expect(svgs(byId(codex, 'ir-state-t4'))).toEqual([['mods/irrlicht/icons/state-ready.svg', BRAND.ready]]);
    // The Claude session is waiting: two pause bars in --waiting.
    const bars = [];
    walk(byId(claude, 'ir-state-t3'), (n) => {
      if (arg(n, 'bg')) bars.push(arg(n, 'bg'));
    });
    expect(bars).toEqual([BRAND.waiting, BRAND.waiting]);
    expect(texts(codex)).toEqual(expect.arrayContaining(['gpt-6.1-sol', '3.9%', '18.8K']));
    expect(texts(claude)).toEqual(expect.arrayContaining(['opus-5.5', '4.6%', '46.4K']));
    // The fill and the percentage take the pressure hue; the track is the
    // theme's foreground at 8%.
    const fills = [];
    walk(claude, (n) => {
      if (arg(n, 'w') === '4.6%') fills.push(arg(n, 'bg'));
    });
    expect(fills).toEqual([BRAND['pressure-low']]);
    let trackSeen = false;
    walk(claude, (n) => {
      trackSeen = trackSeen || arg(n, 'bg') === track(THEME.foreground);
    });
    expect(trackSeen).toBe(true);
  });

  test('a working session breathes, a busy one shows its subagent count', () => {
    const payload = livePayload();
    payload.groups[1].agents[0] = { ...CLAUDE_SESSION, state: 'working', subagents: { total: 3, working: 2, waiting: 1, ready: 0, error: 0 } };
    const claude = byId(panel(ok(payload)).render(), 'ir-t3');
    const dot = kids(byId(claude, 'ir-state-t3'))[0];
    expect(arg(dot, 'bg')).toBe(BRAND.working);
    expect(call(dot, 'loop')).toEqual(['loop', 'opacity', { from: 1, to: 0.55, period: 1400 }]);
    const badge = byId(claude, 'ir-sub-t3');
    expect(texts(badge)).toEqual(['3']);
    expect(arg(badge, 'bg')).toBe(BRAND.working);
    expect(arg(badge, 'tooltip')).toBe('3 active subagents');
  });

  test('a waiting question and a context near its limit get their notes, on their hue\'s wash', () => {
    const payload = livePayload();
    payload.groups[1].agents[0] = {
      ...CLAUDE_SESSION,
      metrics: { ...CLAUDE_SESSION.metrics, context_utilization_percentage: 86, pressure_level: 'warning', question_headline: 'Run the migration?' },
    };
    const claude = byId(panel(ok(payload)).render(), 'ir-t3');
    expect(texts(byId(claude, 'ir-question-t3'))).toEqual(['Run the migration?']);
    expect(arg(byId(claude, 'ir-question-t3'), 'bg')).toBe(wash(BRAND.waiting));
    expect(texts(byId(claude, 'ir-pressure-t3'))).toEqual(['Switch to a fresh session soon']);
    expect(svgs(byId(claude, 'ir-pressure-t3'))).toEqual([['mods/irrlicht/icons/alert.svg', BRAND['pressure-high']]]);
    expect(texts(claude)).toContain('86.0%');
  });

  test('clicking a row focuses its pane; the header\'s dot is green with the daemon\'s address', () => {
    const root = panel(ok()).render();
    arg(byId(root, 'ir-t3'), 'on_click')();
    expect(world.ran).toEqual([['pane.focus', { terminal: 't3' }]]);
    expect(arg(byId(root, 'ir-connection'), 'bg')).toBe(BRAND['ws-connected']);
    expect(arg(byId(root, 'ir-connection'), 'tooltip')).toBe('Irrlicht at localhost:7837');
    expect(texts(root)).toContain('Irrlicht');
  });

  test('"focus-<n>" focuses row n as drawn, which is not agent n in Herdr\'s order', () => {
    const view = panel(ok());
    view.command('focus-1');
    view.command('focus-2');
    view.command('focus-3');
    view.command('refresh');
    expect(world.ran).toEqual([['pane.focus', { terminal: 't4' }], ['pane.focus', { terminal: 't3' }]]);
  });

  test('clicking a group header folds it, and again unfolds it', () => {
    const view = panel(ok());
    let notified = 0;
    const cx = { notify: () => notified++ };
    arg(byId(view.render(), 'ir-group-0'), 'on_click')({}, cx);
    expect(() => byId(byId(view.render(), 'ir-list'), 'ir-t4')).toThrow();
    expect(svgs(byId(view.render(), 'ir-group-0'))[0][0]).toBe('mods/irrlicht/icons/chevron-right.svg');
    arg(byId(view.render(), 'ir-group-0'), 'on_click')({}, cx);
    expect(texts(byId(view.render(), 'ir-t4'))).toContain('1');
    expect(notified).toBe(2);
  });
});

describe('the drawn panel, daemon not answering', () => {
  test('every agent is in the trailing group, in Herdr\'s order, with Herdr\'s status in Irrlicht\'s palette', () => {
    const root = panel({ phase: 'unreachable', where: 'localhost:7837' }).render();
    const list = byId(root, 'ir-list');
    expect(texts(list).filter((w) => /agent/.test(w))).toEqual(['Other agents', '2 agents']);
    expect(texts(byId(list, 'ir-t3'))).toContain('1');
    // Both agents report "done" to Herdr: Irrlicht's ready glyph.
    expect(svgs(byId(list, 'ir-state-t3'))).toEqual([['mods/irrlicht/icons/state-ready.svg', BRAND.ready]]);
    expect(texts(root)).toContain('Irrlicht daemon not reachable at localhost:7837');
    expect(arg(byId(root, 'ir-connection'), 'bg')).toBe(BRAND['ws-disconnected']);
  });

  test('a panel fault is written on the error wash', () => {
    const root = panel({ phase: 'error', message: 'x is not a function' }).render();
    let washed = null;
    walk(root, (n) => {
      if (texts(n).length === 1 && texts(n)[0] === 'Irrlicht panel error: x is not a function') washed = washed || arg(n, 'bg');
    });
    expect(washed).toBe(wash(BRAND.error));
  });

  test('with no agents the panel says so', () => {
    world.agents = [];
    expect(texts(panel().render())).toContain('No agents yet. Agents show up here when they report their status to Herdr.');
  });
});
