import * as vscode from 'vscode';
import type { ResolvedDaemonUrl } from './config';
import type { DaemonClient } from './daemonClient';
import type { SessionStore } from './sessionStore';
import { formatStatus } from './statusText';

export function createStatusBar(store: SessionStore, client: DaemonClient, getUrl: () => ResolvedDaemonUrl): vscode.Disposable & { text(): string } {
  const item = vscode.window.createStatusBarItem(vscode.StatusBarAlignment.Left, 100);
  item.command = 'irrlicht.openPanel';
  const render = (): void => {
    const { text, tooltip } = formatStatus(store.counts(), client.state, getUrl().url);
    item.text = text;
    item.tooltip = tooltip;
  };
  render();
  item.show();
  const subs = [store.onDidChange(render), client.onStatus(render)];
  return {
    text: () => item.text,
    dispose: () => {
      for (const s of subs) s.dispose();
      item.dispose();
    },
  };
}
