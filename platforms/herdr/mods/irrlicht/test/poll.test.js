import { describe, expect, test } from 'vitest';
import { pollOnce, signature } from '../lib/poll.js';
import { panelRows, statusLine } from '../lib/rows.js';
import { LIVE_AGENTS, LIVE_TERMINALS, livePayload } from './fixtures.js';

const HOME_ADDR = '/home/dev/.local/share/irrlicht/irrlichd.addr';

// io for pollOnce: an addr file naming `port`, and a fetch that answers
// `respond(url)`. Every fetch and every addr-file read is recorded.
function io({ port = 7837, respond }) {
  const asked = [];
  return {
    asked,
    io: {
      ladder: {
        bindAddr: async () => undefined,
        irrlichtHome: async () => undefined,
        home: async () => '/home/dev',
        isRegularFile: async (p) => { asked.push(['stat', p]); return p === HOME_ADDR; },
        read: async () => `127.0.0.1:${port}\n`,
      },
      fetch: async (url) => { asked.push(['fetch', url]); return respond(url); },
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
    expect(got.sessions.map((s) => s.pid)).toEqual([46690, 46222, 72988]);
    expect(w.asked.at(-1)).toEqual(['fetch', 'http://localhost:7843/api/v1/sessions']);
  });

  test('a refused connection is "unreachable" and never throws', async () => {
    const w = io({ respond: () => { throw new Error('error sending request for url'); } });
    expect(await pollOnce({ phase: 'starting' }, w.io)).toEqual({ phase: 'unreachable', where: 'localhost:7837' });
  });

  test('something else on the port (a 404, a non-JSON body, another JSON shape) is "unexpected"', async () => {
    const notFound = io({ respond: () => answer({}, { status: 404 }) });
    expect((await pollOnce({}, notFound.io)).phase).toBe('unexpected');
    const notJson = io({ respond: () => ({ ok: true, status: 200, json: async () => { throw new SyntaxError('x'); } }) });
    expect((await pollOnce({}, notJson.io)).phase).toBe('unexpected');
    const otherShape = io({ respond: () => answer({ hello: 'world' }) });
    expect((await pollOnce({}, otherShape.io)).phase).toBe('unexpected');
  });

  test('the address is kept while the daemon answers, and looked up again after it stopped', async () => {
    const w = io({ port: 7843, respond: () => answer(livePayload()) });
    const first = await pollOnce({ phase: 'starting' }, w.io);
    const stats = () => w.asked.filter(([k]) => k === 'stat').length;
    expect(stats()).toBe(1);
    await pollOnce(first, w.io);
    expect(stats()).toBe(1);
    await pollOnce({ phase: 'unreachable', where: 'localhost:7843' }, w.io);
    expect(stats()).toBe(2);
  });

  test('a fault in the panel itself propagates, so it is never shown as a stopped daemon', async () => {
    const w = io({ respond: () => answer(livePayload()) });
    w.io.ladder = null;
    await expect(pollOnce({ phase: 'starting' }, w.io)).rejects.toThrow();
  });
});

describe('statusLine', () => {
  test('tells an unreachable daemon, a foreign answer and a panel fault apart', () => {
    const lines = [
      statusLine({ phase: 'ok', where: 'localhost:7843' }),
      statusLine({ phase: 'unreachable', where: 'localhost:7843' }),
      statusLine({ phase: 'unexpected', where: 'localhost:7843' }),
      statusLine({ phase: 'error', message: 'x is not a function' }),
      statusLine({ phase: 'starting' }),
    ].map((l) => l.text);
    expect(new Set(lines).size).toBe(lines.length);
    expect(lines[1]).toBe('Irrlicht daemon not reachable at localhost:7843');
    expect(lines[3]).toBe('Irrlicht panel error: x is not a function');
  });
});

describe('panelRows', () => {
  test('lists every agent in Herdr\'s order, with gauges once the daemon answered', () => {
    const sessions = [];
    const before = panelRows(LIVE_AGENTS, LIVE_TERMINALS, null);
    expect(before.map((r) => [r.address, r.label, r.gauge.kind])).toEqual([
      ['t3', 'herdr-demo · TCP slow start notes', 'none'],
      ['t4', 'herdr-demo · Create DNS resolution notes | codex', 'none'],
    ]);
    expect(panelRows(LIVE_AGENTS, LIVE_TERMINALS, sessions).every((r) => r.tooltip === 'no Irrlicht session matched')).toBe(true);
  });

  test('a matched row says how it was matched', async () => {
    const w = io({ respond: () => answer(livePayload()) });
    const { sessions } = await pollOnce({}, w.io);
    const rows = panelRows(LIVE_AGENTS, LIVE_TERMINALS, sessions);
    expect(rows.map((r) => [r.gauge.text, r.tooltip])).toEqual([
      ['4.6%', '4.6% of 1.0M context, safe · claude-opus-5-5 · matched by pid'],
      ['3.9%', '3.9% of 475K context, safe · gpt-6.1-sol · matched by agent and folder'],
    ]);
  });

  test('a session that changes no row does not change the signature', async () => {
    const w = io({ respond: () => answer(livePayload()) });
    const a = await pollOnce({}, w.io);
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
