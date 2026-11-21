// Committed mutants of lib/poll.js (AGENTS.md: commit the mutation rather than
// describing it): the stall guard and the re-lookup backoff. Each mutant
// breaks one rule; the test that rule exists for must then fail. Each anchor
// must occur exactly once in lib/poll.js, so a stale anchor fails here
// instead of silently testing the unmutated file.

import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { pathToFileURL } from 'node:url';
import { afterAll, describe, expect, test } from 'vitest';
import { stallIsReportedOnceAfterStallMs } from './contracts.js';
import { HOME_ADDR, livePayload, world } from './fixtures.js';

const LIB = new URL('../lib/', import.meta.url);
const source = (name) => readFileSync(new URL(name, LIB), 'utf8');
const dir = mkdtempSync(join(tmpdir(), 'irrlicht-herdr-poll-mutant-'));
afterAll(() => rmSync(dir, { recursive: true, force: true }));

// loadMutant writes lib/poll.js with `anchor` replaced, next to copies of the
// modules it imports, and loads it.
async function loadMutant(name, anchor, replacement) {
  const original = source('poll.js');
  expect(original.split(anchor).length - 1, `anchor for ${name} must occur exactly once in lib/poll.js`).toBe(1);
  const sub = join(dir, name);
  mkdirSync(sub, { recursive: true });
  for (const dep of ['address.js', 'sessions.js']) writeFileSync(join(sub, dep), source(dep));
  writeFileSync(join(sub, 'poll.js'), original.replace(anchor, replacement));
  return import(pathToFileURL(join(sub, 'poll.js')).href);
}

// A daemon that answered once and then stopped, polled every 3 s for 27 s:
// the address may be looked up again once (the poll after the first
// failure), not on every poll.
async function lookupsWhileDown(poll) {
  const w = world({ env: { HOME: '/home/dev' }, files: { [HOME_ADDR]: { text: '127.0.0.1:7843\n' } } });
  const clock = { now: 0 };
  let up = true;
  const io = {
    ladder: w.io,
    now: () => clock.now,
    fetch: async () => {
      if (!up) throw new Error('refused');
      return { ok: true, json: async () => livePayload() };
    },
  };
  let state = await poll.pollOnce({ phase: 'starting' }, io);
  clock.now += poll.RERESOLVE_MS;
  up = false;
  for (let i = 0; i < 10; i++) {
    state = await poll.pollOnce(state, io);
    clock.now += 3000;
  }
  return w.asked.filter(([k]) => k === 'isRegularFile').length;
}

describe('committed mutants of the poll rules', () => {
  test('the unmutated poll looks the address up twice in that stretch', async () => {
    expect(await lookupsWhileDown(await import('../lib/poll.js'))).toBe(2);
  });

  test('dropping the backoff looks the address up on every failed poll', async () => {
    const poll = await loadMutant(
      'no-backoff',
      "return prev.phase !== 'ok' && now - prev.resolvedAt >= RERESOLVE_MS;",
      "return prev.phase !== 'ok';",
    );
    expect(await lookupsWhileDown(poll)).not.toBe(2);
  });

  test('dropping the stall guard never reports a stall', async () => {
    const poll = await loadMutant(
      'no-stall',
      "if (state.phase === 'stalled' || now - startedAt < STALL_MS) return null;",
      'return null;',
    );
    expect(() => stallIsReportedOnceAfterStallMs(poll.stallState, poll.STALL_MS)).toThrow();
  });
});
