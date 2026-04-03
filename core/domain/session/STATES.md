# Session State Machine Reference

This document defines the complete, MECE state machine for coding assistant session detection.
It is agent-agnostic and applies to all adapters (Claude Code, Codex, future agents).

## States (3)

| State | Definition |
|-------|-----------|
| **working** | Agent actively processing: generating text, executing tools, waiting for tool results, running hooks, compacting context, retrying API errors, or any operation that does NOT require user input. |
| **waiting** | Agent explicitly needs user action: a user-blocking tool is open (AskUserQuestion, ExitPlanMode) and the agent cannot continue until the user responds. |
| **ready** | Agent is idle: finished its turn and sitting at prompt, process exited, transcript removed, or session was never started. |

### MECE Proof

Three-level decision tree covers all possible states:
1. Is there an open user-blocking tool? **Yes** → `waiting`
2. Is the agent actively processing? **Yes** → `working`
3. Otherwise → `ready`

## State Transitions (8)

```
ID   From        → To          Trigger                        Detection
───  ──────────  ──────────    ─────────────────────────────  ──────────────────────────────
T1   [none]      → working     New transcript file appears     FSEvents CREATE
T2   working     → ready       Agent finished turn             IsAgentDone()=true
T3   ready       → working     New transcript activity         FSEvents WRITE on ready session
T4   working     → ready       Process exited                  kqueue EVFILT_PROC NOTE_EXIT
T5   waiting     → ready       Process exited                  kqueue EVFILT_PROC NOTE_EXIT
T6   working     → ready       Transcript file deleted         FSEvents REMOVE
T7   working     → waiting     User-blocking tool open         NeedsUserAttention()=true
T8   waiting     → working     User responded (tool result)    NeedsUserAttention()=false
```

### Impossible Transitions

- ready → waiting: Cannot skip working; any activity goes through working first
- waiting → ready (via content): Agent can't finish while a tool is open; only process exit clears

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

## Detection Logic

### `NeedsUserAttention()` → triggers `waiting`

```
HasOpenToolCall=true AND any LastOpenToolName ∈ {AskUserQuestion, ExitPlanMode}
```

### `IsAgentDone()` → triggers `ready`

```
HasOpenToolCall=false AND LastEventType ∈ {assistant, assistant_message, assistant_output}
```

### User-Blocking Tools

Tools that wait for user interaction before returning:
- `AskUserQuestion` — explicitly asks user a question
- `ExitPlanMode` — asks user to approve plan

### Message Events (affect LastEventType)

```
user, assistant, tool_use, tool_call, tool_result,
user_message, assistant_message, user_input, assistant_output, message
```

### Non-Message Events (do NOT affect LastEventType)

System: `stop_hook_summary`, `turn_duration`, `local_command`, `compact_boundary`, `api_error`
Management: `permission-mode`, `attachment`, `file-history-snapshot`, `progress`, `last-prompt`

## Scenario Mapping

| Scenario | State |
|----------|-------|
| New transcript file appears | working |
| User sent message, assistant generating | working |
| Assistant called tool (stop_reason=tool_use) | working |
| Tool executing | working |
| Tool result returned, assistant continues | working |
| Multiple parallel tools, some pending | working |
| Subagent spawned (Agent tool) | working |
| Background bash command running | working |
| Context compaction | working |
| API error, retrying | working |
| Hooks running | working |
| Slash command executing | working |
| Session opened, no first message yet | working |
| Permission prompt (open tool, idle) | working |
| AskUserQuestion tool open | **waiting** |
| ExitPlanMode tool open | **waiting** |
| Assistant finished (end_turn, no tools) | **ready** |
| Process exited | **ready** |
| User pressed ESC / cancelled | **ready** |
| Transcript deleted | **ready** |
| Session never started | **ready** |
| Stale orphaned session | **ready** |

## Subagent State Tracking

Subagent sessions run independent state machines with the same 3 states.
Parent sessions carry a `SubagentSummary` with aggregate counts:

```
SubagentSummary {
    total:   int
    working: int
    waiting: int
    ready:   int
}
```

## Orthogonal Axes (not states)

- **CompactionState**: `not_compacting` | `compacting` | `post_compact` — overlaid on `working`
- **Adapter**: `claude-code` | `codex` | future — identifies source agent
- **PressureLevel**: `safe` | `caution` | `warning` | `critical` — context window utilization

## Adding a New Agent Adapter

1. Create adapter in `core/adapters/inbound/agents/<name>/adapter.go`
2. Point to the agent's transcript directory
3. If JSONL format differs, implement custom `MetricsCollector` producing `SessionMetrics`
4. Register watcher in `main.go`
5. State machine logic is identical — no changes needed
