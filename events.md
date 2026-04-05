# Irrlicht Session State Machine

## States (3, MECE)

| State | Definition |
|-------|-----------|
| **`working`** | Agent actively processing (tools, text generation, hooks, compaction, API retries) |
| **`waiting`** | User-blocking tool open -- agent needs user to respond (AskUserQuestion, ExitPlanMode) |
| **`ready`** | Agent idle at prompt, waiting for next user message |

### Decision Tree

1. Is there an open user-blocking tool? **Yes** -> `waiting`
2. Is the agent actively processing? **Yes** -> `working`
3. Otherwise -> `ready`

---

## Application Lifecycle Events

These events manage the existence of sessions -- creation and deletion. They are independent of the working/waiting/ready state machine.

| User Scenario | Before | After | Technical Trigger | Detection |
|--------------|--------|-------|-------------------|-----------|
| User opens Claude Code | no session | `ready` | `claude` process appears | Process scanner (`pgrep -x claude`, 1s poll) |
| User types first message | pre-session exists | real session created | New `.jsonl` file created | fsnotify CREATE |
| Real transcript appears for pre-session | pre-session + real session | real session only | `cleanupPreSessionsForProject` | Automatic on EventNewSession with transcript path |
| User exits normally (`/quit`, Ctrl-D) | any state | **deleted** | Process exits | kqueue NOTE_EXIT |
| User cancels (ESC) | any state | **deleted** | Process exits | kqueue NOTE_EXIT |
| Process killed (SIGKILL, SIGTERM, crash) | any state | **deleted** | Process exits | kqueue NOTE_EXIT |
| Transcript file deleted | any state | `ready` | File removed from disk | fsnotify REMOVE |
| Daemon starts, finds dead PID on disk | session file exists | **deleted** | `syscall.Kill(pid, 0)` returns ESRCH | Synchronous check in `seedFromDisk` |
| Dead PID detected during runtime | session exists | **deleted** | `syscall.Kill(pid, 0)` returns ESRCH | Periodic liveness sweep (5s) |

**deleted** = session file removed from disk and memory, `session_deleted` broadcast via WebSocket. The session ceases to exist.

### Pre-session Lifecycle

Pre-sessions (`proc-<pid>`) are synthetic sessions created by the process scanner before any transcript exists. They allow the UI to show a session as soon as the user opens Claude Code.

1. Process scanner detects `claude` process via `pgrep`
2. Checks `hasActiveSession`: skips if a transcript was modified in the last 60s (file watcher handles those)
3. Creates pre-session with `proc-<pid>` ID, state `ready`
4. When real transcript arrives, pre-session is deleted and replaced by the real session

### PID Discovery and Monitoring

| Step | Mechanism | Details |
|------|-----------|---------|
| Discovery | `lsof -t <transcript>` | One-shot on session creation; retried async on activity if PID=0 |
| Registration | `EVFILT_PROC NOTE_EXIT` | kqueue watches the PID for exit |
| Liveness sweep | `syscall.Kill(pid, 0)` | Every 5s, checks all sessions; deletes dead ones |
| Startup cleanup | `syscall.Kill(pid, 0)` | Synchronous in `seedFromDisk`; dead PIDs deleted before kqueue registration |

---

## Session State Transitions

These transitions change the working/waiting/ready state of an existing session.

| User Scenario | Before | After | Technical Trigger | Detection |
|--------------|--------|-------|-------------------|-----------|
| User sends message, assistant starts | `ready` | `working` | Transcript write | fsnotify WRITE, `NeedsUserAttention()=false`, `IsAgentDone()=false` |
| Assistant calls tool (stop_reason=tool_use) | `working` | `working` | Transcript write | Open tool call count > 0 |
| Tool result returned, assistant continues | `working` | `working` | Transcript write | Activity event, agent still processing |
| Assistant finished turn (end_turn) | `working` | `ready` | `turn_duration` or `stop_hook_summary` system event | `IsAgentDone()=true` |
| User cancelled mid-turn (ESC) | `working` | `ready` | `stop_hook_summary` system event | `IsAgentDone()=true` |
| AskUserQuestion tool opened | `working` | `waiting` | Tool use in transcript | `NeedsUserAttention()=true` |
| ExitPlanMode tool opened | `working` | `waiting` | Tool use in transcript | `NeedsUserAttention()=true` |
| User answers question / approves plan | `waiting` | `working` | Tool result in transcript | `NeedsUserAttention()=false` |
| Tool call pending permission for >5s | `working` | `waiting` | No transcript activity for 5s with open non-blocking tool call | `staleToolTimeout` timer in SessionDetector (skipped for `bypassPermissions` mode and Agent-only calls) |
| User approves permission / tool completes | `waiting` | `working` | Tool result in transcript | Activity resumes, `ClassifyState` re-evaluates |

### Impossible Transitions

- `ready` -> `waiting`: Cannot skip `working`; any activity goes through `working` first
- `waiting` -> `ready` (via content): Agent cannot finish while a blocking tool is open; only process exit clears it (as deletion)

---

## Core Detection Logic

### `NeedsUserAttention()` -> triggers `waiting`

```
HasOpenToolCall=true AND any LastOpenToolName in {AskUserQuestion, ExitPlanMode}
```

### `IsAgentDone()` -> triggers `ready`

```
Primary:  LastEventType == "turn_done"
Fallback: HasOpenToolCall=false AND LastEventType in {assistant, assistant_message, assistant_output}
```

### Turn Completion Signals

The transcript tailer maps these system events to `LastEventType = "turn_done"`:

| System Subtype | When Written |
|---------------|-------------|
| `turn_duration` | End of each agent turn (primary signal) |
| `stop_hook_summary` | After stop hooks run (fallback when turn_duration is absent) |

### Transcript Event Classification

**Message events** (affect `LastEventType`):
`user`, `assistant`, `tool_use`, `tool_call`, `tool_result`, `user_message`, `assistant_message`, `user_input`, `assistant_output`, `message`

**System events** (do NOT affect `LastEventType`, except turn completion):
`turn_duration`, `stop_hook_summary`, `local_command`, `compact_boundary`, `api_error`

**Management events** (ignored):
`permission-mode`, `attachment`, `file-history-snapshot`, `progress`, `last-prompt`

### User-Blocking Tools

| Tool | Description |
|------|-------------|
| `AskUserQuestion` | Explicitly asks the user a question |
| `ExitPlanMode` | Asks the user to approve the plan |

### Permission-Pending Detection (Stale Tool Call)

When a session is `working` with open tool calls that are NOT user-blocking, a 5-second timer starts. If no new transcript activity arrives before the timer fires, the session transitions to `waiting` — the tool call is likely pending user permission approval.

**Skipped when:**
- `PermissionMode` is `bypassPermissions` (all tools auto-execute)
- All open tools are `Agent` (subagents legitimately run for minutes)

The `PermissionMode` field is extracted from `permission-mode` JSONL events. Known values: `default`, `plan`, `bypassPermissions`.

---

## Subagent Detection

Parent-child relationships derived from a working session with an open `Agent` tool call in the same project dir. Parent sessions carry a `SubagentSummary`:

```
SubagentSummary { total, working, waiting, ready int }
```

Subagent sessions run independent state machines with the same 3 states.

---

## Orthogonal Axes (not states)

| Axis | Values |
|------|--------|
| **CompactionState** | `not_compacting` / `compacting` / `post_compact` -- overlaid on `working` |
| **Adapter** | `claude-code` / `codex` / future -- identifies source agent |
| **PressureLevel** | `safe` / `caution` / `warning` / `critical` -- context window utilization |

---

## State Persistence

Session files: `~/Library/Application Support/Irrlicht/instances/<sessionID>.json`
Atomic writes via temp file + rename. Real-time updates fan out via WebSocket (`session_created`, `session_updated`, `session_deleted`).

Memory store merges disk on `ListAll` to pick up sessions created externally (e.g. by `irrlicht-hook`).

## Session Discovery Paths

| Assistant | Transcript Location |
|-----------|-------------------|
| Claude Code | `~/.claude/projects/**/*.jsonl` |
| OpenAI Codex | `~/.codex/**/*.jsonl` |
