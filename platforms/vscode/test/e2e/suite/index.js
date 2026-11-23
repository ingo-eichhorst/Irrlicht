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

let frameDebug = () => 'n/a';
const workspaceColors = () => vscode.workspace.getConfiguration('workbench').inspect('colorCustomizations')?.workspaceValue;
const show = () => JSON.stringify(workspaceColors());

// A check that cannot see its input must not pass: the seed has to be visible before anything else runs.
function assertSeed() {
  const folder = vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
  if (!folder) throw new Error('e2e: no workspace folder is open; the terminal frame cannot be tested');
  assertOutsideExtension(folder);
  if (!deepEqual(workspaceColors(), SEED)) {
    throw new Error(`e2e: workspace colorCustomizations is not the seed; saw ${show()}`);
  }
}

function assertOutsideExtension(folder) {
  const extRoot = path.resolve(__dirname, '..', '..', '..');
  if (isInside(extRoot, folder)) {
    throw new Error(`e2e: workspace folder ${folder} is inside the extension root ${extRoot}; refusing to write into the repository`);
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

// Seq numbers must stay consecutive: a gap makes the client rehydrate from the stub's empty session list.
const nextSeq = (ctx) => ctx.seq++;

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
// The editor-area keys the frame may write (#2175): the box (one group, or no tab edges) and the two tab edges.
const EDITOR_KEYS = ['editor.border', 'tab.activeBorderTop', 'tab.unfocusedActiveBorderTop'];
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
const editorRestored = (c) => hasNone(c, EDITOR_KEYS);
// border-and-tint: an opaque tint on both backgrounds (never the raw state colour), plus the border.
function tinted(c, color) {
  const tint = c['terminal.background'];
  return tint !== undefined && tint !== color && hasAll(c, { 'panel.background': tint, 'panel.border': color });
}
const activeName = () => vscode.window.activeTerminal?.name;
const isActive = (name) => () => activeName() === name;
const showActiveName = () => `active=${activeName()}`;
const showActive = () => `active=${activeName()} workspace colorCustomizations=${show()} tabs=${tabsText()} frame=${JSON.stringify(frameDebug())}`;
const showColors = () => `workspace colorCustomizations=${show()}`;
const tabsText = () => {
  const groups = vscode.window.tabGroups.all.map((g) => g.tabs.map((t) => t.label + (t.isActive ? '*' : '')));
  return JSON.stringify(groups);
};
const isAgentTerminalTab = (t) => t.input instanceof vscode.TabInputTerminal && t.label === 'e2e-agent';
const isReadmeTab = (t) => t.input instanceof vscode.TabInputText && t.input.uri.path.endsWith('/README.md');
const readmeTab = () => vscode.window.tabGroups.all.flatMap((g) => g.tabs).find(isReadmeTab);
const agentTabActive = () => vscode.window.tabGroups.all.some((g) => g.activeTab !== undefined && isAgentTerminalTab(g.activeTab));

const HOLD_MS = 1000;

// Like poll, but `fn` must stay true for HOLD_MS in a row.
async function pollHeld(name, fn, observed, timeoutMs) {
  let heldSince = 0;
  await poll(name, () => {
    if (!fn()) { heldSince = 0; return false; }
    if (!heldSince) heldSince = Date.now();
    return Date.now() - heldSince >= HOLD_MS;
  }, observed, timeoutMs);
}

// A tab opened in front of the editor-area terminal hides it: that group no longer shows an agent terminal,
// so the editor frame goes (#2175: each area is framed by the agent terminal it is showing). This reverses
// the pre-#2175 behaviour, where the frame held behind the tab.
async function editorFrameLeavesWithTab() {
  const termGroup = vscode.window.tabGroups.all.find((g) => g.tabs.some(isAgentTerminalTab));
  if (!termGroup) throw new Error(`4g terminal tab not found in any group: ${show()}`);
  const readmeUri = vscode.Uri.joinPath(vscode.workspace.workspaceFolders[0].uri, 'README.md');
  await vscode.window.showTextDocument(readmeUri, { preview: false, viewColumn: termGroup.viewColumn, preserveFocus: false });
  try {
    await poll('4g README.md is the active tab in front of the terminal', () => readmeTab()?.isActive === true,
      () => `tabs=${tabsText()}`);
    // The frame is debounced, so a wrong frame lands ~200 ms after the tab change: the restore must HOLD past that.
    await pollHeld('4g hidden editor terminal no longer frames the editor', () => editorRestored(customizations()) && panelRestored(customizations()),
      showActive, 5000);
  } finally {
    const t = readmeTab();
    if (t) await vscode.window.tabGroups.close(t);
  }
}

async function frameInEditor({ terminal }) {
  // Editor location: the frame moves to the editor keys and the panel keys go back to the user's values.
  await vscode.commands.executeCommand('workbench.action.terminal.moveToEditor');
  await poll('4g terminal in editor frames the editor', () => editorFramed(customizations(), COLOR.waiting), showActive);
  await editorFrameLeavesWithTab();
  await poll('4g e2e-agent tab active again', agentTabActive, () => `tabs=${tabsText()}`);
  await poll('4g terminal in front again frames the editor', () => editorFramed(customizations(), COLOR.waiting), showActive);
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

async function frameFollowsState(ctx) {
  const { stub, api, terminal, tty } = ctx;
  terminal.show();
  await poll('4g e2e-agent active', isActive('e2e-agent'), showActiveName);
  const pushedAt = Date.now();
  stub.push({ type: 'session_updated', session: session('e2e-agent-session', 'working', tty), seq: nextSeq(ctx) });
  await poll('4g working frame', () => frameIs(COLOR.working),
    () => `workspace colorCustomizations=${show()} pairings=${JSON.stringify(api.getPairings())}`);
  const latency = Date.now() - pushedAt;
  if (latency > 2000) throw new Error(`e2e step "4g" frame took ${latency} ms after the push (limit 2000 ms)`);
  console.log(`[e2e] 4g frame latency ${latency} ms`);
  assertUserKeysKept();

  stub.push({ type: 'session_updated', session: session('e2e-agent-session', 'waiting', tty), seq: nextSeq(ctx) });
  await poll('4g waiting frame', () => frameIs(COLOR.waiting), showColors);
}

// VS Code does not tell an extension which panel terminals are on screen, so every agent terminal in the
// panel counts (#2175): a plain terminal brought to the front keeps the frame of the agent behind it.
async function plainTerminalKeepsPanelFrame(plain, terminal) {
  plain.show();
  await poll('4g plain terminal active', isActive('e2e-plain'), showActiveName);
  await pollHeld('4g plain terminal in front keeps the panel frame', () => frameIs(COLOR.waiting), showActive, 5000);
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

async function pairTerminal(ctx, terminal, sessionId, state) {
  const pid = await terminal.processId;
  if (!pid) throw new Error(`terminal for ${sessionId} has no processId`);
  const tty = await ttyOf(pid);
  ctx.stub.push({ type: 'session_updated', session: session(sessionId, state, tty), seq: nextSeq(ctx) });
  await poll(`4h pairing ${sessionId} ${state}`, () => ctx.api.getPairings().some((p) => p.sessionId === sessionId && p.state === state),
    () => `pid=${pid} tty=${tty} pairings=${JSON.stringify(ctx.api.getPairings())} pairing state=${JSON.stringify(ctx.api.getPairingDebugState())}`);
  return tty;
}

const isTheActive = (terminal) => () => vscode.window.activeTerminal === terminal;

// Makes the panel terminal `terminal` the active one. With an editor-area terminal focused, Terminal.show()
// alone was seen to change nothing for 20 s (no active-terminal event at all), so the panel is focused too.
async function activatePanelTerminal(name, terminal) {
  terminal.show();
  await vscode.commands.executeCommand('workbench.action.focusPanel');
  await poll(name, isTheActive(terminal), showActiveName);
}

// The modern layout before VS Code 1.147 draws no editor tab edges, so with two or more editor groups the
// frame falls back to the editor.border box there (terminalFrame.ts tabEdgesDrawn). The e2e runs 1.147 by
// default; this keeps the two-group steps meaningful under VSCODE_TEST_VERSION=1.140.0 too.
function tabEdgesDrawnHere() {
  if (vscode.workspace.getConfiguration('workbench').get('experimental.modernUI') !== true) return true;
  const [major, minor] = vscode.version.split('.').map(Number);
  return major > 1 || minor >= 147;
}
// With two or more groups: no box where tab edges are drawn, else the box in `color`.
const boxAcrossGroups = (c, color) => (tabEdgesDrawnHere() ? hasNone(c, ['editor.border']) : c['editor.border'] === color);
const panelIs = (c, color) => hasAll(c, { 'modernPanel.border': color, 'panel.border': color, 'terminal.border': color });

// h. observations 2 and 4 of #2175: a terminal with the SAME name as the panel agent, in the editor area, in
// another state. Both areas are framed at once, each by its own terminal, whichever of the two is active.
async function twinInEditor(ctx, extra) {
  const twin = vscode.window.createTerminal({ name: 'e2e-agent', location: vscode.TerminalLocation.Editor });
  extra.push(twin);
  ctx.twin = twin;
  ctx.twinTty = await pairTerminal(ctx, twin, 'e2e-twin-session', 'ready');
  twin.show();
  await poll('4h twin active', isTheActive(twin), showActiveName);
  const both = () => {
    const c = customizations();
    return panelIs(c, COLOR.waiting) && hasAll(c, { 'editor.border': COLOR.ready, 'tab.activeBorderTop': COLOR.ready });
  };
  await poll('4h editor twin active: panel waiting AND editor ready at once', both, showActive);
  await activatePanelTerminal('4h panel agent active', ctx.terminal);
  // The same-name case: a label match would put the panel agent in the editor area and paint it waiting.
  await pollHeld('4h panel agent active: panel waiting AND editor ready at once', both, showActive, 5000);
}

// h. observation 3: with two editor groups the frame marks only the agent terminal's group (VS Code 1.147
// draws the tab edges), never the box around every group.
async function twoEditorGroups(ctx) {
  const readmeUri = vscode.Uri.joinPath(vscode.workspace.workspaceFolders[0].uri, 'README.md');
  await vscode.window.showTextDocument(readmeUri, { preview: false, viewColumn: vscode.ViewColumn.Beside, preserveFocus: false });
  try {
    await poll('4h README in a second group, active', () => vscode.window.tabGroups.all.length >= 2 && readmeTab()?.isActive === true
      && vscode.window.tabGroups.activeTabGroup.tabs.includes(readmeTab()), () => `tabs=${tabsText()}`);
    const unfocused = () => {
      const c = customizations();
      return hasAll(c, { 'tab.unfocusedActiveBorderTop': COLOR.ready }) && hasNone(c, ['tab.activeBorderTop', 'editorGroup.border']) && boxAcrossGroups(c, COLOR.ready);
    };
    console.log(`[e2e] 4h VS Code ${vscode.version}: tab edges drawn=${tabEdgesDrawnHere()}`);
    await pollHeld('4h agent group unfocused: only its tab edge, no box', unfocused, showActive, 5000);
    ctx.twin.show();
    await poll('4h twin active again', isTheActive(ctx.twin), showActiveName);
    const focused = () => {
      const c = customizations();
      return hasAll(c, { 'tab.activeBorderTop': COLOR.ready }) && hasNone(c, ['tab.unfocusedActiveBorderTop', 'editorGroup.border']) && boxAcrossGroups(c, COLOR.ready);
    };
    await pollHeld('4h agent group focused: only its tab edge, no box', focused, showActive, 5000);
  } finally {
    const t = readmeTab();
    if (t) await vscode.window.tabGroups.close(t);
  }
}

// h. moving the twin's editor to a new group gives it a new tab while the twin stays the active terminal, so
// VS Code fires no active-terminal change: the binding can only settle when its window ends.
async function twinMovedToNewGroup(ctx) {
  ctx.twin.show();
  await poll('4h twin active before the move', isTheActive(ctx.twin), showActiveName);
  const groupsBefore = vscode.window.tabGroups.all.length;
  await vscode.commands.executeCommand('workbench.action.moveEditorToRightGroup');
  await poll('4h twin editor in a new group', () => vscode.window.tabGroups.all.length === groupsBefore + 1, () => `tabs=${tabsText()}`);
  const both = () => {
    const c = customizations();
    return panelIs(c, COLOR.waiting) && hasAll(c, { 'tab.activeBorderTop': COLOR.ready }) && boxAcrossGroups(c, COLOR.ready);
  };
  await pollHeld('4h moved twin: panel waiting AND its tab edge ready', both, showActive, 5000);
}

// h. observation 1: split panes in the panel with different states; the most urgent one wins even when the
// less urgent pane is the active one.
async function splitPanes(ctx, extra) {
  ctx.stub.push({ type: 'session_updated', session: session('e2e-agent-session', 'ready', ctx.tty), seq: nextSeq(ctx) });
  await activatePanelTerminal('4h panel agent active for the split', ctx.terminal);
  await poll('4h panel ready', () => panelIs(customizations(), COLOR.ready), showActive);
  const before = new Set(vscode.window.terminals);
  await vscode.commands.executeCommand('workbench.action.terminal.split');
  let split;
  await poll('4h split pane opened', () => (split = vscode.window.terminals.find((t) => !before.has(t))) !== undefined,
    () => `terminals=${vscode.window.terminals.map((t) => t.name)}`);
  extra.push(split);
  await pairTerminal(ctx, split, 'e2e-split-session', 'waiting');
  await activatePanelTerminal('4h less urgent pane active', ctx.terminal);
  await pollHeld('4h split panes: the waiting pane wins over the active ready pane', () => panelIs(customizations(), COLOR.waiting),
    showActive, 5000);
}

async function stepTerminalFrame(ctx) {
  let plain;
  const extra = [];
  try {
    await frameFollowsState(ctx);
    plain = vscode.window.createTerminal({ name: 'e2e-plain' });
    await plainTerminalKeepsPanelFrame(plain, ctx.terminal);
    await frameInEditor(ctx);
    await frameSettingModes(ctx.cfg);
  } finally {
    try { plain?.dispose(); } catch { /* ignore */ }
    try { await ctx.cfg.update('terminalFrame', undefined, GLOBAL); } catch { /* ignore */ }
  }
  try {
    await twinInEditor(ctx, extra);
    await twoEditorGroups(ctx);
    await twinMovedToNewGroup(ctx);
    await splitPanes(ctx, extra);
  } finally {
    for (const t of extra) { try { t.dispose(); } catch { /* ignore */ } }
  }
}

async function activateAgainst(stub, cfg) {
  await cfg.update('daemonUrl', `http://127.0.0.1:${stub.port}`, GLOBAL);
  const ext = vscode.extensions.getExtension('irrlicht.irrlicht-vscode');
  if (!ext) throw new Error('extension irrlicht.irrlicht-vscode not found');
  const api = await ext.activate();
  if (typeof api?.getConnectionState !== 'function') throw new Error('extension did not return the test API');
  frameDebug = () => api.getFrameDebugState();
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
async function stepStatusBar(ctx) {
  const { stub, api } = ctx;
  stub.push({ type: 'session_updated', session: session('e2e-wait', 'waiting'), seq: nextSeq(ctx) });
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
  stub.push({ type: 'session_updated', session: session('e2e-agent-session', 'working', tty), seq: nextSeq(ctx) });
  await poll('4d pairing e2e-agent working', () => pairedAs('working'), observed);
  stub.push({ type: 'session_updated', session: session('e2e-agent-session', 'ready', tty), seq: nextSeq(ctx) });
  await poll('4d pairing e2e-agent ready', () => pairedAs('ready'), observed);
}

// d. a terminal closed before VS Code reports its shell pid must not stall pairing for every later terminal
// (seen while adding #2175's steps: the resolver waited forever on such a terminal's processId).
async function stepPairingAfterBlip(ctx) {
  for (let i = 0; i < 3; i++) {
    const blip = vscode.window.createTerminal({ name: `e2e-blip-${i}` });
    await new Promise((r) => setTimeout(r, 20 * i));
    blip.dispose();
  }
  const after = vscode.window.createTerminal({ name: 'e2e-after-blip' });
  try {
    const pid = await after.processId;
    if (!pid) throw new Error('e2e-after-blip has no processId');
    const tty = await ttyOf(pid);
    ctx.stub.push({ type: 'session_updated', session: session('e2e-after-blip-session', 'working', tty), seq: nextSeq(ctx) });
    await poll('4d terminal opened after a closed one still pairs', () => ctx.api.getPairings().some((p) => p.sessionId === 'e2e-after-blip-session'),
      () => `pid=${pid} tty=${tty} pairings=${JSON.stringify(ctx.api.getPairings())} pairing state=${JSON.stringify(ctx.api.getPairingDebugState())}`);
  } finally {
    after.dispose();
    // A real daemon ends the session with its process. Without this, a later terminal that reuses the
    // freed TTY would pair with the stale session.
    ctx.stub.push({ type: 'session_deleted', session: session('e2e-after-blip-session', 'working'), seq: nextSeq(ctx) });
  }
}

// f. click-to-focus: a focus_requested frame raises the paired terminal
async function stepFocus(ctx) {
  const { stub, api, tty } = ctx;
  const other = vscode.window.createTerminal({ name: 'e2e-other' });
  try {
    other.show();
    await poll('4f other terminal active', isActive('e2e-other'), showActiveName);
    stub.push({ type: 'focus_requested', session: session('e2e-agent-session', 'ready', tty), seq: nextSeq(ctx) });
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
  // irrlicht.daemonUrl is left at the seeded stub port: clearing it would restart the client against the
  // default URL, the real daemon on 7837, while the window is still open (review of #2175).
  try { if (stub.server?.listening) await stub.stop(); } catch { /* ignore */ }
}

// The runner seeds irrlicht.daemonUrl with this port before VS Code starts, so the extension's startup
// activation already reaches the stub; a missing port means the seed is gone and the default URL (the real
// daemon) would be used, so refuse to run.
function seededStubPort() {
  const port = Number(process.env.IRRLICHT_E2E_STUB_PORT);
  if (!(port > 0) || port === 7837) throw new Error(`e2e: IRRLICHT_E2E_STUB_PORT is not a usable port: ${process.env.IRRLICHT_E2E_STUB_PORT}`);
  const seeded = vscode.workspace.getConfiguration('irrlicht').inspect('daemonUrl')?.globalValue;
  if (seeded !== `http://127.0.0.1:${port}`) throw new Error(`e2e: irrlicht.daemonUrl is not seeded with the stub port; saw ${JSON.stringify(seeded)}`);
  return port;
}

async function run() {
  assertSeed();
  const stub = new StubDaemon();
  await stub.start(seededStubPort());
  const ctx = { stub, cfg: vscode.workspace.getConfiguration('irrlicht'), api: undefined, terminal: undefined, tty: undefined, seq: 1 };
  try {
    ctx.api = await timed('activate', () => activateAgainst(stub, ctx.cfg));
    await timed('4a connection online', () => poll('4a connection online', () => ctx.api.getConnectionState() === 'online',
      () => `state=${ctx.api.getConnectionState()}`));
    await timed('4b webview iframe reached daemon', () => stepWebview(stub));
    await timed('4c status bar shows waiting count', () => stepStatusBar(ctx));
    await timed('4d terminal pairing follows state', () => stepPairing(ctx));
    await timed('4d pairing survives a terminal closed at once', () => stepPairingAfterBlip(ctx));
    await timed('4f focus_requested activates paired terminal', () => stepFocus(ctx));
    // g. terminal frame: the agent terminals' state colours are written into the workspace settings
    // h. (inside 4g) per-area frame, same-name terminals, two editor groups, split panes (#2175)
    await timed('4g terminal frame', () => stepTerminalFrame(ctx));
    await timed('4e offline detected', () => stepOffline(ctx));
    await timed('4e auto-reconnect', () => stepReconnect(ctx));
  } finally {
    await cleanup(ctx);
  }
  console.log(`[e2e] all steps passed in ${Date.now() - t0} ms`);
}

module.exports = { run };
