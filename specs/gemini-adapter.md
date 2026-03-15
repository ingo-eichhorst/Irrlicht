# Gemini CLI Adapter — Design Document

*Research output for ir-6uu. Relates to ir-0jl (implementation bead) and GH#31 Phase 2.*

---

## Summary

Google's Gemini CLI has a **hook system that is structurally identical to Claude Code's**. The same stdin/stdout JSON protocol, the same event lifecycle model, and comparable session metadata are all present. An Irrlicht adapter for Gemini CLI is entirely feasible with minimal architectural divergence from the existing `irrlicht-hook` receiver.

The primary differences are:
- Different event names (e.g. `BeforeAgent` vs `UserPromptSubmit`)
- Different config file path (`.gemini/settings.json` vs `.claude/settings.json`)
- Different transcript storage location (`~/.gemini/tmp/`)
- Model metadata arrives in a different hook event (`BeforeModel`/`AfterModel` rather than `SessionStart`)
- Token counts are per-model in the response's `usageMetadata` field

---

## 1. Gemini CLI Overview

| Property | Value |
|---|---|
| **Repo** | https://github.com/google-gemini/gemini-cli |
| **Language** | TypeScript (Node.js) |
| **License** | Apache 2.0 |
| **Released** | April 2025 |
| **Stars** | ~97,800 (March 2026) |
| **Hook docs** | `docs/hooks/reference.md`, `docs/hooks/writing-hooks.md` |
| **Hook types** | `packages/core/src/hooks/types.ts` |

---

## 2. Hook System

### 2.1 Protocol (identical to Claude Code)

- **Input**: JSON blob on `stdin` delivered to the hook process
- **Output**: JSON blob on `stdout` (or empty for pass-through)
- **Logging**: Write to `stderr` only — stdout must be pure JSON
- **Exit codes**: `0` = success/continue, `2` = block/abort, other = warning

### 2.2 Configuration

Hooks are configured in `.gemini/settings.json` at two levels:

| Level | Path | Scope |
|---|---|---|
| User (global) | `~/.gemini/settings.json` | All sessions on machine |
| Project | `.gemini/settings.json` | Sessions in that directory |

Example configuration (equivalent to what `settings-merger` would inject):

```json
{
  "hooks": {
    "SessionStart": [
      { "hooks": [{ "type": "command", "command": "/usr/local/bin/irrlicht-hook", "timeout": 5000 }] }
    ],
    "SessionEnd": [
      { "hooks": [{ "type": "command", "command": "/usr/local/bin/irrlicht-hook" }] }
    ],
    "BeforeAgent": [
      { "matcher": "*", "hooks": [{ "type": "command", "command": "/usr/local/bin/irrlicht-hook" }] }
    ],
    "AfterAgent": [
      { "matcher": "*", "hooks": [{ "type": "command", "command": "/usr/local/bin/irrlicht-hook" }] }
    ],
    "AfterModel": [
      { "matcher": "*", "hooks": [{ "type": "command", "command": "/usr/local/bin/irrlicht-hook" }] }
    ]
  }
}
```

### 2.3 Hook Events

Gemini CLI emits 11 hook events:

| Event | When It Fires | Can Block |
|---|---|---|
| `SessionStart` | Session starts, resumes, or after `/clear` | No |
| `SessionEnd` | Session exits | No |
| `BeforeAgent` | After user submits prompt, before agent loop | Yes (exit 2) |
| `AfterAgent` | When agent loop ends (back to user prompt) | No |
| `BeforeModel` | Before each LLM API request | Yes |
| `AfterModel` | After each LLM API response chunk | No |
| `BeforeTool` | Before any tool execution | Yes |
| `AfterTool` | After tool completes | No |
| `BeforeToolSelection` | Before LLM chooses tools | No |
| `Notification` | System/advisory alerts | No |
| `PreCompress` | Before context compression | No |

### 2.4 Base Input Schema (all events)

```typescript
{
  "session_id": string,         // UUID for this session (stable across all events)
  "transcript_path": string,    // Absolute path to session transcript JSON
  "cwd": string,                // Current working directory (project root)
  "hook_event_name": string,    // e.g. "BeforeAgent", "AfterModel"
  "timestamp": string           // ISO 8601
}
```

### 2.5 Event-Specific Fields

**`BeforeAgent`:**
```typescript
{
  "prompt": string              // The user's prompt text
}
```

**`BeforeModel` / `AfterModel`:**
```typescript
{
  "llm_request": {
    "model": string,            // e.g. "gemini-2.5-pro"
    "messages": [...],
    "config": { "temperature": number, ... },
    "toolConfig": { "mode": string, "allowedFunctionNames": string[] }
  },
  // AfterModel only:
  "llm_response": {
    "candidates": [{ "content": { "role": string, "parts": [...] }, "finishReason": string }],
    "usageMetadata": { "totalTokenCount": number }
  }
}
```

**`BeforeTool` / `AfterTool`:**
```typescript
{
  "tool_name": string,          // e.g. "read_file", "run_shell_command"
  "tool_input": { ... },        // Tool-specific parameters
  // AfterTool only:
  "tool_output": { ... }        // Tool execution result
}
```

**`SessionStart`:**
```typescript
{
  "source": "startup" | "resume" | "clear" | "compact"
}
```

**`SessionEnd`:**
```typescript
{
  "exit_reason": string         // "clear" | "logout" | "prompt_input_exit" | other
}
```

---

## 3. State Mapping

Direct mapping from Gemini CLI hook events to Irrlicht states:

| Irrlicht State | Gemini CLI Trigger Events | Notes |
|---|---|---|
| **`working`** | `BeforeAgent` | User submitted prompt; agent processing |
| **`working`** | `BeforeModel` | LLM API call in flight |
| **`working`** | `BeforeTool` | Tool executing |
| **`waiting`** | `AfterAgent` | Agent done, awaiting next user prompt |
| **`waiting`** | `Notification` | System advisory / permission needed |
| **`ready`** | `SessionStart` (source=startup) | Fresh session, no work yet |
| **`working`** | `SessionStart` (source=resume\|clear\|compact) | Session resumed |
| **delete session** | `SessionEnd` (non-ESC) | Session terminated normally |
| **`cancelled_by_user`** | `SessionEnd` (exit_reason=prompt_input_exit) | User pressed ESC |

### 3.1 State Machine

```
Application Launch
    ↓
  ready ←──────────────────────────────────┐
    ↓ SessionStart (startup)               │
    ↓ SessionStart (resume/clear/compact)  │
 working ←─────────────────────┐           │
    ↓ AfterAgent / Notification│           │ SessionEnd
    ↓                          │           │ (non-ESC)
 waiting ──── BeforeAgent ─────┘           │
    ↓                                      │
  ready ────────────────────────────────── ┘
    ↓ SessionEnd (prompt_input_exit)
 cancelled_by_user → (auto-deleted after 30s)
```

### 3.2 Comparison with Claude Code

| Claude Code Event | Gemini CLI Equivalent | State |
|---|---|---|
| `UserPromptSubmit` | `BeforeAgent` | → working |
| `Notification` | `Notification` | → waiting |
| `Stop` / `SubagentStop` | `AfterAgent` | → ready/waiting |
| `SessionStart` | `SessionStart` | → working/ready |
| `SessionEnd` | `SessionEnd` | → delete/cancelled |
| `PreToolUse` | `BeforeTool` | working (maintained) |
| `PostToolUse` | `AfterTool` | working (maintained) |
| `PreCompact` | `PreCompress` | working (maintained) |
| *(no equivalent)* | `BeforeModel` / `AfterModel` | working + token data |

The notable **improvement** in Gemini CLI: token counts arrive via `AfterModel` in real-time during the session, eliminating the need to tail the transcript for token estimation.

---

## 4. Session Metadata

### 4.1 Available from Hook Events

| Field | Source Event(s) | Notes |
|---|---|---|
| `session_id` | All events | UUID, stable for session lifetime |
| `transcript_path` | All events | `~/.gemini/tmp/<project_id>/chats/<session_id>.json` |
| `cwd` | All events | Project root directory |
| `model` | `AfterModel` (in `llm_response`) | e.g. `"gemini-2.5-pro"` |
| `tokens_in` (cumulative) | `AfterModel` (in `usageMetadata.totalTokenCount`) | Per-API-call; must accumulate |
| `tool_name` | `BeforeTool` / `AfterTool` | Tool being executed |
| `prompt` | `BeforeAgent` | User's prompt text |
| `source` | `SessionStart` | startup/resume/clear/compact |
| `exit_reason` | `SessionEnd` | Why session ended |

### 4.2 Environment Variables (available to hook process)

| Variable | Value |
|---|---|
| `GEMINI_PROJECT_DIR` | Absolute project root path |
| `GEMINI_SESSION_ID` | Current session UUID |
| `GEMINI_CWD` | Current working directory |
| `CLAUDE_PROJECT_DIR` | Alias for `GEMINI_PROJECT_DIR` (compatibility shim) |

### 4.3 No Direct Cost Data

Gemini CLI does **not** expose pricing information via hooks. Only token counts are available. Cost estimation would require a local price table (same approach as existing Irrlicht `price-table.json`).

---

## 5. Transcript Format

### 5.1 Storage Location

```
~/.gemini/tmp/<project_identifier>/chats/<session_id>.json
```

The `project_identifier` is derived from the project's absolute path (hashed or encoded). Checkpoints are stored at:

```
~/.gemini/tmp/<project_identifier>/checkpoints/
```

### 5.2 Format Difference

Gemini CLI transcripts are **JSON objects** (not JSONL line-by-line). The transcript is a structured conversation history, not a stream of newline-delimited events like Claude Code's `.jsonl` format.

This means the existing `transcript-tailer` (which tails the last ~64 KB of a JSONL file) **cannot be reused directly** for Gemini sessions. Options:

1. **Parse the full JSON transcript** on each read — simpler but reads the full file each time
2. **Rely on `AfterModel` hook data** for token counts instead of transcript tailing — preferred approach since token data arrives via hook
3. **Derive msgs/min from hook event timestamps** — count `BeforeAgent` events per 60s window

**Recommendation**: For Gemini adapter, use hook-delivered token data (`AfterModel.usageMetadata.totalTokenCount`) accumulated per-session, and derive msgs/min from `BeforeAgent` event timestamps. Skip transcript tailing for Gemini sessions.

---

## 6. Additional Monitoring Mechanisms

### 6.1 `--output-format stream-json` (JSONL stream)

For non-interactive (scripted) use, Gemini CLI can emit newline-delimited JSON to stdout:

```bash
gemini --output-format stream-json -p "prompt"
```

JSONL event types and their state mapping:

| JSONL Event Type | Irrlicht State |
|---|---|
| `init` | ready |
| `message` (role: user) | → working |
| `tool_use` | working |
| `message` (role: assistant, delta: true) | working |
| `tool_result` | working |
| `result` | waiting/ready |
| `error` | error |

The `result` event carries session stats:

```json
{
  "type": "result",
  "status": "success",
  "stats": {
    "total_tokens": 12450,
    "input_tokens": 10000,
    "output_tokens": 2450,
    "cached": 500,
    "duration_ms": 8300,
    "tool_calls": 3,
    "models": {
      "gemini-2.5-pro": { "total_tokens": 12450, "input_tokens": 10000, "output_tokens": 2450 }
    }
  }
}
```

This is useful for **batch/scripted** Gemini usage monitoring but not for interactive sessions.

### 6.2 OpenTelemetry File Export

Setting `GEMINI_TELEMETRY_OUTFILE=/path/to/telemetry.jsonl` causes Gemini CLI to write structured OTLP telemetry including:
- `gemini_cli.hook_call` events
- `gemini_cli.tool_call` events
- `gemini_cli.api_request` / `gemini_cli.api_response` events with full metadata

This is an **alternative** monitoring channel but adds complexity. The hook system is sufficient and preferred.

### 6.3 DevTools WebSocket (Port 25417)

When `GEMINI_CLI_ACTIVITY_LOG_TARGET` is set, Gemini CLI starts an internal WebSocket at `ws://127.0.0.1:25417/ws` broadcasting HTTP logs and activity events. This is an undocumented mechanism — fragile and not recommended for production adapter use.

---

## 7. Implementation Plan

### 7.1 What Reuses from `irrlicht-hook`

The existing Go hook receiver is highly reusable:
- Stdin JSON parsing infrastructure
- Atomic session file writes (`~/Library/Application Support/Irrlicht/instances/`)
- Orphan PID detection
- Path sanitization and security validation
- Logging infrastructure
- Session state file schema (`version`, `session_id`, `state`, `cwd`, etc.)

### 7.2 What Needs to Change

| Component | Change Required |
|---|---|
| **`domain/event/event.go`** | Add Gemini event names to `validEventNames`; add `LLMRequest`/`LLMResponse` fields |
| **`application/services/event_service.go`** | Add Gemini state transition cases (`BeforeAgent`→working, `AfterAgent`→waiting, `AfterModel`→accumulate tokens) |
| **`adapters/outbound/metrics/`** | Add alternative token source from `AfterModel` hook data (bypass transcript tailing for Gemini sessions) |
| **`tools/settings-merger/`** | New target: `~/.gemini/settings.json` with Gemini hook format |
| **`cmd/irrlicht-hook/`** | Add CLI flag or auto-detect to select Claude vs Gemini event schema |

### 7.3 Event Name Collision Risk

One important consideration: the hook receiver currently validates against a whitelist of Claude Code event names. Gemini uses completely different names. Two options:

**Option A: Single binary, auto-detect schema** — inspect `hook_event_name` at runtime; if it matches Gemini names, apply Gemini logic. Zero config for users.

**Option B: Separate binary `gemini-irrlicht-hook`** — simpler validation, cleaner separation of concerns. Two binaries to distribute.

**Recommendation**: Option A (auto-detect), since the event name sets are disjoint (no overlap between Claude and Gemini event names). The binary auto-selects the correct state machine based on which event it receives.

### 7.4 Speculative Waiting Equivalent

Claude Code's speculative waiting (spawning a background process on `PreToolUse` to detect pending approvals) does not have a direct Gemini equivalent. Gemini CLI's `BeforeTool` is blockable, so the tool either runs or is blocked immediately — there is no 6-second Notification delay as with Claude Code. The speculative waiting mechanism is **not needed** for Gemini.

### 7.5 Model Capacity Table

Gemini model context windows (for `context_used_%` calculation):

| Model | Context Window |
|---|---|
| `gemini-2.5-pro` | 1,048,576 tokens (1M) |
| `gemini-2.5-flash` | 1,048,576 tokens (1M) |
| `gemini-2.0-flash` | 1,048,576 tokens (1M) |
| `gemini-1.5-pro` | 2,097,152 tokens (2M) |
| `gemini-1.5-flash` | 1,048,576 tokens (1M) |

These should be added to `model-capacity`'s capacity table.

---

## 8. Settings Merger for Gemini

The existing `settings-merger` targets `~/.claude/settings.json`. A Gemini equivalent must target `~/.gemini/settings.json` with this structure:

```json
{
  "hooks": {
    "SessionStart": [
      { "hooks": [{ "type": "command", "command": "/usr/local/bin/irrlicht-hook", "timeout": 5000 }] }
    ],
    "SessionEnd": [
      { "hooks": [{ "type": "command", "command": "/usr/local/bin/irrlicht-hook" }] }
    ],
    "BeforeAgent": [
      { "matcher": "*", "hooks": [{ "type": "command", "command": "/usr/local/bin/irrlicht-hook" }] }
    ],
    "AfterAgent": [
      { "matcher": "*", "hooks": [{ "type": "command", "command": "/usr/local/bin/irrlicht-hook" }] }
    ],
    "AfterModel": [
      { "matcher": "*", "hooks": [{ "type": "command", "command": "/usr/local/bin/irrlicht-hook" }] }
    ]
  }
}
```

**Minimum required hooks** for state tracking: `SessionStart`, `SessionEnd`, `BeforeAgent`, `AfterAgent`.

**Optional but valuable**: `AfterModel` (for real-time token counts).

---

## 9. Key Files in Gemini CLI Source

For implementors needing to reference Gemini CLI internals:

| File | Purpose |
|---|---|
| `packages/core/src/hooks/types.ts` | Complete TypeScript type definitions for all hook inputs/outputs |
| `packages/core/src/output/types.ts` | `OutputFormat` enum, all JSONL stream event types |
| `packages/core/src/output/stream-json-formatter.ts` | JSONL stream event emitter |
| `packages/core/src/telemetry/activity-types.ts` | `ActivityType` enum |
| `docs/hooks/reference.md` | Complete hook I/O specification |
| `docs/hooks/writing-hooks.md` | Hook implementation guide with examples |
| `integration-tests/hooks-system.test.ts` | Integration test patterns |

---

## 10. Open Questions

1. **Single binary vs two binaries?** Auto-detect schema (Option A) is cleaner for users but mixes two event vocabularies in one binary. A `gemini-irrlicht-hook` binary would be simpler to test in isolation.

2. **Token accumulation strategy?** `AfterModel.usageMetadata.totalTokenCount` appears to be cumulative per-call, not per-session. Need to verify whether this resets between `BeforeAgent`/`AfterAgent` cycles or accumulates across the entire session.

3. **Transcript path stability?** The `transcript_path` field delivered via hooks may change across session resumes or `/clear` operations. Need to verify whether session_id is stable across a `/clear` (Claude Code: `clear` creates a new session_id; Gemini: unknown).

4. **`source` field on `SessionStart`?** Gemini's `SessionStart` includes a `source` field (startup/resume/clear/compact) analogous to Claude Code. Confirm whether `source=resume` should map to `working` or `ready` (consistent with Claude Code behavior: resume → ready).

5. **Gemini Flash Thinking model?** `gemini-2.5-flash-thinking` has extended thinking tokens not counted in `usageMetadata.totalTokenCount`. Token display may be incomplete for thinking-mode responses.

---

*Research conducted: 2026-03-15. Gemini CLI version at time of research: latest main branch (~April 2025 initial release + updates through March 2026).*
