---
name: ir:test-mac
description: >
  Build and run a dev irrlicht daemon and/or macOS Swift app for local
  testing. Two independent axes: MODE (replace, default — takes over
  production on port 7837/production state; separate — coexists on port
  7838/isolated state) and TARGET (full, default — daemon + app; daemon —
  just rebuild+restart irrlichd, leave the running app to reconnect; macos —
  just rebuild+relaunch the Swift app against whatever daemon is already up).
  Use when the user says "test mac", "restart mac", "rebuild mac",
  "restart just the daemon", "rebuild the mac app only", or "/ir:test-mac".
---

# Build & Run macOS Dev Stack (replace by default; componentized restart)

Build the irrlicht daemon and/or Swift app, then run them, governed by two
independent axes:

- **MODE** — `replace` (default) or `separate`:
  - **replace** — take over from production. Kill the running production
    app + daemon (and any dev instance), then run the freshly built dev
    binaries on the **production port 7837** with the **production state
    dir** (no `IRRLICHT_HOME` override). Because it's on 7837 it receives the
    statusline quota feed and sees the same on-disk sessions/cost data —
    i.e. it behaves like production but runs your dev code. There is only
    one instance afterward, and the Swift app is installed **directly into
    `/Applications/Irrlicht.app`** (see TARGET below) rather than a separate
    bundle, since a human only ever looks at one running app.
  - **separate** — a dev instance that **coexists with production**. The dev
    daemon binds port **7838** and stores its state under a worktree-local
    `IRRLICHT_HOME`; the dev app is assembled at `/tmp/IrrlichtDev.app` and
    connects to 7838 via `IRRLICHT_DAEMON_PORT`. Production stays up
    untouched on 7837. Note: the Claude Code statusline hook is hardcoded to
    POST quota data to 7837, so a separate dev instance shows the
    **subscription empty-state** (no rate-limit data) — that's expected.
- **TARGET** — `full` (default), `daemon`, or `macos`:
  - **full** — rebuild and restart both the daemon and the app. Today's only
    behavior before this axis existed.
  - **daemon** — rebuild + restart just `irrlichd`; skip the Swift
    build/install/relaunch steps entirely and leave whatever app is
    currently running to reconnect to the fresh daemon.
  - **macos** — rebuild + relaunch just the Swift app, pointed at whatever
    daemon is already up (adopts it — same adoption behavior `full` relies
    on). Requires a daemon to already be reachable on the target port; it
    does not start one.

  These compose: e.g. "replace, daemon only" tests a Go-only change against
  the currently-open production-replacing app; "separate, macos only"
  iterates on Swift UI against an already-running isolated dev daemon.

## Steps

0. **Set the run config.** Default to `MODE=replace` and `TARGET=full` —
   don't ask. Only change `MODE` to `separate` when the user explicitly
   requested it (e.g. `/ir:test-mac separate`, or "alongside production" /
   "don't touch production"). Only change `TARGET` when the user asked to
   restart just one component (e.g. "just restart the daemon", "rebuild the
   mac app only"). Note that replace is destructive (it kills production and
   lets a dev binary read/write production state, and — for `TARGET=macos`
   or `full` — overwrites `/Applications/Irrlicht.app`'s executable in
   place); that's the intended default, but it's why the final teardown step
   is required to get production back.

   Every later step branches on `$MODE`/`$TARGET`/`$PORT`/`$DEV_HOME`/`$SOCK`.
   **These are shell variables, and each fenced block runs in a fresh
   shell**, so when you execute a later step you must carry the step-0
   values forward (re-declare them, or inline the literal port/path) — an
   empty `$PORT` makes the daemon bind `127.0.0.1:` (invalid) and an empty
   `$SOCK` turns the socket cleanup into a no-op.
   ```bash
   REPO_ROOT="$(git rev-parse --show-toplevel)"        # worktree root if in a worktree, else main repo
   IRRLICHTD_BIN="/Users/ingo/projects/irrlicht/core/bin/irrlichd"  # stable path: build target == launch target
   PROD_APP="/Applications/Irrlicht.app"                            # replace mode installs directly here
   PROD_BACKUP="/Users/ingo/projects/irrlicht/.build/irrlicht-prod-backup/Irrlicht.app"  # untouched-original copy, made once
   DEV_APP="/tmp/IrrlichtDev.app"                                   # separate mode only
   MODE="replace"         # default; set to "separate" only if the user explicitly asked for it
   TARGET="full"          # default; set to "daemon" or "macos" to restart just one component

   # Every later gate is an exact-string check with no default branch — a
   # typo here (e.g. "seperate") would otherwise silently no-op every step
   # instead of erroring, so validate the two literals up front.
   case "$MODE" in
     replace|separate) ;;
     *) echo "ERROR: MODE must be 'replace' or 'separate' (got '$MODE')" >&2; exit 1 ;;
   esac
   case "$TARGET" in
     daemon|macos|full) ;;
     *) echo "ERROR: TARGET must be 'daemon', 'macos', or 'full' (got '$TARGET')" >&2; exit 1 ;;
   esac

   if [ "$MODE" = "replace" ]; then
     PORT=7837                                          # production port (statusline quota hook targets this)
     DEV_HOME=""                                        # no IRRLICHT_HOME override → production state dir
     SOCK="$HOME/.local/share/irrlicht/irrlichd.sock"   # production socket
     APP_TARGET="$PROD_APP"
   else
     PORT=7838                                          # isolated dev port
     DEV_HOME="$REPO_ROOT/.build/irrlicht-home"         # isolated state dir (IRRLICHT_HOME)
     SOCK="$DEV_HOME/irrlichd.sock"
     APP_TARGET="$DEV_APP"
     mkdir -p "$DEV_HOME"
   fi
   echo "MODE=$MODE TARGET=$TARGET PORT=$PORT DEV_HOME=${DEV_HOME:-<production>}"
   ```
   `$IRRLICHTD_BIN` is the main repo's bin (a stable absolute path) so the
   build and launch steps always agree even when `$REPO_ROOT` is a worktree
   — the source compiled is still the worktree's (`go build` runs in
   `$REPO_ROOT/core`). `$PROD_BACKUP` is likewise a stable main-repo path
   (not worktree-relative) so it survives across worktree runs and removals.

1. **Build the Go daemon** — the daemon resolves the dashboard from
   `platforms/web/index.html` at runtime via a walk-up search from its own
   executable; no embed, no codegen. Skipped when `TARGET=macos` (no daemon
   change requested).
   ```bash
   if [ "$TARGET" = "daemon" ] || [ "$TARGET" = "full" ]; then
     cd "$REPO_ROOT/core" && go build -o "$IRRLICHTD_BIN" ./cmd/irrlichd
   fi
   ```
   Note: the binary is built from the worktree's source but placed at the
   stable `$IRRLICHTD_BIN` path that the start-daemon step launches.

2. **Build the Swift app** (compile only — no bundle mutation yet). Skipped
   when `TARGET=daemon` (no app change requested). Building is safe to do
   before killing anything since it only writes into `.build/`; the actual
   install into a live bundle happens after the kill step below.
   ```bash
   if [ "$TARGET" = "macos" ] || [ "$TARGET" = "full" ]; then
     cd "$REPO_ROOT/platforms/macos" && swift build 2>&1 | tail -5
   fi
   ```

3. **Kill the instances this mode+target replaces.** Daemon and app are
   killed independently so a `TARGET=daemon`/`macos`-only run leaves the
   other component alone. The app is killed *before* the daemon (matching
   `restore-prod.sh`'s order) so a still-alive app never observes a
   momentary daemon-less gap and reacts by spawning its own replacement,
   which could win the port race against the daemon step 6 starts next.
   ```bash
   if [ "$TARGET" = "macos" ] || [ "$TARGET" = "full" ]; then
     if [ "$MODE" = "replace" ]; then
       APP_KILL_PATTERN="Irrlicht\.app/Contents/MacOS/Irrlicht"  # about to have its executable overwritten in place
       pkill -f "$APP_KILL_PATTERN" 2>/dev/null
       pkill -f "IrrlichtDev"       2>/dev/null   # any leftover separate-mode dev app, for tidiness
     else
       APP_KILL_PATTERN="IrrlichtDev"   # prior dev app only
       pkill -f "$APP_KILL_PATTERN" 2>/dev/null
     fi
     # step 5 is about to back up / overwrite this same app's on-disk bundle
     # (replace mode) — wait for the process to actually exit rather than a
     # flat sleep, so we never copy or overwrite files a dying process still
     # has open.
     for _ in 1 2 3 4 5; do
       pgrep -f "$APP_KILL_PATTERN" >/dev/null 2>&1 || break
       sleep 1
     done
     if [ "$MODE" = "replace" ] && pgrep -f "$APP_KILL_PATTERN" >/dev/null 2>&1; then
       echo "ABORT: the app process is still running — refusing to overwrite its bundle in step 5." >&2
       return 1 2>/dev/null || exit 1
     fi
   fi
   if [ "$TARGET" = "daemon" ] || [ "$TARGET" = "full" ]; then
     if [ "$MODE" = "replace" ]; then
       pkill -x "irrlichd" 2>/dev/null   # ALL daemons by exact name (prod's bundled one + any standalone dev one)
     else
       pkill -f "core/bin/irrlichd" 2>/dev/null   # prior dev daemon only (NOT production)
     fi
     PORT_PIDS="$(lsof -ti tcp:$PORT 2>/dev/null)"   # belt-and-suspenders: anything still on the target port
     [ -n "$PORT_PIDS" ] && kill $PORT_PIDS 2>/dev/null
     sleep 1
   fi
   ```

4. **Clean up the stale socket** for the target instance. Only meaningful
   when the daemon is (re)starting.
   ```bash
   if [ "$TARGET" = "daemon" ] || [ "$TARGET" = "full" ]; then
     rm -f "$SOCK"
   fi
   ```

5. **Install the app bundle.** Skipped when `TARGET=daemon`.
   - **replace** — install straight into `$PROD_APP` (no more parallel
     `/tmp/IrrlichtDev.app`, since it shares production's bundle identifier
     and a human only reviews one running app at a time). Back up the
     **untouched** original bundle as a full directory copy — a full-bundle
     backup sidesteps any code-signature mismatch between the outer bundle
     and its nested binaries that a partial-file restore could otherwise
     leave behind. The teardown step restores from this copy.

     The backup is refreshed (not just made once-ever) whenever the
     currently-installed app is still genuinely Developer-ID-signed — never
     trust an existing backup blindly, since it can predate a newer
     production release installed since (e.g. via `/ir:release`), which
     would otherwise make the teardown step silently reinstall a stale
     build. If the app is *not* currently Developer-ID-signed (a prior
     replace-mode run's dev build, still installed) and no backup exists
     either, refuse to proceed rather than overwriting the only remaining
     copy without a safety net. The build outputs are also checked for
     existence before anything in the live bundle is touched, so a failed
     `swift build` aborts here instead of leaving `Sparkle.framework`
     deleted with nothing to replace it.
     ```bash
     if [ "$TARGET" = "macos" ] || [ "$TARGET" = "full" ]; then
       if [ "$MODE" = "replace" ]; then
         DEBUG_BIN="$REPO_ROOT/platforms/macos/.build/arm64-apple-macosx/debug/Irrlicht"
         DEBUG_SPARKLE="$REPO_ROOT/platforms/macos/.build/arm64-apple-macosx/debug/Sparkle.framework"
         if [ ! -x "$DEBUG_BIN" ] || [ ! -d "$DEBUG_SPARKLE" ]; then
           echo "ERROR: swift build did not produce $DEBUG_BIN / $DEBUG_SPARKLE — not touching $PROD_APP." >&2
           exit 1
         fi
         if [ ! -d "$PROD_APP" ]; then
           echo "ERROR: $PROD_APP is not installed — run the DMG/PKG installer first." >&2
           exit 1
         fi
         if codesign -dv --verbose=4 "$PROD_APP" 2>&1 | grep -q "^Authority=Developer ID Application"; then
           rm -rf "$PROD_BACKUP"
           mkdir -p "$(dirname "$PROD_BACKUP")"
           if ! cp -R "$PROD_APP" "$PROD_BACKUP"; then
             echo "ERROR: backup of $PROD_APP to $PROD_BACKUP failed — not touching $PROD_APP." >&2
             rm -rf "$PROD_BACKUP"
             exit 1
           fi
           echo "Backed up the untouched production bundle to $PROD_BACKUP (restore-prod.sh restores from this)."
         elif [ ! -d "$PROD_BACKUP" ]; then
           echo "ERROR: $PROD_APP isn't Developer-ID-signed (looks like a leftover dev build) and no backup exists — refusing to overwrite it with no safety net. Run the DMG/PKG installer first." >&2
           exit 1
         fi
         cp "$DEBUG_BIN" "$APP_TARGET/Contents/MacOS/Irrlicht"
         rm -rf "$APP_TARGET/Contents/Frameworks/Sparkle.framework"
         cp -R "$DEBUG_SPARKLE" "$APP_TARGET/Contents/Frameworks/Sparkle.framework"
         # Refresh the icon too, so a developer iterating on Resources/ assets
         # sees the same result in replace mode as in separate mode instead of
         # a stale production copy.
         cp "$REPO_ROOT/platforms/macos/Irrlicht/Resources/AppIcon.icns" "$APP_TARGET/Contents/Resources/AppIcon.icns"
         # Stamp the version string too — otherwise Info.plist still carries
         # whatever release version was last installed (e.g. "0.5.7"), so
         # Settings/About would show a stale release version on a dev build
         # even though the binary itself is freshly compiled from source.
         # Matches separate mode's hardcoded "dev" (see its Info.plist template
         # below) so both modes read the same in the UI.
         /usr/libexec/PlistBuddy -c "Set :CFBundleShortVersionString dev" "$APP_TARGET/Contents/Info.plist"
         /usr/libexec/PlistBuddy -c "Set :CFBundleVersion dev" "$APP_TARGET/Contents/Info.plist"
         # The SwiftPM debug binary links Sparkle as @rpath/Sparkle.framework but only
         # carries an @loader_path rpath (= Contents/MacOS) — it lacks the bundle-layout
         # rpath a release .app build adds. Without this, dyld can't find the embedded
         # framework and the app crashes at launch. (Must run BEFORE the codesign step
         # below, since it mutates the binary and invalidates the signature.)
         install_name_tool -add_rpath @executable_path/../Frameworks "$APP_TARGET/Contents/MacOS/Irrlicht"
       fi
     fi
     ```
   - **separate** — unchanged: assemble a fresh `/tmp/IrrlichtDev.app` bundle
     from scratch so `UNUserNotificationCenter` (desktop notifications) works.
     ```bash
     if [ "$TARGET" = "macos" ] || [ "$TARGET" = "full" ]; then
       if [ "$MODE" = "separate" ]; then
         rm -rf "$APP_TARGET"
         mkdir -p "$APP_TARGET/Contents/MacOS" "$APP_TARGET/Contents/Resources"
         cp "$REPO_ROOT/platforms/macos/.build/arm64-apple-macosx/debug/Irrlicht" "$APP_TARGET/Contents/MacOS/Irrlicht"
         cp "$REPO_ROOT/platforms/macos/Irrlicht/Resources/AppIcon.icns" "$APP_TARGET/Contents/Resources/AppIcon.icns"
         # Embed Sparkle.framework (required since v0.4.7 auto-update integration)
         mkdir -p "$APP_TARGET/Contents/Frameworks"
         cp -R "$REPO_ROOT/platforms/macos/.build/arm64-apple-macosx/debug/Sparkle.framework" "$APP_TARGET/Contents/Frameworks/"
         install_name_tool -add_rpath @executable_path/../Frameworks "$APP_TARGET/Contents/MacOS/Irrlicht"
     cat > "$APP_TARGET/Contents/Info.plist" << 'PLIST'
     <?xml version="1.0" encoding="UTF-8"?>
     <!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
     <plist version="1.0">
     <dict>
         <key>CFBundleExecutable</key>
         <string>Irrlicht</string>
         <key>CFBundleIdentifier</key>
         <string>io.irrlicht.app</string>
         <key>CFBundleIconFile</key>
         <string>AppIcon</string>
         <key>CFBundleName</key>
         <string>Irrlicht Dev</string>
         <key>CFBundlePackageType</key>
         <string>APPL</string>
         <key>CFBundleShortVersionString</key>
         <string>dev</string>
         <key>LSUIElement</key>
         <true/>
         <key>NSAppleEventsUsageDescription</key>
         <string>Irrlicht uses AppleScript to bring the correct iTerm2 or Terminal.app window and tab to the front when you click a session row.</string>
         <key>NSFocusStatusUsageDescription</key>
         <string>Irrlicht uses macOS Focus status to silence notification sounds and spoken alerts while you're in Do Not Disturb, Sleep, or any other Focus mode.</string>
     </dict>
     </plist>
     PLIST
       fi
     fi
     ```
   - **codesign (both modes, identical).** Sign with the persistent
     "Irrlicht Dev" identity if it exists; otherwise fall back to ad-hoc (TCC
     permissions will need to be re-granted each rebuild). Run
     `tools/dev-sign-setup.sh` once to install the identity. Uses the
     dev-only entitlements file (no `com.apple.developer.*` entries — Apple
     gates those to its own certificates and launchd will refuse to spawn a
     self-signed/ad-hoc binary that claims them).
     ```bash
     if [ "$TARGET" = "macos" ] || [ "$TARGET" = "full" ]; then
       ENTITLEMENTS="$REPO_ROOT/platforms/macos/Irrlicht/Resources/Irrlicht-dev.entitlements"
       if security find-identity -v -p codesigning 2>/dev/null | grep -q "Irrlicht Dev"; then
           codesign --force --deep --sign "Irrlicht Dev" --entitlements "$ENTITLEMENTS" "$APP_TARGET" 2>&1
       else
           codesign --force --deep --sign - --entitlements "$ENTITLEMENTS" "$APP_TARGET" 2>&1
       fi
     fi
     ```
     Heads up for **replace** mode specifically: since the dev build is now
     signed and launched at production's own path, expect one extra
     Accessibility/Automation re-grant prompt the first time this runs after
     picking up this change (TCC keys grants off path + code identity, and
     the dev identity is new at this path) — the stable "Irrlicht Dev"
     identity should keep that grant across subsequent dev rebuilds, and
     production's original grant (tied to its own Developer ID signature) is
     unaffected once restored.

6. **Start the daemon** with `--record` for lifecycle event capture. Skipped
   when `TARGET=macos`. In **separate** mode it gets `IRRLICHT_HOME`
   (isolated state) plus `IRRLICHT_PERMISSION_MODE=grant-all` — a fresh
   isolated state dir has no consent answers (#570), so without it the
   daemon monitors nothing until the permission wizard is answered. Drop
   that variable when the point of the session is to test the wizard
   itself. In **replace** mode `IRRLICHT_HOME` is omitted so it reads/writes
   the production state dir — including the user's real permission answers
   (no grant-all there).
   ```bash
   if [ "$TARGET" = "daemon" ] || [ "$TARGET" = "full" ]; then
     if [ "$MODE" = "replace" ]; then
       IRRLICHT_BIND_ADDR=127.0.0.1:$PORT \
         nohup "$IRRLICHTD_BIN" --record > /tmp/irrlichd-dev.log 2>&1 & disown
     else
       IRRLICHT_HOME="$DEV_HOME" IRRLICHT_BIND_ADDR=127.0.0.1:$PORT \
         IRRLICHT_PERMISSION_MODE=grant-all \
         nohup "$IRRLICHTD_BIN" --record > /tmp/irrlichd-dev.log 2>&1 & disown
     fi
   fi
   ```

7. **Wait for a reachable daemon — and HARD-ABORT if there isn't one.** This
   is a gate, not a courtesy sleep. In replace mode the app adopts an
   already-reachable daemon on 7837 and skips its own spawn/pkill; if no
   `--record` daemon is up when the app launches, the app (port 7837 ⇒
   `isCustomPort` false) runs `pkill -x irrlichd` — killing whatever daemon
   is there — and respawns one **without** `--record`, silently defeating
   the whole point. So if `/state` never answers, stop here and do not
   launch the app.
   ```bash
   if [ "$TARGET" = "daemon" ] || [ "$TARGET" = "full" ]; then
     # We just (re)started this daemon above — poll for it to come up.
     READY=""
     for i in 1 2 3 4 5 6 7 8; do
       curl -fsS --max-time 1 "http://127.0.0.1:$PORT/state" >/dev/null 2>&1 && { READY=1; break; }
       sleep 1
     done
     if [ -z "$READY" ]; then
       echo "ABORT: daemon never became reachable on $PORT — not launching the app." >&2
       echo "       (Launching now would let the app pkill our daemon and respawn one without --record.)" >&2
       echo "       Check /tmp/irrlichd-dev.log." >&2
       return 1 2>/dev/null || exit 1
     fi
   else
     # TARGET=macos: we didn't (re)start a daemon — one must already be
     # reachable for the app to adopt, or it will spawn its own (without
     # --record, in replace mode).
     if ! curl -fsS --max-time 1 "http://127.0.0.1:$PORT/state" >/dev/null 2>&1; then
       echo "ABORT: no daemon reachable on $PORT and TARGET=macos doesn't start one." >&2
       echo "       Run with TARGET=daemon or TARGET=full first." >&2
       return 1 2>/dev/null || exit 1
     fi
   fi
   lsof -iTCP:$PORT -sTCP:LISTEN -P -n 2>/dev/null
   ```

8. **Start the app** — launched via LaunchServices so `Bundle.main` resolves
   correctly. Skipped when `TARGET=daemon`. In **separate** mode,
   `IRRLICHT_DAEMON_PORT`/`IRRLICHT_HOME` point it at the isolated dev
   daemon. In **replace** mode, no env overrides are passed: the app uses
   the default port 7837 + production state and, finding the daemon already
   reachable, adopts it instead of spawning its own.
   ```bash
   if [ "$TARGET" = "macos" ] || [ "$TARGET" = "full" ]; then
     if [ "$MODE" = "replace" ]; then
       open --stdout /tmp/irrlicht-app-dev.log --stderr /tmp/irrlicht-app-dev.log "$APP_TARGET"
     else
       open --env IRRLICHT_DAEMON_PORT=$PORT --env IRRLICHT_HOME="$DEV_HOME" \
         --stdout /tmp/irrlicht-app-dev.log --stderr /tmp/irrlicht-app-dev.log "$APP_TARGET"
     fi
   fi
   ```

9. **Verify** — whichever components this run touched are up, and the
   daemon is serving sessions. In replace mode, also confirm quota data is
   present (the whole point of 7837).
   ```bash
   if [ "$TARGET" = "daemon" ] || [ "$TARGET" = "full" ]; then
     pgrep -f "bin/irrlichd" && curl -s http://127.0.0.1:$PORT/api/v1/sessions | head -c 200
   fi
   if [ "$TARGET" = "macos" ] || [ "$TARGET" = "full" ]; then
     if [ "$MODE" = "replace" ]; then
       pgrep -f "Irrlicht\.app/Contents/MacOS/Irrlicht"
     else
       pgrep -f "IrrlichtDev"
     fi
   fi
   if [ "$MODE" = "replace" ]; then
     # 0 is normal right after launch — quota data only appears once Claude Code's
     # statusline posts its next tick to 7837 (seconds to a minute). Re-run to confirm.
     echo; echo "rate-limit mentions (0 now is fine; should climb after the next statusline tick):" \
       "$(curl -s http://127.0.0.1:$PORT/api/v1/sessions | grep -o 'rate_limit\|used_percent' | wc -l | tr -d ' ')"
   fi
   ```

10. **Tearing down (replace mode) — REQUIRED to get production back.**
    Quitting the app is NOT enough, for two independent reasons:
    - **The daemon**: in replace mode the app only *adopted* the daemon
      started in step 6 (it never owns the process —
      `DaemonManager.start()` returns early on a reachable daemon without
      recording it, so its `terminateProcess()` is a no-op), and that
      daemon was `nohup`/`disown`'d, so it keeps running on 7837 after the
      app exits. A relaunched production app would find it still reachable
      and **adopt it** — you'd be running the production UI against the dev
      `--record` daemon without realizing it.
    - **The app itself** (only if a `TARGET=macos`/`full` run happened): its
      executable + Sparkle.framework inside `/Applications/Irrlicht.app`
      were overwritten with the dev build. Relaunching that bundle launches
      the **dev** binary, not production, until it's restored.

    Use the bundled **`restore-prod.sh`** — it does the whole sequence (kill
    the app + daemon → restore the backed-up production bundle if one was
    ever overwritten → GATE on 7837 actually freeing → launch
    `/Applications/Irrlicht.app` → confirm prod's own daemon comes up). Note:
    the installer does NOT substitute for this teardown by itself — it
    replaces the app bundle, but leaves the dev daemon running on 7837 (which
    the freshly-installed app would still adopt), so run this script (or at
    least the `pkill -x irrlichd`) regardless.
    ```bash
    "$(git rev-parse --show-toplevel)/.claude/skills/ir:test-mac/restore-prod.sh"
    ```

## Notes
- **separate mode — production keeps running.** The production Irrlicht.app (from DMG) and its bundled daemon stay on port 7837 with state under `~/.local/share/irrlicht/` + `~/Library/Application Support/Irrlicht/`. The dev instance binds port 7838 and routes its WRITTEN state — socket, recordings, history, session store, ledgers, and cost store — beneath `IRRLICHT_HOME=$DEV_HOME`, so it never prunes or mutates production's session/cost data. The dev app reaches the dev daemon because `IRRLICHT_DAEMON_PORT` (via `open --env`) overrides the hardcoded default; `DaemonManager` also skips its global `pkill` when a custom port is set, so it can't take production down.
- **separate mode shares with production:** it reads the same `~/.claude` transcripts (so the dev UI shows the same live sessions) and appends to the same `~/Library/Application Support/Irrlicht/logs/events.log`. It does NOT share the on-disk session/ledger/cost stores — and it does NOT receive the statusline quota feed (that hook posts only to 7837), so the subscription panel shows its empty-state.
- **replace mode — single instance on production's footprint.** Runs the dev binaries on port 7837 with the production state dir (no `IRRLICHT_HOME`), so the statusline quota feed and the production session/cost/ledger stores all apply, and (for `TARGET=macos`/`full`) the Swift app is installed directly into `/Applications/Irrlicht.app` rather than a parallel bundle. Because the dev daemon runs with `--record`, recordings land in the production recordings dir (`~/.local/share/irrlicht/recordings/`). **⚠️ The dev daemon mutates production data.** Without `IRRLICHT_HOME` its startup sweeps (`PruneStale` / dead-proc / orphan-ledger / cost prune) run against the real `~/.local/share/irrlicht/` + `~/Library/Application Support/Irrlicht/` stores — this is exactly the isolation #448 added, deliberately removed here. Only use replace mode when the dev build's on-disk schema matches the installed production build; a dev branch mid-migration can prune or rewrite production sessions/ledgers/cost rows that the production binary then misreads. **Returning to production requires the teardown step** (run `restore-prod.sh`) — quitting the app does NOT stop the adopted daemon, and a relaunched (or freshly *reinstalled*) production app will silently adopt the leftover dev daemon on 7837 if you skip it. Running the installer is not a substitute: it replaces the app bundle, not the running daemon.
- **Backup freshness.** `PROD_BACKUP` is refreshed automatically whenever step 5 finds `$PROD_APP` still Developer-ID-signed (i.e. a genuine, untouched production build) — so installing a *new* production release via the DMG/PKG installer, then running replace mode again, correctly captures the new release as the restore baseline instead of reinstalling a stale one. If `$PROD_APP` is already dev-signed (mid-test) and `PROD_BACKUP` is missing (e.g. `.build` was wiped), step 5 refuses to proceed rather than overwriting the last remaining copy with no safety net — reinstall via the DMG/PKG installer to recover.
- Daemon logs: `/tmp/irrlichd-dev.log` · Swift app logs: `/tmp/irrlicht-app-dev.log`
- **TCC stability**: run `tools/dev-sign-setup.sh` once to install the `"Irrlicht Dev"` self-signed code signing identity. The skill automatically signs with it when present, giving the app a stable designated requirement so Accessibility/Automation grants persist across rebuilds. Without it, every rebuild invalidates TCC and requires re-granting in System Settings.
