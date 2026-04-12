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

2. **Build the Swift app**
   ```bash
   cd /Users/ingo/projects/irrlicht/platforms/macos && swift build 2>&1 | tail -5
   ```

3. **Kill all running irrlicht processes** (production app, production daemon, debug app, debug daemon)
   ```bash
   pkill -f "Irrlicht.app" 2>/dev/null
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

7. **Start the dev Swift app**
   ```bash
   nohup /Users/ingo/projects/irrlicht/platforms/macos/.build/arm64-apple-macosx/debug/Irrlicht > /tmp/irrlicht-app-dev.log 2>&1 & disown
   ```

8. **Verify** — confirm both processes are running and the daemon is serving sessions
   ```bash
   pgrep -f "bin/irrlichd" && pgrep -f "\.build.*Irrlicht" && curl -s http://localhost:7837/api/v1/sessions | head -1
   ```

## Notes
- The production Irrlicht.app (from DMG) bundles its own daemon. It MUST be killed before starting the dev daemon, otherwise port 7837 will be occupied.
- Daemon logs: `/tmp/irrlichd-dev.log`
- Swift app logs: `/tmp/irrlicht-app-dev.log`
