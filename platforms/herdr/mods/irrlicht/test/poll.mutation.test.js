// Committed mutants of lib/poll.js (test/mutant.js says how they are
// loaded): the stall guard and the re-lookup backoff. Each mutant breaks one
// rule; the test that rule exists for must then fail.

import { describe, expect, test } from 'vitest';
import { stallIsReportedOnceAfterStallMs } from './contracts.js';
import { HOME_ADDR, livePayload, world } from './fixtures.js';
import { mutantLoader } from './mutant.js';

const load = mutantLoader('irrlicht-herdr-poll-mutant-');
const loadMutant = (name, anchor, replacement) => load(name, 'lib/poll.js', anchor, replacement);

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
