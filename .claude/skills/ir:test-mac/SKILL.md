---
name: ir:test-mac
description: >
  Build and run a dev irrlicht daemon + macOS Swift app for local testing.
  Asks whether to run a SEPARATE instance alongside production (isolated state,
  port 7838 — production keeps running) or to REPLACE the running production
  versions (production port 7837 + production state, so the statusline quota
  feed and existing sessions show up). Use when the user says "test mac",
  "restart mac", "rebuild mac", or "/ir:test-mac".
---

# Build & Run macOS Dev Stack (separate or replace)

Build the irrlicht daemon and Swift app, then run them in one of two modes the
user chooses up front:

- **separate** (default, recommended) — a dev instance that **coexists with
  production**. The dev daemon binds port **7838** and stores its state under a
  worktree-local `IRRLICHT_HOME`; the dev app connects to 7838 via
  `IRRLICHT_DAEMON_PORT`. Production stays up untouched on 7837. Only prior
  *dev* instances are replaced on rerun. Note: the Claude Code statusline hook
  is hardcoded to POST quota data to 7837, so a separate dev instance shows the
  **subscription empty-state** (no rate-limit data) — that's expected.
- **replace** — take over from production. Kill the running production app +
  daemon (and any dev instance), then run the freshly built dev binaries on the
  **production port 7837** with the **production state dir** (no `IRRLICHT_HOME`
  override). Because it's on 7837 it receives the statusline quota feed and sees
  the same on-disk sessions/cost data — i.e. it behaves like production but runs
  your dev code. There is only one instance afterward.

## Steps

0. **Ask the user which mode, then set the run config.** Use `AskUserQuestion`
   with two options — "Separate (alongside production)" (recommended, first) and
   "Replace production" — unless the user already said which one they want in
   their message. Then set the config below from the answer. All later steps
   read `$MODE`, `$REPO_ROOT`, `$PORT`, `$DEV_HOME`, `$SOCK`, `$DEV_APP`, and
   `$IRRLICHTD_BIN`.
   ```bash
   REPO_ROOT="$(git rev-parse --show-toplevel)"        # worktree root if in a worktree, else main repo
   IRRLICHTD_BIN="/Users/ingo/projects/irrlicht/core/bin/irrlichd"  # stable path: build target == launch target
   DEV_APP="/tmp/IrrlichtDev.app"
   MODE="separate"        # <-- set to "separate" or "replace" from the user's answer

   if [ "$MODE" = "replace" ]; then
     PORT=7837                                          # production port (statusline quota hook targets this)
     DEV_HOME=""                                        # no IRRLICHT_HOME override → production state dir
     SOCK="$HOME/.local/share/irrlicht/irrlichd.sock"   # production socket
   else
     PORT=7838                                          # isolated dev port
     DEV_HOME="$REPO_ROOT/.build/irrlicht-home"         # isolated state dir (IRRLICHT_HOME)
     SOCK="$DEV_HOME/irrlichd.sock"
     mkdir -p "$DEV_HOME"
   fi
   echo "MODE=$MODE PORT=$PORT DEV_HOME=${DEV_HOME:-<production>}"
   ```
   `$IRRLICHTD_BIN` is the main repo's bin (a stable absolute path) so the build
   and launch steps always agree even when `$REPO_ROOT` is a worktree — the
   source compiled is still the worktree's (`go build` runs in `$REPO_ROOT/core`).

1. **Build the Go daemon** — the daemon resolves the dashboard from `platforms/web/index.html` at runtime via a walk-up search from its own executable; no embed, no codegen.
   ```bash
   cd "$REPO_ROOT/core" && go build -o "$IRRLICHTD_BIN" ./cmd/irrlichd
   ```
   Note: the binary is built from the worktree's source but placed at the stable `$IRRLICHTD_BIN` path that step 5 launches.

2. **Build the Swift app and assemble .app bundle** (identical in both modes)
   ```bash
   cd "$REPO_ROOT/platforms/macos" && swift build 2>&1 | tail -5
   ```
   Then assemble a proper `.app` bundle so that `UNUserNotificationCenter` (desktop notifications) works:
   ```bash
   rm -rf "$DEV_APP"
   mkdir -p "$DEV_APP/Contents/MacOS" "$DEV_APP/Contents/Resources"
   cp "$REPO_ROOT/platforms/macos/.build/arm64-apple-macosx/debug/Irrlicht" "$DEV_APP/Contents/MacOS/Irrlicht"
   cp "$REPO_ROOT/platforms/macos/Irrlicht/Resources/AppIcon.icns" "$DEV_APP/Contents/Resources/AppIcon.icns"
   # Copy the SwiftPM resource bundle so Bundle.module works at runtime
   cp -R "$REPO_ROOT/platforms/macos/.build/arm64-apple-macosx/debug/Irrlicht_Irrlicht.bundle" "$DEV_APP/Contents/Resources/Irrlicht_Irrlicht.bundle" 2>/dev/null || true
   # Embed Sparkle.framework (required since v0.4.7 auto-update integration)
   mkdir -p "$DEV_APP/Contents/Frameworks"
   cp -R "$REPO_ROOT/platforms/macos/.build/arm64-apple-macosx/debug/Sparkle.framework" "$DEV_APP/Contents/Frameworks/"
   # The SwiftPM debug binary links Sparkle as @rpath/Sparkle.framework but only
   # carries an @loader_path rpath (= Contents/MacOS) — it lacks the bundle-layout
   # rpath a release .app build adds. Without this, dyld can't find the embedded
   # framework and the app crashes at launch. (Must run BEFORE the codesign step
   # below, since it mutates the binary and invalidates the signature.)
   install_name_tool -add_rpath @executable_path/../Frameworks "$DEV_APP/Contents/MacOS/Irrlicht"
   cat > "$DEV_APP/Contents/Info.plist" << 'PLIST'
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
   # Use the dev-only entitlements file (no com.apple.developer.* entries —
   # Apple gates those to its own certificates and launchd will refuse to spawn
   # a self-signed/ad-hoc binary that claims them). The full release entitlements
   # at Irrlicht.entitlements include com.apple.developer.focus-status for the
   # production Developer-ID build; in dev, INFocusStatusCenter therefore reports
   # "unauthorized" and FocusMonitor returns false. That's expected — verify the
   # gating logic via unit tests; the live Focus suppression is only testable in
   # the signed release build.
   ENTITLEMENTS="$REPO_ROOT/platforms/macos/Irrlicht/Resources/Irrlicht-dev.entitlements"
   # Sign with the persistent "Irrlicht Dev" identity if it exists; otherwise
   # fall back to ad-hoc (TCC permissions will need to be re-granted each
   # rebuild). Run tools/dev-sign-setup.sh once to install the identity.
   if security find-identity -v -p codesigning 2>/dev/null | grep -q "Irrlicht Dev"; then
       codesign --force --deep --sign "Irrlicht Dev" --entitlements "$ENTITLEMENTS" "$DEV_APP" 2>&1
   else
       codesign --force --deep --sign - --entitlements "$ENTITLEMENTS" "$DEV_APP" 2>&1
   fi
   ```

3. **Kill the instances this mode replaces.**
   - **separate**: only prior *dev* instances; production (on 7837) stays up.
   - **replace**: the production app + ALL daemons (prod is named `irrlichd`,
     exactly like dev) + any prior dev app, so only the fresh build remains.
   ```bash
   if [ "$MODE" = "replace" ]; then
     pkill -f "Irrlicht.app/Contents/MacOS/Irrlicht" 2>/dev/null   # production app (NOT IrrlichtDev.app — different folder)
     pkill -f "IrrlichtDev"                          2>/dev/null   # any prior dev app
     pkill -x "irrlichd"                             2>/dev/null   # ALL daemons by exact name (prod bundle + dev)
   else
     pkill -f "IrrlichtDev"                          2>/dev/null   # prior dev app only
     pkill -f "core/bin/irrlichd"                    2>/dev/null   # prior dev daemon (NOT production, which runs from the .app bundle)
   fi
   PORT_PIDS="$(lsof -ti tcp:$PORT 2>/dev/null)"   # belt-and-suspenders: anything still on the target port
   [ -n "$PORT_PIDS" ] && kill $PORT_PIDS 2>/dev/null
   sleep 2
   ```

4. **Clean up the stale socket** for the target instance.
   ```bash
   rm -f "$SOCK"
   ```

5. **Start the daemon** with `--record` for lifecycle event capture. In
   **separate** mode it gets `IRRLICHT_HOME` (isolated state); in **replace**
   mode `IRRLICHT_HOME` is omitted so it reads/writes the production state dir.
   ```bash
   if [ "$MODE" = "replace" ]; then
     IRRLICHT_BIND_ADDR=127.0.0.1:$PORT \
       nohup "$IRRLICHTD_BIN" --record > /tmp/irrlichd-dev.log 2>&1 & disown
   else
     IRRLICHT_HOME="$DEV_HOME" IRRLICHT_BIND_ADDR=127.0.0.1:$PORT \
       nohup "$IRRLICHTD_BIN" --record > /tmp/irrlichd-dev.log 2>&1 & disown
   fi
   ```

6. **Wait for the daemon to be reachable** before launching the app. In replace
   mode this matters extra: the app adopts an already-reachable daemon on 7837
   and skips its own spawn/pkill, so the manually started `--record` daemon must
   be up first (otherwise the app would `pkill -x irrlichd` it and respawn one
   without `--record`).
   ```bash
   for i in 1 2 3 4 5; do
     curl -fsS --max-time 1 "http://127.0.0.1:$PORT/state" >/dev/null 2>&1 && break
     sleep 1
   done
   lsof -iTCP:$PORT -sTCP:LISTEN -P -n 2>/dev/null
   ```

7. **Start the dev Swift app** — launched via LaunchServices so `Bundle.main`
   resolves correctly. In **separate** mode, `IRRLICHT_DAEMON_PORT`/`IRRLICHT_HOME`
   point it at the isolated dev daemon. In **replace** mode, no env overrides are
   passed: the app uses the default port 7837 + production state and, finding the
   daemon from step 5 already reachable, adopts it instead of spawning its own.
   ```bash
   if [ "$MODE" = "replace" ]; then
     open --stdout /tmp/irrlicht-app-dev.log --stderr /tmp/irrlicht-app-dev.log "$DEV_APP"
   else
     open --env IRRLICHT_DAEMON_PORT=$PORT --env IRRLICHT_HOME="$DEV_HOME" \
       --stdout /tmp/irrlicht-app-dev.log --stderr /tmp/irrlicht-app-dev.log "$DEV_APP"
   fi
   ```

8. **Verify** — dev daemon + app are running and the daemon is serving sessions.
   In replace mode, also confirm quota data is present (the whole point of 7837).
   ```bash
   pgrep -f "bin/irrlichd" && pgrep -f "IrrlichtDev" && curl -s http://127.0.0.1:$PORT/api/v1/sessions | head -c 200
   if [ "$MODE" = "replace" ]; then
     echo; echo "rate-limit mentions (expect >0 once a statusline tick lands):" \
       "$(curl -s http://127.0.0.1:$PORT/api/v1/sessions | grep -o 'rate_limit\|used_percent' | wc -l | tr -d ' ')"
   fi
   ```

## Notes
- **separate mode — production keeps running.** The production Irrlicht.app (from DMG) and its bundled daemon stay on port 7837 with state under `~/.local/share/irrlicht/` + `~/Library/Application Support/Irrlicht/`. The dev instance binds port 7838 and routes its WRITTEN state — socket, recordings, history, session store, ledgers, and cost store — beneath `IRRLICHT_HOME=$DEV_HOME`, so it never prunes or mutates production's session/cost data. The dev app reaches the dev daemon because `IRRLICHT_DAEMON_PORT` (via `open --env`) overrides the hardcoded default; `DaemonManager` also skips its global `pkill` when a custom port is set, so it can't take production down.
- **separate mode shares with production:** it reads the same `~/.claude` transcripts (so the dev UI shows the same live sessions) and appends to the same `~/Library/Application Support/Irrlicht/logs/events.log`. It does NOT share the on-disk session/ledger/cost stores — and it does NOT receive the statusline quota feed (that hook posts only to 7837), so the subscription panel shows its empty-state.
- **replace mode — single instance on production's footprint.** Runs the dev binaries on port 7837 with the production state dir (no `IRRLICHT_HOME`), so the statusline quota feed and the production session/cost/ledger stores all apply. Because the dev daemon runs with `--record`, recordings land in the production recordings dir (`~/.local/share/irrlicht/recordings/`) for the duration. To return to the real production build, relaunch `/Applications/Irrlicht.app` (it will reclaim 7837 after this dev instance is stopped).
- Daemon logs: `/tmp/irrlichd-dev.log` · Swift app logs: `/tmp/irrlicht-app-dev.log`
- **TCC stability**: run `tools/dev-sign-setup.sh` once to install the `"Irrlicht Dev"` self-signed code signing identity. The skill automatically signs with it when present, giving the app a stable designated requirement so Accessibility/Automation grants persist across rebuilds. Without it, every rebuild invalidates TCC and requires re-granting in System Settings.
