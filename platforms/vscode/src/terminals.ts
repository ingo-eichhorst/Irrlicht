import { execFile } from 'node:child_process';
import { existsSync } from 'node:fs';
import * as vscode from 'vscode';
import type { SessionRecord, SessionStore } from './sessionStore';
import { isStateName, stateColorId } from './states';
import { applyPsRows, isPsError, isUnresolved, nextRetryDelay, normalizeTty, pairTerminals, parsePsTtyOutput, resolvePsPath } from './tty';
import type { TermState } from './tty';

interface Pairing { terminal: vscode.Terminal; session: SessionRecord }

export class TerminalPairing implements vscode.Disposable {
  private readonly emitter = new vscode.EventEmitter<void>();
  readonly onDidChange: vscode.Event<void> = this.emitter.event;
  // One entry per terminal once its shell pid has been awaited.
  private readonly terms = new Map<vscode.Terminal, TermState>();
  private current: Pairing[] = [];
  private signature = '';
  private psFailureLogged = false;
  // Resolved once from fixed directories; undefined = pairing unavailable (also always on win32).
  private readonly psPath = process.platform === 'win32' ? undefined : resolvePsPath(existsSync);
  private running = false;
  private again = false;
  private timer: ReturnType<typeof setTimeout> | undefined;
  private disposed = false;
  private readonly disposables: Array<{ dispose(): void }> = [];

  constructor(private readonly store: SessionStore, private readonly log: (m: string) => void) {
    // A store change cannot change what ps reports for a shell pid, so it only re-pairs.
    this.disposables.push(
      store.onDidChange(() => this.recompute()),
      vscode.window.onDidOpenTerminal(() => void this.resolve()),
      vscode.window.onDidCloseTerminal(() => { this.forgetClosed(new Set(vscode.window.terminals)); this.recompute(); }),
    );
  }

  // Kicks off the initial lookup; called once by the owner after construction.
  start(): void {
    if (!this.psPath && process.platform !== 'win32') this.logPsFailureOnce('no ps found in /bin or /usr/bin');
    void this.resolve();
  }

  pairings(): Pairing[] { return this.current; }

  terminalForSession(s: SessionRecord): vscode.Terminal | undefined {
    const tty = normalizeTty(s.launcher?.tty);
    if (!tty) return undefined;
    for (const t of vscode.window.terminals) {
      if (this.terms.get(t)?.tty === tty) return t;
    }
    return undefined;
  }

  // Passes are serialized; a request arriving mid-pass schedules exactly one more.
  private async resolve(): Promise<void> {
    if (this.disposed || !this.psPath) return;
    if (this.running) { this.again = true; return; }
    this.running = true;
    try {
      do {
        this.again = false;
        await this.pass(this.psPath);
      } while (this.again && !this.disposed);
    } finally {
      this.running = false;
    }
    this.scheduleRetry();
  }

  private async pass(psPath: string): Promise<void> {
    if (this.timer) { clearTimeout(this.timer); this.timer = undefined; }
    const live = new Set(vscode.window.terminals);
    this.forgetClosed(live);
    await this.resolvePids([...live].filter((t) => !this.terms.has(t)));
    await this.resolveTtys(psPath, [...live].filter((t) => isUnresolved(this.terms.get(t))));
    this.recompute();
  }

  private forgetClosed(live: Set<vscode.Terminal>): void {
    for (const t of Array.from(this.terms.keys())) if (!live.has(t)) this.terms.delete(t);
  }

  private async resolvePids(terminals: vscode.Terminal[]): Promise<void> {
    await Promise.all(terminals.map(async (t) => {
      let pid: number | undefined;
      try {
        const p = await t.processId;
        if (typeof p === 'number' && p > 0) pid = p;
      } catch { /* unresolvable */ }
      this.terms.set(t, { pid, attempts: 0 });
    }));
  }

  private async resolveTtys(psPath: string, pending: vscode.Terminal[]): Promise<void> {
    if (pending.length === 0) return;
    const rows = await this.runPs(psPath, pending.map((t) => (this.terms.get(t) as TermState).pid as number));
    for (const t of pending) {
      const s = this.terms.get(t);
      if (!s) continue; // closed while ps ran
      // ps itself failed: that says nothing about the pid, so retry rather than mark it dead.
      this.terms.set(t, rows ? applyPsRows(s, rows) : { ...s, attempts: s.attempts + 1 });
    }
  }

  private scheduleRetry(): void {
    if (this.disposed || this.timer) return;
    let attempt = Infinity;
    for (const t of vscode.window.terminals) {
      const s = this.terms.get(t);
      if (isUnresolved(s)) attempt = Math.min(attempt, s?.attempts ?? 0);
    }
    const delay = nextRetryDelay(attempt);
    if (delay === undefined) return;
    this.timer = setTimeout(() => { this.timer = undefined; void this.resolve(); }, delay);
  }

  private runPs(psPath: string, pids: number[]): Promise<Map<number, string | undefined> | undefined> {
    return new Promise((resolve) => {
      execFile(psPath, ['-o', 'pid=,tty=', '-p', pids.join(',')], (err, stdout, stderr) => {
        const out = String(stdout);
        if (isPsError(err, out, String(stderr))) {
          this.logPsFailureOnce(err?.message ?? String(stderr));
          resolve(undefined);
          return;
        }
        resolve(parsePsTtyOutput(out));
      });
    });
  }

  private logPsFailureOnce(detail: string): void {
    if (this.psFailureLogged) return;
    this.psFailureLogged = true;
    this.log(`ps failed, terminal pairing unavailable: ${detail}`);
  }

  private recompute(): void {
    if (this.disposed) return;
    const terminals = vscode.window.terminals;
    const paired = pairTerminals(
      terminals.map((t) => ({ key: t, tty: this.terms.get(t)?.tty })),
      this.store.topLevel(),
    );
    const next: Pairing[] = [];
    for (const t of terminals) {
      const session = paired.get(t);
      if (session) next.push({ terminal: t, session });
    }
    const sig = next.map((p) => `${terminals.indexOf(p.terminal)}:${p.session.session_id}:${p.session.state}:${p.session.project_name ?? ''}`).join('|');
    this.current = next;
    if (sig !== this.signature) {
      this.signature = sig;
      this.emitter.fire();
    }
  }

  dispose(): void {
    this.disposed = true;
    if (this.timer) clearTimeout(this.timer);
    for (const d of this.disposables) d.dispose();
    this.emitter.dispose();
  }
}

export class AgentTerminalsView implements vscode.Disposable, vscode.TreeDataProvider<Pairing> {
  private readonly changed = new vscode.EventEmitter<void>();
  readonly onDidChangeTreeData: vscode.Event<void> = this.changed.event;
  private readonly treeView: vscode.TreeView<Pairing>;
  private readonly disposables: Array<{ dispose(): void }> = [];

  constructor(private readonly pairing: TerminalPairing) {
    this.treeView = vscode.window.createTreeView('irrlicht.agentTerminals', { treeDataProvider: this });
    this.disposables.push(
      this.treeView,
      this.changed,
      vscode.commands.registerCommand('irrlicht.agentTerminals.show', (t: vscode.Terminal) => t.show(false)),
      pairing.onDidChange(() => this.refresh()),
    );
    this.refresh();
  }

  getChildren(element?: Pairing): Pairing[] { return element ? [] : this.pairing.pairings(); }

  getTreeItem({ terminal, session }: Pairing): vscode.TreeItem {
    const item = new vscode.TreeItem(terminal.name, vscode.TreeItemCollapsibleState.None);
    const state = String(session.state);
    item.description = session.project_name ? `${state} · ${session.project_name}` : state;
    const detail = [session.adapter, session.model].filter(Boolean).join(' / ');
    item.tooltip = `State: ${state}` + (detail ? `\n${detail}` : '');
    item.iconPath = isStateName(session.state)
      ? new vscode.ThemeIcon('primitive-square', new vscode.ThemeColor(stateColorId(session.state)))
      : new vscode.ThemeIcon('primitive-square');
    item.command = { command: 'irrlicht.agentTerminals.show', title: 'Show Terminal', arguments: [terminal] };
    return item;
  }

  private refresh(): void {
    const pairs = this.pairing.pairings();
    const n = pairs.filter((p) => p.session.state === 'waiting').length;
    this.treeView.badge = n > 0 ? { value: n, tooltip: `${n} agent terminal${n === 1 ? '' : 's'} waiting for input` } : undefined;
    this.treeView.message = pairs.length === 0 ? 'No integrated terminal in this window hosts an agent session.' : undefined;
    this.changed.fire();
  }

  dispose(): void { for (const d of this.disposables) d.dispose(); }
}
