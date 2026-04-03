# Irrlicht Session Detection — State Machine

Irrlicht uses transcript file-watching (FSEvents) and process monitoring (kqueue) to
detect coding assistant sessions without requiring hooks.

## States (3, MECE)

| State | Definition |
|-------|-----------|
| **`working`** | Agent process alive, actively processing (tools, text generation, hooks, compaction, API retries) |
| **`waiting`** | Agent process alive, turn finished — user must provide input |
| **`ready`** | Session inactive (process exited, transcript removed, cancelled, or never started) |

MECE proof: (1) Is the process alive? No → `ready`. (2) Does the agent need user input? Yes → `waiting`, No → `working`.

See `core/domain/session/STATES.md` for the full reference including scenario mapping.

## Detection Mechanisms

| Mechanism | Technology | Detects |
|-----------|-----------|---------|
| **TranscriptWatcher** | fsnotify (FSEvents on macOS) | New sessions, activity, removals |
| **ProcessWatcher** | kqueue EVFILT_PROC NOTE_EXIT | Process exit → ready (~1ms) |
| **TailerPipeline** | JSONL transcript parsing | Model, tokens, open tool calls, LastEventType |

## State Transitions (8)

| ID | From | To | Trigger | Detection |
|----|------|----|---------|-----------|
| T1 | — | working | New .jsonl file | FSEvents CREATE |
| T2 | working | waiting | Agent finished turn | `IsWaitingForInput()=true` |
| T3 | waiting | working | User sent message | `IsWaitingForInput()=false` |
| T4 | working | ready | Process exited | kqueue NOTE_EXIT |
| T5 | waiting | ready | Process exited | kqueue NOTE_EXIT |
| T6 | working | ready | Transcript deleted | FSEvents REMOVE |
| T7 | waiting | ready | Transcript deleted | FSEvents REMOVE |
| T8 | ready | working | Transcript grows | FSEvents WRITE on ready session |

## State Diagram

```
                  ┌──────────┐
   T1             │          │◄──────────────────────────────┐
  (new file)──────►  working │                               │
                  │          ├───────T4,T6──────┐            │
                  └──┬───▲───┘  (exit/remove)   │            │
                     │   │                      │        T8  │
                T2   │   │  T3                  │  (file     │
           (end_turn,│   │ (user msg            │   grows)   │
            no open  │   │  or new              │            │
            tools)   │   │  activity)           │            │
                     │   │                      ▼            │
                  ┌──▼───┴───┐             ┌────────┐        │
                  │          ├──T5,T7──────►         ├────────┘
                  │ waiting  │ (exit/remove)│  ready  │
                  │          │             │         │
                  └──────────┘             └────────┘
```

## Core Detection Logic

```
IsWaitingForInput() = (LastEventType ∈ {assistant, assistant_message, assistant_output})
                      AND (HasOpenToolCall = false)
```

`HasOpenToolCall` = `count(tool_use) - count(tool_result) > 0` in the parsed transcript tail.

Only **message events** affect `LastEventType`: `user`, `assistant`, `tool_use`, `tool_call`, `tool_result`.
System events and management events are ignored.

## Subagent Detection

Parent-child relationships are derived from:
1. **Heuristic**: Working session with open tool call in same project dir
2. **Fallback**: Single working session in same project dir

Parent sessions carry a `SubagentSummary` with aggregate child state counts.

## Process Exit Detection

One-time `lsof -F p <transcript_path>` at session creation → PID → `EVFILT_PROC NOTE_EXIT`
watcher. Fallback: session with no PID ages out via orphan cleanup.

## Session Discovery Paths

| Assistant | Transcript location |
|-----------|-------------------|
| Claude Code | `~/.claude/projects/**/*.jsonl` |
| OpenAI Codex | `~/.codex/**/*.jsonl` |

## State Persistence

State files stored in `~/Library/Application Support/Irrlicht/instances/<sessionID>.json`
with atomic writes. Real-time updates fan out via WebSocket to the SwiftUI menu bar app.
