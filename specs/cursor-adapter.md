# Cursor Adapter for Irrlicht

*Design doc for Phase 2: extending Irrlicht to monitor Cursor IDE AI sessions*

*Related: GH#31 Phase 2 — implements [ir-2w7]*

---

## Overview

Cursor IDE (a VS Code fork) has a hooks system nearly identical to Claude Code's.
This document specifies how to build a `cursor-hook` adapter that monitors Cursor
Agent sessions and writes the same `SessionState` JSON files that Irrlicht's
SwiftUI app already reads.

**Result:** Irrlicht's menu bar shows Cursor AI sessions alongside Claude Code
sessions, with identical state semantics and a single unified view.

---

## Cursor Hook System

### Config File Locations

Cursor reads hooks from (priority order, later overrides earlier):

| Scope | Path |
|-------|------|
| User (global) | `~/.cursor/hooks.json` |
| Project | `.cursor/hooks.json` (in workspace root) |
| Team | Team-level config (enterprise) |
| Enterprise | Enterprise-level config (highest) |

The installer should merge into `~/.cursor/hooks.json` (same pattern as
`~/.claude/settings.json`), using a variant of the existing settings-merger.

### Config Format

```json
{
  "version": 1,
  "hooks": {
    "sessionStart": [
      {
        "command": "/usr/local/bin/cursor-hook",
        "timeout": 30,
        "type": "command",
        "failClosed": false
      }
    ],
    "sessionEnd": [{ "command": "/usr/local/bin/cursor-hook", "timeout": 30 }],
    "stop": [{ "command": "/usr/local/bin/cursor-hook", "timeout": 30 }],
    "subagentStart": [{ "command": "/usr/local/bin/cursor-hook", "timeout": 30 }],
    "subagentStop": [{ "command": "/usr/local/bin/cursor-hook", "timeout": 30 }],
    "preToolUse": [{ "command": "/usr/local/bin/cursor-hook", "timeout": 30 }],
    "postToolUse": [{ "command": "/usr/local/bin/cursor-hook", "timeout": 30 }],
    "postToolUseFailure": [{ "command": "/usr/local/bin/cursor-hook", "timeout": 30 }],
    "beforeSubmitPrompt": [{ "command": "/usr/local/bin/cursor-hook", "timeout": 30 }],
    "preCompact": [{ "command": "/usr/local/bin/cursor-hook", "timeout": 30 }],
    "afterAgentThought": [{ "command": "/usr/local/bin/cursor-hook", "timeout": 30 }],
    "beforeShellExecution": [{ "command": "/usr/local/bin/cursor-hook", "timeout": 30 }],
    "afterShellExecution": [{ "command": "/usr/local/bin/cursor-hook", "timeout": 30 }]
  }
}
```

`failClosed: false` (default) means hook failures do not block the agent.
Set `failClosed: true` only for control hooks (not applicable here — we are
monitoring only, not gatekeeping).

### Universal Event Payload

Every Cursor hook receives this base payload on stdin:

```json
{
  "hook_event_name": "preToolUse",
  "conversation_id": "conv_abc123",
  "generation_id": "gen_xyz789",
  "model": "claude-sonnet-4-5",
  "cursor_version": "0.45.0",
  "workspace_roots": ["/path/to/project"],
  "user_email": "user@example.com",
  "transcript_path": "/path/to/transcript.jsonl"
}
```

**Key difference from Claude Code:** Cursor uses `conversation_id` (not
`session_id`) as the session identifier. The adapter normalizes this.

### Event-Specific Additional Fields

| Event | Additional fields |
|-------|-------------------|
| `sessionStart` | `source` (`new`/`resume`), `permission_mode` |
| `sessionEnd` | `reason` (`user_exit`/`timeout`/`clear`/`logout`) |
| `stop` | `stop_reason` (`end_turn`/`max_tokens`/`tool_use`) |
| `subagentStart` | `subagent_id`, `parent_conversation_id` |
| `subagentStop` | `subagent_id`, `parent_conversation_id` |
| `preToolUse` | `tool_name`, `tool_input` (object) |
| `postToolUse` | `tool_name`, `tool_input`, `tool_response` |
| `postToolUseFailure` | `tool_name`, `tool_input`, `error` |
| `beforeSubmitPrompt` | `prompt` (user message text) |
| `beforeShellExecution` | `command`, `working_dir` |
| `afterShellExecution` | `command`, `exit_code`, `stdout`, `stderr` |
| `preCompact` | `compact_type` (`auto`/`manual`) |
| `afterAgentThought` | `thought` (reasoning text, may be empty) |

---

## Event → State Mapping

### Primary Mapping Table

| Cursor event | Irrlicht analog | → `state` | Notes |
|---|---|---|---|
| `sessionStart` (source=new) | `SessionStart` (startup) | `ready` | New session, no task yet |
| `sessionStart` (source=resume) | `SessionStart` (resume) | `working` | Resumed mid-task |
| `beforeSubmitPrompt` | `UserPromptSubmit` | `working` | User submitted input |
| `preToolUse` | `PreToolUse` | `working` | Tool about to execute |
| `postToolUse` | `PostToolUse` | `working` | Tool completed |
| `postToolUseFailure` | `PostToolUse` (error) | `working` | Tool failed; session continues |
| `beforeShellExecution` | `PreToolUse` (Bash) | `working` | Shell command starting |
| `afterShellExecution` | `PostToolUse` (Bash) | `working` | Shell command done |
| `stop` | `Stop` | `ready` | Agent turn complete; waiting for user |
| `subagentStart` | *(no Claude Code analog)* | `working` | Subagent spawned |
| `subagentStop` | `SubagentStop` | `ready` | Subagent complete |
| `preCompact` (auto) | `PreCompact` (auto) | `working` (compacting) | Auto-compaction |
| `preCompact` (manual) | `PreCompact` (manual) | `working` (compacting) | Manual compaction |
| `afterAgentThought` | *(no Claude Code analog)* | `working` | Reasoning in progress |
| `sessionEnd` (any reason) | `SessionEnd` | delete | Session terminated |

### "Waiting" State and Speculative Waiting

Cursor has no direct `Notification` equivalent (Claude Code fires `Notification`
when the agent needs user permission). The closest proxies:

1. **`preToolUse` for approval-prone tools** — use the same speculative-wait
   pattern from the Claude Code adapter:
   - Spawn a background timer on `preToolUse` for high-risk tools
   - If no `postToolUse` arrives within 2 s, speculatively transition to `waiting`
   - When `postToolUse` or `postToolUseFailure` fires, cancel the timer and set `working`

2. **Approval-prone tools in Cursor context:**
   - Shell execution tools (any tool whose `tool_name` contains "shell", "bash", "exec", "run")
   - File write tools (any tool whose `tool_name` contains "write", "edit", "create", "delete")
   - `beforeShellExecution` directly (this event always precedes a potentially
     user-visible permission dialog)

3. **Fallback:** `stop` transitions to `ready` (not `waiting`), matching Claude Code's
   `Stop` → `ready` semantics. The distinction: `ready` = task done; `waiting` = blocked
   on user approval.

### Session End Reasons

| `reason` field | Action | Rationale |
|---|---|---|
| `user_exit` | delete session file | User closed the session |
| `timeout` | delete session file | Session timed out |
| `clear` | delete session file | Session was cleared (like `/clear` in Claude Code) |
| `logout` | delete session file | User logged out |
| *(missing/other)* | delete session file | Unknown termination |

Unlike Claude Code, Cursor has no `prompt_input_exit` (ESC on notification prompt)
because the waiting state is handled differently. Therefore `cancelled_by_user`
state is not applicable for Cursor sessions.

---

## Output: SessionState JSON

The adapter writes the same `SessionState` format as the Claude Code hook:

```
~/Library/Application Support/Irrlicht/instances/cursor_<conversation_id>.json
```

**Filename prefix `cursor_`** distinguishes Cursor sessions from Claude Code
sessions in the file system, enabling the UI to show a source indicator.

```json
{
  "version": 1,
  "session_id": "cursor_conv_abc123",
  "state": "working",
  "compaction_state": "not_compacting",
  "model": "claude-sonnet-4-5",
  "cwd": "/path/to/project",
  "transcript_path": "/path/to/transcript.jsonl",
  "git_branch": "main",
  "project_name": "myproject",
  "first_seen": 1742070000,
  "updated_at": 1742070045,
  "confidence": "high",
  "event_count": 12,
  "last_event": "preToolUse",
  "pid": 98765,
  "source": "cursor",
  "metrics": {
    "elapsed_seconds": 45,
    "total_tokens": 12500,
    "model_name": "claude-sonnet-4-5",
    "context_utilization_percentage": 6.25,
    "pressure_level": "low"
  }
}
```

**New field `source`:** `"cursor"` | `"claude-code"` — allows the UI to show
per-session IDE branding without changing the state machine.

**`cwd`:** Populated from `workspace_roots[0]` (Cursor provides an array; take
the first entry as the primary working directory).

**`pid`:** Populated from the hook process's `os.Getppid()` (same mechanism as
Claude Code adapter — the parent PID is the Cursor process).

---

## Implementation Architecture

### New Binary: `cursor-hook`

The adapter is a new binary `cursor-hook` (separate from `irrlicht-hook`) that
shares domain packages but has its own event normalization layer.

**Why a separate binary (not extending `irrlicht-hook`):**
- Cursor's event names differ from Claude Code's (e.g., `conversation_id` vs
  `session_id`, `stop` vs `Stop`, `beforeSubmitPrompt` vs `UserPromptSubmit`)
- The normalization layer would add branching complexity to `irrlicht-hook`
- Separate binaries enable independent versioning, testing, and kill switches
- Users who only use one tool aren't forced to install both

**Shared packages (no duplication):**
- `irrlicht/hook/domain/session` — `SessionState`, `SessionMetrics`, state constants
- `irrlicht/hook/adapters/outbound/filesystem` — atomic session file writes
- `irrlicht/hook/adapters/outbound/logging` — structured JSON logger
- `irrlicht/hook/adapters/outbound/git` — git branch extraction
- `irrlicht/hook/adapters/outbound/metrics` — transcript analysis
- `irrlicht/hook/adapters/outbound/security` — path validation

**New packages:**
- `irrlicht/cursor-hook/domain/event` — `CursorEvent` struct with Cursor field names
- `irrlicht/cursor-hook/adapters/inbound/normalize` — maps Cursor events to Irrlicht's `HookEvent`
- `irrlicht/cursor-hook/cmd/cursor-hook` — entry point (same wiring pattern as `irrlicht-hook`)

### Event Normalization

```
CursorEvent (stdin JSON)
    ↓ normalize
HookEvent (irrlicht domain)
    ↓ SmartStateTransition
TransitionResult
    ↓ filesystem.Save
SessionState JSON file
```

The normalizer translates field names and event name conventions:

```go
func NormalizeEvent(c *CursorEvent) *event.HookEvent {
    return &event.HookEvent{
        HookEventName:  mapEventName(c.HookEventName),
        SessionID:      "cursor_" + c.ConversationID,
        Model:          c.Model,
        CWD:            firstOf(c.WorkspaceRoots),
        TranscriptPath: c.TranscriptPath,
        ToolName:       c.ToolName,
        Source:         c.Source,
        Reason:         mapReason(c.Reason),
        Timestamp:      c.Timestamp,
        // ... other fields
    }
}

func mapEventName(cursorEvent string) string {
    return map[string]string{
        "sessionStart":         "SessionStart",
        "sessionEnd":           "SessionEnd",
        "stop":                 "Stop",
        "subagentStart":        "SessionStart",   // treated as sub-session start
        "subagentStop":         "SubagentStop",
        "preToolUse":           "PreToolUse",
        "postToolUse":          "PostToolUse",
        "postToolUseFailure":   "PostToolUse",    // map to same; tool_response will carry error
        "beforeSubmitPrompt":   "UserPromptSubmit",
        "beforeShellExecution": "PreToolUse",     // speculative wait trigger
        "afterShellExecution":  "PostToolUse",
        "preCompact":           "PreCompact",
        "afterAgentThought":    "PreToolUse",     // keeps session in "working"
    }[cursorEvent]
}
```

**`afterAgentThought` → `PreToolUse`:** The thought event fires while the agent
is reasoning. Mapping it to `PreToolUse` keeps the session in `working` state
during the inference gap between `sessionStart` and the first tool call.

### Speculative Waiting (Shell Execution)

```go
// approvalProneToolNames for Cursor context
var approvalProneToolNames = []string{
    "shell", "bash", "exec", "run",          // shell execution
    "write", "edit", "create", "delete",     // file modification
}

func isApprovalProne(toolName string) bool {
    lower := strings.ToLower(toolName)
    for _, keyword := range approvalProneToolNames {
        if strings.Contains(lower, keyword) {
            return true
        }
    }
    return false
}
```

`beforeShellExecution` always triggers speculative waiting (2 s timer) since
all shell commands in Cursor require explicit user approval by default.

---

## Hook Installation

### Installer Integration

The `settings-merger` is extended with a `--target cursor` flag:

```bash
# Merge irrlicht hooks into ~/.cursor/hooks.json
settings-merger --action merge --target cursor

# Check current status
settings-merger --action check --target cursor

# Disable
settings-merger --action merge-disable --target cursor
```

The Cursor hooks.json format differs from Claude Code's settings.json but the
merge semantics are the same: idempotent, backup-before-write, rollback-safe.

### Kill Switch

```bash
# Environment variable
export IRRLICHT_DISABLED=1

# Settings-based (in ~/.cursor/hooks.json)
{
  "hooks": {
    "irrlicht": { "disabled": true }
  }
}
```

### Manual Installation

```bash
# Install binary
sudo cp build/cursor-hook-darwin-universal /usr/local/bin/cursor-hook
sudo chmod +x /usr/local/bin/cursor-hook

# Configure hooks
settings-merger --action merge --target cursor

# Verify
cursor-hook --version
```

---

## Gap Analysis vs Claude Code Adapter

| Feature | Claude Code | Cursor |
|---------|------------|--------|
| Session start/end | ✅ `SessionStart`/`SessionEnd` | ✅ `sessionStart`/`sessionEnd` |
| User input detection | ✅ `UserPromptSubmit` | ✅ `beforeSubmitPrompt` |
| Tool use tracking | ✅ `PreToolUse`/`PostToolUse` | ✅ `preToolUse`/`postToolUse` |
| Shell execution tracking | ✅ via `PreToolUse:Bash` | ✅ `beforeShellExecution` (explicit) |
| Permission waiting | ✅ `Notification` event | ⚠️ Speculative-only (no direct event) |
| Subagent tracking | ✅ `SubagentStop` | ✅ `subagentStart`/`subagentStop` |
| Context compaction | ✅ `PreCompact` | ✅ `preCompact` |
| Reasoning visibility | ❌ No event | ✅ `afterAgentThought` |
| MCP tool tracking | ❌ No direct event | ✅ `beforeMCPExecution`/`afterMCPExecution` |
| Transcript path | ✅ `transcript_path` | ✅ `transcript_path` |
| Model field | ✅ `model` | ✅ `model` |
| CWD | ✅ `cwd` | ⚠️ `workspace_roots[0]` (array, take first) |
| PID tracking | ✅ `os.Getppid()` | ✅ `os.Getppid()` (same mechanism) |
| Cancelled-by-user state | ✅ `prompt_input_exit` → `cancelled_by_user` | ❌ No equivalent |
| Inline completions | ❌ Not applicable | ⚠️ `beforeTabFileRead`/`afterTabFileEdit` (skip — too noisy) |

**`afterAgentThought` advantage:** Cursor's reasoning events provide finer
granularity during the inference phase — Irrlicht can show `working` state
even while the model is only thinking (no tool calls yet). Claude Code has
no equivalent.

**`Notification` gap:** Cursor's permission model is more implicit. The adapter
uses speculative waiting as a best-effort proxy. In practice, Cursor shows
fewer explicit permission dialogs than Claude Code (Cursor's `failClosed: false`
default means most tools auto-proceed).

---

## Potential Conflicts with "Third-Party Hooks"

Cursor's documentation mentions that it can load hooks from `~/.claude/settings.json`
when the user enables "Third-party skills." This means if Irrlicht is already
configured for Claude Code, those hooks *may* also fire inside Cursor sessions.

**Risk:** The Claude Code `irrlicht-hook` binary would receive Cursor-formatted
events, which differ in field names (`conversation_id` vs `session_id`). The
hook would fail event validation (missing `session_id`) and exit with an error.

**Mitigation in `cursor-hook`:** Use a separate binary with a different hook
name to avoid conflicts. Do not rely on third-party hooks compatibility.

**Mitigation in `irrlicht-hook`:** Add graceful handling for unknown fields —
return exit code 0 (fail-open) when `session_id` is missing but
`conversation_id` is present (indicates a Cursor event misfired to the wrong
hook).

---

## UI Considerations

The SwiftUI `SessionListView` can distinguish Cursor vs Claude Code sessions
via the `source` field in the session state file. Suggested UI treatment:

- Claude Code sessions: existing glyph style, no label
- Cursor sessions: append a small `[C]` or cursor icon badge to the session row

This is a UI-layer concern and does not affect the adapter spec. The adapter
simply writes the `source: "cursor"` field; the UI decides how to render it.

---

## Testing Strategy

### Fixtures

Create `fixtures/cursor/` with sample Cursor hook payloads:

```
fixtures/cursor/
  session-start-new.json
  session-start-resume.json
  before-submit-prompt.json
  pre-tool-use.json
  post-tool-use.json
  post-tool-use-failure.json
  before-shell-execution.json
  after-shell-execution.json
  stop.json
  subagent-start.json
  subagent-stop.json
  pre-compact-auto.json
  after-agent-thought.json
  session-end-user-exit.json
  session-end-clear.json
```

### Unit Tests

- Normalization table: each Cursor event name maps to the expected Irrlicht event name
- `conversation_id` → `session_id` prefixing
- `workspace_roots[0]` → `cwd` extraction
- Speculative waiting trigger: `beforeShellExecution` and approval-prone `preToolUse`
- Session end: all reason values produce `delete_session`

### Integration Tests via `cursor-replay`

Create `cursor-replay` (parallel to `irrlicht-replay`) that pipes Cursor fixture
files to `cursor-hook` stdin and verifies the resulting session state files.

### Test Scenarios

1. New session → agent responds → user submits → agent responds → session ends
2. Shell command requiring approval → speculative waiting → approval granted
3. Subagent spawn → subagent completes → parent resumes
4. Auto-compaction during working session
5. Session end with each `reason` value

---

## File Structure

```
tools/
  cursor-hook/
    cmd/cursor-hook/
      main.go                # Entry point (mirrors irrlicht-hook/cmd/main.go)
    domain/event/
      event.go               # CursorEvent struct
    adapters/inbound/
      normalize/
        normalize.go         # CursorEvent → HookEvent translation
        normalize_test.go
    go.mod                   # Shares irrlicht/hook/* packages as local deps

specs/
  cursor-adapter.md          # This document

fixtures/cursor/
  session-start-new.json
  session-start-resume.json
  ...

tools/settings-merger/
  cursor_merger.go           # ~/.cursor/hooks.json merge support
```

---

## Open Questions

1. **`transcript_path` availability:** The research indicates Cursor provides
   `transcript_path` in the universal payload, but this should be verified
   empirically. If absent, the metrics adapter (transcript tailer) must be
   skipped for Cursor sessions.

2. **Cursor version field:** `cursor_version` in the payload could be stored
   in `SessionState` for diagnostics. Not currently in the schema.

3. **Multiple workspace roots:** When `workspace_roots` has more than one
   entry (multi-root workspace), the adapter takes `[0]`. Consider storing
   the full array or picking the most relevant root (e.g., the one containing
   the active file). Low priority.

4. **Inline completions:** `beforeTabFileRead`/`afterTabFileEdit` fire for
   every inline (Tab) completion — potentially hundreds of events per minute.
   These are deliberately excluded to avoid noise. If users want to track Tab
   completion activity, this can be gated behind a config flag.

5. **`afterAgentResponse` event:** Not included in the hook config above
   because it duplicates `stop`. If research reveals `stop` doesn't fire
   reliably, `afterAgentResponse` is the fallback to transition to `ready`.

6. **Thread-model in Cursor hooks:** Cursor may call hooks concurrently (same
   as Claude Code). The adapter inherits the atomic-write guarantee from the
   shared filesystem adapter. No additional locking needed.

---

*Research completed: 2026-03-15. Implements ir-m45. Blocks ir-2w7.*
