# Session State Machine Reference

This document defines the complete, MECE state machine for coding assistant session detection.
It is agent-agnostic and applies to all adapters (Claude Code, Codex, future agents).

## States (3)

| State | Definition |
|-------|-----------|
| **working** | Agent process is alive AND actively processing: generating text, executing tools, waiting for tool results, running hooks, compacting context, retrying API errors, or any internal operation that does NOT require user input. |
| **waiting** | Agent process is alive AND has completed its turn — user must type a response, approve a permission prompt, or otherwise provide input before the agent can continue. |
| **ready** | Session is inactive: process exited (graceful, crash, or ESC/cancel), transcript removed, or session was never started. |

### MECE Proof

Two-level decision tree covers all possible states:
1. Is the process alive? **No** → `ready`
2. Does the agent need user input? **Yes** → `waiting`, **No** → `working`

## State Transitions (8)

```
ID   From        → To          Trigger                        Detection
───  ──────────  ──────────    ─────────────────────────────  ──────────────────────────────
T1   [none]      → working     New transcript file appears     FSEvents CREATE
T2   working     → waiting     Agent finished turn             IsWaitingForInput()=true
T3   waiting     → working     User sent message/new input     IsWaitingForInput()=false
T4   working     → ready       Process exited                  kqueue EVFILT_PROC NOTE_EXIT
T5   waiting     → ready       Process exited                  kqueue EVFILT_PROC NOTE_EXIT
T6   working     → ready       Transcript file deleted         FSEvents REMOVE
T7   waiting     → ready       Transcript file deleted         FSEvents REMOVE
T8   ready       → working     Transcript file grows           FSEvents WRITE on ready session
```

### Impossible Transitions

- ready → waiting: Cannot skip working; any activity goes through working first
- waiting → waiting: No-op
- working → working: Implicit (stays in state)

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

## Detection Logic

### Core: `IsWaitingForInput()`

```
IsWaitingForInput() = (LastEventType ∈ {assistant, assistant_message, assistant_output})
                      AND (HasOpenToolCall = false)
```

### Inputs (deterministic, from JSONL parsing)

- **LastEventType**: `type` field of the most recent message event
- **HasOpenToolCall**: `count(tool_use) - count(tool_result) > 0`

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
| Assistant finished (end_turn, no tools) | waiting |
| AskUserQuestion tool completed | waiting |
| Process exited | ready |
| User pressed ESC / cancelled | ready |
| Transcript deleted | ready |
| Session never started | ready |
| Stale orphaned session | ready |

## Subagent State Tracking

Subagent sessions run independent state machines with the same 3 states.
Parent sessions carry a `SubagentSummary` with aggregate counts:

```
SubagentSummary {
    total:   int  // Total subagent count
    working: int  // Subagents in working state
    waiting: int  // Subagents in waiting state
    ready:   int  // Subagents in ready state
}
```

Updated whenever a child session transitions state.

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
