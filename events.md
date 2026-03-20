# Irrlicht Session Detection — State Transitions

Irrlicht uses transcript file-watching (FSEvents) and process monitoring (kqueue) to
detect Claude Code sessions without requiring hooks.

## Primary Session States

- **`working`** - Claude actively processing/executing
- **`waiting`** - Waiting for user input or permission (idle + no open tool calls)
- **`ready`** - Session ended (process exited) or no active session
- **`cancelled_by_user`** - Session cancelled by ESC; auto-expires after 30s

## Detection Mechanisms

| Mechanism | Technology | Detects |
|-----------|-----------|---------|
| **TranscriptWatcher** | fsnotify (FSEvents on macOS) | New sessions, activity, removals |
| **GracePeriodTimer** | Per-session 2s idle timer | working → waiting transition |
| **ProcessWatcher** | kqueue EVFILT_PROC NOTE_EXIT | Process exit → ready (~1ms) |
| **TailerPipeline** | JSONL transcript parsing | Model, tokens, open tool calls |

## Transcript Events

| Event | Trigger | Detection | State Transition |
|-------|---------|-----------|------------------|
| **New .jsonl file** | Session starts | FSEvents CREATE | → `working` |
| **File write** | Claude processes | FSEvents WRITE | Reset grace timer, stay `working` |
| **2s idle + no open tools** | Claude waiting | Grace timer fires | `working` → `waiting` |
| **File write after idle** | Session resumes | FSEvents WRITE | `waiting` → `working` |
| **Process exits** | Session ends | kqueue NOTE_EXIT | Any → `ready` |
| **File removed** | Transcript deleted | FSEvents REMOVE | → `ready` or delete |

## State Machine

```
working:
  transcript write (FSEvents)           → stay working, cancel grace timer
  file idle + hasOpenToolCall=true      → stay working (tool executing)
  file idle + hasOpenToolCall=false     → start 2s grace timer
  grace timer fires                     → waiting

waiting:
  transcript write (FSEvents)           → working
  process exits (EVFILT_PROC)          → ready

ready:
  new .jsonl file (FSEvents)           → working (new session)
  existing file grows (FSEvents)       → working (resumed)
```

`hasOpenToolCall` = count of `tool_use` events without a matching `tool_result` in the
transcript tail.

## Subagent Detection

Parent-child relationships are derived from:
1. **File path**: `~/.claude/projects/<hash>/<parent-id>/subagents/agent-<id>.jsonl`
2. **Heuristic**: New session appears in same project dir as a working session with open tool calls

## Session Discovery Paths

| Assistant | Transcript location |
|-----------|-------------------|
| Claude Code | `~/.claude/projects/**/*.jsonl` |
| Others | Extensible registry (future) |

## Process Exit Detection

One-time `lsof -F p <transcript_path>` at session creation → PID → `EVFILT_PROC NOTE_EXIT`
watcher. Fallback: session with no PID ages out via orphan cleanup (1hr TTL).

## State Persistence

State files stored in `~/Library/Application Support/Irrlicht/instances/<sessionID>.json`
with atomic writes. Real-time updates fan out via WebSocket to the SwiftUI menu bar app.

## Integration Notes

- **File Locations**: State files in `~/Library/Application Support/Irrlicht/instances/`
- **Kill Switch**: `IRRLICHT_DISABLED=1` disables the daemon
- **Zero Configuration**: No hooks, no settings.json entries — install the app and it works
