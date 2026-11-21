import { execFile } from 'node:child_process';
import { existsSync } from 'node:fs';
import * as vscode from 'vscode';
import type { SessionRecord, SessionStore } from './sessionStore';
import { isStateName, stateColorId } from './states';
import { classifyPsFailure, nextRetryDelay, normalizeTty, pairTerminals, parsePsTtyOutput, resolvePsPath } from './tty';

interface Pairing { terminal: vscode.Terminal; session: SessionRecord }

export class TerminalPairing implements vscode.Disposable {
  private readonly emitter = new vscode.EventEmitter<void>();
  readonly onDidChange: vscode.Event<void> = this.emitter.event;
  // Resolved TTYs only; a terminal absent here is either not looked up yet or still unresolved.
  private readonly ttys = new Map<vscode.Terminal, string>();
  // Shell pid per terminal once processId has been awaited (undefined = unresolvable, never retried).
  private readonly pidOf = new Map<vscode.Terminal, number | undefined>();
  private readonly attempts = new Map<vscode.Terminal, number>();
  private current: Pairing[] = [];
  private signature = '';
  private psFailureLogged = false;
  // Resolved once from fixed directories; undefined = pairing unavailable (also always on win32).
  private readonly psPath = process.platform === 'win32' ? undefined : resolvePsPath(existsSync);
  private running = false;
  private again = false;
  private lastPassEnd = 0;
  private timer: ReturnType<typeof setTimeout> | undefined;
  private disposed = false;
  private readonly disposables: Array<{ dispose(): void }> = [];

  constructor(private readonly store: SessionStore, private readonly log: (m: string) => void) {
    this.disposables.push(
      store.onDidChange(() => {
        this.recompute();
        // A new session is exactly when an unresolved terminal may need pairing; throttled.
        if (this.hasUnresolved() && Date.now() - this.lastPassEnd >= 500) void this.resolve();
      }),
      vscode.window.onDidOpenTerminal(() => void this.resolve()),
      vscode.window.onDidCloseTerminal(() => void this.resolve()),
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
      if (this.ttys.get(t) === tty) return t;
    }
    return undefined;
  }

  private hasUnresolved(): boolean {
    for (const t of vscode.window.terminals) {
      if (this.pidOf.get(t) !== undefined && !this.ttys.has(t)) return true;
    }
    return false;
  }

  // Passes are serialized; a request arriving mid-pass schedules exactly one more.
  private async resolve(): Promise<void> {
    if (this.disposed) return;
    if (this.running) { this.again = true; return; }
    this.running = true;
    try {
      do {
        this.again = false;
        await this.pass();
      } while (this.again && !this.disposed);
    } finally {
      this.running = false;
      this.lastPassEnd = Date.now();
    }
    this.scheduleRetry();
  }

  private async pass(): Promise<void> {
    if (this.timer) { clearTimeout(this.timer); this.timer = undefined; }
    const live = new Set(vscode.window.terminals);
    this.forgetClosed(live);
    await this.resolvePids([...live].filter((t) => !this.pidOf.has(t)));
    await this.resolveTtys([...live].filter((t) => this.isUnresolved(t)));
    this.recompute();
  }

  private forgetClosed(live: Set<vscode.Terminal>): void {
    for (const m of [this.ttys, this.pidOf, this.attempts] as Array<Map<vscode.Terminal, unknown>>) {
      for (const t of Array.from(m.keys())) if (!live.has(t)) m.delete(t);
    }
  }

  // A terminal with a known shell pid whose TTY has not been found yet.
  private isUnresolved(t: vscode.Terminal): boolean {
    return this.pidOf.get(t) !== undefined && !this.ttys.has(t);
  }

  private async resolvePids(terminals: vscode.Terminal[]): Promise<void> {
    await Promise.all(terminals.map(async (t) => {
      let pid: number | undefined;
      try {
        const p = await t.processId;
        if (typeof p === 'number' && p > 0) pid = p;
      } catch { /* unresolvable */ }
      // Without ps (win32, or none found) a terminal can never be paired: mark it
      // unresolvable so it is never retried.
      this.pidOf.set(t, this.psPath ? pid : undefined);
    }));
  }

  private async resolveTtys(pending: vscode.Terminal[]): Promise<void> {
    if (pending.length === 0) return;
    const byPid = await this.runPs(pending.map((t) => this.pidOf.get(t) as number));
    for (const t of pending) {
      const tty = byPid.get(this.pidOf.get(t) as number);
      if (tty) { this.ttys.set(t, tty); this.attempts.delete(t); }
      else this.attempts.set(t, (this.attempts.get(t) ?? 0) + 1);
    }
  }

  private scheduleRetry(): void {
    if (this.disposed || this.timer) return;
    let attempt = Infinity;
    for (const t of vscode.window.terminals) {
      if (this.isUnresolved(t)) attempt = Math.min(attempt, this.attempts.get(t) ?? 0);
    }
    const delay = nextRetryDelay(attempt);
    if (delay === undefined) return;
    this.timer = setTimeout(() => { this.timer = undefined; void this.resolve(); }, delay);
  }

  private runPs(pids: number[]): Promise<Map<number, string>> {
    return new Promise((resolve) => {
      if (!this.psPath) { resolve(new Map()); return; }
      execFile(this.psPath, ['-o', 'pid=,tty=', '-p', pids.join(',')], (err, stdout, stderr) => {
        // ps exits 1 with no output when none of the pids exist (a dead shell); that is
        // not a failure. A spawn error or stderr text is.
        const out = String(stdout);
        if (classifyPsFailure(err, out, String(stderr)) === 'error') this.logPsFailureOnce(err?.message ?? String(stderr));
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
      terminals.map((t) => ({ key: t, tty: this.ttys.get(t) })),
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
