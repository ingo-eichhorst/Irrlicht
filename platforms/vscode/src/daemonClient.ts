// Daemon connection: WebSocket push stream + REST hydrate, with reconnect backoff.
// No vscode import; sockets and fetch are injectable for tests.
import { WebSocket } from 'ws';
import type { ResolvedDaemonUrl } from './config';
import type { PushMessage, SessionRecord, SessionStore } from './sessionStore';
import type { ConnectionState } from './statusText';

export interface WsLike {
  on(event: 'open' | 'message' | 'close' | 'error', cb: (...args: any[]) => void): void;
  close(): void;
}
export interface DaemonClientOptions { getUrl(): ResolvedDaemonUrl; store: SessionStore; log(msg: string): void;
  fetchImpl?: typeof fetch; createSocket?: (url: string) => WsLike; backoffMs?: { initial: number; max: number } }

type Listener<T> = (v: T) => void;

export class DaemonClient {
  private _state: ConnectionState = 'offline';
  private running = false;
  private gen = 0;
  private socket: WsLike | undefined;
  private timer: ReturnType<typeof setTimeout> | undefined;
  private delay: number;
  private rehydrating = false;
  /** Session frames received while a hydrate GET is in flight; replayed over the snapshot. */
  private pending: PushMessage[] | undefined;
  private readonly statusListeners = new Set<Listener<ConnectionState>>();
  private readonly focusListeners = new Set<Listener<SessionRecord>>();
  private readonly backoff: { initial: number; max: number };

  constructor(private readonly opts: DaemonClientOptions) {
    this.backoff = opts.backoffMs ?? { initial: 1000, max: 10000 };
    this.delay = this.backoff.initial;
  }

  get state(): ConnectionState {
    return this._state;
  }

  start(): void {
    if (this.running) return;
    this.running = true;
    this.connect();
  }

  stop(): void {
    this.running = false;
    this.gen++;
    this.rehydrating = false;
    this.pending = undefined;
    this.cancelTimer();
    this.closeSocket();
    this.setState('offline');
  }

  restart(): void {
    this.stop();
    this.delay = this.backoff.initial;
    this.start();
  }

  onStatus(l: Listener<ConnectionState>): { dispose(): void } {
    this.statusListeners.add(l);
    return { dispose: () => { this.statusListeners.delete(l); } };
  }

  onFocusRequested(l: Listener<SessionRecord>): { dispose(): void } {
    this.focusListeners.add(l);
    return { dispose: () => { this.focusListeners.delete(l); } };
  }

  private connect(): void {
    const gen = ++this.gen;
    const url = this.opts.getUrl();
    this.setState('connecting');
    let sock: WsLike;
    try {
      sock = (this.opts.createSocket ?? ((u: string) => new WebSocket(u)))(url.wsUrl);
    } catch (e) {
      this.opts.log(`socket creation failed: ${String(e)}`);
      this.fail(gen);
      return;
    }
    this.socket = sock;
    sock.on('open', () => {
      if (gen !== this.gen) return;
      void this.hydrate(gen, true);
    });
    sock.on('message', (data: unknown) => {
      if (gen !== this.gen) return;
      this.onMessage(gen, data);
    });
    sock.on('error', (e: unknown) => {
      this.opts.log(`socket error: ${String(e)}`);
      this.fail(gen);
    });
    sock.on('close', () => this.fail(gen));
  }

  private async hydrate(gen: number, initial: boolean): Promise<void> {
    this.pending = [];
    try {
      const url = this.opts.getUrl();
      const doFetch = this.opts.fetchImpl ?? fetch;
      const res = await doFetch(`${url.origin}/api/v1/sessions`);
      if (!res.ok) throw new Error(`GET /api/v1/sessions -> HTTP ${res.status}`);
      const body: unknown = await res.json();
      if (gen !== this.gen) return;
      this.opts.store.hydrate(body);
      const buffered = this.pending ?? [];
      this.pending = undefined;
      for (const m of buffered) this.opts.store.apply(m, true);
      this.delay = this.backoff.initial;
      if (initial) this.setState('online');
    } catch (e) {
      if (gen !== this.gen) return;
      this.opts.log(`hydrate failed: ${String(e)}`);
      this.fail(gen);
    } finally {
      if (gen === this.gen) this.pending = undefined;
      if (!initial && gen === this.gen) this.rehydrating = false;
    }
  }

  private onMessage(gen: number, data: unknown): void {
    try {
      const raw = typeof data === 'string' ? data : String(data);
      const msg = JSON.parse(raw) as PushMessage;
      if (typeof msg !== 'object' || msg === null || typeof msg.type !== 'string') return;
      if (msg.type === 'focus_requested') {
        // Focus frames carry a seq from the same global counter as every other broadcast.
        const gap = this.opts.store.trackSeq(msg);
        if (msg.session) this.emitFocus(msg.session);
        if (gap) this.rehydrate(gen);
        return;
      }
      if (this.pending) {
        this.pending.push(msg);
        return;
      }
      if (this.opts.store.apply(msg) === 'gap') this.rehydrate(gen);
    } catch (e) {
      this.opts.log(`ignoring bad frame: ${String(e)}`);
    }
  }

  private rehydrate(gen: number): void {
    if (this.rehydrating) return;
    this.rehydrating = true;
    void this.hydrate(gen, false);
  }

  private emitFocus(s: SessionRecord): void {
    for (const l of this.focusListeners) {
      try { l(s); } catch (e) { this.opts.log(`focus listener failed: ${String(e)}`); }
    }
  }

  private fail(gen: number): void {
    if (gen !== this.gen) return;
    this.gen++;
    this.rehydrating = false;
    this.pending = undefined;
    this.closeSocket();
    this.setState('offline');
    this.opts.store.clear();
    if (!this.running) return;
    const wait = this.delay;
    this.delay = Math.min(this.delay * 2, this.backoff.max);
    this.cancelTimer();
    this.timer = setTimeout(() => {
      this.timer = undefined;
      if (this.running) this.connect();
    }, wait);
  }

  private closeSocket(): void {
    const s = this.socket;
    this.socket = undefined;
    if (!s) return;
    try { s.close(); } catch { /* already closed */ }
  }

  private cancelTimer(): void {
    if (this.timer !== undefined) clearTimeout(this.timer);
    this.timer = undefined;
  }

  private setState(s: ConnectionState): void {
    if (this._state === s) return;
    this._state = s;
    for (const l of this.statusListeners) {
      try { l(s); } catch (e) { this.opts.log(`status listener failed: ${String(e)}`); }
    }
  }
}
