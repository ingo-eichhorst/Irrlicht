# Irrlicht – design.md

*A razor‑sharp outline of the solution and architecture*

## One‑liner

**Irrlicht** is a local‑first macOS **menu‑bar monitor** for **Claude Code**. It shows a "battery" of all running sessions (● working, ◔ waiting, ✓ finished) and reveals per‑session details on click—installed in **one step** with **no manual config**.

## What matters (UX)

* **Menu‑bar battery:** One glyph per session; scales 1→N automatically; finished sessions auto‑expire after TTL (default 15 min).
* **Dropdown details:** `shortId · state · model`, **msgs/min**, **context used %** (tokens\_in ÷ capacity), **elapsed**; actions: *Open transcript*, *Tail*, *Open cwd in Terminal/VS Code*.
* **Zero‑setup:** Signed `.pkg` installs app + hook receiver and safely merges hooks into `~/.claude/settings.json` (backup & rollback).

## Architecture (minimal, event‑driven)

```
Claude Code (N sessions)
   └─ Hooks ──▶ /usr/local/bin/irrlicht-hook (stdin JSON)
                  └─ writes: ~/Library/Application Support/Irrlicht/instances/<session_id>.json
                  └─ records PID (os.Getppid()) in session file for orphan detection
                  └─ (optional) logs: .../logs/events.log

Irrlicht.app (SwiftUI, MenuBarExtra)
   ├─ File watcher: .../instances/*.json  → render glyphs & dropdown
   ├─ Process monitor: kill(pid, 0) liveness check on every refresh → deletes orphaned sessions
   ├─ Transcript tailer: tail last ~64 KB of transcript.jsonl → msgs/min, tokens_in
   ├─ Model table: context capacity per model → context_used_%
   └─ Actions: open/tail transcript, open cwd in Terminal/VS Code

Fallback (pre‑hook sessions)
   └─ Scanner of ~/.claude*/projects/**/*.jsonl → infer working/waiting/finished until first hook arrives

Installer
   └─ .pkg → app, CLI, LaunchAgent; hook merge into ~/.claude/settings.json (idempotent + backup)
```

## Components

**1) Hook Receiver — `irrlicht-hook`**

* Input: Claude Code **Hook** JSON on stdin; fields used: `hook_event_name`, `session_id`, `transcript_path`, `cwd`, `model`.
* State map: `UserPromptSubmit→working`, `Notification→waiting`, `Stop|SubagentStop→ready`, `SessionEnd(non-ESC)→delete`, `SessionEnd(prompt_input_exit)→cancelled_by_user`, `SessionStart→working`.
* Output: Atomic upsert of `instances/<session_id>.json` (temp file + rename). Size cap 512 KB. Path sanitization.
* Speculative waiting: On `PreToolUse` for approval-prone tools (Bash/Write/Edit/MultiEdit), spawns a detached background process. After 2s with no PostToolUse, transitions to `waiting` speculatively — eliminates the ~6s Notification delay visible to users.
* Orphan cleanup: Records `pid` (via `os.Getppid()`) in every session file. On each hook invocation, scans all sessions: deletes if PID is no longer alive. Legacy sessions without PID are deleted after 1h TTL.

**2) State Store (files)**

* One JSON per session; no DB. Single source of truth for UI.
* Expire finished sessions after TTL; keep `first_seen`, `updated_at`.

**3) Transcript Tailer**

* Tail last \~64 KB of `transcript_path` (JSONL) to compute **messages/min** (60 s window) and read/estimate **tokens\_in**.
* Model→capacity table (editable JSON) to compute \*\*context\_used\_%\`. Optional local price table for cost (off by default).

**4) Process Monitor**

* Both the hook receiver and the SwiftUI app run independent liveness checks using `kill(pid, 0)` (signal 0 — safe, no signal delivered).
* Hook receiver: calls `cleanupOrphanedSessions()` on every invocation; deletes session files for dead PIDs.
* SwiftUI app: runs liveness check in `loadExistingSessions()` (triggered every 1s and on file system events); deletes session files for dead PIDs.
* Legacy sessions (no `pid` field): cleaned up after 1h TTL if in `working` or `waiting` state.

**5) Menu‑Bar App (SwiftUI)**

* `MenuBarExtra` + filesystem watcher (debounce 200 ms). Refresh loop ≤2 s.
* Header renders glyphs; dropdown groups sessions with metrics and actions.

**6) Installer / Uninstaller**

* `.pkg` places: `Irrlicht.app`, `irrlicht-hook`, app‑support dirs, LaunchAgent.
* Post‑install merges hooks into `~/.claude/settings.json` (JSON‑aware, idempotent). Backup & rollback.
* Uninstaller removes artefacts and restores settings from backup (or prunes our entries only).

**7) Fallback Scanner (heuristic)**

* Before hooks are active for a session, infer:

  * `working` if transcript grew in last 5 s; `waiting` if idle ≥60 s and last event suggests user input; else `finished` after TTL.
* Mark `confidence: "low"` until a hook arrives.

## Data Contracts

**instances/\<session\_id>.json**

```json
{
  "version": 1,
  "session_id": "abc123",
  "state": "working|waiting|ready|cancelled_by_user",
  "model": "claude-3.7-sonnet",
  "cwd": "/path/to/project",
  "transcript_path": "/abs/path/transcript.jsonl",
  "pid": 12345,
  "first_seen": 1725560000,
  "updated_at": 1725560123,
  "confidence": "high|low",
  "metrics": {
    "msgs_per_min": 1.4,
    "tokens_in": 92431,
    "context_capacity": 200000,
    "context_used_pct": 46.2,
    "elapsed_s": 840
  }
}
```

## Performance & Safety Guards

* **Latency:** Hook→UI ≤ **2 s** (p95).
* **Resource:** CPU ≤ **5%** (p95) for ≤12 sessions; RSS ≤ **100 MB**.
* **I/O:** Atomic writes, debounced reads; tailing is O(1).
* **Security:** No network by default; user‑space only; signed & notarized; paths sanitized.

## Assumptions

* Claude Code writes transcripts locally and supports Hooks at user‑config `~/.claude/settings.json`.
* Hook config changes apply to **new** sessions; fallback scanner bridges the gap.
* App is not sandboxed (to avoid permission prompts and keep zero‑step UX).

## Roadmap (next)

* Preferences (glyph style, TTL, refresh cadence), price table (optional costs), diagnostics panel, OTLP export (opt‑in).
