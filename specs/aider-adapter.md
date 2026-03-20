# Aider Adapter – Design Specification

*Research output for ir-xvx · Related: GH#31 Phase 2*

---

## 1. Executive Summary

Aider (the AI pair programmer) does **not** have a hook system equivalent to
Claude Code's stdin-delivered JSON events. However, it exposes two usable
signals that together provide sufficient state information for Irrlicht:

1. **Analytics log** (`AIDER_ANALYTICS_LOG`) — JSONL file, append-only, written
   during the session. Best available structured output.
2. **Notifications command** (`AIDER_NOTIFICATIONS_COMMAND`) — shell command
   executed whenever aider transitions to waiting-for-user-input state.

The recommended approach is a **hybrid**: tail the analytics log for working/done
transitions, use the notifications command as the definitive "now waiting" signal.

---

## 2. Aider's Observable Signals

### 2.1 Analytics Log (JSONL)

**Activation:**
```bash
AIDER_ANALYTICS_LOG=/path/to/aider-$PID.jsonl \
AIDER_ANALYTICS=false \   # disable PostHog upload; keep local log
aider ...
```

Each line is a JSON object:
```json
{"event": "launched", "properties": {}, "user_id": "abc123", "time": 1754760000}
{"event": "cli session", "properties": {"main_model": "claude-3-5-sonnet-20241022", ...}, "user_id": "abc123", "time": 1754760001}
{"event": "message_send_starting", "properties": {}, "user_id": "abc123", "time": 1754760010}
{"event": "message_send", "properties": {"main_model": "...", "edit_format": "diff-fenced", "prompt_tokens": 11364, "completion_tokens": 7, "total_tokens": 11371, "cost": 0.00011644}, "user_id": "abc123", "time": 1754760015}
{"event": "command_add", "properties": {"num_files": 2}, "user_id": "abc123", "time": 1754760020}
{"event": "exit", "properties": {"reason": "Completed main CLI coder.run"}, "user_id": "abc123", "time": 1754760099}
```

**Full event inventory:**

| Event | Meaning | Properties |
|-------|---------|------------|
| `launched` | Process started | (none) |
| `cli session` | Interactive REPL started | `main_model`, `edit_format`, `os`, `python_version` |
| `message_send_starting` | LLM call beginning → **WORKING** | (none) |
| `message_send` | LLM call completed | `main_model`, `edit_format`, `prompt_tokens`, `completion_tokens`, `total_tokens`, `cost` |
| `command_<name>` | User ran `/add`, `/drop`, `/undo`, etc. | varies |
| `ai-comments execute` | `--watch-files` triggered a prompt | (none) |
| `ai-comments file-add` | `--watch-files` added a file | (none) |
| `repo` | Git repo found | `num_files` |
| `no-repo` | No git repo | (none) |
| `auto_commits` | Config info | `auto_commits` |
| `model warning` | Unrecognized model | `model` |
| `exit` | Session ending | `reason` (string) |

**Key gap:** There is **no explicit "now waiting for input" event** in the analytics
log. The transition from `message_send` back to waiting is silent. The adapter
must infer it (see §4 State Machine).

### 2.2 Notifications Command

**Activation:**
```bash
AIDER_NOTIFICATIONS=true \
AIDER_NOTIFICATIONS_COMMAND="/path/to/irrlicht-aider-notify --session $AIDER_SESSION_ID" \
aider ...
```

Aider calls `ring_bell()` exactly once when transitioning to waiting-for-input state
after an LLM response. This command receives **no arguments** by default — the adapter
script must be pre-configured with the session ID (via env var injection).

This is the **most reliable "waiting" signal** available. It fires after every LLM
completion and after `/run` commands complete.

**Limitation:** Does not fire on initial startup (before the first LLM call). The
initial waiting state must be inferred from process existence after the `cli session`
event.

### 2.3 Process-Level Signals

- **No PID file** — aider does not write one
- **No lock file** — no advisory lock
- **No Unix domain socket** — no IPC channel
- **Chat history file** (`.aider.chat.history.md`) — append-only markdown; mtime
  changes on every user message and every LLM response, but format is not
  machine-parseable for state detection

Process liveness: `kill(pid, 0)` works for orphan detection (same as Claude Code).

---

## 3. Session Identity

**Critical gap:** Aider has **no per-session UUID**. The `user_id` in analytics events
is a persistent identifier across all sessions (stored in `~/.aider/analytics.token`).

The adapter must **synthesize** a session ID:

```
session_id = sha256(analytics_log_path + launch_timestamp)[:12]
```

Or more simply: use the analytics log filename itself as the session key (since it
embeds the PID when named `aider-$PID.jsonl`).

**Implication:** The Irrlicht hook adapter must be configured with the session ID
before aider starts, and inject it via `AIDER_NOTIFICATIONS_COMMAND`.

---

## 4. State Machine Mapping

| Irrlicht State | Trigger | Source |
|----------------|---------|--------|
| `working` | `message_send_starting` event | Analytics log |
| `working` | `ai-comments execute` event | Analytics log |
| `waiting` | Notifications command fired | AIDER_NOTIFICATIONS_COMMAND |
| `waiting` | 200ms after `message_send` with no new `message_send_starting` | Analytics log + timer |
| `ready` | `exit` event | Analytics log |
| `ready` | Process no longer alive (orphan detection) | kill(pid, 0) |

**Initial state:** After `cli session` event and before first `message_send_starting`,
aider is in `waiting` state (showing the prompt). The adapter transitions to `waiting`
on `cli session`.

**State diagram:**
```
[launched] → waiting (after cli session)
waiting → working  (on message_send_starting)
working → waiting  (on notifications command OR 200ms timeout after message_send)
waiting → ready    (on exit event or process death)
working → ready    (on exit event during LLM call — rare, e.g. Ctrl-C)
```

---

## 5. Metrics Availability

| Metric | Claude Code | Aider |
|--------|------------|-------|
| Token count | Transcript JSONL | `message_send` event (`prompt_tokens`, `completion_tokens`) |
| Cost | Transcript JSONL | `message_send` event (`cost`) |
| Model | `SessionStart` hook | `cli session` event (`main_model`) |
| CWD | `SessionStart` hook | Must be captured at launch time |
| Msgs/min | Transcript JSONL | Count `message_send` events in rolling 60s window |
| Context % | Model capacity table + tokens_in | Same: tokens_in ÷ model_capacity |
| Git branch | `transcript_path` heuristic | Detect from CWD at launch |

Aider metrics are **richer per-event** than Claude Code (cost and token counts come
directly in the analytics event vs. requiring transcript parsing), but require the
adapter to accumulate them from the analytics log rather than reading a transcript.

---

## 6. Integration Architecture

### 6.1 Recommended: Wrapper Script + Analytics Tail

The cleanest integration requires aider to be launched through a thin wrapper:

```bash
#!/bin/bash
# irrlicht-aider — wrapper that instruments aider for Irrlicht monitoring

SESSION_ID="aider-$(date +%s)-$$"
LOG_FILE="/tmp/irrlicht/aider/${SESSION_ID}.jsonl"
mkdir -p "$(dirname "$LOG_FILE")"

# Write synthetic SessionStart event for irrlicht-hook
echo "{\"hook_event_name\":\"SessionStart\",\"session_id\":\"${SESSION_ID}\",\"timestamp\":\"$(date -u +%Y-%m-%dT%H:%M:%SZ)\",\"cwd\":\"$(pwd)\",\"model\":\"\"}" | irrlicht-hook

# Launch aider with instrumentation
AIDER_ANALYTICS_LOG="$LOG_FILE" \
AIDER_ANALYTICS=false \
AIDER_NOTIFICATIONS=true \
AIDER_NOTIFICATIONS_COMMAND="irrlicht-aider-notify '${SESSION_ID}'" \
aider "$@"
EXIT_CODE=$?

# Write synthetic SessionEnd event
echo "{\"hook_event_name\":\"SessionEnd\",\"session_id\":\"${SESSION_ID}\",\"timestamp\":\"$(date -u +%Y-%m-%dT%H:%M:%SZ)\",\"reason\":\"exit\"}" | irrlicht-hook

exit $EXIT_CODE
```

### 6.2 Analytics Log Tailer Component

A new Go component `irrlicht-aider-tail` (or extension of `transcript-tailer`):

```
aider --analytics-log /tmp/irrlicht/aider/$SESSION_ID.jsonl
                              ↓
irrlicht-aider-tail (Go)   ← tails JSONL
   - maps events → HookEvents
   - sends to irrlicht-hook via stdin OR writes directly to instances/
                              ↓
irrlicht-hook / instances/$SESSION_ID.json
                              ↓
frontend/macos menu bar
```

### 6.3 Notifications Shim

```bash
#!/bin/bash
# irrlicht-aider-notify — called by aider on waiting-for-input
SESSION_ID="$1"
echo "{\"hook_event_name\":\"Notification\",\"session_id\":\"${SESSION_ID}\",\"timestamp\":\"$(date -u +%Y-%m-%dT%H:%M:%SZ)\"}" | irrlicht-hook
```

This maps cleanly to Irrlicht's existing `Notification` → `waiting` transition.

---

## 7. Alternative: Direct Process Monitoring (No Wrapper)

If requiring a wrapper script is unacceptable, the SwiftUI app could monitor aider
sessions without a hook receiver:

1. **Process detection:** Scan `ps` or use `NSWorkspace`/`Process` APIs for `aider`
   processes periodically (1-2s).
2. **Log discovery:** For each aider process, check if `AIDER_ANALYTICS_LOG` is set
   in its environment (read from `/proc/<pid>/environ` or `ps -E`).
3. **State inference:** Tail discovered log files. Use heuristics (last event
   timestamp + 5s idle → waiting) when notifications command is not configured.

**Tradeoff:** No wrapper needed, but state accuracy degrades (no reliable "waiting"
signal without the notifications command).

---

## 8. Gaps vs. Claude Code Integration

| Feature | Claude Code | Aider | Mitigation |
|---------|------------|-------|------------|
| Native session ID | ✓ per-session UUID | ✗ persistent user_id | Synthesize from PID+timestamp |
| UserPromptSubmit event | ✓ | ✗ | Infer from `message_send_starting` timing |
| PreToolUse / PostToolUse | ✓ | ✗ | Not applicable (Aider edits files directly) |
| Explicit "waiting" signal | ✓ Notification hook | Partial — notifications command | Map `AIDER_NOTIFICATIONS_COMMAND` → Notification |
| Transcript path | ✓ in every event | ✗ | Expose via wrapper env or CWD heuristic |
| Speculative waiting | ✓ 2s pre-tool timeout | N/A | Not needed — no tool approval workflow |
| Subagent detection | ✓ parent_session_id | ✗ | Not applicable |
| Zero-config | ✓ settings-merger | Requires wrapper or env setup | Installer can configure shell alias |

---

## 9. Implementation Recommendation

**Phase 1 (MVP):** Wrapper script approach.
- Ship `irrlicht-aider` wrapper in the installer alongside `irrlicht-hook`
- Ship `irrlicht-aider-notify` shim
- Extend installer to add shell alias: `alias aider='irrlicht-aider'`
- State machine is fully functional with analytics log + notifications command
- Metrics: model, token counts, cost, msgs/min all available

**Phase 2 (Ambient detection):** Process scanner.
- SwiftUI app scans for `aider` processes without the wrapper
- Discovers analytics logs from process environment
- Degraded accuracy (no "waiting" signal) but zero-config for users who don't use
  the wrapper

**What to build in ir-hsp (Implement Aider adapter):**
1. `irrlicht-aider-tail`: Go binary that tails an aider analytics JSONL and converts
   events to `HookEvent` structs, writing to `instances/` directly
2. `irrlicht-aider-notify`: Shell script shim for the notifications command
3. `irrlicht-aider`: Wrapper shell script
4. Installer extension: shell alias setup

---

## 10. References

- Aider source: `aider/analytics.py` — full event inventory and PostHog client
- Aider source: `aider/io.py` — `ring_bell()`, `llm_started()`, `get_input()` for state machine
- Aider source: `aider/main.py` — CLI entry, env var prefix `AIDER_`
- Aider docs: https://aider.chat/docs/config/options.html#notifications
- Irrlicht hook event model: `tools/irrlicht-hook/domain/event/event.go`
- Related bead: ir-hsp (Implement Aider adapter for irrlicht-hook)
