# Irrlicht Architecture

## System Overview
**Irrlicht** is a macOS menu bar application that provides real-time visual feedback for Claude Code sessions through a distributed monitoring system.

## Architecture Diagram

```
┌──────────────────────────────────────────────────────────────────────────────┐
│                                Claude Code                                   │
│                           (Hook Event Generator)                             │
└─────────────────────┬────────────────────────────────────────────────────────┘
                      │
                      │ Hook Events (JSON/stdin)
                      ▼
┌──────────────────────────────────────────────────────────────────────────────┐
│                         Hook Receiver (Go)                                   │
│                     tools/irrlicht-hook/main.go                              │
│                                                                              │
│  ┌─────────────────┐  ┌──────────────────┐  ┌─────────────────────────────┐  │
│  │ Event Validator │  │ State Transition │  │   Transcript Analytics      │  │
│  │   & Sanitizer   │  │     Engine       │  │  (Token Usage & Metrics)    │  │
│  └─────────────────┘  └──────────────────┘  └─────────────────────────────┘  │
│           │                      │                           │               │
│           └──────────────────────┼───────────────────────────┘               │
│                                  ▼                                           │
│                        ┌───────────────────┐                                 │
│                        │ Session State     │                                 │
│                        │   Management      │                                 │
│                        │ (Atomic Updates)  │                                 │
│                        └───────────────────┘                                 │
└─────────────────────────────────┬────────────────────────────────────────────┘
                                  │
                                  │ JSON State Files
                                  ▼
┌──────────────────────────────────────────────────────────────────────────────┐
│                        File System Storage                                   │
│            ~/Library/Application Support/Irrlicht/instances/                 │
│                                                                              │
│  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐  ┌─────────────────────┐  │
│  │ session1.   │  │ session2.   │  │ session3.   │  │ Structured Logs     │  │
│  │    json     │  │    json     │  │    json     │  │  events.log.*       │  │
│  └─────────────┘  └─────────────┘  └─────────────┘  └─────────────────────┘  │
└─────────────────────────────────┬────────────────────────────────────────────┘
                                  │
                                  │ File System Monitoring
                                  ▼
┌──────────────────────────────────────────────────────────────────────────────┐
│                      SwiftUI Menu Bar App                                    │
│                    Irrlicht.app/Irrlicht/                                    │
│                                                                              │
│  ┌─────────────────┐  ┌──────────────────┐  ┌─────────────────────────────┐  │
│  │ Session Manager │  │   Status Label   │  │     Session List View       │  │
│  │ (File Watcher)  │  │ (Emoji Display)  │  │   (Dropdown Interface)      │  │
│  └─────────────────┘  └──────────────────┘  └─────────────────────────────┘  │
│           │                      ▲                           ▲               │
│           │  Combine Publishers  │  Real-time UI Updates     │               │
│           └──────────────────────┼───────────────────────────┘               │
│                                  │                                           │
└──────────────────────────────────┼───────────────────────────────────────────┘
                                   │
                                   ▼
┌──────────────────────────────────────────────────────────────────────────────┐
│                             macOS Menu Bar                                   │
│                         (Visual Status Display)                              │
│                                                                              │
│        🟡 🔴 🟢  ←  Dynamic Status Indicators (working/waiting/ready)         │
│   (working/waiting/ready)                                                    │
└──────────────────────────────────────────────────────────────────────────────┘
```

## Supporting Components

```
┌───────────────────────────────────────────────────────────────────────────┐
│                           Supporting Tools                                │
│                                                                           │
│  ┌──────────────────┐  ┌─────────────────┐  ┌──────────────────────────┐  │
│  │ Settings Merger  │  │  Test Runner    │  │   Transcript Tailer      │  │
│  │    (Go CLI)      │  │    (Bash)       │  │    (Go Library)          │  │
│  │                  │  │                 │  │                          │  │
│  │ • Hook Config    │  │ • Unit Tests    │  │ • Real-time Analysis     │  │
│  │ • Backup/Restore │  │ • Integration   │  │ • Performance Metrics    │  │
│  │ • Safe Merging   │  │ • E2E Scenarios │  │ • Session Monitoring     │  │
│  └──────────────────┘  └─────────────────┘  └──────────────────────────┘  │
│                                                                           │
│  ┌──────────────────┐  ┌─────────────────┐  ┌──────────────────────────┐  │
│  │ Build System     │  │ Settings Merger │  │     Stress Testing       │  │
│  │   (Bash)         │  │    (Go CLI)     │  │      (Python)            │  │
│  │                  │  │                 │  │                          │  │
│  │ • Cross-platform │  │ • Hook Config   │  │ • Concurrency Tests      │  │
│  │ • Multi-arch     │  │ • Backup/Restore│  │ • Load Simulation        │  │
│  │ • Automated      │  │ • Safe Merging  │  │ • Edge Case Validation   │  │
│  └──────────────────┘  └─────────────────┘  └──────────────────────────┘  │
└───────────────────────────────────────────────────────────────────────────┘
```

## High-Level Flow
```
Claude Code Hook Events → Go Hook Receiver → JSON State Files → SwiftUI Menu Bar App
```

## Core Components

### 1. **Hook Receiver** (`tools/irrlicht-hook/`)
- **Language**: Go
- **Purpose**: Receives and processes Claude Code hook events
- **Key Functions**:
  - Event validation and sanitization
  - Session state management
  - Transcript analysis and metrics computation
  - Atomic file operations for state persistence
- **Entry Point**: `tools/irrlicht-hook/main.go:231`

### 2. **Menu Bar Application** (`Irrlicht.app/`)
- **Language**: SwiftUI
- **Purpose**: Visual interface in macOS menu bar
- **Key Components**:
  - Session state monitoring via file system watching
  - Dynamic status indicators (emoji-based)
  - Real-time UI updates using Combine
- **Entry Point**: `Irrlicht.app/Irrlicht/IrrlichtApp.swift:62`

### 3. **Settings Manager** (`tools/settings-merger/`)
- **Language**: Go
- **Purpose**: Manages Claude Code hook configuration
- **Key Functions**:
  - Safely merges/removes hook configurations
  - Backup and restore capabilities
  - Settings validation
- **Entry Point**: `tools/settings-merger/main.go:11`

### 4. **Supporting Tools**
- **Transcript Tailer** (`tools/transcript-tailer/`): Real-time transcript analysis for performance metrics
- **Build System** (`tools/build-release.sh`): Cross-platform build automation
- **Test Infrastructure** (`tools/test-runner.sh`): Comprehensive testing with concurrency scenarios

## Data Flow

### State Management
1. **Event Processing**: Hook events → State transitions → JSON files
2. **File Storage**: `~/Library/Application Support/Irrlicht/instances/*.json`
3. **State Synchronization**: File system monitoring → UI updates

### Session States
- **working** (🟡): Active Claude Code session
- **waiting** (🔴): Notification requiring user attention
- **ready** (🟢): Session completed or idle

### Compaction States
- **not_compacting**: Normal operation
- **compacting**: Context window compression in progress
- **post_compact**: Recently completed compaction

## Key Design Patterns

### Reliability
- Atomic file operations for state consistency
- Event validation and sanitization
- Graceful error handling and logging
- Kill switch mechanisms (environment variable + settings)

### Performance
- Structured JSON logging with rotation
- Efficient file system monitoring
- Background processing for metrics computation
- Memory-conscious transcript analysis

### Extensibility
- Plugin-style architecture for transcript analysis
- Configurable hook management
- Cross-platform build support (macOS/Linux/Windows)

## Event Processing Flow

```
Hook Event → Validation → State Transition → Metrics Computation → File Write → UI Update
     │            │             │                    │                │           │
     │            │             │                    │                │           └─ SwiftUI
     │            │             │                    │                └─ Atomic JSON
     │            │             │                    └─ Transcript Analysis
     │            │             └─ Smart State Machine
     │            └─ Security & Sanitization
     └─ Claude Code Hook System
```

## Setup and Installation Flow

### Installation Process

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                              Installation Steps                             │
└─────────────────────────────────────────────────────────────────────────────┘

1. Build Phase
┌─────────────────────────────────────────────────────────────────────────────┐
│ ./tools/build-release.sh                                                    │
│   ├─ Go Cross-compilation (darwin-arm64, darwin-amd64, darwin-universal)    │
│   ├─ SwiftUI App Build                                                      │
│   └─ Binary Output: build/irrlicht-hook-*                                   │
└─────────────────────────────────────────────────────────────────────────────┘
                                    │
                                    ▼
2. Hook Installation
┌─────────────────────────────────────────────────────────────────────────────┐
│ sudo cp build/irrlicht-hook-darwin-universal /usr/local/bin/irrlicht-hook   │
│ sudo chmod +x /usr/local/bin/irrlicht-hook                                  │
│   └─ System PATH Integration                                                │
└─────────────────────────────────────────────────────────────────────────────┘
                                    │
                                    ▼
3. Claude Code Configuration
┌─────────────────────────────────────────────────────────────────────────────┐
│ ./tools/settings-merger/settings-merger --action merge                      │
│   ├─ Backup: ~/.claude/settings.json.backup.TIMESTAMP                       │
│   ├─ Merge: Hook configuration into ~/.claude/settings.json                 │
│   └─ Validation: Ensure hooks are properly configured                       │
└─────────────────────────────────────────────────────────────────────────────┘
                                    │
                                    ▼
4. Application Launch
┌─────────────────────────────────────────────────────────────────────────────┐
│ cd Irrlicht.app && swift run &                                              │
│   ├─ Menu Bar Integration                                                   │
│   ├─ File System Monitoring Setup                                           │
│   └─ Real-time UI Initialization                                            │
└─────────────────────────────────────────────────────────────────────────────┘
```

### Uninstallation Process

```bash
# 1. Remove hook configuration
./tools/settings-merger/settings-merger --action remove

# 2. Remove binary
sudo rm /usr/local/bin/irrlicht-hook

# 3. Clean application data (optional)
rm -rf ~/Library/Application\ Support/Irrlicht/

# 4. Kill application
killall swift  # or quit via menu bar
```
