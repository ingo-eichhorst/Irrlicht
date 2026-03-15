# Irrlicht – GitHub Copilot Adapter Design

*Research findings and adapter design for monitoring GitHub Copilot CLI sessions in the Irrlicht menu bar.*

---

## Research Summary

GitHub Copilot CLI (the `copilot` binary, v1.0+) ships a **hook system that mirrors Claude Code's own hooks** in structure and intent: shell commands invoked with event payloads on stdin as newline-delimited JSON. This makes it a natural second adapter for Irrlicht's existing architecture.

The VS Code/JetBrains IDE extensions have no external IPC surface. The cloud-based GitHub Copilot coding agent (GitHub Actions) is not a local process. This document focuses exclusively on **Copilot CLI as the integration target**.

---

## Copilot CLI Hook System

### Hook Configuration

Hooks are JSON files placed in either:
- `~/.copilot/hooks/<name>.json` — personal hooks (all sessions)
- `.github/hooks/<name>.json` — repo-level hooks (sessions in that repo)

**Configuration format:**
```json
{
  "version": 1,
  "hooks": {
    "sessionStart": [
      {
        "type": "command",
        "bash": "/usr/local/bin/irrlicht-hook-copilot",
        "cwd": ".",
        "timeoutSec": 5
      }
    ],
    "preToolUse": [...],
    "postToolUse": [...],
    "sessionEnd": [...],
    "userPromptSubmitted": [...],
    "agentStop": [...],
    "subagentStop": [...],
    "errorOccurred": [...]
  }
}
```

### Eight Hook Events

| Hook Event | Trigger | Can Block Execution |
|---|---|---|
| `sessionStart` | Session begins or resumes | No (can inject context) |
| `sessionEnd` | Session ends | No |
| `userPromptSubmitted` | User submits prompt | No |
| `preToolUse` | Before any tool runs | **Yes** — return `{"permissionDecision": "allow|deny|ask"}` |
| `postToolUse` | After tool completes | No |
| `agentStop` | Main agent finishes response turn | No |
| `subagentStop` | Subagent task completes | No |
| `errorOccurred` | Error during execution | No |

### Stdin Payload Per Event

```json
// sessionStart
{ "timestamp": 1704614400000, "cwd": "/path/to/project",
  "source": "new|resume|startup", "initialPrompt": "string" }

// sessionEnd
{ "timestamp": 1704618000000, "cwd": "/path/to/project",
  "reason": "complete|error|abort|timeout|user_exit" }

// userPromptSubmitted
{ "timestamp": 1704614500000, "cwd": "/path/to/project", "prompt": "string" }

// preToolUse
{ "timestamp": 1704614600000, "cwd": "/path/to/project",
  "toolName": "bash|edit|view|create", "toolArgs": "<JSON string>" }

// postToolUse
{ "timestamp": 1704614700000, "cwd": "/path/to/project",
  "toolName": "string", "toolArgs": "<JSON string>",
  "toolResult": { "resultType": "success|failure|denied", "textResultForLlm": "string" } }

// errorOccurred
{ "timestamp": 1704614800000, "cwd": "/path/to/project",
  "error": { "message": "string", "name": "string", "stack": "string" } }

// agentStop / subagentStop: same shape as sessionEnd minus "reason"
```

### Critical Difference from Claude Code Hooks

**Copilot shell hooks do not include `sessionId` in the payload.** The `sessionId` is available only inside the Copilot SDK's `HookInvocation` struct — it is not passed to external shell command hooks. This is the most important structural difference from Claude Code.

The official Copilot reference hook examples (`awesome-copilot/hooks/`) use `cwd` as the session correlation key. This is the correct approach for shell-hook-based adapters.

---

## Session Identity: `cwd` as Proxy Key

Because Copilot CLI shell hooks emit no `sessionId`, session identity must be derived from `cwd`:

```
session_key = sha256(cwd)[0:16]   // 16-char hex, same as Claude Code's short ID style
```

**State file:** `~/Library/Application Support/Irrlicht/instances/copilot-<session_key>.json`

**Caveats:**
- One session per working directory at a time (standard Copilot usage pattern).
- If a user starts two Copilot sessions in the same directory, state will be clobbered. Copilot CLI does not support this use case.
- On `sessionStart` with `source: "resume"`, the previous state file is updated rather than replaced.

---

## State Machine

Mapping from Copilot hook events to Irrlicht states:

```
sessionStart (source: new|startup)  → working
sessionStart (source: resume)       → working
userPromptSubmitted                 → working
preToolUse (approval tools)         → [speculative waiting after 2s if no postToolUse]
preToolUse (all)                    → working
postToolUse                         → working
agentStop                           → ready
subagentStop                        → ready
errorOccurred                       → ready (with error flag)
sessionEnd (reason: user_exit)      → cancelled_by_user  (auto-expire 30s)
sessionEnd (reason: complete|abort) → delete session file
sessionEnd (reason: error|timeout)  → delete session file
```

**Approval-prone tools** (same speculative waiting logic as Claude Code adapter):
`bash`, `edit`, `create` — if no `postToolUse` within 2s, transition to `waiting` speculatively.

**Permission requested** detection: Copilot does not emit a Notification-equivalent hook for the "waiting for permission" state. The speculative `preToolUse` path above provides a workaround.

---

## Session State File Format

The Copilot adapter uses the same `SessionState` format as the Claude Code adapter, with adapter-specific fields:

```json
{
  "version": 1,
  "session_id": "copilot-a3f9b2c1d4e5f6a7",
  "adapter": "copilot",
  "state": "working|waiting|ready|cancelled_by_user",
  "cwd": "/path/to/project",
  "project_name": "myproject",
  "first_seen": 1725560000,
  "updated_at": 1725560123,
  "confidence": "high",
  "event_count": 4,
  "last_event": "preToolUse",
  "pid": 12345
}
```

**New fields vs. Claude Code sessions:**
- `adapter: "copilot"` — distinguishes source in the UI
- No `transcript_path` (Copilot does not expose transcript location to hooks)
- No `model` (not included in hook payloads)
- No `git_branch` (not in hook payloads; could be derived via `git -C cwd branch`)

---

## Implementation Approach

### Option A: Shell Hook Binary (Recommended for MVP)

Build a second Go binary `irrlicht-hook-copilot` that:
1. Reads one line of JSON from stdin (the hook payload)
2. Extracts `cwd` and derives `session_key`
3. Reads hook event name from `COPILOT_HOOK_EVENT` env var (or hardcodes separate binaries per event — preference TBD based on Copilot's actual delivery mechanism)
4. Applies the state machine
5. Writes `~/Library/Application Support/Irrlicht/instances/copilot-<key>.json`
6. Exits in ≤2s (synchronous hooks block Copilot execution)

**Event name detection:** Copilot's hook config invokes the same binary multiple times via separate stanzas. The binary must know which event triggered it. Two approaches:
- Separate tiny wrapper per event (six wrappers calling the same core)
- Single binary with `--event <name>` flag set in the hook config: `bash: "irrlicht-hook-copilot --event sessionStart"`

The `--event` flag approach is simpler.

**Installer integration:**
- On install, write `~/.copilot/hooks/irrlicht.json` (same merge/backup/rollback pattern as `settings-merger`)
- Kill-switch: `IRRLICHT_DISABLED=1` check at top of binary

### Option B: Copilot SDK Go Process (Richer, for v2)

Build a long-running Go daemon `irrlicht-copilot-daemon` using `github.com/github/copilot-sdk/go`:
1. Connect to `copilot --headless` server via TCP or spawn the SDK with a new session
2. Subscribe to streaming events: `assistant.turn_start`, `session.idle`, `permission.requested`, `session.shutdown`
3. Use `client.GetForegroundSessionID(ctx)` to track the active session
4. Write Irrlicht state files from event stream
5. Access to `sessionId` (solves the cwd proxy problem)
6. OpenTelemetry output available (v1.0.4+)

**Blockers:** SDK is in Technical Preview. Requires `copilot --headless` mode or per-session SDK spawn. More complex architecture.

**Recommendation:** Implement Option A for the current milestone. Option B is the upgrade path once the SDK stabilizes.

---

## Key Similarities to Claude Code Adapter

| Aspect | Claude Code | Copilot CLI |
|---|---|---|
| Hook invocation | stdin JSON, shell command | stdin JSON, shell command |
| Hook config | `~/.claude/settings.json` | `~/.copilot/hooks/<name>.json` |
| Session lifecycle events | SessionStart/Stop/End | sessionStart/agentStop/sessionEnd |
| Tool lifecycle events | PreToolUse/PostToolUse | preToolUse/postToolUse |
| Blocking hook | PreToolUse, UserPromptSubmit, Stop | preToolUse |
| State file location | `…/instances/<session_id>.json` | `…/instances/copilot-<cwd_hash>.json` |
| Kill-switch | `IRRLICHT_DISABLED=1` | same |

## Key Differences from Claude Code Adapter

| Aspect | Claude Code | Copilot CLI |
|---|---|---|
| Session ID in hook | Yes (`session_id` field) | **No** — not in shell hooks |
| Session correlation key | `session_id` | `sha256(cwd)[0:16]` |
| Waiting-state hook | `Notification` event | No equivalent — speculative only |
| Transcript path | Yes (in hook payload) | Not exposed to shell hooks |
| Model name | Yes (in hook payload) | Not exposed to shell hooks |
| Subagent support | `SubagentStop` with `parent_session_id` | `subagentStop` (no parent link in hooks) |
| Process PID | `os.Getppid()` | `os.Getppid()` (same pattern) |

---

## Installer Changes Required

### New: Copilot Hook Configuration Merger

Analogous to `settings-merger` for Claude Code, a `copilot-hooks-merger` tool:
1. Reads/creates `~/.copilot/hooks/irrlicht.json`
2. Writes the 8-event hook stanzas pointing to `irrlicht-hook-copilot`
3. Backs up the existing file before modifying
4. Is idempotent (safe to run multiple times)
5. Has `--action remove` to cleanly uninstall

The merger must handle the case where `~/.copilot/hooks/` does not exist (create it).

### Settings Merger Pattern (Reference)

The existing `settings-merger` pattern (backup → merge → verify) can be adapted directly:
- Replace JSON path `$.hooks.*` in `settings.json` with `$.hooks.{event}[].bash` entries in `irrlicht.json`
- Same dry-run, idempotency, and rollback guarantees

---

## UI Differentiation

The Irrlicht SwiftUI app should distinguish Copilot sessions from Claude Code sessions:
- Session file `adapter: "copilot"` field drives glyph annotation or color
- Proposed: dim purple/blue glyph for Copilot (vs. orange for Claude Code)
- Dropdown header: "GitHub Copilot" vs. "Claude Code"
- No transcript path → "Open transcript" action hidden for Copilot sessions
- No model name → omit model display for Copilot sessions

---

## Blockers and Open Questions

| Question | Impact | Suggested Resolution |
|---|---|---|
| Does Copilot pass event name to hook binary via env var or only via config stanza? | Affects binary design | Test with actual `copilot` binary; use `--event` flag as fallback |
| Does `~/.copilot/hooks/` exist by default or must it be created? | Installer requirement | Create on install if absent |
| Is the hook timeout 30s (docs) or 5s (examples)? | Latency budget | Use 5s as safe upper bound |
| Can `sessionId` be obtained via `COPILOT_SESSION_ID` env var (like Claude's `CLAUDE_SESSION_ID`)? | Session identity | Check Copilot binary source / docs; would eliminate cwd-hash approach |
| Does Copilot CLI support headless mode on all platforms / versions? | Option B feasibility | Verify `copilot --headless` is stable in v1.0.5+ |

---

## Implementation Plan

**Phase 1 (MVP):**
1. Add Copilot hook fixtures to `fixtures/copilot-*.json`
2. Implement `irrlicht-hook-copilot` binary (Go, same Hexagonal Architecture as `irrlicht-hook`)
3. Implement `copilot-hooks-merger` tool
4. Wire `adapter: "copilot"` into `SessionState` model
5. Add Copilot session differentiation to SwiftUI app

**Phase 2 (enrichment):**
1. Derive `git_branch` from `cwd` (same `git` adapter used by existing hook)
2. Investigate `COPILOT_SESSION_ID` env var (if exists, use it to replace cwd-hash)
3. Add `copilot --headless` SDK-based daemon for richer state (Option B)

**Out of scope:**
- IDE extension integration (no viable IPC surface)
- Cloud coding agent integration (not a local process)
- Windows/Linux support (macOS-only per existing scope)

---

## References

- GitHub Copilot CLI hooks docs: [docs.github.com/en/copilot/customizing-copilot/copilot-hooks](https://docs.github.com/en/copilot/customizing-copilot/copilot-hooks)
- Official hook examples: [github.com/github/awesome-copilot/hooks](https://github.com/github/awesome-copilot/tree/main/hooks)
- Copilot SDK: [github.com/github/copilot-sdk](https://github.com/github/copilot-sdk)
- Existing Claude Code adapter: `tools/irrlicht-hook/`
- Existing settings merger: `tools/settings-merger/`

*Research conducted 2026-03-15. Copilot CLI version referenced: 1.0.5.*
