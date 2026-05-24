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

0. **Decide the mode, then set the run config.** If the user typed an explicit
   `separate` or `replace` argument (e.g. `/ir:test-mac replace`), use it. Otherwise
   ask with `AskUserQuestion` — two options, "Separate (alongside production)"
   (recommended, first) and "Replace production". **`replace` is destructive** (it
   kills production and lets a dev binary read/write production state) — never
   default to it; when in doubt, pick `separate`.

   Then edit the `MODE=` line below to the chosen value before running the block —
   it is a placeholder, not a default to run verbatim. Every later step branches on
   `$MODE`/`$PORT`/`$DEV_HOME`/`$SOCK`. **These are shell variables, and each fenced
   block runs in a fresh shell**, so when you execute a later step you must carry the
   step-0 values forward (re-declare them, or inline the literal port/path) — an
   empty `$PORT` makes the daemon bind `127.0.0.1:` (invalid) and an empty `$SOCK`
   turns the socket cleanup into a no-op.
   ```bash
   REPO_ROOT="$(git rev-parse --show-toplevel)"        # worktree root if in a worktree, else main repo
   IRRLICHTD_BIN="/Users/ingo/projects/irrlicht/core/bin/irrlichd"  # stable path: build target == launch target
   DEV_APP="/tmp/IrrlichtDev.app"
   MODE="separate"        # PLACEHOLDER — set to the user's choice ("separate" or "replace") before running

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
     pkill -f "Irrlicht\.app/Contents/MacOS/Irrlicht" 2>/dev/null  # production app (\. is literal; NOT IrrlichtDev.app — different folder)
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

6. **Wait for the daemon to be reachable — and HARD-ABORT if it isn't.** This is a
   gate, not a courtesy sleep. In replace mode the app adopts an already-reachable
   daemon on 7837 and skips its own spawn/pkill; if our `--record` daemon is NOT up
   yet when the app launches, the app (port 7837 ⇒ `isCustomPort` false) runs
   `pkill -x irrlichd` — killing our daemon — and respawns one **without** `--record`,
   silently defeating the whole point. So if `/state` never answers, stop here and do
   not launch the app.
   ```bash
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
     # 0 is normal right after launch — quota data only appears once Claude Code's
     # statusline posts its next tick to 7837 (seconds to a minute). Re-run to confirm.
     echo; echo "rate-limit mentions (0 now is fine; should climb after the next statusline tick):" \
       "$(curl -s http://127.0.0.1:$PORT/api/v1/sessions | grep -o 'rate_limit\|used_percent' | wc -l | tr -d ' ')"
   fi
   ```

9. **Tearing down (replace mode) — REQUIRED to get production back.** Quitting the
   dev app is NOT enough: in replace mode the app only *adopted* the daemon started
   in step 5 (it never owns the process — `DaemonManager.start()` returns early on a
   reachable daemon without recording it, so its `terminateProcess()` is a no-op),
   and that daemon was `nohup`/`disown`'d, so it keeps running on 7837 after the app
   exits. If you then relaunch `/Applications/Irrlicht.app`, production finds the
   leftover dev daemon still reachable and **adopts it** — you'd be running the
   production UI against the dev `--record` daemon without realizing it. So to
   return to production: quit the dev app, then explicitly kill the daemon and
   confirm the port is free *before* relaunching production.

   Use the bundled **`restore-prod.sh`** — it does the whole sequence (kill dev
   app → kill dev daemon → GATE on 7837 actually freeing → launch
   `/Applications/Irrlicht.app` → confirm prod's own daemon comes up). Note: the
   installer does NOT replace this teardown — it swaps the app bundle but leaves
   the dev daemon running on 7837, which the new prod app would still adopt; run
   this script (or at least the `pkill -x irrlichd`) regardless.
   ```bash
   "$(git rev-parse --show-toplevel)/.claude/skills/ir:test-mac/restore-prod.sh"
   ```
   Equivalent by hand, if you want to see each step:
   ```bash
   pkill -f "IrrlichtDev" 2>/dev/null     # quit the dev app
   pkill -x "irrlichd"    2>/dev/null     # stop the adopted dev daemon (the app won't)
   sleep 1
   lsof -iTCP:7837 -sTCP:LISTEN -P -n 2>/dev/null && echo "WARNING: 7837 still held — production will adopt whatever is there" || open /Applications/Irrlicht.app
   ```

## Notes
- **separate mode — production keeps running.** The production Irrlicht.app (from DMG) and its bundled daemon stay on port 7837 with state under `~/.local/share/irrlicht/` + `~/Library/Application Support/Irrlicht/`. The dev instance binds port 7838 and routes its WRITTEN state — socket, recordings, history, session store, ledgers, and cost store — beneath `IRRLICHT_HOME=$DEV_HOME`, so it never prunes or mutates production's session/cost data. The dev app reaches the dev daemon because `IRRLICHT_DAEMON_PORT` (via `open --env`) overrides the hardcoded default; `DaemonManager` also skips its global `pkill` when a custom port is set, so it can't take production down.
- **separate mode shares with production:** it reads the same `~/.claude` transcripts (so the dev UI shows the same live sessions) and appends to the same `~/Library/Application Support/Irrlicht/logs/events.log`. It does NOT share the on-disk session/ledger/cost stores — and it does NOT receive the statusline quota feed (that hook posts only to 7837), so the subscription panel shows its empty-state.
- **replace mode — single instance on production's footprint.** Runs the dev binaries on port 7837 with the production state dir (no `IRRLICHT_HOME`), so the statusline quota feed and the production session/cost/ledger stores all apply. Because the dev daemon runs with `--record`, recordings land in the production recordings dir (`~/.local/share/irrlicht/recordings/`). **⚠️ The dev daemon mutates production data.** Without `IRRLICHT_HOME` its startup sweeps (`PruneStale` / dead-proc / orphan-ledger / cost prune) run against the real `~/.local/share/irrlicht/` + `~/Library/Application Support/Irrlicht/` stores — this is exactly the isolation #448 added, deliberately removed here. Only use replace mode when the dev build's on-disk schema matches the installed production build; a dev branch mid-migration can prune or rewrite production sessions/ledgers/cost rows that the production binary then misreads. **Returning to production requires the step-9 teardown** (run `restore-prod.sh`) — quitting the app does NOT stop the adopted daemon, and a relaunched (or freshly *reinstalled*) production app will silently adopt the leftover dev daemon on 7837 if you skip it. Running the installer is not a substitute: it replaces the app bundle, not the running daemon.
- Daemon logs: `/tmp/irrlichd-dev.log` · Swift app logs: `/tmp/irrlicht-app-dev.log`
- **TCC stability**: run `tools/dev-sign-setup.sh` once to install the `"Irrlicht Dev"` self-signed code signing identity. The skill automatically signs with it when present, giving the app a stable designated requirement so Accessibility/Automation grants persist across rebuilds. Without it, every rebuild invalidates TCC and requires re-granting in System Settings.
