// In-memory mirror of the daemon's sessions. Pure: no vscode import.
import { STATES, isStateName, type StateName } from './states';

export interface SessionRecord { session_id: string; state: StateName | string; parent_session_id?: string; updated_at?: number;
  launcher?: { tty?: string; term_program?: string; vscode_pid?: number }; project_name?: string; adapter?: string; model?: string }
export interface PushMessage { type: string; session?: SessionRecord; seq?: number }
export type ApplyResult = 'changed' | 'unchanged' | 'gap';

function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === 'object' && v !== null && !Array.isArray(v);
}

function collectAgents(agents: unknown, out: SessionRecord[]): void {
  if (!Array.isArray(agents)) return;
  for (const a of agents) {
    if (!isRecord(a)) continue;
    if (typeof a.session_id === 'string' && a.session_id !== '') {
      const { children: _children, ...rest } = a;
      out.push(rest as unknown as SessionRecord);
    }
    collectAgents(a.children, out);
  }
}

function collectGroups(groups: unknown, out: SessionRecord[]): void {
  if (!Array.isArray(groups)) return;
  for (const g of groups) {
    if (!isRecord(g)) continue;
    collectAgents(g.agents, out);
    collectGroups(g.groups, out);
  }
}

export class SessionStore {
  private sessions = new Map<string, SessionRecord>();
  private lastSeq = 0;
  private listeners = new Set<() => void>();

  hydrate(sessionsResponse: unknown): void {
    const flat: SessionRecord[] = [];
    if (Array.isArray(sessionsResponse)) collectGroups(sessionsResponse, flat);
    else if (isRecord(sessionsResponse)) collectGroups(sessionsResponse.groups, flat);
    this.sessions = new Map(flat.map((s) => [s.session_id, s]));
    this.lastSeq = 0;
    this.fire();
  }

  apply(msg: PushMessage): ApplyResult {
    let result: ApplyResult = 'unchanged';
    const id = msg.session?.session_id;
    if (typeof id === 'string' && id !== '') {
      if (msg.type === 'session_created' || msg.type === 'session_updated') {
        this.sessions.set(id, msg.session as SessionRecord);
        result = 'changed';
      } else if (msg.type === 'session_deleted' && this.sessions.delete(id)) {
        result = 'changed';
      }
    }
    if (typeof msg.seq === 'number' && msg.seq > 0) {
      const gap = this.lastSeq > 0 && msg.seq > this.lastSeq + 1;
      this.lastSeq = msg.seq;
      if (gap) result = 'gap';
    }
    if (result !== 'unchanged') this.fire();
    return result;
  }

  clear(): void {
    this.sessions.clear();
    this.lastSeq = 0;
    this.fire();
  }

  all(): SessionRecord[] {
    return [...this.sessions.values()];
  }

  topLevel(): SessionRecord[] {
    return this.all().filter((s) => !s.parent_session_id || !this.sessions.has(s.parent_session_id));
  }

  counts(): Record<StateName, number> {
    const c = {} as Record<StateName, number>;
    for (const s of STATES) c[s] = 0;
    for (const s of this.topLevel()) if (isStateName(s.state)) c[s.state]++;
    return c;
  }

  onDidChange(listener: () => void): { dispose(): void } {
    this.listeners.add(listener);
    return { dispose: () => { this.listeners.delete(listener); } };
  }

  private fire(): void {
    for (const l of [...this.listeners]) {
      try { l(); } catch { /* a listener must not break the store */ }
    }
  }
}
