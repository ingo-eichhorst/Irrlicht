import { describe, expect, test } from 'vitest';
import { buildView, unreachableView, viewFromResponse } from './view.js';
import { HERE_ID, NOW_MS, SESSIONS_PAYLOAD } from './testdata/sessions.fixture.js';

const HOST = 'localhost:7837';
const ok = text => ({ status: 200, ok: true, headers: {}, text });

// LOCKS: every way a poll can fail ends in the one "not reachable" view, and
// none of them throws (a throw out of the poll would be a hook failure).
describe('every failure is the not-reachable view, never a throw', () => {
  const cases = [
    ['a refused connection (fetch rejected)', { error: new Error('ECONNREFUSED') }, 'no connection'],
    ['a policy refusal that rejects', { error: new Error('refused by policy') }, 'no connection'],
    ['no response at all', {}, 'no connection'],
    ['HTTP 500', { response: { status: 500, ok: false, headers: {}, text: 'internal error' } }, 'HTTP 500'],
    ['HTTP 403 (a refusal read as one)', { response: { status: 403, ok: false, headers: {}, text: '' } }, 'HTTP 403'],
    ['a body that is not JSON', { response: ok('<html>proxy login</html>') }, 'not a JSON answer'],
    ['an empty body', { response: ok('') }, 'not a JSON answer'],
    ['JSON that is not an object', { response: ok('42') }, 'not an Irrlicht sessions answer'],
    ['JSON null', { response: ok('null') }, 'not an Irrlicht sessions answer'],
    ['an object with no groups key', { response: ok('{"items":[]}') }, 'not an Irrlicht sessions answer'],
    ['groups of the wrong type', { response: ok('{"groups":{}}') }, 'not an Irrlicht sessions answer'],
  ];
  for (const [name, outcome, reason] of cases) {
    test(name, () => {
      let view;
      expect(() => { view = viewFromResponse({ ...outcome, hostPort: HOST, hereSessionId: HERE_ID, nowMs: NOW_MS }); })
        .not.toThrow();
      expect(view).toEqual(unreachableView(HOST, reason));
    });
  }

  test('the view names the address it asked', () => {
    expect(viewFromResponse({ error: new Error('x'), hostPort: 'localhost:7838', nowMs: NOW_MS }).hostPort)
      .toBe('localhost:7838');
  });
});

describe('buildView over the synthetic payload', () => {
  const view = viewFromResponse({ response: ok(JSON.stringify(SESSIONS_PAYLOAD)), hostPort: HOST, hereSessionId: HERE_ID, nowMs: NOW_MS });

  test('reachable, with every session and the 24h cost', () => {
    expect(view.status).toBe('ok');
    expect(view.sessionCount).toBe(5);
    expect(view.dayCost).toBe('$16.50');
    expect(view.sections.map(s => s.name)).toEqual(['orchard', 'lighthouse', 'fleet / rig-a']);
  });

  test("the quota shown is the here session's account, not the newer sample", () => {
    // sess-other-0003 (acct-test-0002) sampled 115 s later than the here
    // session (acct-test-0001); the here session's own bucket still wins.
    expect(view.quota).toEqual({
      title: 'Claude Max · …0001',
      windows: [
        { label: '5h', usedPercent: 11, tone: 'green', resetsIn: '2h 14m' },
        { label: '7d', usedPercent: 83, tone: 'orange', resetsIn: '3d 4h' },
      ],
      isStale: false,
      retrievalFailure: '',
    });
  });

  test('without a here session, the most recently sampled bucket', () => {
    const v = buildView(SESSIONS_PAYLOAD, { hostPort: HOST, hereSessionId: undefined, nowMs: NOW_MS });
    expect(v.quota.title).toBe('Claude Pro · …0002');
  });

  test('the view is plain data (it lives in $.state)', () => {
    expect(JSON.parse(JSON.stringify(view))).toEqual(view);
  });

  test('a daemon with no sessions', () => {
    expect(buildView({ groups: null }, { hostPort: HOST, hereSessionId: HERE_ID, nowMs: NOW_MS }))
      .toEqual({ status: 'ok', hostPort: HOST, quota: null, sections: [], sessionCount: 0, dayCost: '$0' });
  });
});
