// The matching contracts, as functions taking the match function under test,
// so test/match.test.js runs them against lib/match.js and
// test/match.mutation.test.js against a committed mutant of it.

import { expect } from 'vitest';
import { readSessions } from '../lib/sessions.js';

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

export function session(id, adapter, pid, cwd, percent = 10) {
  return {
    session_id: id, adapter, pid, cwd, state: 'working',
    metrics: { context_utilization_percentage: percent, pressure_level: 'safe', context_window: 1000000 },
  };
}

// The ambiguous pair: two Codex sessions in one repo must never trade numbers.
// Written as one function so test/match.mutation.test.js can run the same
// contract against a mutant of the uniqueness check.
export function ambiguousPairsGetNoGauge(matchFn) {
  const sessionsOf = (list) => readSessions({ groups: [{ agents: list }] });
  const one = herdrAgent('t1', 'codex', { pid: 10, foregroundPid: 11, cwd: '/repo' });
  const two = herdrAgent('t2', 'codex', { pid: 20, foregroundPid: 21, cwd: '/repo' });

  // Two Herdr agents, two sessions, one folder, no pid in common.
  const both = matchFn([one.agent, two.agent], [one.terminal, two.terminal],
    sessionsOf([session('a', 'codex', 12, '/repo', 10), session('b', 'codex', 22, '/repo', 80)]));
  expect(both.size).toBe(0);

  // One Herdr agent, two sessions in its folder (the other runs outside Herdr).
  const lone = matchFn([one.agent], [one.terminal],
    sessionsOf([session('a', 'codex', 12, '/repo'), session('b', 'codex', 22, '/repo')]));
  expect(lone.size).toBe(0);

  // Two Herdr agents in one folder and one session: neither may claim it.
  const shared = matchFn([one.agent, two.agent], [one.terminal, two.terminal],
    sessionsOf([session('a', 'codex', 12, '/repo')]));
  expect(shared.size).toBe(0);
}

// A session a pid already gave to one row is never handed to a second row by
// the folder rule, even when that second row is alone in its folder.
export function claimedSessionsStayWithTheirRow(matchFn) {
  const sessionsOf = (list) => readSessions({ groups: [{ agents: list }] });
  // t1 moved to /other after starting in /repo; t2 runs in /repo, and its own
  // session is not known to the daemon yet.
  const t1 = herdrAgent('t1', 'claude-code', { pid: 10, foregroundPid: 11, cwd: '/other' });
  const t2 = herdrAgent('t2', 'claude-code', { pid: 20, foregroundPid: 21, cwd: '/repo' });
  const got = matchFn([t1.agent, t2.agent], [t1.terminal, t2.terminal],
    sessionsOf([session('s1', 'claude-code', 11, '/repo')]));
  expect(summary(got)).toEqual({ t1: ['s1', 'pid'] });
}


// "Exactly one Herdr agent" counts every agent in the folder, pid-matched
// ones too: t1 runs in /repo but its pid matched a session started in /x, so
// /repo holds two Herdr agents and s2 (an unrelated pid in /repo) goes to
// neither of them.
export function pidMatchedAgentsCountInTheirFolder(matchFn) {
  const sessionsOf = (list) => readSessions({ groups: [{ agents: list }] });
  const t1 = herdrAgent('t1', 'claude-code', { pid: 10, foregroundPid: 11, cwd: '/repo' });
  const t2 = herdrAgent('t2', 'claude-code', { pid: 20, foregroundPid: 21, cwd: '/repo' });
  const got = matchFn([t1.agent, t2.agent], [t1.terminal, t2.terminal],
    sessionsOf([session('s1', 'claude-code', 11, '/x'), session('s2', 'claude-code', 99, '/repo')]));
  expect(summary(got)).toEqual({ t1: ['s1', 'pid'] });
}

// The foreground process is asked first, the terminal's own process second.
export function foregroundPidWinsOverTerminalPid(matchFn) {
  const sessionsOf = (list) => readSessions({ groups: [{ agents: list }] });
  const t1 = herdrAgent('t1', 'pi', { pid: 10, foregroundPid: 11, cwd: '/repo' });
  const got = matchFn([t1.agent], [t1.terminal],
    sessionsOf([session('term', 'pi', 10, '/a'), session('fg', 'pi', 11, '/b')]));
  expect(summary(got)).toEqual({ t1: ['fg', 'pid'] });
}
