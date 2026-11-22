import * as vscode from 'vscode';
import { desiredFrame, FrameSync, parseFrameMode, themeKindOf, type FrameIO, type FrameLocation, type FrameMemo } from './terminalFrame';
import type { TerminalPairing } from './terminals';

const MEMO_KEY = 'irrlicht.terminalFrame.memo';
const NO_WORKSPACE_MESSAGE = 'terminal frame: no folder or workspace is open, so there is no workspace settings file to write; the frame stays off in this window';

// Feeds the active terminal's paired state into a FrameSync that owns the workspace's colour customizations.
export class TerminalFrameController implements vscode.Disposable {
  private sync: FrameSync | undefined;
  private noWorkspaceLogged = false;
  private readonly disposables: Array<{ dispose(): void }> = [];

  constructor(
    private readonly pairing: TerminalPairing,
    private readonly workspaceState: vscode.Memento,
    private readonly log: (m: string) => void,
  ) {}

  start(): void {
    const again = (): void => this.request();
    try {
      this.disposables.push(
        vscode.window.onDidChangeActiveTerminal(again),
        this.pairing.onDidChange(again),
        vscode.workspace.onDidChangeConfiguration((e) => { if (e.affectsConfiguration('irrlicht.terminalFrame')) this.request(); }),
        vscode.window.onDidChangeActiveColorTheme(again),
        vscode.window.tabGroups.onDidChangeTabs(again),
        vscode.window.tabGroups.onDidChangeTabGroups(again),
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
    for (const d of this.disposables.splice(0)) {
      try { d.dispose(); } catch { /* ignore */ }
    }
    this.sync?.dispose();
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
    const mode = parseFrameMode(vscode.workspace.getConfiguration('irrlicht').get('terminalFrame'));
    const t = vscode.window.activeTerminal;
    const state = t ? this.pairing.pairings().find((p) => p.terminal === t)?.session.state : undefined;
    return desiredFrame({
      mode,
      state,
      kind: themeKindOf(vscode.window.activeColorTheme.kind),
      location: t ? locationOf(t) : 'panel',
      customizations: vscode.workspace.getConfiguration('workbench').get('colorCustomizations'),
    });
  }
}

function locationOf(t: vscode.Terminal): FrameLocation {
  for (const group of vscode.window.tabGroups.all) {
    const tab = group.activeTab;
    if (tab && tab.input instanceof vscode.TabInputTerminal && tab.label === t.name) return 'editor';
  }
  return 'panel';
}
