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

// The layout and rows of the live run's agents for `payload`, built by the
// modules under test: `rows` is lib/rows.js and `layout` lib/layout.js, or
// committed mutants of them.
function laidOut(rows, layout, payload) {
  const sessions = readSessions(payload);
  return layout.panelLayout(rows.panelRows(LIVE_AGENTS, LIVE_TERMINALS, sessions), []);
}

// The live payload with the Claude Code session (group 1) changed by `edit`.
function claudeEdited(edit) {
  const p = livePayload();
  p.groups[1].agents[0] = edit(p.groups[1].agents[0]);
  return p;
}

// signatureMisses lists every daemon field whose change leaves the signature
// as it was, so a poll that changes it would not redraw. Each case changes
// one field of the Claude Code session or its group; the error cases compare
// two failed sessions. The pressure alert is not a field of its own: it is
// derived from the state and the fill, which are both cases here.
export function signatureMisses(rows, layout) {
  const sig = (payload) => rows.signature({ phase: 'ok', where: 'localhost:7837' }, laidOut(rows, layout, payload));
  const claude = (edit) => sig(claudeEdited(edit));
  const metrics = (fields) => claude((s) => ({ ...s, metrics: { ...s.metrics, ...fields } }));
  const failed = (fields) => claude((s) => ({ ...s, state: 'error', metrics: { ...s.metrics, ...fields } }));
  const group = (fields) => {
    const p = livePayload();
    p.groups[1] = { ...p.groups[1], ...fields };
    return sig(p);
  };
  const swapped = livePayload();
  swapped.groups.reverse();
  const base = sig(livePayload());
  const failedBase = failed({});
  const cases = [
    ['state', claude((s) => ({ ...s, state: 'working' })), base],
    ['model', metrics({ model_name: 'claude-sonnet-5-1' }), base],
    ['percent', metrics({ context_utilization_percentage: 7.5 }), base],
    ['pressure', metrics({ pressure_level: 'caution' }), base],
    ['tokens', metrics({ total_tokens: 99000 }), base],
    ['subagents', claude((s) => ({ ...s, subagents: { total: 1, working: 1, waiting: 0, ready: 0, error: 0 } })), base],
    ['branch', claude((s) => ({ ...s, git_branch: 'main' })), base],
    ['question', metrics({ question_headline: 'Run the migration?' }), base],
    ['last assistant text', metrics({ last_assistant_text: 'Shall I run the migration now?' }), base],
    ['error message', failed({ session_error: { message: 'Invalid API key' } }), failedBase],
    ['error class', failed({ session_error: { class: 'quota' } }), failedBase],
    ['group name', group({ name: 'claude-renamed' }), base],
    ['gastown', group({ type: 'gastown' }), base],
    ['group order', sig(swapped), base],
  ];
  return cases.filter(([, changed, reference]) => changed === reference).map(([field]) => field);
}

// Every daemon field a row or a group header draws moves the signature, and a
// session no row shows does not.
export function signatureFollowsEveryDrawnField(rows, layout) {
  expect(signatureMisses(rows, layout), 'fields whose change does not move the signature').toEqual([]);
  const sig = (payload) => rows.signature({ phase: 'ok', where: 'localhost:7837' }, laidOut(rows, layout, payload));
  // The session outside Herdr (group 2) is in no row.
  const outside = livePayload();
  outside.groups[2].agents[0] = { ...outside.groups[2].agents[0], metrics: { context_utilization_percentage: 55, pressure_level: 'safe' } };
  expect(sig(outside), 'a session no row shows must not move the signature').toBe(sig(livePayload()));
}

// `focus-<n>` names row n as drawn: on the live run the codex group comes
// first, so focus-1 is Codex's terminal (t4), although Herdr lists t3 first.
export function focusFollowsTheDrawnOrder(rows, layout) {
  const drawn = laidOut(rows, layout, livePayload()).rows;
  expect(['focus-1', 'focus-2', 'focus-3'].map((c) => rows.focusAddress(c, drawn)), 'the agents focus-1..3 name').toEqual(['t4', 't3', null]);
}

// A Gas Town group is marked as one in the layout (GroupView.swift:80-83
// draws its ⛽), and a project's group is not.
export function gastownGroupsAreMarked(rows, layout) {
  const payload = livePayload();
  payload.groups[1] = { ...payload.groups[1], name: 'gastown', type: 'gastown' };
  const marks = laidOut(rows, layout, payload).groups.map((g) => [g.name, g.gastown]);
  expect(marks, 'which groups are marked Gas Town').toEqual([['codex', false], ['gastown', true]]);
}

// The row's tooltip names the session's git branch, which the Swift row draws
// as a column (SessionRowView.swift:394-400) and this one has no room for.
export function tooltipNamesTheBranch(rows) {
  const payload = claudeEdited((s) => ({ ...s, git_branch: 'feat/x' }));
  const [claude] = rows.panelRows(LIVE_AGENTS, LIVE_TERMINALS, readSessions(payload));
  expect(claude.tooltip, 'the tooltip of a session on a branch').toContain(' · branch feat/x · ');
}

// The pressure alert fires at the Swift app's default threshold, 80%
// (ContextPressureThreshold.swift:16), written here as the number rather than
// read from the module under test, so a changed constant cannot pass.
export function pressureAlertFiresAtEighty(rowNotes) {
  const alerts = (percent) => rowNotes({ state: 'working', percent }).filter((n) => n.kind === 'pressure').length;
  expect([alerts(79.9), alerts(80)], 'pressure alerts at 79.9% and at 80%').toEqual([0, 1]);
}

// Each state's hue is its own design-system token: the four states' tokens,
// and the grey of --cancelled for a state this panel cannot read.
export function stateHuesAreTheirTokens(stateHue, brand) {
  expect(stateHue, 'the state hues').toEqual({
    working: brand.working,
    waiting: brand.waiting,
    ready: brand.ready,
    error: brand.error,
    unknown: brand.cancelled,
  });
}

// The repository around this mod, for the checks that read its other files.
// They run only inside this repository (tools/preflight.sh's Herdr gate
// triggers on those files too).
const REPO = new URL('../../../../../', import.meta.url);
export const DESIGN_CSS = new URL('tools/irrlicht-design-system/colors_and_type.css', REPO);
export const SESSION_GO = new URL('core/domain/session/session.go', REPO);

// declaration is the `--name: value` a `;`-separated chunk of CSS ends with,
// as [name, value], or null. Read by hand rather than with one regex over the
// file, whose overlapping quantifiers could backtrack super-linearly
// (SonarCloud javascript:S8786); on colors_and_type.css both read the same 76
// tokens in the same order with the same values (compared once by a scratch
// script, not committed, when this replaced the regex).
const TOKEN_NAME = /^[a-z0-9-]+$/i;

function declaration(chunk) {
  const line = chunk.slice(chunk.lastIndexOf('\n') + 1).trim();
  const colon = line.indexOf(':');
  if (!line.startsWith('--') || colon < 0) return null;
  const name = line.slice(2, colon).trim();
  return TOKEN_NAME.test(name) ? [name, line.slice(colon + 1).trim()] : null;
}

// cssTokens is the `--name: value;` declarations of the design system's CSS,
// the first of each name. It fails rather than answer an empty map, so a file
// this cannot read never passes for one that agrees.
export function cssTokens(css) {
  const tokens = new Map();
  for (const found of css.split(';').map(declaration)) {
    if (found && !tokens.has(found[0])) tokens.set(found[0], found[1]);
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
    const value = new RegExp(String.raw`^\s*${ident}\s*=\s*"([a-z_]+)"`, 'm').exec(goSource);
    expect(value, `${ident} has no string value in session.go`).not.toBeNull();
    return value[1];
  });
  expect(names.length, 'canonicalStates parsed to fewer than four states').toBeGreaterThan(3);
  return names;
}

// coversCanonicalStates: `map` is keyed by exactly the daemon's states plus
// `unknown`, the fallback for a state this panel cannot read.
export function coversCanonicalStates(map, goSource) {
  const sorted = (names) => names.sort((a, b) => a.localeCompare(b));
  expect(sorted(Object.keys(map)), 'the map is keyed by the daemon\'s states plus unknown').toEqual(sorted([...canonicalStates(goSource), 'unknown']));
}
