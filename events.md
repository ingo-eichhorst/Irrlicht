# Irrlicht Session State Machine

## States (4, MECE)

| State | Definition |
|-------|-----------|
| **`working`** | Agent actively processing (tools, text generation, hooks, compaction, API retries) |
| **`waiting`** | User-blocking tool open -- agent needs user to respond (AskUserQuestion, ExitPlanMode) |
| **`ready`** | Agent idle at prompt, waiting for next user message |
| **`error`** | The session's own machinery failed -- provider refused or failed the call, credentials rejected, agent process died mid-turn, or Irrlicht could not read the session (#1796) |

`error` is NOT a tool failure. A grep that matched nothing or a build that
broke is the agent working normally and stays `working`.

The vocabulary is declared once, in `session.CanonicalStates()`; the daemon
derives both `IsCanonicalState` and every message that lists the states from
it, so this table is documentation rather than a second source of truth.

### Decision Tree

1. Is there an open user-blocking tool? **Yes** -> `waiting`
2. Is there an unrecovered session-level failure? **Yes** -> `error`
3. Is the agent actively processing? **Yes** -> `working`
4. Otherwise -> `ready`

Step 2 sits above step 3 deliberately: a terminal provider failure often
leaves a transcript tail that reads like a finished turn, so a lower placement
would report a failed session as `ready` -- green, and silent.

### Leaving `error`

The next SUCCESSFUL turn clears it, and nothing else does -- no timeout, no
minimum hold:

- `error -> working` when the next turn starts
- `error -> ready` on `turn_done`

Known and accepted: with no minimum hold, a provider error that recovers in a
few hundred milliseconds can enter and leave `error` inside one poll interval
and never be seen.

An errored session does not count toward concurrency, the same as `ready`.

---

## Application Lifecycle Events

These events manage the existence of sessions -- creation and deletion. They are independent of the lifecycle state machine.

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
| Liveness sweep | `syscall.Kill(pid, 0)` | Every 5s, backing off to 15s after 3 consecutive clean sweeps; checks all sessions and deletes dead ones |
| Startup cleanup | `syscall.Kill(pid, 0)` | Synchronous in `seedFromDisk`; dead PIDs deleted before kqueue registration |

---

## Session State Transitions

These transitions change the lifecycle state of an existing session.

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

### Impossible Transitions

- `ready` -> `waiting`: Cannot skip `working`; any activity goes through `working` first
- `waiting` -> `ready` (via content): Agent cannot finish while a blocking tool is open; only process exit clears it (as deletion)

---

## Core Detection Logic

### `NeedsUserAttention()` -> triggers `waiting`

```
HasOpenToolCall=true AND any LastOpenToolNames entry in {AskUserQuestion, ExitPlanMode}
```

### `IsAgentDone()` -> triggers `ready`

```
Primary:  LastEventType == "turn_done"
Fallback: HasOpenToolCall=false AND LastEventType in {assistant, assistant_output}
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

Note: transcript silence on a non-blocking open tool call (e.g. a long-running build) is **not** used as a signal. Earlier versions of irrlicht had a 15s stale-tool timer that tried to infer permission-pending state from silence, but it could not distinguish a modal from a long-running tool and produced spurious `working → waiting` flicker. If a real permission-pending signal is ever needed it will come from an adapter-specific marker (e.g. a Claude Code Notification hook), not a wall-clock timer.

---

## Subagent Detection

Parent-child relationships are derived from the transcript path: Claude Code writes an `isSidechain` transcript under `<parent>/subagents/agent-*.jsonl` for every `Agent` tool call, and the fswatcher registers each as a child session linked via `ParentSessionID`. Parent sessions carry a `subagentSummary` under the JSON key `subagents`:

```
subagentSummary { total, working, waiting, ready int }
```

Subagent sessions run independent state machines with the same states.

---

## Orthogonal Axes (not states)

| Axis | Values |
|------|--------|
| **Adapter** | `claude-code` / `codex` / `pi` / `aider` / `opencode` / `kiro-cli` / `gemini-cli` / `antigravity` / `mistral-vibe` -- identifies source agent |
| **PressureLevel** | `safe` / `caution` / `warning` / `critical` -- context window utilization |

---

## State Persistence

Session files: `~/Library/Application Support/Irrlicht/instances/<sessionID>.json`
Atomic writes via temp file + rename. Real-time updates fan out via WebSocket (`session_created`, `session_updated`, `session_deleted`).

Memory store merges disk on `ListAll` to pick up sessions created externally (e.g. by the claudecode hooks receiver, `POST /api/v1/hooks/claudecode`).

## Session Discovery Paths

| Assistant | Transcript Location |
|-----------|-------------------|
| Claude Code | `~/.claude/projects/**/*.jsonl` |
| OpenAI Codex | `~/.codex/**/*.jsonl` |
| Pi | `~/.pi/agent/sessions/**/*.jsonl` |
| Aider | `<project-cwd>/.aider.chat.history.md` |
| OpenCode | `~/.local/share/opencode/opencode.db` (SQLite, polled — not a JSONL glob) |
| Kiro CLI | `~/.kiro/sessions/cli/*.jsonl` |
| Gemini CLI | `~/.gemini/tmp/**/chats/*.jsonl` |
| Antigravity | `~/.gemini/antigravity-cli/brain/**/transcript.jsonl` (CLI) and `~/.gemini/antigravity/brain/**/transcript.jsonl` (IDE) |
| Mistral Vibe | `~/.vibe/logs/session/<session-id>/messages.jsonl` (plus a sibling `meta.json`) |
