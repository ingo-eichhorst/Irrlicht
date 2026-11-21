import { describe, expect, test } from 'vitest';
import { formatTokens, gaugeOf, tokenCount } from '../lib/gauge.js';
import { BRAND } from '../lib/palette.js';
import { readSessions, sessionFacts } from '../lib/sessions.js';
import { CLAUDE_SESSION, CODEX_SESSION } from './fixtures.js';

function factsWith(metrics) {
  return sessionFacts({ session_id: 's', adapter: 'aider', pid: 1, cwd: '/r', metrics });
}

describe('gaugeOf', () => {
  test('the live Claude Code and Codex sessions show their fill to one decimal, as the macOS app does', () => {
    expect(gaugeOf(sessionFacts(CLAUDE_SESSION))).toEqual({
      kind: 'percent', text: '4.6%', fraction: 0.0464, hue: BRAND['pressure-low'], label: '46.4K',
      detail: '4.6% of 1.0M context, safe',
    });
    expect(gaugeOf(sessionFacts(CODEX_SESSION)).text).toBe('3.9%');
  });

  test('each pressure level takes its own pressure token, as SessionMetrics.contextPressureColor does', () => {
    const hue = (pct, level) => gaugeOf(factsWith({ context_utilization_percentage: pct, pressure_level: level, context_window: 200000 })).hue;
    expect(hue(10, 'safe')).toBe(BRAND['pressure-low']);
    expect(hue(65, 'caution')).toBe(BRAND['pressure-medium']);
    expect(hue(85, 'warning')).toBe(BRAND['pressure-high']);
    expect(hue(95, 'critical')).toBe(BRAND['pressure-critical']);
    expect(new Set([hue(10, 'safe'), hue(65, 'caution'), hue(85, 'warning'), hue(95, 'critical')]).size).toBe(4);
  });

  test('the bar\'s label is the Swift row\'s token count', () => {
    const label = (tokens) => gaugeOf(factsWith({ total_tokens: tokens, context_utilization_percentage: 5, pressure_level: 'safe' })).label;
    expect([label(950), label(45000), label(120000), label(1234567)]).toEqual(['950', '45.0K', '120.0K', '1.2M']);
  });

  test('the bar never draws past full', () => {
    expect(gaugeOf(factsWith({ context_utilization_percentage: 130, pressure_level: 'critical' })).fraction).toBe(1);
  });

  test('context_window_unknown shows the raw token count and no percentage, as "used / ?"', () => {
    const g = gaugeOf(factsWith({
      total_tokens: 31000, context_utilization_percentage: 0, pressure_level: 'unknown', context_window_unknown: true,
    }));
    expect(g).toEqual({ kind: 'tokens', text: '31.0K / ?', detail: '31K tokens, context window unknown' });
    expect(JSON.stringify(g)).not.toContain('%');
  });

  test('a window the daemon still reports beside context_window_unknown is printed as the Swift row prints it', () => {
    const text = (window) => gaugeOf(factsWith({ total_tokens: 31000, context_window: window, context_window_unknown: true })).text;
    expect([text(200000), text(1000000), text(512), text(0)]).toEqual(['31.0K / 200K', '31.0K / 1M', '31.0K / 512', '31.0K / ?']);
  });

  test('an unknown window with no tokens yet shows nothing', () => {
    expect(gaugeOf(factsWith({ total_tokens: 0, context_window_unknown: true })).kind).toBe('none');
  });

  test('a session before its first turn (pressure "unknown") shows nothing, not 0%', () => {
    // Codex read this way on the live run before its first answer.
    expect(gaugeOf(factsWith({ total_tokens: 0, context_utilization_percentage: 0, pressure_level: 'unknown' })).kind).toBe('none');
  });

  test('no session, no gauge', () => {
    expect(gaugeOf(undefined).kind).toBe('none');
  });
});

describe('tokenCount', () => {
  // SessionMetrics.formattedTokenCount (SessionState.swift:736-746).
  test.each([
    [0, '—'], [NaN, '—'], [950, '950'], [999, '999'], [1000, '1.0K'], [45000, '45.0K'], [999949, '999.9K'], [1200000, '1.2M'],
  ])('%s → %s', (n, want) => {
    expect(tokenCount(n)).toBe(want);
  });
});

describe('formatTokens', () => {
  test.each([
    [950, '950'], [4200, '4.2K'], [177000, '177K'], [1200000, '1.2M'], [12000000, '12M'], [0, ''], [NaN, ''],
  ])('%s → %s', (n, want) => {
    expect(formatTokens(n)).toBe(want);
  });
});

describe('readSessions', () => {
  test('walks nested groups and leaves children out', () => {
    const got = readSessions({
      groups: [{
        agents: [{ session_id: 'a', pid: 1, children: [{ session_id: 'child', pid: 1 }] }],
        groups: [{ agents: [{ session_id: 'nested', adapter: 'pi', pid: 2, cwd: '/w/' }] }],
      }],
    });
    expect(got.map((s) => [s.id, s.adapter, s.pid, s.cwd])).toEqual([
      ['a', 'claude-code', 1, ''],
      ['nested', 'pi', 2, '/w'],
    ]);
  });

  test('an idle daemon\'s payload ({"groups": null}) is no sessions, not a foreign answer', () => {
    // The daemon encodes an empty dashboard as a nil slice: handlers.go tags
    // `groups` without omitempty, and BuildDashboard (core/domain/session/
    // grouped.go) starts from `var result []*AgentGroup`.
    expect(readSessions({ groups: null, provider_costs: {} })).toEqual([]);
  });

  test('anything but a sessions payload is null', () => {
    expect(readSessions(null)).toBeNull();
    expect(readSessions({})).toBeNull();
    expect(readSessions({ groups: 'x' })).toBeNull();
    expect(readSessions([])).toBeNull();
  });

  test('each session keeps its top-level daemon group, its type, and its place in payload order', () => {
    const got = readSessions({
      groups: [
        { name: 'gastown', type: 'gastown', agents: [{ session_id: 'mayor' }], groups: [{ name: 'rig', agents: [{ session_id: 'polecat' }] }] },
        { name: 'web', agents: [{ session_id: 'a' }, { session_id: 'b' }] },
        { agents: [{ session_id: 'nameless' }] },
      ],
    });
    expect(got.map((s) => [s.id, s.group, s.gastown, s.order])).toEqual([
      ['mayor', 'gastown', true, 0],
      ['polecat', 'gastown', true, 1],
      ['a', 'web', false, 2],
      ['b', 'web', false, 3],
      ['nameless', 'unknown', false, 4],
    ]);
  });

  test('state, branch, active subagents, question and failure are read as the Swift row reads them', () => {
    const [s] = readSessions({ groups: [{ name: 'p', agents: [{
      session_id: 's', state: 'waiting', git_branch: 'main',
      subagents: { total: 5, working: 2, waiting: 1, ready: 2, error: 0 },
      metrics: { question_headline: 'Ship it?', last_assistant_text: 'Shall I ship it now?', session_error: { class: 'quota', message: '' } },
    }] }] });
    expect(s).toMatchObject({
      state: 'waiting', branch: 'main', subagents: 3,
      questionHeadline: 'Ship it?', lastAssistantText: 'Shall I ship it now?', errorMessage: '', errorClass: 'quota',
    });
  });

  test('a pid that is absent, zero or not an integer is no pid', () => {
    const pids = readSessions({ groups: [{ agents: [
      { session_id: 'a' }, { session_id: 'b', pid: 0 }, { session_id: 'c', pid: 1.5 }, { session_id: 'd', pid: '7' },
    ] }] }).map((s) => s.pid);
    expect(pids).toEqual([null, null, null, null]);
  });
});
