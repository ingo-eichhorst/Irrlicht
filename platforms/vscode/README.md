# Irrlicht for VS Code

![Irrlicht in VS Code](media/demo.gif)

Shows what your coding agents are doing without leaving the editor.

- **Dashboard in a panel.** Run **Irrlicht: Open Panel** from the Command Palette, or open the Irrlicht view in the activity bar. Both host the Irrlicht dashboard.
- **Status bar counts.** One count per session state: working, waiting, ready and error. The error count only shows when it is non-zero. Click it to open the panel. When the daemon is not running, the status bar says so and the extension reconnects automatically.
- **Agent Terminals view.** Each integrated terminal that hosts an agent session gets a box in its state colour: purple for working, orange for waiting, green for ready, red for error. Click a box to show that terminal. The view badge counts the terminals that are waiting for you.
- **Click to focus.** Click a row in the dashboard to bring that session's terminal to the front.

## Requirements

The Irrlicht daemon must be running locally: either the macOS app or the `--daemon-only` curl install.

The Agent Terminals view and click-to-focus also need the daemon's **Terminal focus → Capture terminal identity** permission (answer it in the dashboard's permission wizard). Without it the daemon does not record which TTY a session runs in, so no terminal pairs; the dashboard and the status bar work either way.

| Setting | Default | Notes |
| --- | --- | --- |
| `irrlicht.daemonUrl` | `http://127.0.0.1:7837` | Machine-scoped. Loopback http(s) only; any other value falls back to the default. |

## Install

Marketplace publishing is not set up yet; sideload the `.vsix`:

- `code --install-extension irrlicht-vscode-<version>.vsix`, or
- Extensions view, then `...`, then **Install from VSIX...**

Build the `.vsix` from a repository checkout with `tools/build-release.sh --vsix-only`. The file lands in `.build/`.

## Limitations

- VS Code's extension API cannot recolour an existing terminal tab: `Terminal` has no setter for it, and the change-colour command only opens a picker. That is why the state colours live in the Agent Terminals view.
- A terminal is paired with a session by joining the terminal's shell TTY with the session's TTY. An agent running inside tmux does not pair (its TTY is the tmux pane's, not the terminal's). Under herdr the daemon records the TTY of the attached herdr client, so its sessions pair with the terminal running the herdr client, and that terminal's box follows the most recently updated pane. That herdr behaviour is not verified live.
- Raising another VS Code window on click needs the Irrlicht macOS app, because VS Code has no window-raise API.
- The embedded dashboard follows the operating system's light/dark appearance (or its own theme toggle), not the VS Code colour theme.
- Local windows only: Remote-SSH, WSL and Codespaces are not supported.
- Cursor and Windsurf (VS Code forks) are untested.

## Development

From `platforms/vscode`:

- `npm ci` to install.
- `npm test` type-checks (`tsc --noEmit`), validates the manifest (`vsce ls`, which also enforces `@types/vscode` <= `engines.vscode`), runs the frame-src mutation self-test (`test/e2e/frame-src-mutation_test.sh`) and the unit tests.
- `npm run test:e2e` downloads VS Code and runs the end-to-end tests locally. On macOS the runner passes `--use-mock-keychain --use-inmemory-secretstorage` and a short `--user-data-dir`.
- `npm run test:e2e:mutation` re-runs the frame-src mutation: it swaps the iframe CSP for `'none'` and passes only if the e2e fails at step 4b (needs a clean git tree).
- `npm run build` bundles `dist/extension.js`.
