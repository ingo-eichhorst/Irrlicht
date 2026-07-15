# Irrlicht Monitoring Surface

Irrlicht is a daemon that monitors coding agent sessions. It watches transcript files and processes to classify sessions into 3 states: **working**, **waiting**, **ready**. Any upstream agent change that alters the items below can break detection.

> **⚠️ This file is incomplete and documents only 4 of irrlicht's 10 agent adapters.**
> `core/adapters/inbound/agents/` also ships **aider, antigravity, geminicli, kirocli, opencode,
> and vibe**, none of which are described below. Their monitoring surfaces are captured (for now)
> in the per-agent sections of `tracked-releases.md`. Verify claims here against the adapter
> source before briefing an analysis on them — this file has been wrong in ways that produced
> false findings (see the recursive-watcher note below).

## Supported Agents

### 1. Claude Code (`claude-code`)
- **Transcript path**: `~/.claude/projects/<project-dir>/<uuid>.jsonl`
- **The watcher is RECURSIVE, not flat.** `fswatcher` walks pre-existing subdirs
  (`addExistingDirs`) and adds fsnotify watches for new ones as they appear. Claude Code
  writes subagent transcripts to `<project-dir>/<session-uuid>/subagents/agent-*.jsonl`
  (live since 2026-06-12), and these are picked up as child SessionStates by design —
  `parser.go:907-920` documents the file-based path as the single source of truth, which
  is why `CountOpenSubagents()` deliberately returns 0. Sibling dirs also exist:
  `tool-results/`, `workflows/`, `session-memory/`
- **Process binary name**: `claude` (detected via `pgrep -x claude`)
- **Process CWD**: used to match process to project (via `lsof`)
- **Config**: `~/.claude/settings.json` (model fallback)
- **PID tracking**: YES (kqueue EVFILT_PROC for exit detection)
- **Subagent detection**: via open `Agent` tool calls in transcript

#### Transcript parsing dependencies
- **JSONL event structure**: each line is a JSON object with role/type fields
- **Event types recognized**: `user`, `assistant`, `tool_use`, `tool_result`, `turn_done`
- **`turn_done` event**: primary signal that agent finished its turn
- **Tool call structure**: `tool_use` blocks with `name` field; matched against `tool_result`
- **User-blocking tools**: `AskUserQuestion`, `ExitPlanMode` — trigger immediate waiting state
- **`Agent` tool name**: counted as in-process subagents
- **`is_error` on tool_result**: indicates ESC/rejection (maps to ready state)
- **`permission-mode` event**: values `default`, `plan`, `bypassPermissions`
- **Assistant text**: last assistant message checked for trailing `?` (waiting heuristic)
- **Token/cost fields**: `input_tokens`, `output_tokens`, `cache_read_tokens`, `cache_creation_tokens`
- **Model name field**: normalized (e.g., `sonnet` -> `claude-sonnet-4-6`)
- **Context window / utilization fields**: for pressure level calculation

#### Process-level dependencies
- Binary named `claude` found via `pgrep -x claude`
- CWD readable via `lsof -p <pid> -Fn` (macOS)
- Process exit detectable via kqueue or `kill -0`

### 2. OpenAI Codex (`codex`)
- **Transcript path**: `~/.codex/sessions/<YYYY>/<MM>/<DD>/<uuid>.jsonl`
- **Config**: `~/.codex/config.toml` (model fallback)
- **Process monitoring**: NONE
- **Transcript format**: JSONL (similar event structure)

### 3. Pi Coding Agent (`pi`)
- **Transcript path**: `~/.pi/agent/sessions/--<cwd-dashes>--/<timestamp>_<uuid>.jsonl`
- **Config**: `~/.pi/agent/settings.json` (model fallback)
- **Process monitoring**: NONE
- **Transcript format**: JSONL (similar event structure)

### 4. Gas Town Orchestrator (`gastown`)
- **Detection**: `GT_ROOT` environment variable + `gt` binary
- **CLI commands polled** (verified against `poller.go`, 2026-07-15 — exactly four):
  `gt rig list --json`, `gt polecat list --all --json`, `gt dog list --json`, `gt boot status --json`
  - **`gt convoy list --json` is NOT polled.** Convoy survives only in a comment
    (`adapter.go:4`) and permission text (`permission.go:38`). Changes to convoy's JSON
    are irrelevant — nothing reads it.
- **Role derivation**: from path segments under `$GT_ROOT`. Full roleMeta set is
  mayor, deacon, witness, refinery, polecat, crew, **boot, dog** (the latter two are
  already defined in `gastown/types.go`)
- **JSON output schema**: rig objects with polecats, crew fields. Unknown fields are
  ignored by `json.Unmarshal`, so additive upstream fields are harmless; **new enum
  values are the real risk** — e.g. `polecat.state` is passed through raw at
  `poller.go:366`, so an unknown state (like v1.2.0's `review-needed`) surfaces
  unstyled in the UI rather than failing loudly

## State Classification Logic

The classifier checks in order:
1. Open user-blocking tool (`AskUserQuestion` / `ExitPlanMode`) -> **waiting**
2. Turn finished (`turn_done` or last assistant message) + ends with `?` -> **waiting**
3. Turn finished + no question -> **ready**
4. ESC cancellation (user msg + `is_error` tool result + no open tools) -> **ready**
5. Default -> **working**

## What Breaks Irrlicht (Impact Categories)

| Category | Examples | Severity |
|----------|----------|----------|
| **Transcript path change** | Directory moved, nesting changed, file extension changed | CRITICAL — sessions not discovered |
| **Transcript format change** | JSONL schema altered, event types renamed/removed | HIGH — state classification fails |
| **Tool system change** | Tool names renamed, tool_use/tool_result structure changed | HIGH — waiting/subagent detection breaks |
| **Process change** | Binary renamed, CWD no longer accessible | HIGH — PID tracking fails (Claude Code) |
| **Config path/format change** | Settings file moved or reformatted | LOW — only affects model name fallback |
| **New session type** | New agent mode, new transcript location | MEDIUM — sessions invisible until adapter added |
| **New tool category** | New user-blocking tools added | MEDIUM — should be added to waiting detection |
| **Permission system change** | New permission modes, removal of permission-mode events | MEDIUM — `PermissionMode` surfacing affected |
| **CLI output change** | Gas Town `gt` command output format changed | HIGH — orchestrator polling breaks |
