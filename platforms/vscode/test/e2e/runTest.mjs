// Launches a real VS Code with the extension under development and runs
// test/e2e/suite/index.js inside its extension host. Exits non-zero on failure.
import { runTests } from '@vscode/test-electron';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));
const extensionRoot = path.resolve(here, '..', '..');

// VS Code creates an IPC socket inside the user-data-dir; macOS caps socket
// paths at 104 bytes, so the dir must be short.
function shortTmpRoot() {
  const probe = '/xxxxxxxxxxxx/1.147-main.sock';
  return os.tmpdir().length + probe.length <= 100 ? os.tmpdir() : '/tmp';
}

const root = shortTmpRoot();
const userDataDir = fs.mkdtempSync(path.join(root, 'irr-ud-'));
const extensionsDir = fs.mkdtempSync(path.join(root, 'irr-ex-'));
const fakeHome = fs.mkdtempSync(path.join(root, 'irr-home-'));
// The terminal frame writes workbench.colorCustomizations into the workspace's .vscode/settings.json,
// so the window must open a throwaway copy of the fixture, never the tracked folder.
const workspaceDir = fs.mkdtempSync(path.join(root, 'irr-ws-'));
fs.cpSync(path.join(here, 'workspace'), workspaceDir, { recursive: true });
fs.mkdirSync(path.join(workspaceDir, '.vscode'), { recursive: true });
fs.writeFileSync(path.join(workspaceDir, '.vscode', 'settings.json'), JSON.stringify({
  'workbench.colorCustomizations': {
    'panel.border': '#123456',
    'statusBar.background': '#654321',
    '[Default Dark Modern]': { 'terminal.border': '#ABCDEF' },
  },
}, null, 2));
// A user-scope customization that must never be copied into the workspace.
fs.mkdirSync(path.join(userDataDir, 'User'), { recursive: true });
fs.writeFileSync(path.join(userDataDir, 'User', 'settings.json'), JSON.stringify({
  'workbench.colorCustomizations': { 'activityBar.background': '#0A0B0C' },
}, null, 2));

// VS Code writes ~/.vscode-shared; never let that be the real home.
process.env.HOME = fakeHome;
// The throwaway VS Code's terminals must not attach to a live herdr session on the host.
process.env.HERDR_SESSION = 'qa-none';

let code = 0;
try {
  await runTests({
    // Reuse the pre-downloaded build in .vscode-test (test-electron's cache layout).
    cachePath: path.join(extensionRoot, '.vscode-test'),
    version: process.env.VSCODE_TEST_VERSION || '1.147.0',
    extensionDevelopmentPath: extensionRoot,
    extensionTestsPath: path.join(here, 'suite', 'index.js'),
    launchArgs: [
      workspaceDir,
      '--use-mock-keychain', // otherwise VS Code blocks on a login-keychain write
      '--use-inmemory-secretstorage',
      '--disable-workspace-trust',
      '--skip-welcome',
      '--skip-release-notes',
      '--disable-telemetry',
      '--disable-updates',
      '--disable-extensions',
      `--user-data-dir=${userDataDir}`,
      `--extensions-dir=${extensionsDir}`,
    ],
  });
  console.log('e2e: PASSED');
} catch (err) {
  console.error('e2e: FAILED:', err?.message ?? err);
  code = 1;
} finally {
  for (const d of [userDataDir, extensionsDir, fakeHome, workspaceDir]) {
    try { fs.rmSync(d, { recursive: true, force: true }); } catch { /* best effort */ }
  }
}
process.exit(code);
