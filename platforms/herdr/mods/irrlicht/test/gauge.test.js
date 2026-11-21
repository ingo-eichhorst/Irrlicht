import { describe, expect, test } from 'vitest';
import { formatTokens, gaugeOf } from '../lib/gauge.js';
import { readSessions, sessionFacts } from '../lib/sessions.js';
import { CLAUDE_SESSION, CODEX_SESSION } from './fixtures.js';

function factsWith(metrics) {
  return sessionFacts({ session_id: 's', adapter: 'aider', pid: 1, cwd: '/r', metrics });
}

describe('gaugeOf', () => {
  test('the live Claude Code and Codex sessions show their fill to one decimal, as the macOS app does', () => {
    expect(gaugeOf(sessionFacts(CLAUDE_SESSION))).toEqual({
      kind: 'percent', text: '4.6%', fraction: 0.0464, color: 'idle',
      detail: '4.6% of 1.0M context, safe',
    });
    expect(gaugeOf(sessionFacts(CODEX_SESSION)).text).toBe('3.9%');
  });

  test('each pressure level takes its theme colour', () => {
    const color = (pct, level) => gaugeOf(factsWith({ context_utilization_percentage: pct, pressure_level: level, context_window: 200000 })).color;
    expect(color(10, 'safe')).toBe('idle');
    expect(color(65, 'caution')).toBe('working');
    expect(color(85, 'warning')).toBe('blocked');
    expect(color(95, 'critical')).toBe('blocked');
  });

  test('the bar never draws past full', () => {
    expect(gaugeOf(factsWith({ context_utilization_percentage: 130, pressure_level: 'critical' })).fraction).toBe(1);
  });

  test('context_window_unknown shows the raw token count and no percentage', () => {
    const g = gaugeOf(factsWith({
      total_tokens: 31000, context_utilization_percentage: 0, pressure_level: 'unknown', context_window_unknown: true,
    }));
    expect(g).toEqual({ kind: 'tokens', text: '31K', detail: '31K tokens, context window unknown' });
    expect(JSON.stringify(g)).not.toContain('%');
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

  test('anything but a sessions payload is null', () => {
    expect(readSessions(null)).toBeNull();
    expect(readSessions({})).toBeNull();
    expect(readSessions({ groups: 'x' })).toBeNull();
    expect(readSessions([])).toBeNull();
  });

  test('a pid that is absent, zero or not an integer is no pid', () => {
    const pids = readSessions({ groups: [{ agents: [
      { session_id: 'a' }, { session_id: 'b', pid: 0 }, { session_id: 'c', pid: 1.5 }, { session_id: 'd', pid: '7' },
    ] }] }).map((s) => s.pid);
    expect(pids).toEqual([null, null, null, null]);
  });
});
