# Irrlicht Monitoring Surface

Irrlicht is a daemon that monitors coding agent sessions. It watches transcript files and processes to classify sessions into 3 states: **working**, **waiting**, **ready**. Any upstream agent change that alters the items below can break detection.

## Supported Agents

### 1. Claude Code (`claude-code`)
- **Transcript path**: `~/.claude/projects/<project-dir>/<uuid>.jsonl`
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
- **CLI commands polled**: `gt rig list --json`, `gt polecat list --all --json`, `gt convoy list --json`
- **Role derivation**: from path segments under `$GT_ROOT` (mayor, deacon, witness, refinery, polecat, crew)
- **JSON output schema**: rig objects with polecats, crew, convoy fields

## State Classification Logic

The classifier checks in order:
1. Open user-blocking tool (`AskUserQuestion` / `ExitPlanMode`) -> **waiting**
2. Turn finished (`turn_done` or last assistant message) + ends with `?` -> **waiting**
3. Turn finished + no question -> **ready**
4. ESC cancellation (user msg + `is_error` tool result + no open tools) -> **ready**
5. Stale non-blocking tool open + `permissionMode != bypassPermissions` + 5s timeout -> **waiting**
6. All-Agent open tool calls exempt from stale-tool timeout
7. Default -> **working**

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
| **Permission system change** | New permission modes, removal of permission-mode events | MEDIUM — stale-tool timer logic affected |
| **CLI output change** | Gas Town `gt` command output format changed | HIGH — orchestrator polling breaks |
