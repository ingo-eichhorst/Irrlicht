# Irrlicht for VS Code

![Irrlicht in VS Code](media/demo.gif)

Shows what your coding agents are doing without leaving the editor.

- **Dashboard in a panel.** Run **Irrlicht: Open Panel** from the Command Palette, or open the Irrlicht view in the activity bar. Both host the Irrlicht dashboard.
- **Status bar counts.** One count per session state: working, waiting, ready and error. The error count only shows when it is non-zero. Click it to open the panel. When the daemon is not running, the status bar says so and the extension reconnects automatically.
- **Terminal frame.** The panel and the editor area are framed at the same time, each in the state colour of the agent terminal it shows: purple for working, orange for waiting, green for ready, red for error. When one area shows several agent terminals, the most urgent state wins: error, then waiting, then working, then ready. Each terminal's own state is in the Agent Terminals view. When no area shows an agent terminal, your own colours come back. See [Terminal frame](#terminal-frame).
- **Agent Terminals view.** Lists every integrated terminal that hosts an agent session, with its state and project. Each row carries the macOS app's state glyph in the state colour: a solid dot while working, pause bars while waiting, a check circle when ready (error shows VS Code's circled error mark). Click a row to show that terminal. The view badge counts the terminals that are waiting for you.
- **Click to focus.** Click a row in the dashboard to bring that session's terminal to the front.

## Requirements

The Irrlicht daemon must be running locally: either the macOS app or the `--daemon-only` curl install.

The terminal frame, the Agent Terminals view and click-to-focus also need the daemon's **Terminal focus → Capture terminal identity** permission (answer it in the dashboard's permission wizard). Without it the daemon does not record which TTY a session runs in, so no terminal pairs; the dashboard and the status bar work either way.

| Setting | Default | Notes |
| --- | --- | --- |
| `irrlicht.daemonUrl` | `http://127.0.0.1:7837` | Machine-scoped. Loopback http(s) only; any other value falls back to the default. |
| `irrlicht.terminalFrame` | `border` | `off`, `border`, or `border-and-tint`. See [Terminal frame](#terminal-frame). |

## Terminal frame

VS Code's extension API has no way to colour or outline one terminal, so the extension does what [Peacock](https://marketplace.visualstudio.com/items?itemName=johnpapa.vscode-peacock) does for whole windows: it writes colour keys into `workbench.colorCustomizations` in the **workspace settings**: the folder's `.vscode/settings.json`, or the `.code-workspace` file of a multi-root workspace.

- **Each area on its own.** The panel and the editor area are framed independently, each by the agent terminals it shows (a terminal paired with an agent session). Plain terminals do not count.
- **Panel: every agent terminal in it counts.** VS Code does not tell an extension which panel terminals are on screen together in split panes, or which are hidden behind another terminal tab. So the panel takes the most urgent state of all agent terminals in the panel, including one whose tab is not in front, and a plain terminal brought to the front keeps the frame of the agent terminal behind it. With the default `border` the panel gets `modernPanel.border` (the outline around the panel in VS Code's modern layout), plus `panel.border`, `panelTitle.activeBorder`, `terminal.tab.activeBorder` and `terminal.border` (the panel's top edge, the active-tab underline and the split-pane rules in the classic layout).
- **Editor area: the terminal in front of each editor group.** An agent terminal counts while its tab is the one its group shows. A file opened in front of it in the same group takes the frame away until the terminal is in front again.
  - With one editor group, the editor area gets `editor.border` (the outline around the editor area in the modern layout) and `tab.activeBorderTop` (the top edge of the terminal's tab).
  - With two or more editor groups, that outline would surround every group, so only the top edge of the agent terminal's tab is coloured: `tab.activeBorderTop` while its group is the active one, `tab.unfocusedActiveBorderTop` while it is not. Agent terminals in front of several groups that are not active share that second key, and the most urgent of them wins. With three or more groups, `tab.unfocusedActiveBorderTop` also colours the top edge of the front tab of every other group that is not active, whatever that tab shows.
  - The outline still spans every editor group in VS Code's modern layout before 1.147. VS Code 1.140's modern layout does not draw tab edges at all (its connected tab style replaces them with its own border), so there the extension falls back to `editor.border` around the whole editor area whenever there are two or more groups, in the most urgent state of the agent terminals in front of them. 1.147 draws the tab edges; 1.141 to 1.146 were not tested and get the fallback. The classic layout draws tab edges in every version.
- **Tint.** `border-and-tint` also tints `terminal.background` towards the most urgent framed state, and `panel.background` with the same tint while the panel is framed. VS Code has one `terminal.background` for every terminal in the window, so the tint is one colour even when the two areas show different states; the borders carry each area's own state.
- **Your colours come back.** The extension remembers the values you had for those keys and puts them back when no area shows an agent terminal, and when you set `irrlicht.terminalFrame` to `off`. Every other key in `workbench.colorCustomizations`, including theme-scoped blocks such as `"[Default Dark Modern]": {...}`, keeps its value. VS Code rewrites the whole `workbench.colorCustomizations` value when it writes, though, so comments inside that block are lost. If you edit one of those keys while a frame is shown, your edit is kept and is what comes back.
- **Closing the window keeps the frame on disk.** VS Code disconnects an extension from the window before it shuts the extension down, so the extension cannot write the restore when the window closes or reloads. The frame colours stay in the workspace settings file until that folder or workspace is opened again; the extension then restores your values within about a second of starting.
- **Before you uninstall,** set `irrlicht.terminalFrame` to `off` in your user settings, and open any folder that was closed while it showed a frame, so the extension can put your values back. VS Code keeps an uninstalled extension running until the next restart and then no longer runs it, so it gets no chance to clean up afterwards.
- **No folder, no frame.** With no folder or workspace open there is no workspace settings file, so the frame stays off in that window. It never writes your user settings.
- **Mind `git status`.** The colours land in the workspace settings file, so a repository that commits that file shows it as changed while a frame is up, and an agent working in that repository can see the change too. In a folder without `.vscode/settings.json` the first frame creates the file, and after the restore VS Code leaves it behind as `{}`. Set `irrlicht.terminalFrame` to `off` in such repositories, or keep the file out of version control.
- **Side effects.** In the modern layout, `modernPanel.border` also outlines the active tab of the side bars, and with `border-and-tint`, `panel.background` also tints the Problems and Output views.
- **Tint base.** The extension cannot read the theme's real terminal background, so the tint is mixed into a fixed dark (`#181818`) or light (`#F8F8F8`) background, the panel background of Dark Modern and Light Modern and within a few shades of VS Code's 2026 default themes. With other themes the tinted terminal looks different from the theme's own. That is why the tint is not the default.
- **Precedence.** A theme-scoped value you set for one of those keys (inside `"[Theme Name]": {...}`) wins over the frame. A top-level `irrlicht.state.<state>` entry in `workbench.colorCustomizations` changes the frame colour too.

## Install

Marketplace publishing is not set up yet; sideload the `.vsix`:

- `code --install-extension irrlicht-vscode-<version>.vsix`, or
- Extensions view, then `...`, then **Install from VSIX...**

Build the `.vsix` from a repository checkout with `tools/build-release.sh --vsix-only`. The file lands in `.build/`.

## Limitations

- VS Code's extension API cannot colour one terminal or one split pane. `Terminal` has no colour setter, the change-colour command only opens a picker, and a terminal's colour only tints its icon in the tab list. Every theme colour, `terminal.background` included, applies to the whole window. That is why the state colour is painted as a frame through workspace colour customizations, one colour per area (and, in the editor area, per tab edge), and why the Agent Terminals view lists every agent terminal with its own state.
- VS Code does not link an editor tab to the terminal it shows, and agents give their terminals the same title (Claude Code names every terminal "✳ Claude Code"). So the extension learns which terminal an editor tab shows: from a terminal and an editor tab that appear together, from an editor moving to another group, and from the terminal that becomes active when a tab comes to the front of the active editor group. A terminal whose name no other terminal has is placed by its name, unless a terminal with that name closed while the tab was still unlearned. Until a tab's terminal is learned, every terminal with that tab's name counts in neither area, because any one of them could be the one in the tab; their states show in the Agent Terminals view meanwhile. That happens after the window reloads, when restored editor-area terminals share a name with another terminal (for example one Claude Code terminal in the panel and one in an editor tab: neither area is framed) until the tab comes to the front of the active editor group again (switch to another tab or group and back), and for a moment while a new tab or terminal has no title yet. A tab can be matched with the wrong one of two same-named terminals if you switch to the other one within about 300 ms of the tab coming to the front, or if both are created in or moved into the editor area within about 300 ms of each other.
- In the classic layout (`workbench.experimental.modernUI` off) the panel has no outline, so `border` shows as the panel's top edge, the active-tab underline and the tab-list rule, and an editor-area terminal shows as the top edge of its tab. `border-and-tint` reads more clearly there.
- A terminal is paired with a session by joining the terminal's shell TTY with the session's TTY. An agent running inside tmux does not pair (its TTY is the tmux pane's, not the terminal's). Under herdr the daemon records the TTY of the attached herdr client, so its sessions pair with the terminal running the herdr client, and that terminal's row and frame follow the most recently updated pane. That herdr behaviour is not verified live.
- Raising another VS Code window on click needs the Irrlicht macOS app, because VS Code has no window-raise API.
- The embedded dashboard follows the operating system's light/dark appearance (or its own theme toggle), not the VS Code colour theme.
- Local windows only: Remote-SSH, WSL and Codespaces are not supported.
- Cursor and Windsurf (VS Code forks) are untested.

## Development

From `platforms/vscode`:

- `npm ci` to install.
- `npm test` type-checks (`tsc --noEmit`), validates the manifest (`vsce ls`, which also enforces `@types/vscode` <= `engines.vscode`), runs the frame-src mutation self-test (`test/e2e/frame-src-mutation_test.sh`) and the unit tests.
- `npm run test:e2e` downloads VS Code and runs the end-to-end tests locally. On macOS the runner passes `--use-mock-keychain --use-inmemory-secretstorage` and a short `--user-data-dir`. It opens a throwaway copy of `test/e2e/workspace` (step 4g writes the terminal frame into that copy's `.vscode/settings.json`) and deletes it afterwards. It seeds `irrlicht.daemonUrl` with the stub daemon's port before VS Code starts, so the extension never connects to a daemon running on the machine. It runs VS Code 1.147.0 by default; `VSCODE_TEST_VERSION=1.140.0 npm run test:e2e` runs another version (1.140 exercises the editor-box fallback for the modern layout).
- `npm run test:e2e:mutation` re-runs the frame-src mutation: it swaps the iframe CSP for `'none'` and passes only if the e2e fails at step 4b (needs a clean git tree).
- `npm run build` bundles `dist/extension.js`.
