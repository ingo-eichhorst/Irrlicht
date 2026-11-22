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

const isObject = (v) => typeof v === 'object' && v !== null;

function deepEqual(a, b) {
  if (a === b) return true;
  if (!isObject(a) || !isObject(b)) return false;
  if (Array.isArray(a) !== Array.isArray(b)) return false;
  const ka = Object.keys(a);
  if (ka.length !== Object.keys(b).length) return false;
  return ka.every((k) => Object.hasOwn(b, k) && deepEqual(a[k], b[k]));
}

const workspaceColors = () => vscode.workspace.getConfiguration('workbench').inspect('colorCustomizations')?.workspaceValue;
const show = () => JSON.stringify(workspaceColors());

// A check that cannot see its input must not pass: the seed has to be visible before anything else runs.
function assertSeed() {
  const folder = vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
  if (!folder) throw new Error('e2e: no workspace folder is open; the terminal frame cannot be tested');
  const extRoot = path.resolve(__dirname, '..', '..', '..');
  if (isInside(extRoot, folder)) {
    throw new Error(`e2e: workspace folder ${folder} is inside the extension root ${extRoot}; refusing to write into the repository`);
  }
  if (!deepEqual(workspaceColors(), SEED)) {
    throw new Error(`e2e: workspace colorCustomizations is not the seed; saw ${show()}`);
  }
}

// True when `p` is `root` or below it.
function isInside(root, p) {
  const rel = path.relative(root, p);
  if (rel === '') return true;
  return !rel.startsWith('..') && !path.isAbsolute(rel);
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

const GLOBAL = vscode.ConfigurationTarget.Global;
const COLOR = { working: '#8B5CF6', waiting: '#FF9500', ready: '#34C759' };
// The codicon the Agent Terminals row renders for a state: the macOS app's glyph.
const ICON = { working: 'circle-large-filled', ready: 'pass-filled' };
const USER_PANEL_BORDER = SEED['panel.border'];
const customizations = () => workspaceColors() ?? {};
const hasAll = (c, entries) => Object.entries(entries).every(([k, v]) => c[k] === v);
const hasNone = (c, keys) => keys.every((k) => !(k in c));
const frameIs = (color) => hasAll(customizations(), { 'modernPanel.border': color, 'panel.border': color });
// The panel keys hold the user's values again: their panel.border, and none of the frame-only keys.
const panelRestored = (c) => hasAll(c, { 'panel.border': USER_PANEL_BORDER }) && hasNone(c, ['modernPanel.border', 'terminal.border']);
const editorFramed = (c, color) => hasAll(c, { 'editor.border': color, 'tab.activeBorderTop': color }) && panelRestored(c);
// border-and-tint: an opaque tint on both backgrounds (never the raw state colour), plus the border.
function tinted(c, color) {
  const tint = c['terminal.background'];
  return tint !== undefined && tint !== color && hasAll(c, { 'panel.background': tint, 'panel.border': color });
}
const activeName = () => vscode.window.activeTerminal?.name;
const isActive = (name) => () => activeName() === name;
const showActiveName = () => `active=${activeName()}`;
const showActive = () => `active=${activeName()} workspace colorCustomizations=${show()}`;
const showColors = () => `workspace colorCustomizations=${show()}`;
const tabsText = () => {
  const groups = vscode.window.tabGroups.all.map((g) => g.tabs.map((t) => t.label + (t.isActive ? '*' : '')));
  return JSON.stringify(groups);
};
const isAgentTerminalTab = (t) => t.input instanceof vscode.TabInputTerminal && t.label === 'e2e-agent';
const isReadmeTab = (t) => t.input instanceof vscode.TabInputText && t.input.uri.path.endsWith('/README.md');
const readmeTab = () => vscode.window.tabGroups.all.flatMap((g) => g.tabs).find(isReadmeTab);
const agentTabActive = () => vscode.window.tabGroups.all.some((g) => g.activeTab !== undefined && isAgentTerminalTab(g.activeTab));

// Like poll, but `fn` must stay true for `holdMs` in a row.
async function pollHeld(name, fn, observed, holdMs, timeoutMs) {
  let heldSince = 0;
  await poll(name, () => {
    if (!fn()) { heldSince = 0; return false; }
    if (!heldSince) heldSince = Date.now();
    return Date.now() - heldSince >= holdMs;
  }, observed, timeoutMs);
}

// A tab opened in front of the editor-area terminal hides it but must not move the frame back to the panel.
async function editorFrameHoldsBehindTab() {
  const termGroup = vscode.window.tabGroups.all.find((g) => g.tabs.some(isAgentTerminalTab));
  if (!termGroup) throw new Error(`4g terminal tab not found in any group: ${show()}`);
  const readmeUri = vscode.Uri.joinPath(vscode.workspace.workspaceFolders[0].uri, 'README.md');
  await vscode.window.showTextDocument(readmeUri, { preview: false, viewColumn: termGroup.viewColumn, preserveFocus: false });
  try {
    await poll('4g README.md is the active tab in front of the terminal', () => readmeTab()?.isActive === true,
      () => `tabs=${tabsText()}`);
    console.log(`[e2e] 4g active terminal with README in front: ${activeName()}`);
    // The frame is debounced, so a wrong re-frame lands ~200 ms after the tab change: the editor frame must HOLD past that.
    await pollHeld('4g hidden editor terminal still frames the editor', () => editorFramed(customizations(), COLOR.waiting),
      showActive, 1000, 5000);
  } finally {
    const t = readmeTab();
    if (t) await vscode.window.tabGroups.close(t);
  }
}

async function frameInEditor({ terminal }) {
  // Editor location: the frame moves to the editor keys and the panel keys go back to the user's values.
  await vscode.commands.executeCommand('workbench.action.terminal.moveToEditor');
  await poll('4g terminal in editor frames the editor', () => editorFramed(customizations(), COLOR.waiting), showActive);
  await editorFrameHoldsBehindTab();
  await poll('4g e2e-agent tab active again', agentTabActive, () => `tabs=${tabsText()}`);
  await vscode.commands.executeCommand('workbench.action.terminal.moveToTerminalPanel');
  await poll('4g terminal back in panel frames the panel',
    () => frameIs(COLOR.waiting) && hasNone(customizations(), ['editor.border', 'tab.activeBorderTop']), showActive);
  terminal.show();
  await poll('4g e2e-agent active in the panel', isActive('e2e-agent'), showActiveName);
}

// None of the user's other keys change, and nothing from user scope is copied into the workspace.
function assertUserKeysKept() {
  const v = customizations();
  if (v['statusBar.background'] !== SEED['statusBar.background']) throw new Error(`4g statusBar.background changed: ${show()}`);
  if (!deepEqual(v['[Default Dark Modern]'], SEED['[Default Dark Modern]'])) throw new Error(`4g theme block changed: ${show()}`);
  if ('activityBar.background' in v) throw new Error(`4g user-scope key leaked into the workspace: ${show()}`);
}

async function frameFollowsState({ stub, api, terminal, tty }) {
  terminal.show();
  await poll('4g e2e-agent active', isActive('e2e-agent'), showActiveName);
  const pushedAt = Date.now();
  stub.push({ type: 'session_updated', session: session('e2e-agent-session', 'working', tty), seq: 5 });
  await poll('4g working frame', () => frameIs(COLOR.working),
    () => `workspace colorCustomizations=${show()} pairings=${JSON.stringify(api.getPairings())}`);
  const latency = Date.now() - pushedAt;
  if (latency > 2000) throw new Error(`e2e step "4g" frame took ${latency} ms after the push (limit 2000 ms)`);
  console.log(`[e2e] 4g frame latency ${latency} ms`);
  assertUserKeysKept();

  stub.push({ type: 'session_updated', session: session('e2e-agent-session', 'waiting', tty), seq: 6 });
  await poll('4g waiting frame', () => frameIs(COLOR.waiting), showColors);
}

async function plainTerminalRestores(plain, terminal) {
  plain.show();
  await poll('4g plain terminal restores user values', () => panelRestored(customizations()), showActive);
  terminal.show();
  await poll('4g e2e-agent again waiting frame', () => frameIs(COLOR.waiting), showActive);
}

async function frameSettingModes(cfg) {
  await cfg.update('terminalFrame', 'off', GLOBAL);
  await poll('4g setting off restores user values', () => {
    const c = customizations();
    return hasAll(c, { 'panel.border': USER_PANEL_BORDER }) && hasNone(c, ['modernPanel.border']);
  }, showColors);

  await cfg.update('terminalFrame', 'border-and-tint', GLOBAL);
  await poll('4g border-and-tint tints', () => tinted(customizations(), COLOR.waiting), showColors);

  await cfg.update('terminalFrame', undefined, GLOBAL);
  await poll('4g default setting removes tint',
    () => hasNone(customizations(), ['terminal.background', 'panel.background']) && frameIs(COLOR.waiting), showColors);
}

async function stepTerminalFrame(ctx) {
  let plain;
  try {
    await frameFollowsState(ctx);
    plain = vscode.window.createTerminal({ name: 'e2e-plain' });
    await plainTerminalRestores(plain, ctx.terminal);
    await frameInEditor(ctx);
    await frameSettingModes(ctx.cfg);
  } finally {
    try { plain?.dispose(); } catch { /* ignore */ }
    try { await ctx.cfg.update('terminalFrame', undefined, GLOBAL); } catch { /* ignore */ }
  }
}

async function activateAgainst(stub, cfg) {
  await cfg.update('daemonUrl', `http://127.0.0.1:${stub.port}`, GLOBAL);
  const ext = vscode.extensions.getExtension('irrlicht.irrlicht-vscode');
  if (!ext) throw new Error('extension irrlicht.irrlicht-vscode not found');
  const api = await ext.activate();
  if (typeof api?.getConnectionState !== 'function') throw new Error('extension did not return the test API');
  return api;
}

// b. the webview iframe reached the daemon
async function stepWebview(stub) {
  await vscode.commands.executeCommand('irrlicht.openPanel');
  const iframeHit = () => stub.requests.some((r) => r.method === 'GET' && r.url === '/?embed=vscode' && r.dest === 'iframe');
  await poll('4b webview iframe GET /?embed=vscode', iframeHit,
    () => `requests seen by stub: ${JSON.stringify(stub.requests)}`);
}

// c. status bar counts
async function stepStatusBar({ stub, api }) {
  stub.push({ type: 'session_updated', session: session('e2e-wait', 'waiting'), seq: 1 });
  await poll('4c status text contains "$(bell) 1"', () => api.getStatusText().includes('$(bell) 1'),
    () => `status=${JSON.stringify(api.getStatusText())}`);
}

// d. terminal pairing: the row follows the session's state and shows its glyph
async function stepPairing(ctx) {
  const { stub, api } = ctx;
  ctx.terminal = vscode.window.createTerminal({ name: 'e2e-agent' });
  const pid = await ctx.terminal.processId;
  if (!pid) throw new Error('terminal has no processId');
  const tty = await ttyOf(pid);
  ctx.tty = tty;
  const pairedAs = (state) => {
    const p = api.getPairings().find((x) => x.terminalName === 'e2e-agent');
    return p?.state === state && p.icon === ICON[state];
  };
  const observed = () => `tty=${tty} pairings=${JSON.stringify(api.getPairings())}`;
  stub.push({ type: 'session_updated', session: session('e2e-agent-session', 'working', tty), seq: 2 });
  await poll('4d pairing e2e-agent working', () => pairedAs('working'), observed);
  stub.push({ type: 'session_updated', session: session('e2e-agent-session', 'ready', tty), seq: 3 });
  await poll('4d pairing e2e-agent ready', () => pairedAs('ready'), observed);
}

// f. click-to-focus: a focus_requested frame raises the paired terminal
async function stepFocus({ stub, api, tty }) {
  const other = vscode.window.createTerminal({ name: 'e2e-other' });
  try {
    other.show();
    await poll('4f other terminal active', isActive('e2e-other'), showActiveName);
    stub.push({ type: 'focus_requested', session: session('e2e-agent-session', 'ready', tty), seq: 4 });
    await poll('4f focus_requested activates e2e-agent', isActive('e2e-agent'),
      () => `active=${activeName()} pairings=${JSON.stringify(api.getPairings())}`);
  } finally {
    try { other.dispose(); } catch { /* ignore */ }
  }
}

// e. the daemon goes away: offline, and the frame is removed with the sessions
async function stepOffline({ stub, api }) {
  await stub.stop();
  await poll('4e offline', () => api.getConnectionState() === 'offline', () => `state=${api.getConnectionState()}`);
  await poll('4e status text contains "offline"', () => api.getStatusText().includes('offline'),
    () => `status=${JSON.stringify(api.getStatusText())}`);
  await poll('4e frame removed when sessions go away', () => deepEqual(workspaceColors(), SEED), showColors);
}

async function restartStub(stub, port) {
  try { await stub.start(port); return true; } catch (e) {
    if (e.code !== 'EADDRINUSE') throw e;
    return false;
  }
}

// e. ... and comes back on the same port
async function stepReconnect({ stub, api }) {
  const port = stub.port;
  await poll('4e restart stub on same port', () => restartStub(stub, port), () => `port ${port} still in use`);
  await poll('4e reconnect online', () => api.getConnectionState() === 'online', () => `state=${api.getConnectionState()}`, 25000);
}

async function timed(name, fn) {
  const s = Date.now();
  const result = await fn();
  step(name, s);
  return result;
}

async function cleanup({ stub, cfg, terminal }) {
  try { terminal?.dispose(); } catch { /* ignore */ }
  try { await cfg.update('daemonUrl', undefined, GLOBAL); } catch { /* ignore */ }
  try { if (stub.server?.listening) await stub.stop(); } catch { /* ignore */ }
}

async function run() {
  assertSeed();
  const stub = new StubDaemon();
  await stub.start(0);
  const ctx = { stub, cfg: vscode.workspace.getConfiguration('irrlicht'), api: undefined, terminal: undefined, tty: undefined };
  try {
    ctx.api = await timed('activate', () => activateAgainst(stub, ctx.cfg));
    await timed('4a connection online', () => poll('4a connection online', () => ctx.api.getConnectionState() === 'online',
      () => `state=${ctx.api.getConnectionState()}`));
    await timed('4b webview iframe reached daemon', () => stepWebview(stub));
    await timed('4c status bar shows waiting count', () => stepStatusBar(ctx));
    await timed('4d terminal pairing follows state', () => stepPairing(ctx));
    await timed('4f focus_requested activates paired terminal', () => stepFocus(ctx));
    // g. terminal frame: the active agent terminal's state colour is written into the workspace settings
    await timed('4g terminal frame', () => stepTerminalFrame(ctx));
    await timed('4e offline detected', () => stepOffline(ctx));
    await timed('4e auto-reconnect', () => stepReconnect(ctx));
  } finally {
    await cleanup(ctx);
  }
  console.log(`[e2e] all steps passed in ${Date.now() - t0} ms`);
}

module.exports = { run };
