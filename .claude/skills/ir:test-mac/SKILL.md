---
name: ir:test-mac
description: >
  Build and run a dev irrlicht daemon + macOS Swift app for local testing,
  alongside the production Irrlicht.app (which keeps running). The dev instance
  uses an isolated state dir (IRRLICHT_HOME) and a separate port (7838), so it
  never disturbs production on port 7837. Use when the user says "test mac",
  "restart mac", "rebuild mac", or "/ir:test-mac".
---

# Build & Run macOS Dev Stack (alongside production)

Build the irrlicht daemon and Swift app and run them as a **dev instance that
coexists with production**. Production stays up the whole time: the dev daemon
binds port **7838** (vs production's 7837) and stores its state under a
worktree-local `IRRLICHT_HOME`, and the dev app is told to connect to 7838 via
`IRRLICHT_DAEMON_PORT`. Only prior *dev* instances are replaced on rerun.

## Steps

0. **Detect repo root and set the dev instance config** — use the worktree root if running in a worktree, otherwise the main repo
   ```bash
   REPO_ROOT="$(git rev-parse --show-toplevel)"
   DEV_PORT=7838
   DEV_HOME="$REPO_ROOT/.build/irrlicht-home"   # isolated state dir (IRRLICHT_HOME)
   mkdir -p "$DEV_HOME"
   ```
   All subsequent steps use `$REPO_ROOT`, `$DEV_PORT`, and `$DEV_HOME`.

1. **Build the Go daemon** — the daemon resolves the dashboard from `platforms/web/index.html` at runtime via a walk-up search from its own executable; no embed, no codegen.
   ```bash
   cd "$REPO_ROOT/core" && go build -o /Users/ingo/projects/irrlicht/core/bin/irrlichd ./cmd/irrlichd
   ```
   Note: the binary is always placed in the main repo's `bin/` so the launch step has a stable path.

2. **Build the Swift app and assemble .app bundle**
   ```bash
   cd "$REPO_ROOT/platforms/macos" && swift build 2>&1 | tail -5
   ```
   Then assemble a proper `.app` bundle so that `UNUserNotificationCenter` (desktop notifications) works:
   ```bash
   DEV_APP="/tmp/IrrlichtDev.app"
   rm -rf "$DEV_APP"
   mkdir -p "$DEV_APP/Contents/MacOS" "$DEV_APP/Contents/Resources"
   cp "$REPO_ROOT/platforms/macos/.build/arm64-apple-macosx/debug/Irrlicht" "$DEV_APP/Contents/MacOS/Irrlicht"
   cp "$REPO_ROOT/platforms/macos/Irrlicht/Resources/AppIcon.icns" "$DEV_APP/Contents/Resources/AppIcon.icns"
   # Copy the SwiftPM resource bundle so Bundle.module works at runtime
   cp -R "$REPO_ROOT/platforms/macos/.build/arm64-apple-macosx/debug/Irrlicht_Irrlicht.bundle" "$DEV_APP/Contents/Resources/Irrlicht_Irrlicht.bundle" 2>/dev/null || true
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

3. **Kill only the prior DEV instances** — leave production (Irrlicht.app + its daemon on 7837) untouched. Target the dev app and whatever holds the dev port.
   ```bash
   pkill -f "IrrlichtDev" 2>/dev/null
   lsof -ti tcp:$DEV_PORT 2>/dev/null | xargs kill 2>/dev/null
   sleep 2
   ```

4. **Clean up the stale DEV socket** (under the isolated state dir, never production's)
   ```bash
   rm -f "$DEV_HOME/irrlichd.sock"
   ```

5. **Start the dev daemon** — isolated state (`IRRLICHT_HOME`) on the dev port, with `--record` for lifecycle event capture
   ```bash
   cd "$REPO_ROOT/core" && \
     IRRLICHT_HOME="$DEV_HOME" IRRLICHT_BIND_ADDR=127.0.0.1:$DEV_PORT \
     nohup ./bin/irrlichd --record > /tmp/irrlichd-dev.log 2>&1 & disown
   ```

6. **Wait for daemon to be ready** — confirm the dev port is listening before launching the app
   ```bash
   sleep 2 && lsof -iTCP:$DEV_PORT -sTCP:LISTEN -P -n 2>/dev/null
   ```

7. **Start the dev Swift app** — launched via LaunchServices so `Bundle.main` resolves correctly; `--env` tells it to connect to the dev daemon on `$DEV_PORT` instead of production's 7837
   ```bash
   open --env IRRLICHT_DAEMON_PORT=$DEV_PORT --stdout /tmp/irrlicht-app-dev.log --stderr /tmp/irrlicht-app-dev.log /tmp/IrrlichtDev.app
   ```

8. **Verify** — confirm the dev processes are running and the dev daemon is serving sessions (production on 7837 is unaffected)
   ```bash
   pgrep -f "bin/irrlichd" && pgrep -f "IrrlichtDev" && curl -s http://127.0.0.1:$DEV_PORT/api/v1/sessions | head -1
   ```

## Notes
- **Production keeps running.** The production Irrlicht.app (from DMG) and its bundled daemon stay on port 7837 with state under `~/.local/share/irrlicht/`. The dev instance is fully isolated: port `$DEV_PORT` (7838) + `IRRLICHT_HOME=$DEV_HOME`. The dev app reaches the dev daemon because `IRRLICHT_DAEMON_PORT` (via `open --env`) overrides the hardcoded default; `DaemonManager` also skips its global `pkill` when a custom port is set, so it can't take production down.
- Both daemons watch the same `~/.claude` transcripts, so the dev UI shows the same live sessions as production — that's intended for "see the UI render" testing.
- Daemon logs: `/tmp/irrlichd-dev.log`
- Swift app logs: `/tmp/irrlicht-app-dev.log`
- **TCC stability**: run `tools/dev-sign-setup.sh` once to install the `"Irrlicht Dev"` self-signed code signing identity. The skill automatically signs with it when present, giving the app a stable designated requirement so Accessibility/Automation grants persist across rebuilds. Without it, every rebuild invalidates TCC and requires re-granting in System Settings.
