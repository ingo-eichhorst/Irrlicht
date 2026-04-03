# Irrlicht Session Detection — State Machine

Irrlicht uses transcript file-watching (FSEvents) and process monitoring (kqueue) to
detect coding assistant sessions without requiring hooks.

## States (3, MECE)

| State | Definition |
|-------|-----------|
| **`working`** | Agent actively processing (tools, text generation, hooks, compaction, API retries) |
| **`waiting`** | User-blocking tool open — agent needs user to respond (AskUserQuestion, ExitPlanMode) |
| **`ready`** | Agent idle: finished turn at prompt, process exited, transcript removed, or cancelled |

See `core/domain/session/STATES.md` for the full reference including scenario mapping.

## Detection Mechanisms

| Mechanism | Technology | Detects |
|-----------|-----------|---------|
| **TranscriptWatcher** | fsnotify (FSEvents on macOS) | New sessions, activity, removals |
| **ProcessWatcher** | kqueue EVFILT_PROC NOTE_EXIT | Process exit → ready (~1ms) |
| **TailerPipeline** | JSONL transcript parsing | Model, tokens, open tool calls, tool names |

## State Transitions (8)

| ID | From | To | Trigger | Detection |
|----|------|----|---------|-----------|
| T1 | — | working | New .jsonl file | FSEvents CREATE |
| T2 | working | ready | Agent finished turn | `IsAgentDone()=true` |
| T3 | ready | working | New activity | FSEvents WRITE on ready session |
| T4 | working | ready | Process exited | kqueue NOTE_EXIT |
| T5 | waiting | ready | Process exited | kqueue NOTE_EXIT |
| T6 | working | ready | Transcript deleted | FSEvents REMOVE |
| T7 | working | waiting | User-blocking tool open | `NeedsUserAttention()=true` |
| T8 | waiting | working | User responded | `NeedsUserAttention()=false` |

## State Diagram

```
                  ┌──────────┐
   T1             │          │◄──────────────────────────────┐
  (new file)──────►  working │                               │
                  │          ├───────T4,T6──────┐            │
                  └──┬───▲───┘  (exit/remove)   │            │
                     │   │                      │        T3  │
                T7   │   │  T8                  │  (new      │
          (user-     │   │ (user                │   activity)│
           blocking  │   │  responded)          │            │
           tool)     │   │                      ▼            │
                  ┌──▼───┴───┐             ┌────────┐        │
                  │          ├──T5─────────►         ├────────┘
                  │ waiting  │ (exit)       │  ready  │◄─── T2 (agent done)
                  │          │             │         │
                  └──────────┘             └────────┘
```

## Core Detection Logic

**`NeedsUserAttention()`** → triggers `waiting`:
```
HasOpenToolCall=true AND any LastOpenToolName ∈ {AskUserQuestion, ExitPlanMode}
```

**`IsAgentDone()`** → triggers `ready`:
```
HasOpenToolCall=false AND LastEventType ∈ {assistant, assistant_message, assistant_output}
```

## Subagent Detection

Parent-child relationships derived from working session with open tool call in same project dir.
Parent sessions carry a `SubagentSummary` with aggregate child state counts.

## Process Exit Detection

One-time `lsof -F p <transcript_path>` at session creation → PID → `EVFILT_PROC NOTE_EXIT`.
Fallback: session with no PID ages out via orphan cleanup.

## Session Discovery Paths

| Assistant | Transcript location |
|-----------|-------------------|
| Claude Code | `~/.claude/projects/**/*.jsonl` |
| OpenAI Codex | `~/.codex/**/*.jsonl` |

## State Persistence

State files stored in `~/Library/Application Support/Irrlicht/instances/<sessionID>.json`
with atomic writes. Real-time updates fan out via WebSocket to the SwiftUI menu bar app.
