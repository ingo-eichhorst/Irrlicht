// Daemon connection: WebSocket push stream + REST hydrate, with reconnect backoff.
// No vscode import; sockets and fetch are injectable for tests.
import { WebSocket } from 'ws';
import type { ResolvedDaemonUrl } from './config';
import type { PushMessage, SessionRecord, SessionStore } from './sessionStore';
import { Emitter } from './emitter';

export interface WsLike {
  on(event: 'open' | 'message' | 'close' | 'error', cb: (...args: any[]) => void): void;
  close(): void;
}
export interface DaemonClientOptions { getUrl(): ResolvedDaemonUrl; store: SessionStore; log(msg: string): void;
  fetchImpl?: typeof fetch; createSocket?: (url: string) => WsLike; backoffMs?: { initial: number; max: number } }

export type ConnectionState = 'connecting' | 'online' | 'offline';

function isFrame(v: unknown): v is PushMessage {
  if (typeof v !== 'object' || v === null) return false;
  return typeof (v as { type?: unknown }).type === 'string';
}

/** The frame in `data` when it is an object with a string `type`, else undefined. Throws on invalid JSON. */
function parseFrame(data: unknown): PushMessage | undefined {
  const msg: unknown = JSON.parse(String(data));
  return isFrame(msg) ? msg : undefined;
}

export class DaemonClient {
  private _state: ConnectionState = 'offline';
  private running = false;
  private gen = 0;
  private socket: WsLike | undefined;
  private timer: ReturnType<typeof setTimeout> | undefined;
  private delay: number;
  /** Seq of the last stamped frame of this socket; every frame passes the check on arrival. */
  private lastSeq = 0;
  private hydrating = false;
  /** A gap or rehydrate request arrived while a hydrate was running: fetch once more when it lands. */
  private again = false;
  /** Session frames received while a hydrate GET is in flight; replayed over the snapshot. */
  private pending: PushMessage[] | undefined;
  private readonly status: Emitter<ConnectionState>;
  private readonly focus: Emitter<SessionRecord>;
  private readonly backoff: { initial: number; max: number };

  constructor(private readonly opts: DaemonClientOptions) {
    this.backoff = opts.backoffMs ?? { initial: 1000, max: 10000 };
    this.delay = this.backoff.initial;
    this.status = new Emitter((e) => opts.log(`status listener failed: ${String(e)}`));
    this.focus = new Emitter((e) => opts.log(`focus listener failed: ${String(e)}`));
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
    this.teardown();
    this.setState('offline');
  }

  restart(): void {
    this.stop();
    this.delay = this.backoff.initial;
    this.start();
  }

  onStatus(l: (s: ConnectionState) => void): { dispose(): void } {
    return this.status.event(l);
  }

  onFocusRequested(l: (s: SessionRecord) => void): { dispose(): void } {
    return this.focus.event(l);
  }

  private connect(): void {
    const gen = ++this.gen;
    this.lastSeq = 0;
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
      void this.hydrate(gen);
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

  private async hydrate(gen: number): Promise<void> {
    if (this.hydrating) {
      this.again = true;
      return;
    }
    this.hydrating = true;
    try {
      do {
        this.again = false;
        this.pending = [];
        const body = await this.fetchSnapshot();
        if (gen !== this.gen) return;
        this.applySnapshot(body);
      } while (this.again);
      this.delay = this.backoff.initial;
      this.setState('online');
    } catch (e) {
      if (gen !== this.gen) return;
      this.opts.log(`hydrate failed: ${String(e)}`);
      this.fail(gen);
    } finally {
      if (gen === this.gen) this.hydrating = false;
    }
  }

  private async fetchSnapshot(): Promise<unknown> {
    const doFetch = this.opts.fetchImpl ?? fetch;
    const res = await doFetch(`${this.opts.getUrl().origin}/api/v1/sessions`);
    if (!res.ok) throw new Error(`GET /api/v1/sessions -> HTTP ${res.status}`);
    return res.json();
  }

  /** Hydrates the store, then replays over it the frames buffered while the GET was in flight. */
  private applySnapshot(body: unknown): void {
    this.opts.store.hydrate(body);
    const buffered = this.pending ?? [];
    this.pending = undefined;
    for (const m of buffered) this.opts.store.apply(m, true);
  }

  private onMessage(gen: number, data: unknown): void {
    try {
      const msg = parseFrame(data);
      if (!msg) return;
      // Every frame advances the cursor on arrival, whatever its type and whether or not it is buffered.
      this.advanceSeq(gen, msg.seq);
      this.dispatch(msg);
    } catch (e) {
      this.opts.log(`ignoring bad frame: ${String(e)}`);
    }
  }

  /** Records a stamped frame's seq; a jump past lastSeq + 1 is a gap and rehydrates. */
  private advanceSeq(gen: number, seq: unknown): void {
    // JSON.parse never yields NaN, so `seq <= 0` rejects exactly what `!(seq > 0)` would.
    if (typeof seq !== 'number' || seq <= 0) return;
    const gap = this.lastSeq > 0 && seq > this.lastSeq + 1;
    this.lastSeq = seq;
    if (gap) void this.hydrate(gen);
  }

  private dispatch(msg: PushMessage): void {
    if (msg.type === 'focus_requested') {
      if (msg.session) this.focus.fire(msg.session);
      return;
    }
    if (this.pending) this.pending.push(msg);
    else this.opts.store.apply(msg);
  }

  private teardown(): void {
    this.gen++;
    this.hydrating = false;
    this.again = false;
    this.pending = undefined;
    this.cancelTimer();
    this.closeSocket();
  }

  private fail(gen: number): void {
    if (gen !== this.gen) return;
    this.teardown();
    this.setState('offline');
    this.opts.store.clear();
    if (!this.running) return;
    const wait = this.delay;
    this.delay = Math.min(this.delay * 2, this.backoff.max);
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
    this.status.fire(s);
  }
}
