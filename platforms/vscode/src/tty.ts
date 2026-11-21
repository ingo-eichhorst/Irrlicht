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

// Delay before retrying a still-unresolved TTY lookup; attempt counts from 0.
export function nextRetryDelay(attempt: number): number {
  if (!Number.isFinite(attempt) || attempt < 0) return RETRY_DELAYS_MS[0];
  const i = Math.floor(attempt);
  return i < RETRY_DELAYS_MS.length ? RETRY_DELAYS_MS[i] : RETRY_STEADY_MS;
}
