<div align="center">

# ✦ Irrlicht — Menu-Bar Telemetry for AI Coding Agents (macOS)

![Banner](assets/banner.png)

[![Coverage](https://img.shields.io/endpoint?url=https%3A%2F%2Fgist.githubusercontent.com%2Fingo-eichhorst%2F9f14c8e5f25c1ccf5d6500c1685fd9fb%2Fraw%2Fcoverage.json&color=%238B5CF6)](https://github.com/ingo-eichhorst/Irrlicht/actions/workflows/coverage.yml)
[![License](https://img.shields.io/badge/license-MIT-orange?color=%23FF9500)](LICENSE)
[![Version](https://img.shields.io/badge/dynamic/json?url=https%3A%2F%2Fraw.githubusercontent.com%2Fingo-eichhorst%2FIrrlicht%2Fmain%2Fversion.json&query=%24.version&label=version&color=%2334C759)](version.json)

[![ARS](https://img.shields.io/badge/ARS-Agent--Assisted%207.3%2F10-yellow)](https://github.com/ingo-eichhorst/agent-readyness)

[🌐 Landing Page](https://ingo-eichhorst.github.io/Irrlicht/) · [📖 Documentation](https://ingo-eichhorst.github.io/Irrlicht/docs/quickstart.html) · [📦 Latest Release](https://github.com/ingo-eichhorst/Irrlicht/releases/latest)

</div>

---

**Irrlicht** is a macOS menu bar application that monitors AI coding agent sessions (Claude Code, OpenAI Codex, Pi), providing instant visual feedback on session states. The name comes from German folklore—*Irrlicht* is the German word for will-o'-the-wisp, the ghostly lights that drift over the moors and marshes of the Harz mountains. In Goethe's *Faust*, an Irrlicht guides Faust and Mephistopheles up the Brocken on Walpurgis Night, a treacherous companion on a treacherous path. Harzer miners told of phantom lights in the tunnels near Clausthal-Zellerfeld—sometimes a warning, sometimes a promise of ore ahead.

This Irrlicht flips the old myth: instead of luring you astray, it guides you with honest signals about where your attention is needed.

## Philosophy

> *"In die Traum- und Zaubersphäre / sind wir, scheint es, eingegangen."*
> — Goethe, *Faust I* (Walpurgisnacht), the Irrlicht speaking as it leads Faust up the Brocken
>
> By night, in the mist-hollows of the Harz, an *Irrlicht* lures wanderers off the path and into the bog.
> By day, in our terminals, the real danger is different: ten tasks, four Claude sessions, and no sense of where attention should go.
>
> **Irrlicht** flips the myth: it's the *tamed* will-o'-the-wisp—small, honest lights that appear exactly where you need them.

Irrlicht watches agent transcript files (Claude Code, Codex, Pi), turns activity into a deterministic state machine, and renders them as quiet, legible beacons. Local-first, atomic writes, zero configuration.

```
Transcript Files → FSEvents/kqueue → SessionDetector → State Machine → Menu Bar
```

### The Light System

Each session appears as a simple icon that tells the truth:
- **🟣** **working** — the agent is thinking, building, streaming (purple)
- **🟠** **waiting** — it needs you; the story pauses for your judgment (orange)
- **🟢** **ready** — the path ahead is clear, ready for new work (green)
- **✦** **no sessions** — clean slate, ready for new work (white sparkle)

No ghosts. **Files → State → Light.**

## Features

![UI Features](assets/irrlicht-explainer.png)

### Menu Bar Indicators
- **Individual colored status indicators** for each active agent session
- **Scales with Demand**: Shows first 5 sessions + "…" when 7+ sessions exist
- **Real-time updates**: Status changes reflected within 1 second

### Session Information & Features
- **Complete session context**: Track project name, git branch, working directory, Claude model, and current state for each active session
- **Real-time performance metrics**: Monitor elapsed time, token usage (1.2K, 15.0K, 1.5M), and context utilization with live updates
- **Context pressure indicators**: Visual warnings (🟢 safe, 🟡 caution, 🔴 warning, ⚠️ critical) alert you before auto-compaction at 155K tokens
- **Session management tools**: Reset stuck sessions, delete completed ones, or drag-and-drop to reorder by priority
- **Smart display handling**: Clean empty state when idle, automatic overflow management for 7+ sessions

## Quick Start

### Prerequisites

- **macOS**: Primary target platform
- **Go 1.21+**: For building the daemon
- **Swift 5.9+**: For SwiftUI menu bar application
- **Claude Code, Codex, and/or Pi**: At least one supported AI coding agent

### Installation

**Option A — DMG (recommended):**

1. Download `Irrlicht-<version>.dmg` from [Releases](https://github.com/ingo-eichhorst/Irrlicht/releases)
2. Open the DMG and drag **Irrlicht.app** to **Applications**
3. Launch Irrlicht from Applications

The app embeds the monitoring daemon — everything runs from a single application. No separate services to manage.

**Option B — Build from source:**

```bash
git clone https://github.com/ingo-eichhorst/Irrlicht.git
cd Irrlicht
./platforms/build-release.sh
open .build/Irrlicht-*.dmg
```

**That's it.** No hooks to configure, no settings to merge — Irrlicht watches transcript files automatically.

### State Files

Session states are stored as atomic JSON files:
- **Location**: `~/Library/Application Support/Irrlicht/instances/`
- **Format**: `<session_id>.json`
- **Content**: Current state, timestamp, metadata, performance metrics

Example state file:
```json
{
  "session_id": "sess_abc123",
  "state": "working",
  "timestamp": "2024-09-05T14:30:00.000Z",
  "last_event": "transcript_activity",
  "model": "claude-sonnet-4-6",
  "cwd": "/Users/ingo/projects/my-project",
  "pid": 12345,
  "metrics": {
    "elapsed_seconds": 180,
    "total_tokens": 15000,
    "context_utilization_percentage": 7.5,
    "pressure_level": "safe"
  }
}
```

## Development

### Project Structure

```
├── core/                      # Go daemon (single module, hexagonal architecture)
│   ├── cmd/irrlichd/          # Daemon entry point
│   ├── domain/                # SessionState, TranscriptEvent, GasTown state types
│   ├── ports/
│   │   ├── inbound/           # AgentWatcher, GasTownCollector interfaces
│   │   └── outbound/          # SessionRepository, Logger, ProcessWatcher, etc.
│   ├── adapters/
│   │   ├── inbound/           # Drives events into the app
│   │   │   ├── claudecode/    # Watches ~/.claude/projects for Claude Code sessions
│   │   │   ├── codex/         # Watches ~/.codex for OpenAI Codex sessions
│   │   │   ├── pi/            # Watches ~/.pi/agent/sessions for Pi sessions
│   │   │   ├── gastown/       # Watches Gas Town daemon state + polls gt CLI
│   │   │   └── fswatcher/     # Shared fsnotify-based watcher implementation
│   │   └── outbound/          # App calls out to external systems
│   │       ├── filesystem/    # Session state persistence
│   │       ├── process/       # kqueue process exit monitoring
│   │       ├── graceperiod/   # Per-session idle timers
│   │       ├── git/           # Git metadata resolution
│   │       ├── metrics/       # Transcript metrics computation
│   │       ├── websocket/     # WebSocket fan-out hub
│   │       └── ...            # logging, mdns, memory, gtbin
│   ├── application/services/  # SessionDetector orchestration
│   └── pkg/                   # tailer, capacity utilities
├── platforms/
│   ├── macos/                 # SwiftUI menu bar application + DaemonManager
│   ├── web/                   # Web frontend (embedded into daemon)
│   └── build-release.sh       # Release build → .app bundle with embedded daemon + DMG
├── fixtures/                  # Sample transcript files and edge cases
```

### Building from Source

```bash
# Build all components
./platforms/build-release.sh

# Build daemon
cd core && go build ./cmd/irrlichd/

# Build SwiftUI app
cd platforms/macos && swift build
```

### Validation

The single entry point for verifying the full system contract:

```bash
./validate.sh
```

This runs in order: Go build → Swift build → Go tests → Swift tests → integration tests. Exit code 0 means all claims passed. **A change is not done until `./validate.sh` passes.**

Individual components:

```bash
# Run specific component tests
cd core && go test -v ./...
cd platforms/macos && swift test
```

### Session Detection

Irrlicht detects agent sessions via transcript file-watching (Claude Code, Codex, Pi):

| Detection | Technology | Transition |
|-----------|-----------|------------|
| New `.jsonl` file | FSEvents | → **working** |
| File write activity | FSEvents | Reset idle timer |
| 2s idle + no open tools | Grace timer | → **waiting** |
| Process exit | kqueue NOTE_EXIT | → **ready** |

See [events.md](events.md) for the full state machine.

## Technical Details

### Architecture

```
Irrlicht.app (single artifact)
  ├── Irrlicht (SwiftUI menu bar UI)
  │   └── DaemonManager   (spawns/monitors/restarts irrlichd)
  └── irrlichd (Go daemon, embedded in app bundle)
      ├── Inbound Adapters
      │   ├── Claude Code    (~/.claude/projects/**/*.jsonl via fsnotify)
      │   ├── Codex          (~/.codex/**/*.jsonl via fsnotify)
      │   ├── Pi             (~/.pi/agent/sessions/**/*.jsonl via fsnotify)
      │   └── Gas Town       (daemon/state.json watcher + gt CLI poller)
      ├── TailerPipeline     (JSONL parsing → model, tokens, tool call tracking)
      ├── GracePeriodTimer   (per-session 2s idle → waiting)
      └── ProcessWatcher     (kqueue EVFILT_PROC NOTE_EXIT → ready)
```

- **Single-artifact packaging**: The `.app` bundle contains both the SwiftUI UI and the Go daemon — no separate installs, no version drift
- **Daemon lifecycle**: The app automatically spawns `irrlichd` on launch. If an external daemon is already running (e.g. via LaunchAgent), it uses that instead. If the daemon crashes, the app restarts it with exponential backoff
- **State Machine**: Maintains deterministic session states in JSON files
- **Communication**: HTTP API + WebSocket on port 7837 for real-time UI updates
- **File System**: Atomic writes ensure consistency across concurrent sessions

### Performance Specifications

- **Latency**: ~50-200ms session detection via FSEvents; ~1ms process exit via kqueue
- **Memory**: <5MB typical footprint
- **Disk**: <100KB state files, <50MB logs (with rotation)
- **Concurrency**: Tested up to 8 simultaneous sessions
- **Context Accuracy**: Real-time tracking with model-specific context windows

### Logging System

Structured JSON logs with automatic rotation:
- **Location**: `~/Library/Application Support/Irrlicht/logs/`
- **Format**: `irrlicht.log` (current), `irrlicht.log.1` (rotated)
- **Max size**: 10MB per file, 5 files retained
- **Content**: All session events, state transitions, errors

### Safety Guarantees

✅ **Zero configuration**: No hooks, no settings — install and run
✅ **Idempotent**: Multiple runs produce identical results
✅ **Non-destructive**: Never corrupts existing configurations
✅ **Atomic**: Either fully succeeds or fails cleanly
✅ **Kill switch**: `IRRLICHT_DISABLED=1` disables the daemon

## Support

### Troubleshooting

**Irrlicht not showing in menu bar:**
- Verify the app is running: `ps aux | grep Irrlicht`
- Check state directory exists: `ls ~/Library/Application\ Support/Irrlicht/`
- Look for error logs in `~/Library/Application\ Support/Irrlicht/logs/`

**Sessions not updating:**
- The app manages the daemon automatically — check that both processes are running: `ps aux | grep -E 'Irrlicht|irrlichd'`
- Test daemon health: `curl http://127.0.0.1:7837/state`
- Check IRRLICHT_DISABLED environment variable
- If the daemon isn't starting, check Console.app for logs from `com.anthropic.irrlicht`

**Orphaned sessions (session stuck after agent exits):**
- Sessions include a `pid` field tracking the agent process
- kqueue monitors process exit; sessions are deleted immediately when their process exits
- Ready sessions without activity are automatically cleaned up after 30 minutes (configurable via Session TTL in app settings)
- Orphan transcript files from previous runs are skipped during startup
- To manually clear orphaned sessions: `rm ~/Library/Application\ Support/Irrlicht/instances/*.json`

### Contributing

1. Fork the repository
2. Create a feature branch (`git checkout -b feature/amazing-feature`)
3. Write tests for new functionality
4. Ensure all checks pass: `./validate.sh`
5. Commit your changes with descriptive messages
6. Submit a pull request

**For AI coding agents:** run `./validate.sh` after every change. A task is only complete when exit code is 0. Never mark a task done based only on compilation. If validation fails, inspect the failing test and fix the root cause — do not skip or comment out failing assertions.

## Coding Agent Support

Irrlicht is designed to be **agent-verifiable**: an AI coding agent can inspect app state and validate its own changes without human assistance.

### Passive observability — read current state

Session state files are the ground truth. An agent can read them directly:

```bash
# See all active sessions
ls ~/Library/Application\ Support/Irrlicht/instances/

# Check session count and states
cat ~/Library/Application\ Support/Irrlicht/instances/*.json | jq '{id: .session_id, state: .state}'
```

This works without any app changes — the state files are always present while sessions are active.

### Active validation — run executable claims

```bash
./validate.sh   # must exit 0 before any change is considered done
```

The validation harness is a semantic firewall around agent-authored changes:

```
agent generates change → ./validate.sh executes claims → only exit 0 counts as success
```

### Visual verification

To verify rendering without human review, open the menu bar popup and capture a screenshot:

```bash
# Open popup via AppleScript
osascript -e 'tell application "System Events" to click menu bar item 1 of menu bar 2 of process "Irrlicht"'
screencapture -x /tmp/irrlicht-check.png
# Pass the image to your vision model for visual assertion
```

Tools like [Peekaboo](https://github.com/steipete/Peekaboo) combine screenshot capture and vision analysis into a single CLI call.

## Star History

[![Star History Chart](https://api.star-history.com/svg?repos=ingo-eichhorst/Irrlicht&type=Date)](https://star-history.com/#ingo-eichhorst/Irrlicht&Date)

### License

MIT License - see [LICENSE](LICENSE) file for details.

### Community

- **Issues**: [GitHub Issues](https://github.com/ingo-eichhorst/Irrlicht/issues)
- **Discussions**: [GitHub Discussions](https://github.com/ingo-eichhorst/Irrlicht/discussions)

---

*"Führ uns gut und mach dir Ehre" — Follow the right light.*
