// Pure HTML/CSP builders for the dashboard wrapper page (no vscode import).
// The wrapper frames the daemon-served dashboard; the extension host tells it
// about connectivity via postMessage because the iframe is cross-origin.

const ORIGIN_RE = /^https?:\/\/(?:[A-Za-z0-9.-]+|\[[0-9A-Fa-f:.]+\])(?::\d{1,5})?$/;
const NONCE_RE = /^[A-Za-z0-9+/=_-]+$/;
const CSP_SOURCE_RE = /^[^;"<>&,\r\n]+$/;

function assertOrigin(v: string): void {
  if (!ORIGIN_RE.test(v)) throw new Error(`invalid daemon origin: ${JSON.stringify(v)}`);
}
function assertNonce(v: string): void {
  if (!NONCE_RE.test(v)) throw new Error('invalid nonce');
}
function assertCspSource(v: string): void {
  if (!CSP_SOURCE_RE.test(v)) throw new Error(`invalid cspSource: ${JSON.stringify(v)}`);
}

function esc(s: string): string {
  return s.replaceAll('&', '&amp;').replaceAll('<', '&lt;').replaceAll('>', '&gt;').replaceAll('"', '&quot;').replaceAll("'", '&#39;');
}

export function buildCsp(o: { daemonOrigin: string; cspSource: string; nonce: string }): string {
  assertOrigin(o.daemonOrigin);
  assertNonce(o.nonce);
  assertCspSource(o.cspSource);
  return [
    "default-src 'none'",
    `frame-src ${o.daemonOrigin}`,
    `script-src 'nonce-${o.nonce}'`,
    `style-src ${o.cspSource} 'nonce-${o.nonce}'`,
    `img-src ${o.cspSource} data:`,
  ].join('; ');
}

const STYLE = `
[hidden] { display: none !important; }
html, body { height: 100%; margin: 0; padding: 0; }
body { color: var(--vscode-foreground); background: var(--vscode-editor-background); font-family: var(--vscode-font-family); }
iframe { display: block; width: 100%; height: 100vh; border: 0; }
#notice { padding: 16px; }
#notice .hint { margin-top: 8px; color: var(--vscode-descriptionForeground); }
`;

const SCRIPT = String.raw`
const vscode = acquireVsCodeApi();
const frame = document.getElementById('dashboard');
const notice = document.getElementById('notice');
const msg = document.getElementById('notice-msg');
const hint = document.getElementById('notice-hint');
const iframeUrl = frame.dataset.url;
let loaded = false;
function setNotice(text, showHint) {
  msg.textContent = text;
  hint.hidden = !showHint;
  notice.hidden = false;
}
window.addEventListener('message', (e) => {
  const m = e.data;
  if (!m || m.type !== 'daemon') return;
  const url = typeof m.url === 'string' ? m.url : '';
  if (m.connected === true) {
    if (!loaded) { frame.src = iframeUrl; loaded = true; }
    frame.hidden = false;
    notice.hidden = true;
  } else {
    frame.hidden = true;
    if (loaded) { frame.src = 'about:blank'; loaded = false; }
    setNotice('Irrlicht daemon not running at ' + url + ' \u2014 retrying\u2026', true);
  }
});
vscode.postMessage({ type: 'ready' });
`;

export function buildWrapperHtml(o: { daemonOrigin: string; cspSource: string; nonce: string }): string {
  const csp = buildCsp(o);
  return `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta http-equiv="Content-Security-Policy" content="${csp}">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Irrlicht</title>
<style nonce="${o.nonce}">${STYLE}</style>
</head>
<body>
<div id="notice"><div id="notice-msg">Connecting to the Irrlicht daemon at ${esc(o.daemonOrigin)}…</div><div id="notice-hint" class="hint" hidden>Start Irrlicht, or set irrlicht.daemonUrl if it runs on another port.</div></div>
<iframe id="dashboard" title="Irrlicht dashboard" data-url="${esc(o.daemonOrigin)}/?embed=vscode" hidden></iframe>
<script nonce="${o.nonce}">${SCRIPT}</script>
</body>
</html>
`;
}
