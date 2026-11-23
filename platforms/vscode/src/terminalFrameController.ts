import * as vscode from 'vscode';
import { desiredFrame, FrameSync, parseFrameMode, tabEdgesDrawn, themeKindOf, type FrameIO, type FrameMemo } from './terminalFrame';
import { BIND_WINDOW_MS, TerminalLocator, type Layout } from './terminalLocator';
import type { TerminalPairing } from './terminals';

const MEMO_KEY = 'irrlicht.terminalFrame.memo';
const NO_WORKSPACE_MESSAGE = 'terminal frame: no folder or workspace is open, so there is no workspace settings file to write; the frame stays off in this window';

type VsLayout = Layout<vscode.Tab, vscode.Terminal>;

// Frames the panel and the editor area independently, each by the paired agent terminals it shows, through a
// FrameSync that owns the workspace's colour customizations. Terminals are located by TerminalLocator.
export class TerminalFrameController implements vscode.Disposable {
  private sync: FrameSync | undefined;
  private noWorkspaceLogged = false;
  // A pending binding re-frames once its window has passed, even if no other event arrives.
  private readonly locator = new TerminalLocator<vscode.Tab, vscode.Terminal>(Date.now, BIND_WINDOW_MS, {
    timers: { set: (fn, ms) => setTimeout(fn, ms), clear: (h) => clearTimeout(h as ReturnType<typeof setTimeout>) },
    onDue: () => this.request(),
  });
  private readonly disposables: Array<{ dispose(): void }> = [];

  constructor(
    private readonly pairing: TerminalPairing,
    private readonly workspaceState: vscode.Memento,
    private readonly log: (m: string) => void,
  ) {}

  start(): void {
    const again = (): void => this.request();
    const tabsChanged = (): void => {
      this.guard(() => { this.locator.observeTabs(layout()); });
      this.request();
    };
    try {
      this.locator.prime(layout());
      this.disposables.push(
        vscode.window.onDidChangeActiveTerminal(() => {
          this.guard(() => this.locator.observeActiveTerminal(layout()));
          this.request();
        }),
        vscode.window.onDidOpenTerminal(again),
        vscode.window.onDidCloseTerminal(again),
        this.pairing.onDidChange(again),
        vscode.workspace.onDidChangeConfiguration((e) => {
          if (e.affectsConfiguration('irrlicht.terminalFrame') || e.affectsConfiguration('workbench.experimental.modernUI')) this.request();
        }),
        vscode.window.onDidChangeActiveColorTheme(again),
        vscode.window.tabGroups.onDidChangeTabs(tabsChanged),
        vscode.window.tabGroups.onDidChangeTabGroups(tabsChanged),
      );
    } catch (e) {
      this.log(`terminal frame: could not subscribe to events: ${String(e)}`);
    }
    this.request();
  }

  async restore(): Promise<void> {
    try {
      await this.sync?.restore();
    } catch (e) {
      this.log(`terminal frame: restore failed: ${String(e)}`);
    }
  }

  dispose(): void {
    this.locator.dispose();
    for (const d of this.disposables.splice(0)) {
      try { d.dispose(); } catch { /* ignore */ }
    }
    this.sync?.dispose();
  }

  /** The locator's state and the places it computes now; only for the e2e's failure messages. */
  debugState(): unknown {
    const current = layout();
    const describe = (x: vscode.Tab | vscode.Terminal): string => ('input' in x ? `tab:${x.label}@${x.group.viewColumn}` : `term:${x.name}#${vscode.window.terminals.indexOf(x)}`);
    const places = [...this.locator.locate(current)].map(([t, p]) => [describe(t), p]);
    return { locator: this.locator.debugState(describe), places, active: current.activeTerminal && describe(current.activeTerminal) };
  }

  private guard(fn: () => void): void {
    try { fn(); } catch (e) { this.log(`terminal frame: ${String(e)}`); }
  }

  private hasWorkspace(): boolean {
    return Boolean(vscode.workspace.workspaceFolders?.length) || Boolean(vscode.workspace.workspaceFile);
  }

  private getSync(): FrameSync | undefined {
    if (this.sync) return this.sync;
    if (!this.hasWorkspace()) {
      if (!this.noWorkspaceLogged) {
        this.noWorkspaceLogged = true;
        this.log(NO_WORKSPACE_MESSAGE);
      }
      return undefined;
    }
    const io: FrameIO = {
      read: () => vscode.workspace.getConfiguration('workbench').inspect('colorCustomizations')?.workspaceValue,
      write: (next) => Promise.resolve(vscode.workspace.getConfiguration('workbench').update('colorCustomizations', next, vscode.ConfigurationTarget.Workspace)),
      loadMemo: () => this.workspaceState.get<FrameMemo>(MEMO_KEY),
      saveMemo: (memo) => Promise.resolve(this.workspaceState.update(MEMO_KEY, memo)),
      log: this.log,
    };
    this.sync = new FrameSync(io);
    return this.sync;
  }

  private request(): void {
    try {
      const sync = this.getSync();
      if (!sync) return;
      sync.request(this.compute());
    } catch (e) {
      this.log(`terminal frame: ${String(e)}`);
    }
  }

  private compute(): Record<string, string> {
    const current = layout();
    const places = this.locator.locate(current);
    const stateOf = new Map(this.pairing.pairings().map((p) => [p.terminal, p.session.state] as const));
    const panel: unknown[] = [];
    const focused: unknown[] = [];
    const unfocused: unknown[] = [];
    // Plain terminals have no paired state and count nowhere; unknown places count nowhere either.
    for (const [terminal, place] of places) {
      if (!stateOf.has(terminal)) continue;
      const state = stateOf.get(terminal);
      if (place.area === 'panel') panel.push(state);
      else if (place.area === 'editor' && place.front) (current.groups[place.group].active ? focused : unfocused).push(state);
    }
    const workbench = vscode.workspace.getConfiguration('workbench');
    return desiredFrame({
      mode: parseFrameMode(vscode.workspace.getConfiguration('irrlicht').get('terminalFrame')),
      kind: themeKindOf(vscode.window.activeColorTheme.kind),
      panel,
      editor: {
        focused,
        unfocused,
        groupCount: current.groups.length,
        tabEdges: tabEdgesDrawn(vscode.version, workbench.get('experimental.modernUI')),
      },
      customizations: workbench.get('colorCustomizations'),
    });
  }
}

const isTerminalTab = (tab: vscode.Tab): boolean => tab.input instanceof vscode.TabInputTerminal;

/** The window as the locator sees it. */
function layout(): VsLayout {
  return {
    groups: vscode.window.tabGroups.all.map((g) => ({
      terminalTabs: g.tabs.filter(isTerminalTab).map((tab) => ({ tab, label: tab.label })),
      front: g.activeTab && isTerminalTab(g.activeTab) ? g.activeTab : undefined,
      active: g.isActive,
    })),
    terminals: vscode.window.terminals.map((terminal) => ({ terminal, name: terminal.name })),
    activeTerminal: vscode.window.activeTerminal,
  };
}
