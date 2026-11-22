import * as vscode from 'vscode';
import { resolveDaemonUrl, type DaemonUrlSource, type ResolvedDaemonUrl } from './config';
import { DaemonClient, type ConnectionState } from './daemonClient';
import { Emitter } from './emitter';
import { SessionStore } from './sessionStore';
import { createStatusBar } from './statusBar';
import { TerminalFrameController } from './terminalFrameController';
import { AgentTerminalsView, TerminalPairing } from './terminals';
import { DashboardPanel, DashboardViewProvider } from './webview';

export interface IrrlichtTestApi {
  getStatusText(): string;
  getConnectionState(): ConnectionState;
  getPairings(): Array<{ terminalName: string; sessionId: string; state: string }>;
}

let frameController: TerminalFrameController | undefined;

export function activate(ctx: vscode.ExtensionContext): IrrlichtTestApi | undefined {
  const out = vscode.window.createOutputChannel('Irrlicht');
  const log = (m: string): void => out.appendLine(m);

  // The daemon URL is resolved here only: once now, and again when the setting changes.
  const resolve = (): ResolvedDaemonUrl => {
    const resolved = resolveDaemonUrl(vscode.workspace.getConfiguration('irrlicht').get('daemonUrl'));
    if (resolved.warning) void vscode.window.showWarningMessage(resolved.warning);
    return resolved;
  };
  let url = resolve();
  const originChanged = new Emitter<ResolvedDaemonUrl>((e) => log(`url listener failed: ${String(e)}`));
  const urls: DaemonUrlSource = { current: () => url, onDidChangeOrigin: originChanged.event };

  const store = new SessionStore();
  const client = new DaemonClient({ getUrl: urls.current, store, log });
  const pairing = new TerminalPairing(store, log);
  pairing.start();
  frameController = new TerminalFrameController(pairing, ctx.workspaceState, log);
  frameController.start();
  const statusBar = createStatusBar(store, client, () => url.origin);
  const terminalsView = new AgentTerminalsView(pairing);

  ctx.subscriptions.push(
    out,
    pairing,
    frameController,
    statusBar,
    terminalsView,
    vscode.commands.registerCommand('irrlicht.openPanel', () => DashboardPanel.show(ctx, client, urls)),
    vscode.window.registerWebviewViewProvider(DashboardViewProvider.viewId, new DashboardViewProvider(ctx, client, urls), {
      webviewOptions: { retainContextWhenHidden: true },
    }),
    vscode.workspace.onDidChangeConfiguration((e) => {
      if (!e.affectsConfiguration('irrlicht.daemonUrl')) return;
      const next = resolve();
      const changed = next.origin !== url.origin;
      url = next;
      if (!changed) return;
      client.restart();
      originChanged.fire(next);
    }),
    client.onFocusRequested((s) => pairing.terminalForSession(s)?.show(false)),
    { dispose: () => client.stop() },
  );

  client.start();

  if (ctx.extensionMode !== vscode.ExtensionMode.Test) return undefined;
  return {
    getStatusText: () => statusBar.text(),
    getConnectionState: () => client.state,
    getPairings: () =>
      pairing.pairings().map((p) => ({ terminalName: p.terminal.name, sessionId: p.session.session_id, state: String(p.session.state) })),
  };
}

// Best effort: puts the user's colour customizations back; the memo lets the next activation finish it.
export function deactivate(): Promise<void> | undefined {
  return frameController?.restore();
}
