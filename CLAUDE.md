# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Rules
- Always checkout a dedicated branch before working on an issue

## Overview

Irrlicht is a macOS menu bar application that monitors Claude Code sessions, providing visual feedback on session states through a system of colored indicators. It consists of a Go-based hook receiver, a SwiftUI menu bar application, and several supporting tools.

## Architecture

The system follows this flow:
```
Claude Code Hook Events → Go Hook Receiver → JSON State Files → SwiftUI Menu Bar App
```

**Key Components:**
- **irrlicht-hook** (Go): Receives Claude Code hook events and maintains session state files
- **Irrlicht.app** (SwiftUI): Menu bar application that displays session states
- **Settings merger** (Go): Manages Claude Code hook configuration
- **Supporting tools**: Build scripts, test runners, and replay utilities

**State Management:**
- Session states stored as JSON files in `~/Library/Application Support/Irrlicht/instances/`
- Three states: `working`, `waiting`, `ready`
- Real-time updates via file system monitoring and periodic refresh

## Development Commands

### Building
```bash
# Build all components (cross-platform)
./tools/build-release.sh

# Build just the hook receiver
cd core && go build -o irrlicht-hook ./cmd/irrlicht-hook/

# Build SwiftUI app
cd Irrlicht.app && swift build

# Run SwiftUI app
cd Irrlicht.app && swift run
```

### Testing
```bash
# Run complete test suite
./tools/test-runner.sh

# Test hook receiver with sample events
./tools/irrlicht-replay fixtures/session-start.json

# Run Go component tests
cd tools/settings-merger && go test -v
cd core && go test -v ./...

# Test SwiftUI components
cd Irrlicht.app && swift test
```

### Installation & Configuration
```bash
# Install hook receiver to PATH
sudo cp build/irrlicht-hook-darwin-universal /usr/local/bin/irrlicht-hook
sudo chmod +x /usr/local/bin/irrlicht-hook

# Configure Claude Code hooks
./tools/settings-merger/settings-merger --action merge

# Disable hooks (kill switch)
export IRRLICHT_DISABLED=1
# or
./tools/settings-merger/settings-merger --action merge-disable
```

### Development Workflow
```bash
# Run SwiftUI app for manual testing
cd Irrlicht.app && swift run &

# Replay sample events to test the hook receiver
./tools/irrlicht-replay fixtures/session-start.json

# Clean up test data
rm -rf ~/Library/Application\ Support/Irrlicht/instances
killall swift
```

## Code Structure

**Go Components:**
- `core/`: Hook receiver using Hexagonal Architecture (Ports & Adapters); single Go module
  - `domain/session/` — SessionState, SessionMetrics, pure state machine
  - `domain/event/` — HookEvent struct and validation
  - `ports/inbound/` — EventHandler interface
  - `ports/outbound/` — SessionRepository, Logger, GitResolver, MetricsCollector interfaces
  - `adapters/inbound/stdin/` — reads events from stdin
  - `adapters/outbound/filesystem/` — JSON session file repository
  - `adapters/outbound/logging/` — rotating structured JSON logger
  - `adapters/outbound/git/` — git branch extraction
  - `adapters/outbound/metrics/` — transcript-tailer wrapper
  - `adapters/outbound/security/` — path validation
  - `application/services/` — EventService orchestration (processEvent, cleanup, speculative wait)
  - `cmd/irrlicht-hook/` — entry point, dependency injection wiring only
  - `pkg/tailer/` — real-time transcript analysis for performance metrics
  - `pkg/capacity/` — token capacity and context utilization tracking
- `tools/settings-merger/`: Manages `~/.claude/settings.json` hook configuration
- `platform/macos/`: macOS installer package builder

**SwiftUI App (`Irrlicht.app/`):**
- `Irrlicht/IrrlichtApp.swift`: Main app entry point
- `Irrlicht/Models/SessionState.swift`: Session state and metrics data models
- `Irrlicht/Managers/SessionManager.swift`: File system monitoring and state management
- `Irrlicht/Views/SessionListView.swift`: Menu bar UI components

**Key Patterns:**
- All Go modules use structured logging with JSON output
- State files use atomic writes for consistency
- SwiftUI app uses Combine for reactive state updates
- File system changes trigger immediate UI updates
- All tools support `--help` flag for usage information

## Hook Event Flow

see events.md

## Testing Approach

- Unit tests for Go components using standard `go test`
- Integration tests with sample hook events via `irrlicht-replay`
- SwiftUI tests using `swift test`
- Concurrency testing with multiple simultaneous sessions
- Build verification across macOS architectures (Intel/Apple Silicon)

## UI Verification

AI coding agents can verify Irrlicht's UI state programmatically after making code changes.
Use these methods without human guidance — they require no interactive steps.

### Method 1: Debug state file (recommended)

Set `IRRLICHT_DEBUG=1` when launching the app. On every session update, `SessionManager`
writes current state to `~/.irrlicht/debug-state.json`:

```bash
IRRLICHT_DEBUG=1 swift run --package-path Irrlicht.app &
# ... make changes, trigger hook events ...
cat ~/.irrlicht/debug-state.json
```

Example output:
```json
{
  "lastUpdated": "2026-03-15T22:17:52Z",
  "readyCount": 1,
  "sessionCount": 2,
  "sessions": [
    {
      "contextUtilization": 42.5,
      "id": "sess_abc123def456",
      "model": "claude-sonnet-4-6",
      "projectName": "mayor",
      "state": "working",
      "totalTokens": 85000
    }
  ],
  "waitingCount": 0,
  "workingCount": 1
}
```

State dump path: `~/.irrlicht/debug-state.json`

### Method 2: HTTP state endpoint

When `irrlichtd` is running, query the `/state` endpoint for the same JSON format:

```bash
curl -s http://127.0.0.1:7837/state | jq .
# Check a specific field:
curl -s http://127.0.0.1:7837/state | jq '.workingCount'
# Verify a session exists:
curl -s http://127.0.0.1:7837/state | jq '.sessions[] | select(.state == "working")'
```

### Method 3: Screenshot + vision

Full workflow for visual verification of the menu bar popup:

```bash
# Step 1: Open the menu bar popup (requires Irrlicht app to be running)
osascript -e 'tell application "System Events" to click menu bar item 1 of menu bar 2'

# Step 2: Capture screenshot (no shutter sound, saves to file)
screencapture -x /tmp/irrlicht-check.png

# Step 3: Read the image file with your vision model to verify the UI
# (Use the Read tool on /tmp/irrlicht-check.png for visual inspection)
```

To close the popup after verification:
```bash
osascript -e 'key code 53'  # Escape key
```

### Method 4: Accessibility tree inspection

Session rows and key UI elements have accessibility identifiers for AXorcist/Peekaboo inspection:
- Parent sessions: `session-card-<session-id>`
- Subagent sessions: `subagent-card-<session-id>`
- State icon: `session-state-icon-<session-id>`
- Model label: `session-model-label-<session-id>`
- Context/metrics bar: `session-context-bar-<session-id>`

```bash
# With AXorcist (if installed):
axorcist query --identifier "session-card-*"
axorcist query --identifier "session-state-icon-*"
axorcist query --identifier "session-context-bar-*"

# With Peekaboo (if installed):
peekaboo find --accessibility-id "session-card-*"
```
