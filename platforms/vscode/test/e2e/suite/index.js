'use strict';
// Runs inside the VS Code extension host. Plain CommonJS, no compile step.
const http = require('node:http');
const path = require('node:path');
const { execFile } = require('node:child_process');
const { promisify } = require('node:util');
const vscode = require('vscode');
const { WebSocketServer } = require('ws');

const execFileP = promisify(execFile);
const t0 = Date.now();

function step(name, since) {
  console.log(`[e2e] PASS ${name} (+${Date.now() - since} ms, total ${Date.now() - t0} ms)`);
}

async function poll(name, fn, observed, timeoutMs = 20000) {
  const start = Date.now();
  for (;;) {
    let ok = false;
    try { ok = await fn(); } catch { ok = false; }
    if (ok) return Date.now() - start;
    if (Date.now() - start > timeoutMs) {
      throw new Error(`e2e step "${name}" timed out after ${Date.now() - start} ms; observed: ${observed()}`);
    }
    await new Promise((r) => setTimeout(r, 100));
  }
}

const SEED = {
  'panel.border': '#123456',
  'statusBar.background': '#654321',
  '[Default Dark Modern]': { 'terminal.border': '#ABCDEF' },
};

function deepEqual(a, b) {
  if (a === b) return true;
  if (typeof a !== 'object' || typeof b !== 'object' || a === null || b === null) return false;
  if (Array.isArray(a) !== Array.isArray(b)) return false;
  const ka = Object.keys(a);
  const kb = Object.keys(b);
  return ka.length === kb.length && ka.every((k) => Object.hasOwn(b, k) && deepEqual(a[k], b[k]));
}

const workspaceColors = () => vscode.workspace.getConfiguration('workbench').inspect('colorCustomizations')?.workspaceValue;
const show = () => JSON.stringify(workspaceColors());

// A check that cannot see its input must not pass: the seed has to be visible before anything else runs.
function assertSeed() {
  const folder = vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
  if (!folder) throw new Error('e2e: no workspace folder is open; the terminal frame cannot be tested');
  const extRoot = path.resolve(__dirname, '..', '..', '..');
  const rel = path.relative(extRoot, folder);
  if (rel === '' || (!rel.startsWith('..') && !path.isAbsolute(rel))) {
    throw new Error(`e2e: workspace folder ${folder} is inside the extension root ${extRoot}; refusing to write into the repository`);
  }
  if (!deepEqual(workspaceColors(), SEED)) {
    throw new Error(`e2e: workspace colorCustomizations is not the seed; saw ${show()}`);
  }
}

class StubDaemon {
  constructor() {
    this.requests = [];
    this.groups = [];
    this.server = null;
    this.wss = null;
    this.port = 0;
  }

  async start(port) {
    const server = http.createServer((req, res) => {
      this.requests.push({ method: req.method, url: req.url, dest: req.headers['sec-fetch-dest'] });
      const pathname = String(req.url).split('?')[0];
      if (req.method === 'GET' && pathname === '/') {
        res.writeHead(200, { 'content-type': 'text/html' });
        res.end('<!doctype html><title>stub</title><p>stub dashboard</p>');
      } else if (req.method === 'GET' && pathname === '/api/v1/sessions') {
        res.writeHead(200, { 'content-type': 'application/json' });
        res.end(JSON.stringify({ groups: this.groups }));
      } else if (req.method === 'POST' && /^\/api\/v1\/sessions\/[^/]+\/focus$/.test(pathname)) {
        res.writeHead(200);
        res.end();
      } else {
        res.writeHead(404);
        res.end();
      }
    });
    this.wss = new WebSocketServer({ server, path: '/api/v1/sessions/stream' });
    await new Promise((resolve, reject) => {
      server.once('error', reject);
      server.listen(port || 0, '127.0.0.1', () => { server.off('error', reject); resolve(); });
    });
    this.server = server;
    this.port = server.address().port;
  }

  push(frame) {
    const data = JSON.stringify(frame);
    for (const c of this.wss.clients) c.send(data);
  }

  async stop() {
    for (const c of this.wss.clients) c.terminate();
    await new Promise((r) => this.wss.close(r));
    const closed = new Promise((r) => this.server.close(r));
    this.server.closeAllConnections();
    await closed;
  }
}

function session(id, state, tty) {
  return { session_id: id, state, updated_at: Math.floor(Date.now() / 1000), project_name: 'e2e', launcher: tty ? { tty } : undefined };
}

async function ttyOf(pid) {
  let last = '';
  await poll(`tty of pid ${pid}`, async () => {
    const { stdout } = await execFileP('ps', ['-o', 'tty=', '-p', String(pid)]);
    last = stdout.trim();
    return last && last !== '??';
  }, () => `last ps output: ${JSON.stringify(last)}`, 10000);
  return last.startsWith('/dev/') ? last : `/dev/${last}`;
}

const COLOR = { working: '#8B5CF6', waiting: '#FF9500', ready: '#34C759' };
const customizations = () => workspaceColors() ?? {};
const frameIs = (c) => { const v = customizations(); return v['modernPanel.border'] === c && v['panel.border'] === c; };
const tabsText = () => {
  const groups = vscode.window.tabGroups.all.map((g) => g.tabs.map((t) => t.label + (t.isActive ? '*' : '')));
  return JSON.stringify(groups);
};

async function frameInEditor({ terminal }) {
  // Editor location: the frame moves to the editor keys and the panel keys go back to the user's values.
  await vscode.commands.executeCommand('workbench.action.terminal.moveToEditor');
  await poll('4g terminal in editor frames the editor', () => {
    const c = customizations();
    return c['editor.border'] === COLOR.waiting && c['tab.activeBorderTop'] === COLOR.waiting
      && c['panel.border'] === '#123456' && !('modernPanel.border' in c) && !('terminal.border' in c);
  }, () => `active=${vscode.window.activeTerminal?.name} workspace colorCustomizations=${show()}`);
  // A tab opened in front of the terminal hides it but must not move the frame back to the panel.
  const termGroup = vscode.window.tabGroups.all.find((g) => g.tabs.some((t) => t.input instanceof vscode.TabInputTerminal && t.label === 'e2e-agent'));
  if (!termGroup) throw new Error(`4g terminal tab not found in any group: ${show()}`);
  const readmeUri = vscode.Uri.joinPath(vscode.workspace.workspaceFolders[0].uri, 'README.md');
  await vscode.window.showTextDocument(readmeUri, { preview: false, viewColumn: termGroup.viewColumn, preserveFocus: false });
  const readmeTab = () => vscode.window.tabGroups.all.flatMap((g) => g.tabs)
    .find((t) => t.input instanceof vscode.TabInputText && t.input.uri.path.endsWith('/README.md'));
  try {
    await poll('4g README.md is the active tab in front of the terminal', () => readmeTab()?.isActive === true,
      () => `tabs=${tabsText()}`);
    console.log(`[e2e] 4g active terminal with README in front: ${vscode.window.activeTerminal?.name}`);
    // The frame is debounced, so a wrong re-frame lands ~200 ms after the tab change: the editor frame must HOLD past that.
    let heldSince = 0;
    await poll('4g hidden editor terminal still frames the editor', () => {
      const c = customizations();
      const ok = c['editor.border'] === COLOR.waiting && c['tab.activeBorderTop'] === COLOR.waiting
        && c['panel.border'] === '#123456' && !('modernPanel.border' in c) && !('terminal.border' in c);
      if (!ok) { heldSince = 0; return false; }
      if (!heldSince) heldSince = Date.now();
      return Date.now() - heldSince >= 1000;
    }, () => `active=${vscode.window.activeTerminal?.name} workspace colorCustomizations=${show()}`, 5000);
  } finally {
    const t = readmeTab();
    if (t) await vscode.window.tabGroups.close(t);
  }
  await poll('4g e2e-agent tab active again', () => vscode.window.tabGroups.all.some((g) => g.activeTab?.input instanceof vscode.TabInputTerminal && g.activeTab.label === 'e2e-agent'),
    () => `tabs=${tabsText()}`);
  await vscode.commands.executeCommand('workbench.action.terminal.moveToTerminalPanel');
  await poll('4g terminal back in panel frames the panel', () => {
    const c = customizations();
    return frameIs(COLOR.waiting) && !('editor.border' in c) && !('tab.activeBorderTop' in c);
  }, () => `active=${vscode.window.activeTerminal?.name} workspace colorCustomizations=${show()}`);
  terminal.show();
  await poll('4g e2e-agent active in the panel', () => vscode.window.activeTerminal?.name === 'e2e-agent',
    () => `active=${vscode.window.activeTerminal?.name}`);
}

async function stepTerminalFrame({ stub, api, terminal, tty, cfg }) {
  let plain;
  try {
    terminal.show();
    await poll('4g e2e-agent active', () => vscode.window.activeTerminal?.name === 'e2e-agent',
      () => `active=${vscode.window.activeTerminal?.name}`);
    const pushedAt = Date.now();
    stub.push({ type: 'session_updated', session: session('e2e-agent-session', 'working', tty), seq: 5 });
    await poll('4g working frame', () => frameIs(COLOR.working),
      () => `workspace colorCustomizations=${show()} pairings=${JSON.stringify(api.getPairings())}`);
    const latency = Date.now() - pushedAt;
    if (latency > 2000) throw new Error(`e2e step "4g" frame took ${latency} ms after the push (limit 2000 ms)`);
    console.log(`[e2e] 4g frame latency ${latency} ms`);
    const v = customizations();
    if (v['statusBar.background'] !== '#654321') throw new Error(`4g statusBar.background changed: ${show()}`);
    if (!deepEqual(v['[Default Dark Modern]'], SEED['[Default Dark Modern]'])) throw new Error(`4g theme block changed: ${show()}`);
    if ('activityBar.background' in v) throw new Error(`4g user-scope key leaked into the workspace: ${show()}`);

    stub.push({ type: 'session_updated', session: session('e2e-agent-session', 'waiting', tty), seq: 6 });
    await poll('4g waiting frame', () => frameIs(COLOR.waiting), () => `workspace colorCustomizations=${show()}`);

    plain = vscode.window.createTerminal({ name: 'e2e-plain' });
    plain.show();
    await poll('4g plain terminal restores user values', () => {
      const c = customizations();
      return c['panel.border'] === '#123456' && !('modernPanel.border' in c) && !('terminal.border' in c);
    }, () => `active=${vscode.window.activeTerminal?.name} workspace colorCustomizations=${show()}`);

    terminal.show();
    await poll('4g e2e-agent again waiting frame', () => frameIs(COLOR.waiting),
      () => `active=${vscode.window.activeTerminal?.name} workspace colorCustomizations=${show()}`);

    await frameInEditor({ terminal });
    await cfg.update('terminalFrame', 'off', vscode.ConfigurationTarget.Global);
    await poll('4g setting off restores user values', () => {
      const c = customizations();
      return c['panel.border'] === '#123456' && !('modernPanel.border' in c);
    }, () => `workspace colorCustomizations=${show()}`);

    await cfg.update('terminalFrame', 'border-and-tint', vscode.ConfigurationTarget.Global);
    await poll('4g border-and-tint tints', () => {
      const c = customizations();
      return c['terminal.background'] !== undefined && c['terminal.background'] === c['panel.background']
        && c['terminal.background'] !== COLOR.waiting && c['panel.border'] === COLOR.waiting;
    }, () => `workspace colorCustomizations=${show()}`);

    await cfg.update('terminalFrame', undefined, vscode.ConfigurationTarget.Global);
    await poll('4g default setting removes tint', () => {
      const c = customizations();
      return !('terminal.background' in c) && !('panel.background' in c) && frameIs(COLOR.waiting);
    }, () => `workspace colorCustomizations=${show()}`);
  } finally {
    try { if (plain) plain.dispose(); } catch { /* ignore */ }
    try { await cfg.update('terminalFrame', undefined, vscode.ConfigurationTarget.Global); } catch { /* ignore */ }
  }
}

async function run() {
  assertSeed();
  const stub = new StubDaemon();
  await stub.start(0);
  const cfg = vscode.workspace.getConfiguration('irrlicht');
  let terminal;
  let api;
  let s = Date.now();
  try {
    await cfg.update('daemonUrl', `http://127.0.0.1:${stub.port}`, vscode.ConfigurationTarget.Global);

    const ext = vscode.extensions.getExtension('irrlicht.irrlicht-vscode');
    if (!ext) throw new Error('extension irrlicht.irrlicht-vscode not found');
    api = await ext.activate();
    if (!api || typeof api.getConnectionState !== 'function') throw new Error('extension did not return the test API');
    step('activate', s);

    // a. connected
    s = Date.now();
    await poll('4a connection online', () => api.getConnectionState() === 'online', () => `state=${api.getConnectionState()}`);
    step('4a connection online', s);

    // b. webview iframe reached the daemon
    s = Date.now();
    await vscode.commands.executeCommand('irrlicht.openPanel');
    const iframeHit = () => stub.requests.some((r) => r.method === 'GET' && r.url === '/?embed=vscode' && r.dest === 'iframe');
    await poll('4b webview iframe GET /?embed=vscode', iframeHit,
      () => `requests seen by stub: ${JSON.stringify(stub.requests)}`);
    step('4b webview iframe reached daemon', s);

    // c. status bar counts
    s = Date.now();
    stub.push({ type: 'session_updated', session: session('e2e-wait', 'waiting'), seq: 1 });
    await poll('4c status text contains "$(bell) 1"', () => api.getStatusText().includes('$(bell) 1'),
      () => `status=${JSON.stringify(api.getStatusText())}`);
    step('4c status bar shows waiting count', s);

    // d. terminal pairing
    s = Date.now();
    terminal = vscode.window.createTerminal({ name: 'e2e-agent' });
    const pid = await terminal.processId;
    if (!pid) throw new Error('terminal has no processId');
    const tty = await ttyOf(pid);
    stub.push({ type: 'session_updated', session: session('e2e-agent-session', 'working', tty), seq: 2 });
    const pairing = () => api.getPairings().find((p) => p.terminalName === 'e2e-agent');
    await poll('4d pairing e2e-agent working', () => pairing()?.state === 'working',
      () => `tty=${tty} pairings=${JSON.stringify(api.getPairings())}`);
    stub.push({ type: 'session_updated', session: session('e2e-agent-session', 'ready', tty), seq: 3 });
    await poll('4d pairing e2e-agent ready', () => pairing()?.state === 'ready',
      () => `tty=${tty} pairings=${JSON.stringify(api.getPairings())}`);
    step('4d terminal pairing follows state', s);

    // f. click-to-focus: a focus_requested frame raises the paired terminal
    s = Date.now();
    const other = vscode.window.createTerminal({ name: 'e2e-other' });
    try {
      other.show();
      await poll('4f other terminal active', () => vscode.window.activeTerminal?.name === 'e2e-other',
        () => `active=${vscode.window.activeTerminal?.name}`);
      stub.push({ type: 'focus_requested', session: session('e2e-agent-session', 'ready', tty), seq: 4 });
      await poll('4f focus_requested activates e2e-agent', () => vscode.window.activeTerminal?.name === 'e2e-agent',
        () => `active=${vscode.window.activeTerminal?.name} pairings=${JSON.stringify(api.getPairings())}`);
    } finally {
      try { other.dispose(); } catch { /* ignore */ }
    }
    step('4f focus_requested activates paired terminal', s);

    // g. terminal frame: the active agent terminal's state colour is written into the workspace settings
    s = Date.now();
    await stepTerminalFrame({ stub, api, terminal, tty, cfg });
    step('4g terminal frame', s);

    // e. daemon goes away and comes back
    s = Date.now();
    const port = stub.port;
    await stub.stop();
    await poll('4e offline', () => api.getConnectionState() === 'offline', () => `state=${api.getConnectionState()}`);
    await poll('4e status text contains "offline"', () => api.getStatusText().includes('offline'),
      () => `status=${JSON.stringify(api.getStatusText())}`);
    await poll('4e frame removed when sessions go away', () => deepEqual(workspaceColors(), SEED),
      () => `workspace colorCustomizations=${show()}`);
    step('4e offline detected', s);

    s = Date.now();
    await poll('4e restart stub on same port', async () => {
      try { await stub.start(port); return true; } catch (e) {
        if (e.code !== 'EADDRINUSE') throw e;
        return false;
      }
    }, () => `port ${port} still in use`);
    await poll('4e reconnect online', () => api.getConnectionState() === 'online', () => `state=${api.getConnectionState()}`, 25000);
    step('4e auto-reconnect', s);
  } finally {
    try { if (terminal) terminal.dispose(); } catch { /* ignore */ }
    try { await cfg.update('daemonUrl', undefined, vscode.ConfigurationTarget.Global); } catch { /* ignore */ }
    try { if (stub.server?.listening) await stub.stop(); } catch { /* ignore */ }
  }
  console.log(`[e2e] all steps passed in ${Date.now() - t0} ms`);
}

module.exports = { run };
