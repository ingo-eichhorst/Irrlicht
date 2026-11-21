import { describe, expect, test } from 'vitest';
import { RERESOLVE_MS, STALL_MS, createPoller, pollOnce, stallState } from '../lib/poll.js';
import { focusAddress, panelRows, signature, statusLine } from '../lib/rows.js';
import { stallIsReportedOnceAfterStallMs } from './contracts.js';
import { HOME_ADDR, LIVE_AGENTS, LIVE_TERMINALS, livePayload, world } from './fixtures.js';

// io for pollOnce and createPoller: an addr file naming `port`, a fetch that
// answers `respond(url)`, and a clock the test moves. The ladder's file
// checks (world()) and every fetch are recorded.
function io({ port = 7837, respond }) {
  const w = world({ env: { HOME: '/home/dev' }, files: { [HOME_ADDR]: { text: `127.0.0.1:${port}\n` } } });
  const clock = { now: 1_000_000 };
  const fetches = [];
  return {
    clock,
    fetches,
    lookups: () => w.asked.filter(([k]) => k === 'isRegularFile').length,
    io: {
      ladder: w.io,
      fetch: async (url) => { fetches.push(url); return respond(url); },
      now: () => clock.now,
    },
  };
}

const answer = (payload, { status = 200 } = {}) => ({
  ok: status >= 200 && status < 300, status, json: async () => payload,
});

describe('pollOnce', () => {
  test('a daemon that answers: its sessions, at the address the ladder found', async () => {
    const w = io({ port: 7843, respond: () => answer(livePayload()) });
    const got = await pollOnce({ phase: 'starting' }, w.io);
    expect(got.phase).toBe('ok');
    expect(got.where).toBe('localhost:7843');
    expect(got.sessions.map((s) => s.pid)).toEqual([46690, 46222, 70001]);
    expect(w.fetches).toEqual(['http://localhost:7843/api/v1/sessions']);
  });

  test('an idle daemon (no session at all) is "ok" with no sessions', async () => {
    const w = io({ respond: () => answer({ groups: null, provider_costs: {} }) });
    expect(await pollOnce({ phase: 'starting' }, w.io)).toMatchObject({ phase: 'ok', sessions: [] });
  });

  test('a refused connection is "unreachable" and never throws', async () => {
    const w = io({ respond: () => { throw new Error('error sending request for url'); } });
    expect(await pollOnce({ phase: 'starting' }, w.io)).toMatchObject({ phase: 'unreachable', where: 'localhost:7837' });
  });

  test('something else on the port (a 404, a non-JSON body, another JSON shape) is "unexpected"', async () => {
    const notFound = io({ respond: () => answer({}, { status: 404 }) });
    expect((await pollOnce({}, notFound.io)).phase).toBe('unexpected');
    const notJson = io({ respond: () => ({ ok: true, status: 200, json: async () => { throw new SyntaxError('x'); } }) });
    expect((await pollOnce({}, notJson.io)).phase).toBe('unexpected');
    const otherShape = io({ respond: () => answer({ hello: 'world' }) });
    expect((await pollOnce({}, otherShape.io)).phase).toBe('unexpected');
  });

  test('the address is kept while the daemon answers', async () => {
    const w = io({ port: 7843, respond: () => answer(livePayload()) });
    let state = await pollOnce({ phase: 'starting' }, w.io);
    for (let i = 0; i < 20; i++) {
      w.clock.now += 3000;
      state = await pollOnce(state, w.io);
    }
    expect(w.lookups()).toBe(1);
  });

  test('once it stops answering, the address is looked up again at most every RERESOLVE_MS', async () => {
    let up = true;
    const w = io({ port: 7843, respond: () => { if (up) return answer(livePayload()); throw new Error('refused'); } });
    let state = await pollOnce({ phase: 'starting' }, w.io);
    w.clock.now += RERESOLVE_MS; // a long healthy stretch
    up = false;
    state = await pollOnce(state, w.io); // the poll that fails asked the kept address
    expect(state.phase).toBe('unreachable');
    expect(w.lookups()).toBe(1);
    w.clock.now += 3000;
    state = await pollOnce(state, w.io); // the next one looks again: the last lookup is old
    expect(w.lookups()).toBe(2);
    for (let i = 0; i < 9; i++) { // 27 s of failed polls ask the same address
      w.clock.now += 3000;
      state = await pollOnce(state, w.io);
    }
    expect(w.lookups()).toBe(2);
    expect(w.fetches.length).toBe(12);
    w.clock.now += 3000; // 30 s since the last lookup
    state = await pollOnce(state, w.io);
    expect(w.lookups()).toBe(3);
  });

  test('a fault in the panel itself propagates, so it is never shown as a stopped daemon', async () => {
    const w = io({ respond: () => answer(livePayload()) });
    w.io.ladder = null;
    await expect(pollOnce({ phase: 'starting' }, w.io)).rejects.toThrow();
  });
});

describe('stallState', () => {
  const ok = { phase: 'ok', where: 'localhost:7843', sessions: [] };

  test('reports a stall once, from STALL_MS on', () => {
    stallIsReportedOnceAfterStallMs(stallState, STALL_MS);
  });

  test('a request in flight for less than STALL_MS keeps what is shown', () => {
    expect(stallState(ok, 1000, 1000 + STALL_MS - 1)).toBeNull();
  });

  test('from STALL_MS on, the panel says the daemon is not answering, and drops the old numbers', () => {
    const got = stallState(ok, 1000, 1000 + STALL_MS);
    expect(got).toEqual({ phase: 'stalled', where: 'localhost:7843' });
    expect(statusLine(got).text).toBe('Irrlicht daemon at localhost:7843 is not answering');
  });

  test('a stall is reported once, not again on every tick', () => {
    expect(stallState({ phase: 'stalled', where: 'localhost:7843' }, 0, 10 * STALL_MS)).toBeNull();
  });

  test('a first request that hangs, before any address was shown, still reads plainly', () => {
    expect(statusLine(stallState({ phase: 'starting' }, 0, STALL_MS)).text).toBe('Irrlicht daemon is not answering');
  });
});

describe('createPoller', () => {
  // A fetch whose answers the test releases one at a time.
  function held() {
    const pending = [];
    return {
      pending,
      respond: () => new Promise((resolve) => pending.push(resolve)),
      release: (value) => pending.shift()(value),
    };
  }

  test('sends no second request while one is in flight, and reports a stall once', async () => {
    const h = held();
    const w = io({ respond: h.respond });
    const poller = createPoller(w.io);
    const first = poller.step();
    expect(first.request).toBeInstanceOf(Promise);
    await Promise.resolve();
    w.clock.now += 3000;
    expect(poller.step()).toEqual({ kind: 'idle' });
    w.clock.now += STALL_MS;
    expect(poller.step()).toMatchObject({ kind: 'stalled', state: { phase: 'stalled' } });
    w.clock.now += 3000;
    expect(poller.step()).toEqual({ kind: 'idle' });
    await new Promise((r) => setTimeout(r, 0));
    expect(w.fetches.length).toBe(1);

    h.release(answer(livePayload()));
    expect((await first.request).phase).toBe('ok');
    expect(poller.step().request).toBeInstanceOf(Promise);
  });

  test('a fault inside the panel becomes the "error" phase, and the next tick tries again', async () => {
    const w = io({ respond: () => answer(livePayload()) });
    w.io.ladder = null;
    const poller = createPoller(w.io);
    expect(await poller.step().request).toMatchObject({ phase: 'error' });
    expect(statusLine({ phase: 'error', message: 'x' }).text).toBe('Irrlicht panel error: x');
    expect(poller.step().request).toBeInstanceOf(Promise);
  });
});

describe('statusLine', () => {
  test('tells an unreachable daemon, a foreign answer, a stalled daemon and a panel fault apart', () => {
    const lines = [
      statusLine({ phase: 'ok', where: 'localhost:7843' }),
      statusLine({ phase: 'unreachable', where: 'localhost:7843' }),
      statusLine({ phase: 'unexpected', where: 'localhost:7843' }),
      statusLine({ phase: 'stalled', where: 'localhost:7843' }),
      statusLine({ phase: 'error', message: 'x is not a function' }),
      statusLine({ phase: 'starting' }),
    ].map((l) => l.text);
    expect(new Set(lines).size).toBe(lines.length);
    expect(lines[1]).toBe('Irrlicht daemon not reachable at localhost:7843');
    expect(lines[4]).toBe('Irrlicht panel error: x is not a function');
  });
});

describe('panelRows', () => {
  test('lists every agent in Herdr\'s order, with gauges once the daemon answered', () => {
    const before = panelRows(LIVE_AGENTS, LIVE_TERMINALS, null);
    expect(before.map((r) => [r.address, r.label, r.gauge.kind])).toEqual([
      ['t3', 'herdr-demo · TCP slow start notes', 'none'],
      ['t4', 'herdr-demo · Create DNS resolution notes | codex', 'none'],
    ]);
    expect(panelRows(LIVE_AGENTS, LIVE_TERMINALS, []).every((r) => r.tooltip === 'no Irrlicht session matched')).toBe(true);
  });

  test('a matched row says how it was matched', async () => {
    const { sessions } = await pollOnce({}, io({ respond: () => answer(livePayload()) }).io);
    const rows = panelRows(LIVE_AGENTS, LIVE_TERMINALS, sessions);
    expect(rows.map((r) => [r.gauge.text, r.tooltip])).toEqual([
      ['4.6%', '4.6% of 1.0M context, safe · claude-opus-5-5 · matched by pid'],
      ['3.9%', '3.9% of 475K context, safe · gpt-6.1-sol · matched by agent and folder'],
    ]);
  });

  test('an agent on another machine gets no gauge, and its tooltip says why', async () => {
    const { sessions } = await pollOnce({}, io({ respond: () => answer(livePayload()) }).io);
    const remote = { ...LIVE_AGENTS[0], address: 'vps/t3' };
    const remoteTerminal = { ...LIVE_TERMINALS[1], address: 'vps/t3' };
    const [row] = panelRows([remote], [remoteTerminal], sessions);
    expect(row.gauge.kind).toBe('none');
    expect(row.tooltip).toBe('on another machine: Irrlicht reads this machine\'s sessions only');
  });

  test('a session that changes no row does not change the signature', async () => {
    const a = await pollOnce({}, io({ respond: () => answer(livePayload()) }).io);
    const moved = livePayload();
    moved.groups[2].agents[0] = { ...moved.groups[2].agents[0], metrics: { context_utilization_percentage: 55, pressure_level: 'safe' } };
    const b = await pollOnce({}, io({ respond: () => answer(moved) }).io);
    const sig = (s) => signature(s, panelRows(LIVE_AGENTS, LIVE_TERMINALS, s.sessions));
    expect(sig(b)).toBe(sig(a));
    const shown = livePayload();
    shown.groups[1].agents[0] = { ...shown.groups[1].agents[0], metrics: { ...shown.groups[1].agents[0].metrics, context_utilization_percentage: 7.5 } };
    const c = await pollOnce({}, io({ respond: () => answer(shown) }).io);
    expect(sig(c)).not.toBe(sig(a));
  });
});

describe('focusAddress', () => {
  test('"focus-<n>" names agent n in Herdr\'s order, counting from 1', () => {
    expect(focusAddress('focus-1', LIVE_AGENTS)).toBe('t3');
    expect(focusAddress('focus-2', LIVE_AGENTS)).toBe('t4');
  });

  test('a row that does not exist, or another command, names nothing', () => {
    expect(focusAddress('focus-3', LIVE_AGENTS)).toBeNull();
    expect(focusAddress('focus-0', LIVE_AGENTS)).toBeNull();
    expect(focusAddress('refresh', LIVE_AGENTS)).toBeNull();
  });
});
