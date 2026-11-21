// Pure TTY helpers: normalize tty spellings and pair terminals with sessions.
import type { SessionRecord } from './sessionStore';

const NO_TTY = new Set(['', '?', '??', '-']);
const SAFE = /^[A-Za-z0-9/._-]+$/;

export function normalizeTty(raw: string | undefined): string | undefined {
  if (raw === undefined) return undefined;
  let t = raw.trim();
  if (NO_TTY.has(t)) return undefined;
  if (t.startsWith('/dev/')) t = t.slice('/dev/'.length);
  if (t === '' || t.includes('..') || !SAFE.test(t)) return undefined;
  return '/dev/' + t;
}

// Parses the output of `ps -o pid=,tty= -p a,b,c`.
export function parsePsTtyOutput(out: string): Map<number, string> {
  const result = new Map<number, string>();
  for (const line of out.split(/\r?\n/)) {
    const m = /^\s*(\d+)\s+(\S+)\s*$/.exec(line);
    if (!m) continue;
    const tty = normalizeTty(m[2]);
    if (tty) result.set(Number(m[1]), tty);
  }
  return result;
}

export function pairTerminals<K>(
  terminals: Array<{ key: K; tty?: string }>,
  topLevel: SessionRecord[],
): Map<K, SessionRecord> {
  const byTty = new Map<string, SessionRecord>();
  for (const s of topLevel) {
    const tty = normalizeTty(s.launcher?.tty);
    if (!tty) continue;
    const cur = byTty.get(tty);
    if (!cur || (s.updated_at ?? 0) > (cur.updated_at ?? 0)) byTty.set(tty, s);
  }
  const result = new Map<K, SessionRecord>();
  for (const t of terminals) {
    const tty = normalizeTty(t.tty);
    const s = tty ? byTty.get(tty) : undefined;
    if (s) result.set(t.key, s);
  }
  return result;
}

const RETRY_DELAYS_MS = [500, 1000, 2000];
const RETRY_STEADY_MS = 5000;
// Timed retries stop at this attempt count (3 fast + 9 steady = ~50s); a terminal
// open/close or a store change may still trigger one more pass afterwards.
export const MAX_RETRY_ATTEMPTS = 12;

// Delay before retrying a still-unresolved TTY lookup; attempt counts from 0.
// Returns undefined once the cap is reached: stop scheduling timed retries.
export function nextRetryDelay(attempt: number): number | undefined {
  if (attempt >= MAX_RETRY_ATTEMPTS) return undefined;
  if (!Number.isFinite(attempt) || attempt < 0) return RETRY_DELAYS_MS[0];
  const i = Math.floor(attempt);
  return i < RETRY_DELAYS_MS.length ? RETRY_DELAYS_MS[i] : RETRY_STEADY_MS;
}

// Classifies a finished `ps -p` run. "no-match" is ps's normal answer when none of
// the pids exist (exit 1, nothing printed anywhere); anything else that failed is a
// real error (spawn failure such as ENOENT, stderr text, or another exit code).
export function classifyPsFailure(
  err: { code?: unknown } | null | undefined,
  stdout: string,
  stderr: string,
): 'ok' | 'no-match' | 'error' {
  if (!err || stdout.trim() !== '') return 'ok';
  if (err.code === 1 && stderr.trim() === '') return 'no-match';
  return 'error';
}

const PS_CANDIDATES = ['/bin/ps', '/usr/bin/ps'];

// Resolves ps from fixed, unwritable directories instead of PATH (sonar S4036).
export function resolvePsPath(exists: (p: string) => boolean): string | undefined {
  return PS_CANDIDATES.find(exists);
}
