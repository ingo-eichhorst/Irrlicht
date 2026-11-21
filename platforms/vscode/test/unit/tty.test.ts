import { describe, expect, it } from 'vitest';
import { classifyPsFailure, nextRetryDelay, normalizeTty, pairTerminals, parsePsTtyOutput, resolvePsPath } from '../../src/tty';
import type { SessionRecord } from '../../src/sessionStore';

const sess = (id: string, tty: string | undefined, updated_at?: number): SessionRecord => ({
  session_id: id,
  state: 'working',
  updated_at,
  launcher: tty === undefined ? undefined : { tty },
});

type Pairer = typeof pairTerminals;

// The latest-updated_at-wins guard, factored so it can run against the real
// pairing (must pass) and the committed mutant below (must throw).
function checkLatestWins(pair: Pairer): void {
  const old = sess('old', 'ttys001', 10);
  const fresh = sess('fresh', '/dev/ttys001', 20);
  const r = pair([{ key: 't1', tty: '/dev/ttys001' }], [old, fresh]);
  expect(r.get('t1')?.session_id).toBe('fresh');
  const r2 = pair([{ key: 't1', tty: '/dev/ttys001' }], [fresh, old]);
  expect(r2.get('t1')?.session_id).toBe('fresh');
}

describe('normalizeTty', () => {
  it('normalizes the accepted spellings', () => {
    expect(normalizeTty('ttys042')).toBe('/dev/ttys042');
    expect(normalizeTty('/dev/ttys021')).toBe('/dev/ttys021');
    expect(normalizeTty('pts/3')).toBe('/dev/pts/3');
    expect(normalizeTty('  ttys001 \n')).toBe('/dev/ttys001');
  });
  it('returns undefined for "no tty" markers', () => {
    for (const v of [undefined, '', '?', '??', '-', '  ']) expect(normalizeTty(v)).toBeUndefined();
  });
  it('rejects hostile or malformed input', () => {
    for (const v of ['tty s1', '../etc/passwd', '/dev/../etc', 'ttys0;rm', 'tty$1', 'a\tb']) {
      expect(normalizeTty(v)).toBeUndefined();
    }
  });
});

describe('parsePsTtyOutput', () => {
  it('parses macOS output with leading spaces and ?? ttys', () => {
    const m = parsePsTtyOutput('  4242 ttys042\n 4243 ??\n');
    expect([...m.entries()]).toEqual([[4242, '/dev/ttys042']]);
  });
  it('parses Linux output', () => {
    expect(parsePsTtyOutput('4242 pts/3\n').get(4242)).toBe('/dev/pts/3');
  });
  it('ignores blank and garbage lines', () => {
    const m = parsePsTtyOutput('\nnot a line\n 7 ttys001\n');
    expect([...m.keys()]).toEqual([7]);
  });
});

describe('pairTerminals', () => {
  it('matches a terminal to the session on its tty', () => {
    const s = sess('a', '/dev/ttys001');
    const r = pairTerminals([{ key: 't1', tty: '/dev/ttys001' }], [s]);
    expect(r.get('t1')).toBe(s);
  });
  it('omits terminals with no matching session', () => {
    const r = pairTerminals([{ key: 't1', tty: '/dev/ttys009' }], [sess('a', '/dev/ttys001')]);
    expect(r.size).toBe(0);
  });
  it('omits unresolved terminals', () => {
    const r = pairTerminals([{ key: 't1' }], [sess('a', '/dev/ttys001')]);
    expect(r.size).toBe(0);
  });
  it('picks the latest updated_at when sessions share a tty', () => {
    checkLatestWins(pairTerminals);
  });
  // Committed in-language mutant: always keep the first session seen per tty.
  it('mutant: always keeping the first session is caught by the latest-wins check', () => {
    const keepFirst: Pairer = <K>(terminals: Array<{ key: K; tty?: string }>, topLevel: SessionRecord[]) => {
      const byTty = new Map<string, SessionRecord>();
      for (const s of topLevel) {
        const tty = normalizeTty(s.launcher?.tty);
        if (tty && !byTty.has(tty)) byTty.set(tty, s);
      }
      const out = new Map<K, SessionRecord>();
      for (const t of terminals) {
        const tty = normalizeTty(t.tty);
        const s = tty ? byTty.get(tty) : undefined;
        if (s) out.set(t.key, s);
      }
      return out;
    };
    expect(() => checkLatestWins(keepFirst)).toThrow();
  });
  it('treats missing updated_at as 0 and keeps the first on ties', () => {
    const a = sess('a', 'ttys001');
    const b = sess('b', 'ttys001');
    expect(pairTerminals([{ key: 1, tty: '/dev/ttys001' }], [a, b]).get(1)?.session_id).toBe('a');
    const c = sess('c', 'ttys001', 1);
    expect(pairTerminals([{ key: 1, tty: '/dev/ttys001' }], [a, c]).get(1)?.session_id).toBe('c');
  });
  it('ignores sessions without a launcher or tty', () => {
    const r = pairTerminals([{ key: 't1', tty: '/dev/ttys001' }], [sess('a', undefined), sess('b', '??')]);
    expect(r.size).toBe(0);
  });
});

describe('nextRetryDelay', () => {
  it('backs off 0.5s, 1s, 2s then holds at 5s', () => {
    expect([0, 1, 2, 3, 4, 11].map(nextRetryDelay)).toEqual([500, 1000, 2000, 5000, 5000, 5000]);
  });
  it('clamps invalid attempts to the first delay', () => {
    expect(nextRetryDelay(-1)).toBe(500);
    expect(nextRetryDelay(NaN)).toBe(500);
  });
});

describe('nextRetryDelay cap', () => {
  it('stops timed retries once the attempt cap is reached', () => {
    expect(nextRetryDelay(12)).toBeUndefined();
    expect(nextRetryDelay(50)).toBeUndefined();
    expect(nextRetryDelay(Infinity)).toBeUndefined();
  });
});

describe('classifyPsFailure', () => {
  it('treats exit 1 with no output as "no pid matched"', () => {
    expect(classifyPsFailure({ code: 1 }, '', '')).toBe('no-match');
  });
  it('treats spawn failures as errors', () => {
    expect(classifyPsFailure({ code: 'ENOENT' }, '', '')).toBe('error');
    expect(classifyPsFailure({ code: 'EACCES' }, '', '')).toBe('error');
  });
  it('treats stderr text or other exit codes as errors', () => {
    expect(classifyPsFailure({ code: 1 }, '', 'ps: illegal option')).toBe('error');
    expect(classifyPsFailure({ code: 2 }, '', '')).toBe('error');
  });
  it('is ok when there is no error', () => {
    expect(classifyPsFailure(null, '4242 ttys001\n', '')).toBe('ok');
  });
  it('is ok when ps printed output despite a non-zero exit', () => {
    expect(classifyPsFailure({ code: 1 }, '4242 ttys001\n', '')).toBe('ok');
  });
});

describe('resolvePsPath', () => {
  it('prefers /bin/ps, then /usr/bin/ps, else undefined', () => {
    expect(resolvePsPath(() => true)).toBe('/bin/ps');
    expect(resolvePsPath((p) => p === '/usr/bin/ps')).toBe('/usr/bin/ps');
    expect(resolvePsPath(() => false)).toBeUndefined();
  });
});
