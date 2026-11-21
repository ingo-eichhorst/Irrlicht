// The matching, stall, signature, palette and state-vocabulary contracts, as
// functions taking the code under test, so the *.test.js files run them
// against lib/ and the *.mutation.test.js files against committed mutants of
// it.

import { expect } from 'vitest';
import { readSessions } from '../lib/sessions.js';
import { LIVE_AGENTS, LIVE_TERMINALS, livePayload } from './fixtures.js';

export function summary(matches) {
  return Object.fromEntries([...matches].map(([address, m]) => [address, [m.session.id, m.via]]));
}

// A Herdr agent and its terminal in a folder, for the synthetic cases.
export function herdrAgent(address, app, { pid, foregroundPid, cwd }) {
  return {
    agent: { address, app, status: 'working', space_name: 's', tab_name: '1', title: '' },
    terminal: { address, pid, cwd, foreground: { pid: foregroundPid, cwd } },
  };
}

// sessionsOf is a sessions payload holding `list`, as the panel reads it.
export const sessionsOf = (list) => readSessions({ groups: [{ agents: list }] });

// matchHerdr runs `matchFn` over herdrAgent() pairs and raw sessions.
export function matchHerdr(matchFn, herdr, sessions) {
  return matchFn(herdr.map((h) => h.agent), herdr.map((h) => h.terminal), sessionsOf(sessions));
}

export function session(id, adapter, pid, cwd) {
  return {
    session_id: id, adapter, pid, cwd, state: 'working',
    metrics: { context_utilization_percentage: 10, pressure_level: 'safe', context_window: 1000000 },
  };
}

// The ambiguous pair: two Codex sessions in one repo must never trade numbers.
// Written as one function so test/match.mutation.test.js can run the same
// contract against a mutant of the uniqueness check.
export function ambiguousPairsGetNoGauge(matchFn) {
  const one = herdrAgent('t1', 'codex', { pid: 10, foregroundPid: 11, cwd: '/repo' });
  const two = herdrAgent('t2', 'codex', { pid: 20, foregroundPid: 21, cwd: '/repo' });

  // Two Herdr agents, two sessions, one folder, no pid in common.
  const both = matchHerdr(matchFn, [one, two],
    [session('a', 'codex', 12, '/repo'), session('b', 'codex', 22, '/repo')]);
  expect(both.size).toBe(0);

  // One Herdr agent, two sessions in its folder (the other runs outside Herdr).
  const lone = matchHerdr(matchFn, [one], [session('a', 'codex', 12, '/repo'), session('b', 'codex', 22, '/repo')]);
  expect(lone.size).toBe(0);

  // Two Herdr agents in one folder and one session: neither may claim it.
  const shared = matchHerdr(matchFn, [one, two], [session('a', 'codex', 12, '/repo')]);
  expect(shared.size).toBe(0);
}

// A session a pid already gave to one row is never handed to a second row by
// the folder rule, even when that second row is alone in its folder.
export function claimedSessionsStayWithTheirRow(matchFn) {
  // t1 moved to /other after starting in /repo; t2 runs in /repo, and its own
  // session is not known to the daemon yet.
  const t1 = herdrAgent('t1', 'claude-code', { pid: 10, foregroundPid: 11, cwd: '/other' });
  const t2 = herdrAgent('t2', 'claude-code', { pid: 20, foregroundPid: 21, cwd: '/repo' });
  const got = matchHerdr(matchFn, [t1, t2], [session('s1', 'claude-code', 11, '/repo')]);
  expect(summary(got)).toEqual({ t1: ['s1', 'pid'] });
}


// "Exactly one Herdr agent" counts every agent in the folder, pid-matched
// ones too: t1 runs in /repo but its pid matched a session started in /x, so
// /repo holds two Herdr agents and s2 (an unrelated pid in /repo) goes to
// neither of them.
export function pidMatchedAgentsCountInTheirFolder(matchFn) {
  const t1 = herdrAgent('t1', 'claude-code', { pid: 10, foregroundPid: 11, cwd: '/repo' });
  const t2 = herdrAgent('t2', 'claude-code', { pid: 20, foregroundPid: 21, cwd: '/repo' });
  const got = matchHerdr(matchFn, [t1, t2],
    [session('s1', 'claude-code', 11, '/x'), session('s2', 'claude-code', 99, '/repo')]);
  expect(summary(got)).toEqual({ t1: ['s1', 'pid'] });
}

// The foreground process is asked first, the terminal's own process second.
export function foregroundPidWinsOverTerminalPid(matchFn) {
  const t1 = herdrAgent('t1', 'pi', { pid: 10, foregroundPid: 11, cwd: '/repo' });
  const got = matchHerdr(matchFn, [t1], [session('term', 'pi', 10, '/a'), session('fg', 'pi', 11, '/b')]);
  expect(summary(got)).toEqual({ t1: ['fg', 'pid'] });
}

// An agent on another machine ("vps/t7") is never matched: its pid and folder
// are that machine's, and the daemon only sees this one. Here the remote
// agent's foreground pid and folder both equal a local session's.
export function remoteAgentsAreNeverMatched(matchFn) {
  const remote = herdrAgent('vps/t7', 'claude-code', { pid: 10, foregroundPid: 11, cwd: '/repo' });
  expect(matchHerdr(matchFn, [remote], [session('local', 'claude-code', 11, '/repo')]).size).toBe(0);
}

// A request in flight shows nothing new before STALL_MS, then one stalled
// state that keeps the address and drops the numbers, and not again on later
// ticks.
export function stallIsReportedOnceAfterStallMs(stallStateFn, stallMs) {
  const ok = { phase: 'ok', where: 'localhost:7843', sessions: [] };
  expect(stallStateFn(ok, 1000, 1000 + stallMs - 1)).toBeNull();
  expect(stallStateFn(ok, 1000, 1000 + stallMs)).toEqual({ phase: 'stalled', where: 'localhost:7843' });
  expect(stallStateFn({ phase: 'stalled', where: 'localhost:7843' }, 0, 10 * stallMs)).toBeNull();
}

// Every daemon field a row or a group header draws moves the signature, so a
// poll that changes one redraws the panel; a session no row shows does not.
// `rows` is lib/rows.js (signature, panelRows) and `layout` lib/layout.js
// (panelLayout), or committed mutants of them.
export function signatureFollowsEveryDrawnField(rows, layout) {
  const sig = (payload) => {
    const sessions = readSessions(payload);
    const state = { phase: 'ok', where: 'localhost:7837', sessions };
    return rows.signature(state, layout.panelLayout(rows.panelRows(LIVE_AGENTS, LIVE_TERMINALS, sessions), []));
  };
  // The Claude Code session (group 1 of the live payload), changed by `edit`.
  const claude = (edit) => {
    const p = livePayload();
    p.groups[1].agents[0] = edit(p.groups[1].agents[0]);
    return sig(p);
  };
  const metrics = (fields) => claude((s) => ({ ...s, metrics: { ...s.metrics, ...fields } }));
  const failed = (fields) => claude((s) => ({ ...s, state: 'error', metrics: { ...s.metrics, ...fields } }));
  const renamed = livePayload();
  renamed.groups[1].name = 'claude-renamed';
  const swapped = livePayload();
  swapped.groups.reverse();
  const base = sig(livePayload());
  const moved = {
    state: claude((s) => ({ ...s, state: 'working' })),
    model: metrics({ model_name: 'claude-sonnet-5-1' }),
    percent: metrics({ context_utilization_percentage: 7.5 }),
    pressure: metrics({ pressure_level: 'caution' }),
    tokens: metrics({ total_tokens: 99000 }),
    subagents: claude((s) => ({ ...s, subagents: { total: 1, working: 1, waiting: 0, ready: 0, error: 0 } })),
    question: metrics({ question_headline: 'Run the migration?' }),
    'group name': sig(renamed),
    'group order': sig(swapped),
  };
  for (const [field, got] of Object.entries(moved)) expect(got, `a change of ${field} must move the signature`).not.toBe(base);
  expect(failed({ session_error: { message: 'quota' } }), 'the error line\'s text must move the signature')
    .not.toBe(failed({}));
  // The session outside Herdr (group 2) is in no row.
  const outside = livePayload();
  outside.groups[2].agents[0] = { ...outside.groups[2].agents[0], metrics: { context_utilization_percentage: 55, pressure_level: 'safe' } };
  expect(sig(outside)).toBe(base);
}

// The repository around this mod, for the checks that read its other files.
// They run only inside this repository (tools/preflight.sh's Herdr gate
// triggers on those files too).
const REPO = new URL('../../../../../', import.meta.url);
export const DESIGN_CSS = new URL('tools/irrlicht-design-system/colors_and_type.css', REPO);
export const SESSION_GO = new URL('core/domain/session/session.go', REPO);

// cssTokens is the `--name: value;` declarations of the design system's CSS,
// the first of each name. It fails rather than answer an empty map, so a file
// this cannot read never passes for one that agrees.
export function cssTokens(css) {
  const tokens = new Map();
  for (const m of css.matchAll(/--([a-z0-9-]+)\s*:\s*([^;]+);/gi)) {
    if (!tokens.has(m[1])) tokens.set(m[1], m[2].trim());
  }
  expect(tokens.size, 'no --token declarations were read from colors_and_type.css').toBeGreaterThan(10);
  return tokens;
}

// brandMatchesCss: every hue in `brand` (lib/palette.js BRAND) is the CSS
// token of the same name, and a key naming no token fails, never skips.
export function brandMatchesCss(brand, css) {
  const tokens = cssTokens(css);
  const keys = Object.keys(brand);
  expect(keys.length, 'the palette names no hue').toBeGreaterThan(0);
  for (const key of keys) {
    expect(tokens.has(key), `--${key} is not a token in colors_and_type.css`).toBe(true);
    expect(brand[key].toUpperCase(), `--${key}`).toBe(tokens.get(key).toUpperCase());
  }
}

// canonicalStates reads the daemon's state vocabulary as
// tools/state-vocabulary-lint.sh does: the identifiers in session.go's
// `canonicalStates = []string{...}`, each resolved to its string. It fails
// when the literal or a value cannot be found.
export function canonicalStates(goSource) {
  const literal = /canonicalStates\s*=\s*\[\]string\{([^}]*)\}/.exec(goSource);
  expect(literal, 'no canonicalStates literal in session.go').not.toBeNull();
  const names = literal[1].split(',').map((s) => s.trim()).filter(Boolean).map((ident) => {
    const value = new RegExp(`^\\s*${ident}\\s*=\\s*"([a-z_]+)"`, 'm').exec(goSource);
    expect(value, `${ident} has no string value in session.go`).not.toBeNull();
    return value[1];
  });
  expect(names.length, 'canonicalStates parsed to fewer than four states').toBeGreaterThan(3);
  return names;
}

// coversCanonicalStates: `map` is keyed by exactly the daemon's states plus
// `unknown`, the fallback for a state this panel cannot read.
export function coversCanonicalStates(map, goSource) {
  expect(Object.keys(map).sort()).toEqual([...canonicalStates(goSource), 'unknown'].sort());
}
