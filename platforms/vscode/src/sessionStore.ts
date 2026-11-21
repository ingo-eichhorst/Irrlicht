// In-memory mirror of the daemon's sessions. Pure: no vscode import.
import { Emitter } from './emitter';
import { STATES, isStateName, type StateName } from './states';

export interface SessionRecord { session_id: string; state: StateName | string; parent_session_id?: string; updated_at?: number;
  launcher?: { tty?: string }; project_name?: string; adapter?: string; model?: string }
export interface PushMessage { type: string; session?: SessionRecord; seq?: number }

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
  private readonly changed = new Emitter<void>();

  hydrate(sessionsResponse: unknown): void {
    const flat: SessionRecord[] = [];
    if (Array.isArray(sessionsResponse)) collectGroups(sessionsResponse, flat);
    else if (isRecord(sessionsResponse)) collectGroups(sessionsResponse.groups, flat);
    this.sessions = new Map(flat.map((s) => [s.session_id, s]));
    this.changed.fire();
  }

  /** With newerOnly (replay over a snapshot) an upsert strictly older than the held record is dropped (a same-second tie applies). */
  apply(msg: PushMessage, newerOnly = false): boolean {
    const changed = this.applySession(msg, newerOnly);
    if (changed) this.changed.fire();
    return changed;
  }

  private applySession(msg: PushMessage, newerOnly: boolean): boolean {
    const id = msg.session?.session_id;
    if (typeof id !== 'string' || id === '') return false;
    if (msg.type === 'session_deleted') return this.sessions.delete(id);
    if (msg.type !== 'session_created' && msg.type !== 'session_updated') return false;
    const held = this.sessions.get(id)?.updated_at;
    if (newerOnly && held !== undefined && held > (msg.session?.updated_at ?? 0)) return false;
    this.sessions.set(id, msg.session as SessionRecord);
    return true;
  }

  clear(): void {
    if (this.sessions.size === 0) return;
    this.sessions.clear();
    this.changed.fire();
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
    return this.changed.event(listener);
  }
}
