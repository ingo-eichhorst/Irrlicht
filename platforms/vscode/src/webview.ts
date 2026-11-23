import { randomBytes } from 'node:crypto';
import * as vscode from 'vscode';
import type { DaemonUrlSource } from './config';
import type { DaemonClient } from './daemonClient';
import { buildWrapperHtml } from './webviewHtml';

function newNonce(): string {
  return randomBytes(16).toString('base64').replace(/[^A-Za-z0-9]/g, '');
}

function render(webview: vscode.Webview, origin: string): void {
  webview.html = buildWrapperHtml({ daemonOrigin: origin, cspSource: webview.cspSource, nonce: newNonce() });
}

/** Wires a webview to the daemon client: renders the wrapper, relays connectivity, re-renders on origin change. */
function attach(webview: vscode.Webview, client: DaemonClient, urls: DaemonUrlSource): vscode.Disposable {
  const post = (): void => {
    const state = client.state;
    // 'connecting' sends nothing so the initial connecting notice is not replaced by offline wording.
    if (state === 'connecting') return;
    void webview.postMessage({ type: 'daemon', connected: state === 'online', url: urls.current().origin });
  };
  render(webview, urls.current().origin);
  return vscode.Disposable.from(
    webview.onDidReceiveMessage((m: unknown) => {
      if ((m as { type?: unknown } | null)?.type === 'ready') post();
    }),
    client.onStatus(() => post()),
    // CSP frame-src changes with the origin; the re-rendered page re-posts 'ready'.
    urls.onDidChangeOrigin((u) => render(webview, u.origin)),
  );
}

export class DashboardPanel {
  private static current: DashboardPanel | undefined;

  static show(ctx: vscode.ExtensionContext, client: DaemonClient, urls: DaemonUrlSource): void {
    if (DashboardPanel.current) {
      DashboardPanel.current.panel.reveal();
      return;
    }
    const media = vscode.Uri.joinPath(ctx.extensionUri, 'media');
    const panel = vscode.window.createWebviewPanel('irrlicht.panel', 'Irrlicht', vscode.ViewColumn.Active, {
      enableScripts: true,
      retainContextWhenHidden: true,
      localResourceRoots: [media],
    });
    panel.iconPath = vscode.Uri.joinPath(media, 'irrlicht.svg');
    DashboardPanel.current = new DashboardPanel(panel, client, urls);
  }

  private readonly sub: vscode.Disposable;

  private constructor(private readonly panel: vscode.WebviewPanel, client: DaemonClient, urls: DaemonUrlSource) {
    this.sub = attach(panel.webview, client, urls);
    panel.onDidDispose(() => {
      this.sub.dispose();
      DashboardPanel.current = undefined;
    });
  }
}

export class DashboardViewProvider implements vscode.WebviewViewProvider {
  static readonly viewId = 'irrlicht.dashboard';

  constructor(
    private readonly ctx: vscode.ExtensionContext,
    private readonly client: DaemonClient,
    private readonly urls: DaemonUrlSource,
  ) {}

  resolveWebviewView(view: vscode.WebviewView): void {
    view.webview.options = {
      enableScripts: true,
      localResourceRoots: [vscode.Uri.joinPath(this.ctx.extensionUri, 'media')],
    };
    const sub = attach(view.webview, this.client, this.urls);
    view.onDidDispose(() => sub.dispose());
  }
}
