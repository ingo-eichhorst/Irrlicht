import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { resolveDaemonUrl, type ResolvedDaemonUrl } from '../../src/config';
import { DaemonClient, type WsLike } from '../../src/daemonClient';
import { SessionStore } from '../../src/sessionStore';
import type { ConnectionState } from '../../src/statusText';

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

  it('a seq gap triggers exactly one coalesced rehydrate', async () => {
    let resolveGet: (v: any) => void = () => {};
    let calls = 0;
    const r = rig(() => {
      calls++;
      if (calls === 1) return Promise.resolve({ ok: true, json: async () => body(['s1']) });
      return new Promise((res) => { resolveGet = res; });
    });
    r.client.start();
    r.sockets[0].open();
    await tick();
    const m = (seq: number) => ({ type: 'session_updated', seq, session: { session_id: 's1', state: 'ready' } });
    r.sockets[0].message(m(1));
    r.sockets[0].message(m(5)); // gap -> rehydrate (GET #2 pending)
    r.sockets[0].message(m(9)); // another gap while pending -> coalesced
    await tick();
    expect(r.fetchMock).toHaveBeenCalledTimes(2);
    resolveGet({ ok: true, json: async () => body(['fresh']) });
    await tick();
    expect(r.store.all().map((x) => x.session_id)).toEqual(['fresh']);
    expect(r.fetchMock).toHaveBeenCalledTimes(2);
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
});
