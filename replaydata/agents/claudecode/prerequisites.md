# Prerequisites — claudecode

Maintainer-authored checklist of items that must be set up out-of-band
before `agent-onboard record` can drive Claude Code through scenarios.

Once every item below is complete, run:

```
touch .agent-onboarding/prereqs-claudecode.ok
```

…to acknowledge. The recorder refuses to start until that file exists and
is newer than this manifest. If you update this manifest later, the recorder
will fail again until the .ok file is re-touched.

## Items

- [ ] `claude` CLI installed and on PATH (`claude --version` works).
- [ ] Anthropic subscription login complete (`claude` runs without auth errors).
- [ ] Optional: API key set if you want to exercise scenarios that bypass the subscription path.
- [ ] tmux installed and on PATH (`tmux -V` works) — required for pane / pipepane sensors.
- [ ] lsof installed — required for net sensor. Pre-installed on macOS; `apt install lsof` on Debian/Ubuntu.
- [ ] For `desktop-local`: Claude Desktop 1.46388.4 is installed, running, and signed in.
- [ ] For `desktop-local`: the caller has macOS Accessibility access.
- [ ] For `desktop-local`: no other user or process controls Claude Desktop during the run.
- [ ] For `desktop-local`: no other process edits the daemon-declared agent
      config files from recorder startup until the runner reports that its
      post-install seal is complete.

## Notes

- Hooks-based scenarios (`permission-hook-denial`, `subagent-spawn`) need
  the Claude Code hook integration installed; the skill's
  `install-instructions.md` covers that.
- The Phase 0 survey (`/ir:onboard-agent survey claudecode`) proposes
  additional per-scenario prereqs in its output. Review and copy any that
  apply into this manifest.
- `run-cell.sh --execution-profile desktop-local claudecode 2-1_basic-turn`
  runs one no-tool Local turn. The runner creates the workspace under
  repository-root `.build`, selects a free loopback recorder port, and archives
  only the new session that matches its registry and transcript identity.
- A cell whose recipe carries a `script` is driven step by step (#1888). The
  Desktop driver elicits `send`, `wait_turn`, `sleep`, `interrupt`, `keys`,
  `mode`, `model`, `archive`, and `start_session`; every other step type is
  refused BEFORE the run as `not runnable through Desktop`, naming the Desktop
  control that is missing. The full per-recipe verdict is committed at
  `tools/onboarding-factory/internal/desktopdriver/testdata/claudecode-desktop-recipe-census.txt`.
  A `keys` step is limited to `Escape` and `Enter`: the helper requires a
  false-to-true postcondition per keystroke, and no other key has one in the
  composer.
- The mode and model the driver selects are the COMPOSER's two popup menus,
  chosen per session through the accessibility tree. They are not app-wide
  configuration changes, which the next note still refuses.
- Packaged Claude Desktop removes `CLAUDE_USER_DATA_DIR` unless an internal,
  signed E2E token validates it. The Desktop runner therefore uses the normal
  profile. It refuses app-wide settings, environment, mock, model, mode,
  plugin, skill, and MCP changes. It verifies the relevant config paths and
  uses the recording daemon's managed-file snapshot for its temporary hook.
  This boundary was verified in
  `/Applications/Claude.app/Contents/Resources/app.asar`, package entry
  `.vite/build/index.pre.js`. The packaged branch contains
  `E.app.isPackaged&&!qJ&&delete process.env.CLAUDE_USER_DATA_DIR`. The `qJ`
  authorization path requires both `CLAUDE_CDP_AUTH` and
  `CLAUDE_USER_DATA_DIR`, then verifies the signed token and requested path
  before `app.setPath("userData", ...)` can run.
- The normal-profile boundary covers these exact paths:
  `$HOME/Library/Application Support/Claude/claude-code-sessions/` for the
  owned session registry; `claude_desktop_config.json`, `config.json`,
  `cowork-enabled-cli-ops.json`, and `extensions-blocklist.json` below the
  same Claude support directory; `$HOME/.claude.json`; and the
  `$HOME/.claude/plugins/` and `$HOME/.claude/skills/` trees. The driver
  records the bytes of every pre-existing registry row. The driver requires
  the other listed paths to keep the same type, mode, symlink target, and file
  digest. Any difference fails cleanup instead of being treated as unchanged.
- The recording daemon declares its writable configuration paths through
  `irrlichd --print-managed-files`. The runner snapshots those files before
  hook installation. Before the real daemon starts, the runner applies Claude
  Code's declared hook, status-line, and instruction closures to the baseline
  bytes in a disposable shadow home. The real post-install files must match
  this expected state before the runner publishes the seal. Cleanup restores
  the snapshot only if every managed path still matches the seal. A later
  external edit causes a refusal and remains in place. The clone-wide lock
  prevents another Desktop runner from entering this lifecycle. An unrelated
  process does not honor that lock. Such a process can also write during an
  installer's own read-and-rename window, before the expected-state comparison,
  or between cleanup's last byte comparison and its atomic restore rename. The
  exclusive-control prerequisite therefore still applies to these files.
