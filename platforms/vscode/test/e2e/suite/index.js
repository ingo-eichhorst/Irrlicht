'use strict';
// Runs inside the VS Code extension host. Plain CommonJS, no compile step.
const http = require('node:http');
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
      const u = { pathname: String(req.url).split('?')[0] };
      if (req.method === 'GET' && u.pathname === '/') {
        res.writeHead(200, { 'content-type': 'text/html' });
        res.end('<!doctype html><title>stub</title><p>stub dashboard</p>');
      } else if (req.method === 'GET' && u.pathname === '/api/v1/sessions') {
        res.writeHead(200, { 'content-type': 'application/json' });
        res.end(JSON.stringify({ groups: this.groups }));
      } else if (req.method === 'POST' && /^\/api\/v1\/sessions\/[^/]+\/focus$/.test(u.pathname)) {
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
  const start = Date.now();
  while (Date.now() - start < 10000) {
    const { stdout } = await execFileP('ps', ['-o', 'tty=', '-p', String(pid)]);
    last = stdout.trim();
    if (last && last !== '??') return last.startsWith('/dev/') ? last : `/dev/${last}`;
    await new Promise((r) => setTimeout(r, 100));
  }
  throw new Error(`no tty for pid ${pid} after ${Date.now() - start} ms; last ps output: ${JSON.stringify(last)}`);
}

async function run() {
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

    // e. daemon goes away and comes back
    s = Date.now();
    const port = stub.port;
    await stub.stop();
    await poll('4e offline', () => api.getConnectionState() === 'offline', () => `state=${api.getConnectionState()}`);
    await poll('4e status text contains "offline"', () => api.getStatusText().includes('offline'),
      () => `status=${JSON.stringify(api.getStatusText())}`);
    step('4e offline detected', s);

    s = Date.now();
    const restartDeadline = Date.now() + 20000;
    for (;;) {
      try { await stub.start(port); break; } catch (e) {
        if (e.code !== 'EADDRINUSE' || Date.now() > restartDeadline) throw e;
        await new Promise((r) => setTimeout(r, 100));
      }
    }
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
