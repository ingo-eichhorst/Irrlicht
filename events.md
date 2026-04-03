# Irrlicht Session Detection — State Machine

Irrlicht uses transcript file-watching (FSEvents) and process monitoring (kqueue) to
detect coding assistant sessions without requiring hooks.

## States (3, MECE)

| State | Definition |
|-------|-----------|
| **`working`** | Agent actively processing (tools, text generation, hooks, compaction, API retries) |
| **`waiting`** | User-blocking tool open — agent needs user to respond (AskUserQuestion, ExitPlanMode) |
| **`ready`** | Agent idle: finished turn at prompt, waiting for next user message |

### MECE Decision Tree

Three-level decision tree covers all possible states:
1. Is there an open user-blocking tool? **Yes** → `waiting`
2. Is the agent actively processing? **Yes** → `working`
3. Otherwise → `ready`

## Detection Mechanisms

| Mechanism | Technology | Detects |
|-----------|-----------|---------|
| **TranscriptWatcher** | fsnotify (FSEvents on macOS) | New sessions, activity, removals |
| **ProcessWatcher** | kqueue EVFILT_PROC NOTE_EXIT | Process exit → session removed |
| **TailerPipeline** | JSONL transcript parsing | Model, tokens, open tool calls, tool names |

## State Transitions

| ID | From | To | Trigger | Detection |
|----|------|----|---------|-----------|
| T0 | — | `ready` | Agent started | FSEvents CREATE on .jsonl |
| T1 | `ready` | `working` | New .jsonl file | FSEvents CREATE |
| T2 | `working` | `ready` | Agent finished turn | `IsAgentDone()=true` |
| T3 | `ready` | `working` | New activity | FSEvents WRITE on ready session |
| T4 | `working` | **removed** | Process exited | kqueue NOTE_EXIT |
| T5 | `waiting` | **removed** | Process exited | kqueue NOTE_EXIT |
| T6 | `working` | **removed** | Transcript deleted | FSEvents REMOVE |
| T7 | `working` | `waiting` | User-blocking tool open | `NeedsUserAttention()=true` |
| T8 | `waiting` | `working` | User responded | `NeedsUserAttention()=false` |
| T9 | any | **removed** | Process exit or transcript deletion | kqueue NOTE_EXIT / FSEvents REMOVE |

**removed** = session is deleted from the display entirely. There is no `removed` state — the session ceases to exist. This applies to any process termination (SIGKILL, SIGTERM, crash, or normal exit via `/quit`) and to transcript deletion.

### Impossible Transitions

- `ready` → `waiting`: Cannot skip `working`; any activity goes through `working` first
- `waiting` → `ready` (via content): Agent cannot finish while a blocking tool is open; only process exit clears it (as removal)

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
           blocking  │   │  responded)          ▼            │
           tool)     │   │               [session removed]   │
                  ┌──▼───┴───┐                               │
                  │          ├──T5─(exit)──[session removed] │
                  │ waiting  │                               │
                  │          │        ┌────────┐             │
                  └──────────┘        │  ready │◄─── T2 ────┘
                                      │        │  (agent done)
                                      └────────┘
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

### User-Blocking Tools

Tools that suspend the agent and wait for user interaction before returning:

| Tool | Description |
|------|-------------|
| `AskUserQuestion` | Explicitly asks the user a question |
| `ExitPlanMode` | Asks the user to approve the plan |

### Message Events (affect `LastEventType`)

```
user, assistant, tool_use, tool_call, tool_result,
user_message, assistant_message, user_input, assistant_output, message
```

### Non-Message Events (do NOT affect `LastEventType`)

System: `stop_hook_summary`, `turn_duration`, `local_command`, `compact_boundary`, `api_error`  
Management: `permission-mode`, `attachment`, `file-history-snapshot`, `progress`, `last-prompt`

## Scenario Mapping

| Scenario | State |
|----------|-------|
| New transcript file appears | `ready` |
| User sent message, assistant generating | `working` |
| Assistant called tool (stop_reason=tool_use) | `working` |
| Tool executing | `working` |
| Tool result returned, assistant continues | `working` |
| Multiple parallel tools, some pending | `working` |
| Subagent spawned (Agent tool) | `working` |
| Background bash command running | `working` |
| Context compaction | `working` |
| API error, retrying | `working` |
| Hooks running | `working` |
| Slash command executing | `working` |
| Session opened, no first message yet | `working` |
| Permission prompt (open tool, idle) | `working` |
| `AskUserQuestion` tool open | `waiting` |
| `ExitPlanMode` tool open | `waiting` |
| Assistant finished (end_turn, no tools) | `ready` |
| User pressed ESC / cancelled | `ready` |
| Process exited (SIGTERM, SIGKILL, crash, `/quit`) | **removed** |
| Transcript deleted | **removed** |
| Session never started | **removed** |
| Stale orphaned session (no PID, no activity) | **removed** |

## Subagent Detection

Parent-child relationships derived from a working session with an open `Agent` tool call in the same project dir. Parent sessions carry a `SubagentSummary` with aggregate child state counts:

```
SubagentSummary {
    total:   int
    working: int
    waiting: int
    ready:   int
}
```

Subagent sessions run independent state machines with the same 3 states.

## Process Exit Detection

One-time `lsof -F p <transcript_path>` at session creation → PID → `EVFILT_PROC NOTE_EXIT`.

When NOTE_EXIT fires for any reason (SIGKILL, SIGTERM, crash, clean exit), the session is removed immediately — it is not transitioned to `ready`. A session without a resolvable PID is treated as an orphan and cleaned up.

## Session Discovery Paths

| Assistant | Transcript location |
|-----------|-------------------|
| Claude Code | `~/.claude/projects/**/*.jsonl` |
| OpenAI Codex | `~/.codex/**/*.jsonl` |

## State Persistence

State files stored in `~/Library/Application Support/Irrlicht/instances/<sessionID>.json`
with atomic writes. Real-time updates fan out via WebSocket to the SwiftUI menu bar app.
On session removal, the state file is deleted.

## Orthogonal Axes (not states)

These overlay on the state machine without adding new states:

| Axis | Values |
|------|--------|
| **CompactionState** | `not_compacting` \| `compacting` \| `post_compact` — overlaid on `working` |
| **Adapter** | `claude-code` \| `codex` \| future — identifies source agent |
| **PressureLevel** | `safe` \| `caution` \| `warning` \| `critical` — context window utilization |

## Adding a New Agent Adapter

1. Create adapter in `core/adapters/inbound/agents/<name>/adapter.go`
2. Point to the agent's transcript directory
3. If JSONL format differs, implement custom `MetricsCollector` producing `SessionMetrics`
4. Register watcher in `main.go`
5. State machine logic is identical — no changes needed
