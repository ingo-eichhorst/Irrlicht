import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { resolveDaemonUrl, type ResolvedDaemonUrl } from '../../src/config';
import { DaemonClient, type ConnectionState, type WsLike } from '../../src/daemonClient';
import { SessionStore } from '../../src/sessionStore';

class FakeSocket implements WsLike {
  handlers: Record<string, Array<(...a: any[]) => void>> = {};
  closed = false;
  constructor(public url: string) {}
  on(ev: string, cb: (...a: any[]) => void): void {
    (this.handlers[ev] ??= []).push(cb);
  }
  close(): void {
    this.closed = true;
  }
  emit(ev: string, ...a: any[]): void {
    for (const cb of this.handlers[ev] ?? []) cb(...a);
  }
  open(): void {
    this.emit('open');
  }
  message(obj: unknown): void {
    this.emit('message', typeof obj === 'string' ? obj : JSON.stringify(obj));
  }
}

const body = (ids: string[]) => ({ groups: [{ name: 'g', agents: ids.map((id) => ({ session_id: id, state: 'working' })) }] });

interface Rig {
  client: DaemonClient;
  store: SessionStore;
  sockets: FakeSocket[];
  fetchMock: ReturnType<typeof vi.fn>;
  statuses: ConnectionState[];
  setUrl(u: string): void;
}

function rig(fetchImpl?: (url: string) => Promise<any>): Rig {
  let url = resolveDaemonUrl('http://127.0.0.1:7837');
  const sockets: FakeSocket[] = [];
  const fetchMock = vi.fn(fetchImpl ?? (async () => ({ ok: true, json: async () => body(['s1']) })));
  const store = new SessionStore();
  const client = new DaemonClient({
    getUrl: (): ResolvedDaemonUrl => url,
    store,
    log: () => {},
    fetchImpl: fetchMock as unknown as typeof fetch,
    createSocket: (u) => {
      const s = new FakeSocket(u);
      sockets.push(s);
      return s;
    },
    backoffMs: { initial: 1000, max: 10000 },
  });
  const statuses: ConnectionState[] = [];
  client.onStatus((s) => statuses.push(s));
  return { client, store, sockets, fetchMock, statuses, setUrl: (u) => { url = resolveDaemonUrl(u); } };
}

const tick = (ms = 0) => vi.advanceTimersByTimeAsync(ms);

beforeEach(() => vi.useFakeTimers());
afterEach(() => vi.useRealTimers());

describe('DaemonClient', () => {
  it('connects, hydrates on open, then goes online', async () => {
    const r = rig();
    expect(r.client.state).toBe('offline');
    r.client.start();
    expect(r.client.state).toBe('connecting');
    expect(r.sockets[0].url).toBe('ws://127.0.0.1:7837/api/v1/sessions/stream');
    r.sockets[0].open();
    await tick();
    expect(r.fetchMock).toHaveBeenCalledTimes(1);
    expect(r.fetchMock.mock.calls[0][0]).toBe('http://127.0.0.1:7837/api/v1/sessions');
    expect(r.client.state).toBe('online');
    expect(r.store.all().map((x) => x.session_id)).toEqual(['s1']);
    expect(r.statuses).toEqual(['connecting', 'online']);
  });

  it('applies push messages to the store and ignores unparsable frames', async () => {
    const r = rig();
    r.client.start();
    r.sockets[0].open();
    await tick();
    r.sockets[0].message({ type: 'session_created', session: { session_id: 's2', state: 'ready' } });
    r.sockets[0].message('not json {');
    expect(r.store.all().map((x) => x.session_id).sort()).toEqual(['s1', 's2']);
    r.sockets[0].message({ type: 'session_deleted', session: { session_id: 's1', state: 'working' } });
    expect(r.store.all().map((x) => x.session_id)).toEqual(['s2']);
  });

  it('a seq gap rehydrates; further gaps during that hydrate coalesce into exactly one more', async () => {
    const resolvers: Array<(v: any) => void> = [];
    let calls = 0;
    const r = rig(() => {
      calls++;
      if (calls === 1) return Promise.resolve({ ok: true, json: async () => body(['s1']) });
      return new Promise((res) => { resolvers.push(res); });
    });
    r.client.start();
    r.sockets[0].open();
    await tick();
    const m = (seq: number) => ({ type: 'session_updated', seq, session: { session_id: 's1', state: 'ready' } });
    r.sockets[0].message(m(1));
    r.sockets[0].message(m(5)); // gap -> rehydrate (GET #2 pending)
    r.sockets[0].message(m(9)); // another gap while pending -> run again, not a parallel GET
    await tick();
    expect(r.fetchMock).toHaveBeenCalledTimes(2);
    resolvers[0]({ ok: true, json: async () => body(['fresh']) });
    await tick();
    expect(r.fetchMock).toHaveBeenCalledTimes(3);
    resolvers[1]({ ok: true, json: async () => body(['fresh2']) });
    await tick();
    // The final snapshot is authoritative; frames buffered for the earlier GET were replayed over that one only.
    expect(r.store.all().map((x) => x.session_id)).toEqual(['fresh2']);
    expect(r.fetchMock).toHaveBeenCalledTimes(3);
  });

  it('close goes offline, clears the store and reconnects after 1s then 2s', async () => {
    const r = rig();
    r.client.start();
    r.sockets[0].open();
    await tick();
    r.sockets[0].emit('close');
    expect(r.client.state).toBe('offline');
    expect(r.store.all()).toEqual([]);
    expect(r.sockets).toHaveLength(1);
    await tick(999);
    expect(r.sockets).toHaveLength(1);
    await tick(1);
    expect(r.sockets).toHaveLength(2);
    // second socket fails before ever opening: next delay is 2s
    r.sockets[1].emit('error', new Error('refused'));
    r.sockets[1].emit('close'); // error+close must not double-schedule or double-emit
    await tick(1999);
    expect(r.sockets).toHaveLength(2);
    await tick(1);
    expect(r.sockets).toHaveLength(3);
    expect(r.statuses.filter((s) => s === 'offline')).toHaveLength(2);
  });

  it('backoff caps at max and resets after a successful hydrate', async () => {
    const r = rig();
    r.client.start();
    for (let i = 0, delay = 1000; i < 6; i++, delay = Math.min(delay * 2, 10000)) {
      r.sockets[i].emit('close');
      await tick(delay);
      expect(r.sockets).toHaveLength(i + 2);
    }
    // delays were 1,2,4,8,10,10; next would be 10
    r.sockets[6].open();
    await tick();
    expect(r.client.state).toBe('online');
    r.sockets[6].emit('close');
    await tick(1000);
    expect(r.sockets).toHaveLength(8);
  });

  it('restart picks up a changed URL', async () => {
    const r = rig();
    r.client.start();
    r.sockets[0].open();
    await tick();
    r.setUrl('http://127.0.0.1:7838');
    r.client.restart();
    expect(r.sockets[0].closed).toBe(true);
    expect(r.sockets[1].url).toBe('ws://127.0.0.1:7838/api/v1/sessions/stream');
    r.sockets[1].open();
    await tick();
    expect(r.fetchMock.mock.calls[1][0]).toBe('http://127.0.0.1:7838/api/v1/sessions');
    expect(r.client.state).toBe('online');
  });

  it('stop prevents reconnect and closes the socket', async () => {
    const r = rig();
    r.client.start();
    r.sockets[0].emit('close');
    r.client.stop();
    await tick(60000);
    expect(r.sockets).toHaveLength(1);
    r.client.start();
    r.client.stop();
    expect(r.sockets[1].closed).toBe(true);
    r.sockets[1].emit('close');
    await tick(60000);
    expect(r.sockets).toHaveLength(2);
  });

  it('emits focus_requested to listeners and does not store it', async () => {
    const r = rig();
    const got: string[] = [];
    r.client.onFocusRequested((s) => got.push(s.session_id));
    r.client.start();
    r.sockets[0].open();
    await tick();
    r.sockets[0].message({ type: 'focus_requested', session: { session_id: 'focus-me', state: 'ready' } });
    expect(got).toEqual(['focus-me']);
    expect(r.store.all().map((x) => x.session_id)).toEqual(['s1']);
  });

  it('a failing hydrate GET closes the socket and reconnects', async () => {
    let n = 0;
    const r = rig(async () => {
      if (++n === 1) return { ok: false, status: 500, json: async () => ({}) };
      return { ok: true, json: async () => body(['s1']) };
    });
    r.client.start();
    r.sockets[0].open();
    await tick();
    expect(r.sockets[0].closed).toBe(true);
    expect(r.client.state).toBe('offline');
    await tick(1000);
    expect(r.sockets).toHaveLength(2);
    r.sockets[1].open();
    await tick();
    expect(r.client.state).toBe('online');
  });

  it('a rejecting fetch is also a failed connection', async () => {
    let n = 0;
    const r = rig(async () => {
      if (++n === 1) throw new Error('boom');
      return { ok: true, json: async () => body(['s1']) };
    });
    r.client.start();
    r.sockets[0].open();
    await tick();
    expect(r.client.state).toBe('offline');
    await tick(1000);
    expect(r.sockets).toHaveLength(2);
  });

  it('records the seq of focus_requested frames so a click does not look like a gap', async () => {
    const r = rig();
    r.client.start();
    r.sockets[0].open();
    await tick();
    r.sockets[0].message({ type: 'session_updated', seq: 10, session: { session_id: 's1', state: 'ready' } });
    r.sockets[0].message({ type: 'focus_requested', seq: 11, session: { session_id: 's1', state: 'ready' } });
    r.sockets[0].message({ type: 'session_updated', seq: 12, session: { session_id: 's1', state: 'working' } });
    await tick();
    expect(r.fetchMock).toHaveBeenCalledTimes(1);
  });

  it('does not let a stale hydrate snapshot clobber a newer push that arrived while it was in flight', async () => {
    let resolveFetch!: (v: any) => void;
    const r = rig(() => new Promise((res) => { resolveFetch = res; }));
    r.client.start();
    r.sockets[0].open();
    await tick();
    r.sockets[0].message({ type: 'session_updated', seq: 5, session: { session_id: 'X', state: 'waiting', updated_at: 200 } });
    resolveFetch({ ok: true, json: async () => ({ groups: [{ name: 'g', agents: [{ session_id: 'X', state: 'working', updated_at: 100 }] }] }) });
    await tick();
    expect(r.store.all().find((x) => x.session_id === 'X')?.state).toBe('waiting');
  });

  describe('seq cursor advances for every frame on arrival, including buffered ones', () => {
    // A hydrate GET the test resolves by hand, one deferred per call.
    function deferredRig() {
      const gets: Array<(v: any) => void> = [];
      const r = rig(() => new Promise((res) => { gets.push(res); }));
      const reply = (i: number, ids: Array<[string, string, number]>) =>
        gets[i]({ ok: true, json: async () => ({ groups: [{ name: 'g', agents: ids.map(([id, state, ua]) => ({ session_id: id, state, updated_at: ua })) }] }) });
      return { r, gets, reply };
    }
    const upd = (seq: number, state: string, ua: number) => ({ type: 'session_updated', seq, session: { session_id: 's1', state, updated_at: ua } });
    const focus = (seq: number) => ({ type: 'focus_requested', seq, session: { session_id: 's1', state: 'ready' } });
    const stateOf = (r: Rig) => r.store.all().find((x) => x.session_id === 's1')?.state;

    it('focus frames around a buffered session frame are not a gap: one hydrate, no lost update, no stale overwrite', async () => {
      const { r, gets, reply } = deferredRig();
      r.client.start();
      r.sockets[0].open();
      await tick(); // GET #1 in flight
      r.sockets[0].message(focus(5));
      r.sockets[0].message(upd(6, 'waiting', 200)); // buffered
      r.sockets[0].message(focus(7));
      await tick();
      expect(gets).toHaveLength(1); // no second, overlapping hydrate
      reply(0, [['s1', 'working', 100]]);
      await tick();
      expect(stateOf(r)).toBe('waiting'); // the buffered seq-6 update survived the snapshot
      r.sockets[0].message(upd(8, 'error', 300)); // live
      await tick();
      expect(gets).toHaveLength(1);
      expect(stateOf(r)).toBe('error'); // never overwritten by an older snapshot
    });

    it('a real gap among buffered frames triggers a second GET once the first lands', async () => {
      const { r, gets, reply } = deferredRig();
      r.client.start();
      r.sockets[0].open();
      await tick();
      r.sockets[0].message(upd(1, 'waiting', 200));
      r.sockets[0].message(upd(3, 'ready', 300)); // seq 2 is missing
      await tick();
      expect(gets).toHaveLength(1); // still one hydrate at a time
      reply(0, [['s1', 'working', 100]]);
      await tick();
      expect(gets).toHaveLength(2);
      reply(1, [['s1', 'ready', 300]]);
      await tick();
      expect(gets).toHaveLength(2);
      expect(stateOf(r)).toBe('ready');
      expect(r.client.state).toBe('online');
    });

    it('a new socket restarts the cursor', async () => {
      const r = rig();
      r.client.start();
      r.sockets[0].open();
      await tick();
      r.sockets[0].message(upd(500, 'ready', 1));
      r.sockets[0].emit('close');
      await tick(1000);
      r.sockets[1].open();
      await tick();
      r.sockets[1].message(upd(3, 'working', 2)); // lower than the old socket's cursor, and not a gap
      await tick();
      expect(r.fetchMock).toHaveBeenCalledTimes(2);
    });
  });
});
