import * as vscode from 'vscode';
import { resolveDaemonUrl, type ResolvedDaemonUrl } from './config';
import { DaemonClient } from './daemonClient';
import { SessionStore } from './sessionStore';
import { createStatusBar } from './statusBar';
import type { ConnectionState } from './statusText';
import { AgentTerminalsView, TerminalPairing } from './terminals';
import { DashboardPanel, DashboardViewProvider } from './webview';

export interface IrrlichtTestApi {
  getStatusText(): string;
  getConnectionState(): ConnectionState;
  getPairings(): Array<{ terminalName: string; sessionId: string; state: string }>;
}

export function activate(ctx: vscode.ExtensionContext): IrrlichtTestApi {
  const out = vscode.window.createOutputChannel('Irrlicht');
  const log = (m: string): void => out.appendLine(m);
  let warned = false;

  const getUrl = (): ResolvedDaemonUrl => {
    const raw = vscode.workspace.getConfiguration('irrlicht').get('daemonUrl');
    const resolved = resolveDaemonUrl(raw);
    if (resolved.warning && !warned) {
      warned = true;
      void vscode.window.showWarningMessage(resolved.warning);
    }
    return resolved;
  };

  const store = new SessionStore();
  const client = new DaemonClient({ getUrl, store, log });
  const pairing = new TerminalPairing(store, log);
  const statusBar = createStatusBar(store, client, getUrl);
  const terminalsView = new AgentTerminalsView(pairing);

  ctx.subscriptions.push(
    out,
    pairing,
    statusBar,
    terminalsView,
    vscode.commands.registerCommand('irrlicht.openPanel', () => DashboardPanel.show(ctx, client, getUrl)),
    vscode.window.registerWebviewViewProvider(DashboardViewProvider.viewId, new DashboardViewProvider(ctx, client, getUrl), {
      webviewOptions: { retainContextWhenHidden: true },
    }),
    vscode.workspace.onDidChangeConfiguration((e) => {
      if (e.affectsConfiguration('irrlicht.daemonUrl')) {
        warned = false;
        client.restart();
      }
    }),
    client.onFocusRequested((s) => pairing.terminalForSession(s)?.show(false)),
    { dispose: () => client.stop() },
  );

  client.start();

  return {
    getStatusText: () => statusBar.text(),
    getConnectionState: () => client.state,
    getPairings: () =>
      pairing.pairings().map((p) => ({ terminalName: p.terminal.name, sessionId: p.session.session_id, state: String(p.session.state) })),
  };
}

export function deactivate(): void {}
