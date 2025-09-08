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
cd tools/irrlicht-hook && go build -o irrlicht-hook .

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
cd tools/irrlicht-hook && go test -v

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
# Quick demo setup
cd Irrlicht.app && swift run &
bash demo-phase2.sh

# Clean up test data
rm -rf ~/Library/Application\ Support/Irrlicht/instances
killall swift
```

## Code Structure

**Go Components (`tools/`):**
- `irrlicht-hook/`: Main hook receiver that processes Claude Code events
- `settings-merger/`: Manages `~/.claude/settings.json` hook configuration
- `model-capacity/`: Token capacity and context utilization tracking
- `transcript-tailer/`: Real-time transcript analysis for performance metrics

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