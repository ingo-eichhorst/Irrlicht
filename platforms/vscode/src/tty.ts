// Pure TTY helpers: normalize tty spellings and pair terminals with sessions.
import type { SessionRecord } from './sessionStore';

const NO_TTY = new Set(['', '?', '??', '-']);
const SAFE = /^[A-Za-z0-9/._-]+$/;

export function normalizeTty(raw: string | undefined): string | undefined {
  const trimmed = raw?.trim() ?? '';
  if (NO_TTY.has(trimmed)) return undefined;
  const t = trimmed.startsWith('/dev/') ? trimmed.slice('/dev/'.length) : trimmed;
  // SAFE needs at least one character, so a bare '/dev/' is rejected here too (tty.test.ts pins it).
  if (t.includes('..') || !SAFE.test(t)) return undefined;
  return '/dev/' + t;
}

// Parses the output of `ps -o pid=,tty= -p a,b,c`. A pid is in the map iff ps printed
// it (the process exists); its value is the normalized tty, undefined for `??`.
// A pid missing from the map is a dead process.
export function parsePsTtyOutput(out: string): Map<number, string | undefined> {
  const result = new Map<number, string | undefined>();
  for (const line of out.split(/\r?\n/)) {
    const m = /^\s*(\d+)(?:\s+(\S+))?\s*$/.exec(line);
    if (m) result.set(Number(m[1]), normalizeTty(m[2]));
  }
  return result;
}

// Per-terminal pairing state. `dead`: the shell pid was absent from ps, which a
// later lookup cannot change, so the terminal is never retried.
export interface TermState { pid?: number; tty?: string; attempts: number; dead?: boolean }

// A terminal with a live-as-far-as-known shell pid whose TTY has not been found yet.
export function isUnresolved(s: TermState | undefined): boolean {
  return s?.pid !== undefined && !s.tty && !s.dead;
}

// Folds one ps run into a terminal's state.
export function applyPsRows(s: TermState, rows: Map<number, string | undefined>): TermState {
  if (s.pid === undefined || !rows.has(s.pid)) return { ...s, dead: true };
  const tty = rows.get(s.pid);
  return tty ? { ...s, tty } : { ...s, attempts: s.attempts + 1 };
}

// True when `s` should replace `held`: nothing is held yet, or `s` is strictly newer (a tie keeps `held`).
function supersedes(s: SessionRecord, held: SessionRecord | undefined): boolean {
  return held === undefined || (s.updated_at ?? 0) > (held.updated_at ?? 0);
}

// The most recently updated session per tty; sessions without a usable tty are skipped.
function latestByTty(topLevel: SessionRecord[]): Map<string, SessionRecord> {
  const byTty = new Map<string, SessionRecord>();
  for (const s of topLevel) {
    const tty = normalizeTty(s.launcher?.tty);
    if (tty && supersedes(s, byTty.get(tty))) byTty.set(tty, s);
  }
  return byTty;
}

export function pairTerminals<K>(
  terminals: Array<{ key: K; tty?: string }>,
  topLevel: SessionRecord[],
): Map<K, SessionRecord> {
  const byTty = latestByTty(topLevel);
  const result = new Map<K, SessionRecord>();
  for (const t of terminals) {
    const tty = normalizeTty(t.tty);
    const s = tty && byTty.get(tty);
    if (s) result.set(t.key, s);
  }
  return result;
}

const RETRY_DELAYS_MS = [500, 1000, 2000];
const RETRY_STEADY_MS = 5000;
// Timed retries stop at this attempt count (3 fast + 9 steady = ~50s). Only a
// terminal that exists but has no tty yet is retried; opening another terminal
// starts one more pass.
const MAX_RETRY_ATTEMPTS = 12;

// Delay before retrying a still-unresolved TTY lookup; attempt counts from 0.
// Returns undefined once the cap is reached: stop scheduling timed retries.
export function nextRetryDelay(attempt: number): number | undefined {
  if (attempt >= MAX_RETRY_ATTEMPTS) return undefined;
  return attempt < RETRY_DELAYS_MS.length ? RETRY_DELAYS_MS[attempt] : RETRY_STEADY_MS;
}

// True for a failed `ps -p` run that is a real error (spawn failure such as ENOENT,
// stderr text, or an exit code other than 1). Exit 1 with nothing printed anywhere
// is ps's normal answer when none of the pids exist.
export function isPsError(err: { code?: unknown } | null | undefined, stdout: string, stderr: string): boolean {
  if (!err || stdout.trim() !== '') return false;
  return !(err.code === 1 && stderr.trim() === '');
}

const PS_CANDIDATES = ['/bin/ps', '/usr/bin/ps'];

// Resolves ps from fixed, unwritable directories instead of PATH (sonar S4036).
export function resolvePsPath(exists: (p: string) => boolean): string | undefined {
  return PS_CANDIDATES.find(exists);
}
