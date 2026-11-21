import { describe, expect, test } from 'vitest';
import { matchAgents } from '../lib/match.js';
import { readSessions } from '../lib/sessions.js';
import {
  ambiguousPairsGetNoGauge, claimedSessionsStayWithTheirRow, herdrAgent, session, summary,
} from './contracts.js';
import {
  CLAUDE_SESSION, CODEX_SESSION, LIVE_AGENTS, LIVE_TERMINALS, OTHER_SESSION, livePayload,
} from './fixtures.js';

const facts = (payload) => readSessions(payload);

function run(herdr, sessions) {
  return summary(matchAgents(herdr.map((h) => h.agent), herdr.map((h) => h.terminal), facts({ groups: [{ agents: sessions }] })));
}

describe('the live run (Herdr 0.1.16, Claude Code 2.1.330, Codex 0.163.0)', () => {
  test('Claude Code is matched by its foreground pid, Codex by its folder', () => {
    expect(summary(matchAgents(LIVE_AGENTS, LIVE_TERMINALS, facts(livePayload())))).toEqual({
      t3: [CLAUDE_SESSION.session_id, 'pid'],
      t4: [CODEX_SESSION.session_id, 'folder'],
    });
  });

  test('neither of Codex\'s Herdr pids is its session pid', () => {
    const t4 = LIVE_TERMINALS.find((t) => t.address === 't4');
    expect([t4.foreground.pid, t4.pid]).not.toContain(CODEX_SESSION.pid);
  });

  test('a session outside Herdr and a subagent child match no row', () => {
    const matched = [...matchAgents(LIVE_AGENTS, LIVE_TERMINALS, facts(livePayload())).values()].map((m) => m.session.id);
    expect(matched).not.toContain(OTHER_SESSION.session_id);
    expect(matched).not.toContain(OTHER_SESSION.children[0].session_id);
  });
});

describe('pid', () => {
  test('foreground.pid matches', () => {
    expect(run([herdrAgent('t1', 'claude-code', { pid: 10, foregroundPid: 11, cwd: '/r' })], [session('s', 'claude-code', 11, '/elsewhere')]))
      .toEqual({ t1: ['s', 'pid'] });
  });

  test('the terminal\'s own pid matches when the foreground does not', () => {
    expect(run([herdrAgent('t1', 'pi', { pid: 10, foregroundPid: 11, cwd: '/r' })], [session('s', 'pi', 10, '/elsewhere')]))
      .toEqual({ t1: ['s', 'pid'] });
  });

  test('a pid decides even when another session shares the folder', () => {
    const herdr = [herdrAgent('t1', 'claude-code', { pid: 10, foregroundPid: 11, cwd: '/r' })];
    expect(run(herdr, [session('mine', 'claude-code', 11, '/r'), session('other', 'claude-code', 99, '/r')]))
      .toEqual({ t1: ['mine', 'pid'] });
  });

  test('two sessions on one pid are ambiguous: no gauge, and no folder guess', () => {
    const herdr = [herdrAgent('t1', 'claude-code', { pid: 10, foregroundPid: 11, cwd: '/r' })];
    expect(run(herdr, [session('a', 'claude-code', 11, '/r'), session('b', 'claude-code', 11, '/x')])).toEqual({});
  });
});

describe('folder (agent name + cwd), for agents no pid decided', () => {
  test('one agent and one session sharing the pair match', () => {
    expect(run([herdrAgent('t1', 'codex', { pid: 10, foregroundPid: 11, cwd: '/r' })], [session('s', 'codex', 12, '/r')]))
      .toEqual({ t1: ['s', 'folder'] });
  });

  test('trailing slashes do not split a pair', () => {
    expect(run([herdrAgent('t1', 'codex', { pid: 10, foregroundPid: 11, cwd: '/r/' })], [session('s', 'codex', 12, '/r')]))
      .toEqual({ t1: ['s', 'folder'] });
  });

  test('a different agent name in the same folder is no match', () => {
    expect(run([herdrAgent('t1', 'codex', { pid: 10, foregroundPid: 11, cwd: '/r' })], [session('s', 'claude-code', 12, '/r')]))
      .toEqual({});
  });

  test('an empty adapter is Claude Code', () => {
    expect(run([herdrAgent('t1', 'claude-code', { pid: 10, foregroundPid: 11, cwd: '/r' })], [session('s', '', 12, '/r')]))
      .toEqual({ t1: ['s', 'folder'] });
  });

  test('an agent with no pid and no folder gets nothing', () => {
    const h = herdrAgent('t1', 'codex', { pid: undefined, foregroundPid: undefined, cwd: '' });
    expect(run([h], [session('s', 'codex', 12, '/r')])).toEqual({});
  });
});

describe('never trade numbers', () => {
  test('ambiguous pairs get no gauge', () => {
    ambiguousPairsGetNoGauge(matchAgents);
  });

  test('a pid-claimed session stays with its row', () => {
    claimedSessionsStayWithTheirRow(matchAgents);
  });
});
