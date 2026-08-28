---
name: ir:test-mac
description: >
  Build and run a dev irrlicht daemon and/or macOS Swift app for local
  testing, by invoking this skill's own test-mac.sh. Two independent axes:
  MODE (replace, default — takes over production on port 7837/production
  state; separate — coexists on port 7838/isolated state) and TARGET (full,
  default — daemon + app; daemon — just rebuild+restart irrlichd, leave the
  running app to reconnect; macos — just rebuild+relaunch the Swift app
  against whatever daemon is already up).
  Use when the user says "test mac", "restart mac", "rebuild mac",
  "restart just the daemon", "rebuild the mac app only", or "/ir:test-mac".
---

# Build & Run the macOS Dev Stack

**The procedure is a script. Run it — do not reimplement it inline.**

```bash
.claude/skills/ir:test-mac/test-mac.sh [MODE] [TARGET]     # defaults: replace full
.claude/skills/ir:test-mac/test-mac.sh --help              # the full contract
```

Both arguments are optional and may be given in either order. This file says
*which* arguments to pass and what they cost; `test-mac.sh` is the only place
the steps themselves live, and `restore-prod.sh` beside it is the teardown
half of the same workflow.

Why a script and not fenced blocks to copy out (#1855): every step branches on
`MODE`/`TARGET`/`PORT`/`DEV_HOME`/`SOCK`, and each fenced block an agent copies
out runs in a **fresh shell**. A value dropped between blocks failed silently —
an empty `$PORT` made the daemon bind `127.0.0.1:` (invalid), an empty `$SOCK`
turned the socket cleanup into a no-op that reported nothing. One script is one
variable scope, and that whole class is gone.

## Picking the arguments

**Default to `replace full` — don't ask.** Change an axis only when the user
actually asked for it.

- **MODE=`separate`** only on an explicit request: `/ir:test-mac separate`,
  "alongside production", "don't touch production".
- **TARGET=`daemon`/`macos`** only when the user asked to restart one
  component: "just restart the daemon", "rebuild the mac app only".

### MODE

- **`replace`** (default) — take over from production. Kills the running
  production app + daemon (and any dev instance), then runs the freshly built
  dev binaries on the **production port 7837** with the **production state
  dir** (no `IRRLICHT_HOME` override). Because it is on 7837 it receives the
  statusline quota feed and sees the same on-disk sessions/cost data — it
  behaves like production but runs your dev code. There is one instance
  afterward, and the Swift app is installed **directly into
  `/Applications/Irrlicht.app`** rather than a separate bundle, since a human
  only ever looks at one running app. **Destructive — see Tearing down.**
- **`separate`** — a dev instance that **coexists with production**. The dev
  daemon binds port **7838** and stores its state under a worktree-local
  `IRRLICHT_HOME`; the dev app is assembled at `/tmp/IrrlichtDev.app` and
  connects to 7838 via `IRRLICHT_DAEMON_PORT`. Production stays up untouched on
  7837. Since #1178 the Claude Code hooks and statusline feed follow the
  daemon's own bind address, so the dev daemon receives them — but it installs
  them into the shared `~/.claude/settings.json`, which repoints production's
  hooks at 7838 until production restarts.

### TARGET

- **`full`** (default) — rebuild and restart both the daemon and the app.
- **`daemon`** — rebuild + restart just `irrlichd`; skip the Swift
  build/install/relaunch entirely and leave whatever app is currently running
  to reconnect to the fresh daemon.
- **`macos`** — rebuild + relaunch just the Swift app, pointed at whatever
  daemon is already up (adopts it — the same adoption behaviour `full` relies
  on). Requires a daemon to already be reachable on the target port; it does
  not start one.

The axes compose: `replace daemon` tests a Go-only change against the
currently-open production-replacing app; `separate macos` iterates on Swift UI
against an already-running isolated dev daemon.

## Tearing down (replace mode) — REQUIRED to get production back

Quitting the app is **not** enough, for two independent reasons:

- **The daemon**: in replace mode the app only *adopted* the daemon the script
  started (it never owns the process — `DaemonManager.start()` returns early on
  a reachable daemon without recording it, so its `terminateProcess()` is a
  no-op), and that daemon was `nohup`'d, so it keeps running on 7837 after the
  app exits. A relaunched production app would find it reachable and **adopt
  it** — you would be running the production UI against the dev `--record`
  daemon without realising it.
- **The app itself** (after a `TARGET=macos`/`full` run): its executable +
  `Sparkle.framework` inside `/Applications/Irrlicht.app` were overwritten with
  the dev build, so relaunching that bundle launches the **dev** binary.

```bash
.claude/skills/ir:test-mac/restore-prod.sh
```

It does the whole sequence: kill the app + daemon → restore the backed-up
production bundle if a dev overwrite ever happened → **gate** on 7837 actually
freeing → launch `/Applications/Irrlicht.app` → confirm production's own daemon
comes up. Running the DMG/PKG installer is **not** a substitute: it replaces the
app bundle but leaves the dev daemon on 7837, which the freshly installed app
would still adopt.

## What the script refuses to do

These gates are load-bearing and each one's failure mode is silence rather than
an error. `tools/lib/test-mac-script_test.sh` drives the script against stubs
and asserts every one of them;
`tools/lib/test-mac-script-mutations_test.sh` breaks each in turn and requires
that lock test to go red. Both run inside `tools/preflight.sh --only tools`.

- An unrecognised `MODE`/`TARGET` is refused, not defaulted — a typo like
  `seperate` would otherwise no-op every later step instead of erroring.
- The app is killed **before** the daemon, so a live app never observes a
  daemon-less gap and races to spawn its own replacement.
- It waits for the app process to actually exit before touching its bundle, and
  hard-aborts if it outlives the wait.
- It refuses to install a build product that is missing, stale (any `.swift`
  source newer than the built binary), or produced by a failed `swift build`.
- It refreshes the production backup whenever `/Applications/Irrlicht.app` is
  still Developer-ID-signed, and **refuses to proceed** when the app is
  dev-signed and no backup exists.
- It hard-aborts rather than launching the app when no daemon answers on the
  target port — a gate, not a courtesy sleep. If the app starts with nothing
  reachable on 7837 it runs `pkill -x irrlichd` and respawns a daemon **without**
  `--record`, silently defeating the run.

## Notes

- **separate mode — production keeps running.** The production Irrlicht.app (from DMG) and its bundled daemon stay on port 7837 with state under `~/.local/share/irrlicht/` + `~/Library/Application Support/Irrlicht/`. The dev instance binds port 7838 and routes its WRITTEN state — socket, recordings, history, session store, ledgers, and cost store — beneath `IRRLICHT_HOME`, so it never prunes or mutates production's session/cost data. The dev app reaches the dev daemon because `IRRLICHT_DAEMON_PORT` (via `open --env`) overrides the hardcoded default; `DaemonManager` also skips its global `pkill` when a custom port is set, so it can't take production down.
- **separate mode shares with production:** it reads the same `~/.claude` transcripts (so the dev UI shows the same live sessions) and appends to the same `~/Library/Application Support/Irrlicht/logs/events.log`. It does NOT share the on-disk session/ledger/cost stores. It DOES receive the hook + statusline quota feed since #1178 (both follow `IRRLICHT_BIND_ADDR`), at the cost of repointing the shared `~/.claude/settings.json` at 7838 for as long as the dev daemon is the last one to have installed — production picks its own port back up on its next start.
- **separate mode does NOT install hooks, by design (#1449).** `IRRLICHT_HOME` isolates daemon state; it does not move `~/.claude/settings.json`, `~/.codex/hooks.json` or `~/.config/kitty/kitty.conf`, which follow `$HOME`. A grant-all daemon on 7838 that installed hooks would repoint your REAL config at 7838 and leave it there when the dev daemon dies — the incident #1449 was filed for, which had already happened three times. Those installs are refused with an error naming each file, and the permission shows as "granted but NOT applied" in the wizard. Everything not backed by a shared config file (transcripts, watchers, control) works normally. To test hook installation itself, back the files up (`irrlichd --print-managed-files` lists them) and add `IRRLICHT_ALLOW_SHARED_CONFIG_WRITES=1` — then restore them afterwards.
- **separate mode gets `IRRLICHT_PERMISSION_MODE=grant-all`**, because a fresh isolated state dir has no consent answers (#570) and the daemon would otherwise monitor nothing until the permission wizard is answered. Drop that variable (edit the script for the run) when the point of the session *is* the wizard. replace mode omits it, reading the user's real answers instead.
- **replace mode — single instance on production's footprint.** Runs the dev binaries on port 7837 with the production state dir (no `IRRLICHT_HOME`), so the statusline quota feed and the production session/cost/ledger stores all apply, and (for `TARGET=macos`/`full`) the Swift app is installed directly into `/Applications/Irrlicht.app`. Because the dev daemon runs with `--record`, recordings land in the production recordings dir (`~/.local/share/irrlicht/recordings/`). **⚠️ The dev daemon mutates production data.** Without `IRRLICHT_HOME` its startup sweeps (`PruneStale` / dead-proc / orphan-ledger / cost prune) run against the real `~/.local/share/irrlicht/` + `~/Library/Application Support/Irrlicht/` stores — exactly the isolation #448 added, deliberately removed here. Only use replace mode when the dev build's on-disk schema matches the installed production build; a dev branch mid-migration can prune or rewrite production sessions/ledgers/cost rows that the production binary then misreads.
- **Backup freshness.** The production backup at `.build/irrlicht-prod-backup/Irrlicht.app` is refreshed automatically whenever `/Applications/Irrlicht.app` is still Developer-ID-signed (a genuine, untouched production build) — so installing a *new* production release via the DMG/PKG installer, then running replace mode again, captures the new release as the restore baseline instead of reinstalling a stale one. If the app is already dev-signed (mid-test) and the backup is missing (e.g. `.build` was wiped), the script refuses rather than overwriting the last remaining copy with no safety net — reinstall via the DMG/PKG installer to recover.
- **TCC in replace mode.** The dev build is signed and launched at production's own path, so expect one extra Accessibility/Automation re-grant prompt the first time. Production's own grant (tied to its Developer ID signature) is unaffected once restored. Run `tools/dev-sign-setup.sh` once to install the `"Irrlicht Dev"` self-signed identity — the script signs with it when present, which gives the app a stable designated requirement so grants persist across rebuilds. Without it, every rebuild invalidates TCC.
- Daemon logs: `/tmp/irrlichd-dev.log` · Swift app logs: `/tmp/irrlicht-app-dev.log`
