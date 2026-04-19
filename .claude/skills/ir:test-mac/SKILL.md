---
name: ir:test-mac
description: >
  Build and restart the irrlicht daemon and macOS Swift app for local testing.
  Compiles the Go daemon, builds the Swift app, kills all running instances
  (including production Irrlicht.app), and launches the freshly built versions.
  Use when the user says "test mac", "restart mac", "rebuild mac", or "/ir:test-mac".
---

# Build & Restart macOS Dev Stack

Build the irrlicht daemon and Swift app, then replace all running instances with the new builds.

## Steps

0. **Detect repo root** — use the worktree root if running in a worktree, otherwise the main repo
   ```bash
   REPO_ROOT="$(git rev-parse --show-toplevel)"
   ```
   All subsequent steps use `$REPO_ROOT` instead of hardcoded paths.

1. **Build the Go daemon** — ensure the embedded `ui/` dir exists (generated file, not committed)
   ```bash
   mkdir -p "$REPO_ROOT/core/cmd/irrlichd/ui" && cp /Users/ingo/projects/irrlicht/core/cmd/irrlichd/ui/index.html "$REPO_ROOT/core/cmd/irrlichd/ui/index.html" 2>/dev/null; cd "$REPO_ROOT/core" && go build -o /Users/ingo/projects/irrlicht/core/bin/irrlichd ./cmd/irrlichd
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
   </dict>
   </plist>
   PLIST
   # Sign with the persistent "Irrlicht Dev" identity if it exists; otherwise
   # fall back to ad-hoc (TCC permissions will need to be re-granted each
   # rebuild). Run scripts/dev-sign-setup.sh once to install the identity.
   if security find-identity -v -p codesigning 2>/dev/null | grep -q "Irrlicht Dev"; then
       codesign --force --deep --sign "Irrlicht Dev" "$DEV_APP" 2>&1
   else
       codesign --force --deep --sign - "$DEV_APP" 2>&1
   fi
   ```

3. **Kill all running irrlicht processes** (production app, production daemon, debug app, debug daemon)
   ```bash
   pkill -f "Irrlicht.app" 2>/dev/null
   pkill -f "IrrlichtDev" 2>/dev/null
   pkill -f "\.build.*Irrlicht" 2>/dev/null
   pkill -f irrlichd 2>/dev/null
   sleep 2
   ```

4. **Clean up stale socket**
   ```bash
   rm -f /Users/ingo/.local/share/irrlicht/irrlichd.sock
   ```

5. **Start the dev daemon** (with `--record` for lifecycle event capture)
   ```bash
   cd /Users/ingo/projects/irrlicht/core && nohup ./bin/irrlichd --record > /tmp/irrlichd-dev.log 2>&1 & disown
   ```

6. **Wait for daemon to be ready** — confirm port 7837 is listening before launching the app
   ```bash
   sleep 2 && lsof -iTCP:7837 -sTCP:LISTEN -P -n 2>/dev/null
   ```

7. **Start the dev Swift app** (from the .app bundle via LaunchServices so `Bundle.main` resolves correctly)
   ```bash
   open --stdout /tmp/irrlicht-app-dev.log --stderr /tmp/irrlicht-app-dev.log /tmp/IrrlichtDev.app
   ```

8. **Verify** — confirm both processes are running and the daemon is serving sessions
   ```bash
   pgrep -f "bin/irrlichd" && pgrep -f "IrrlichtDev" && curl -s http://localhost:7837/api/v1/sessions | head -1
   ```

## Notes
- The production Irrlicht.app (from DMG) bundles its own daemon. It MUST be killed before starting the dev daemon, otherwise port 7837 will be occupied.
- Daemon logs: `/tmp/irrlichd-dev.log`
- Swift app logs: `/tmp/irrlicht-app-dev.log`
- **TCC stability**: run `scripts/dev-sign-setup.sh` once to install the `"Irrlicht Dev"` self-signed code signing identity. The skill automatically signs with it when present, giving the app a stable designated requirement so Accessibility/Automation grants persist across rebuilds. Without it, every rebuild invalidates TCC and requires re-granting in System Settings.
