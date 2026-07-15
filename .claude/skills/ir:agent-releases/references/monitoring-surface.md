# Irrlicht Monitoring Surface

Irrlicht is a daemon that monitors coding agent sessions. It watches transcript files and processes to classify sessions into 3 states: **working**, **waiting**, **ready**. Any upstream agent change that alters the items below can break detection.

This file exists to brief a release-sweep analysis (`/ir:agent-releases`) on what it should actually look for. It covers all ten sections irrlicht ships: the **nine agent adapters** in `core/adapters/inbound/agents/` (`all.go`'s `All()` — claude-code, codex, pi, aider, opencode, kiro-cli, gemini-cli, antigravity, mistral-vibe) **plus the Gas Town orchestrator**, which is a different layer entirely (`core/adapters/inbound/orchestrators/gastown` — it polls a CLI, has no `Source` variant, and so is absent from the discovery table below).

*(#1090's title says "4 of 10 agent adapters" and its body lists nine names "+ the gastown orchestrator". Nine + one orchestrator = ten sections; there are not ten agent adapters. Noted because the miscount is exactly the kind of thing this file is supposed to stop.)*

## How to use this file

**Verify against adapter source before briefing an analysis on a claim here.** This file has repeatedly been wrong in ways that produced *confident false findings* — a wrong reference is worse than a thin one, because it costs real assessment time to kill a HIGH report for behavior that always worked. Two such errors were corrected in #1077 (the "flat" Claude Code watcher; `gt convoy list` being polled), and nine more in #1090 — every one is marked **⚠️ corrected (#1090)** inline, so `grep` for that string to see the full list.

Two rules that follow from that history:

- **Cite file:line, not release notes.** An upstream changelog tells you what upstream changed; only the adapter tells you whether irrlicht reads it. Most upstream changes touch nothing irrlicht depends on.
- **A field existing on disk does not mean irrlicht reads it.** Several adapters deliberately ignore data that is right there — kiro's `metering_usage` cost, vibe's `stats.*_price_per_million`, gemini's `.project_root`, kiro's `<uuid>.lock`. Changes to those are non-events.

**Depth is uneven.** The Codex and Pi sections are thin (they predate this file's expansion) and have not had the same source audit as the rest. Treat them as pointers, not as a complete surface.

---

## Cross-cutting machinery

Read this before the per-agent sections — most adapters depend on it, and several past false findings turned on not knowing it.

### Discovery: three source models

An adapter's `Source` variant determines how sessions are found at all. This is the single most structural fact about an adapter.

| Variant | Adapters | How sessions are discovered |
|---|---|---|
| `agent.FilesUnderRoot` | claude-code, codex, pi, gemini-cli, mistral-vibe, kiro-cli, antigravity | fswatcher over a `$HOME`-relative root |
| `agent.FilesUnderCWD` | aider | **No watcher at all** — the process scanner stat-polls `<pid's CWD>/<filename>` |
| `agent.ProcessOwnedStore` | opencode | Dedicated SQLite watcher; full tailer bypass |

`core/domain/agent/source.go`; wired in `core/cmd/irrlichd/wiring.go:53-86` (which **panics** for any `ProcessOwnedStore` adapter other than opencode, `wiring.go:66`).

> **This table is enforced, not asserted.** `TestSourceCensus`
> (`core/adapters/inbound/agents/maps_test.go`) runs the same type-switch over
> the real `agents.All()` and fails when any adapter's variant changes. `Source`
> is a sealed sum, so a brand-new variant fails there too. Don't hand-audit
> these columns — if the test is green, they're right; if you change them, the
> test tells you.

### Tailer tiers

"Does it use the shared tailer" has three answers, not two:

| Tier | Adapters | Meaning |
|---|---|---|
| **(a)** Tailer only | claude-code, codex, pi, gemini-cli, aider | No second file feeds the parser. (claude-code is still not *purely* transcript-driven — its hooks deliver `PermissionPending`/`CompactInProgress` out-of-band, and `settings.json` supplies the model fallback.) |
| **(b)** Tailer + sibling side-read | antigravity, kiro-cli, mistral-vibe | Parser reads a sibling store for data the transcript lacks, via the `TranscriptPathAware` seam |
| **(c)** Full bypass | **opencode** | `MetricsProvider` short-circuits before a tailer is ever constructed (`core/adapters/outbound/metrics/adapter.go:110-112`) |

⚠️ **corrected (#1090):** tier (b) previously read as "everything except opencode uses the tailer normally." Antigravity, kiro-cli, and vibe each depend on a **second file** whose schema is its own break surface.

### The shared tailer — what it does and does NOT handle

`core/pkg/tailer/tailer.go`

- **Re-opens by path each pass** (`tailer.go:478`, `defer file.Close()` at `:482`). No long-lived fd. The cursor is a byte offset `t.lastOffset`, persisted across daemon restarts in the ledger (`parser.go:529`; `LedgerSchemaVersion = 5` at `parser.go:522` — bumping it discards ledgers and forces a full re-scan, which is the escape hatch for a parser fix).
- **Shrink is treated as rotation** (`tailer.go:496-505`): `fileSize < lastOffset` → re-read from byte 0 **and** `resetAccumulatorsForRotation()` (`tailer.go:569-592`), which zeroes cumulative tokens/costs/tasks and calls the optional `ResetForRotation()` parser hook.

⚠️ **corrected (#1090) — do not repeat the claim that "inode/truncation concerns are already handled":**

- **There is zero inode awareness.** No `Sys()`, no `syscall.Stat_t`, no `.Ino` anywhere in the tailer, the metrics adapter, or the fswatcher. The only signal is a size comparison.
- **A same-size rewrite is invisible forever.** `fileSize == lastOffset` seeks to EOF and reads nothing; the offset never regresses. (Vibe's in-place `/rewind` escapes this only because it happens to *shrink*.)
- **A rotation to a larger file resumes mid-stream.** `fileSize > lastOffset` seeks into the middle of the *new* file, drops the partial line as unparseable, and silently continues — with no reset, so old cumulative totals are carried onto new content.
- **The tailer cache is keyed by path** (`metrics/adapter.go:114`), so an inode swap at the same path reuses the same tailer *and the same stateful parser instance*.
- The rotation reset also does **not** clear `openToolCalls`, `lastCWD`, `lastAssistantText`, `lastWasUserInterrupt`, `lastWasToolDenial`, or `metrics.LastEventType` — a rotation replays from 0 with stale classification anchors.

### The fswatcher

`core/adapters/inbound/agents/fswatcher/watcher.go`

- **Recursive, unbounded depth** — `filepath.WalkDir` per root child (`watcher.go:490-499`), plus `addSubtree` for dirs created at runtime (`watcher.go:566-577`). The recursion is load-bearing for Claude Code's `subagents/`, gemini's `chats/<uuid>/`, vibe's `agents/`, and antigravity's 3-deep layout.
- **`.jsonl` only** (`transcriptExt`, `watcher.go:21`; enforced `:303`, `:588`, `:610`). This is how sibling non-transcripts are ignored for free (gemini's `logs.json`/`.project_root`, vibe's `meta.json`, kiro's `.json`/`.lock`). aider's `.md` is invisible to it entirely — hence `FilesUnderCWD`.
- **Session ID** defaults to the filename stem (`extractSessionID`, `watcher.go:608-614`), overridable per adapter via `FilesUnderRoot.SessionIDFromPath`; returning `""` **skips the file** (antigravity uses this to ignore `transcript_full.jsonl`).
- **Max session age**: default **5 days** (`core/domain/config/config.go:13`), overridable via `IRRLICHT_MAX_SESSION_AGE`. Applied to Create/Write, **not** to Remove/Rename (`watcher.go:341-344`).
- Broadcast is non-blocking with a 64-cap channel (`watcher.go:273-279`) — **events are dropped silently** if a subscriber is slow.

### Model resolution — two independent layers

1. `tailer.NormalizeModelName` (`core/pkg/tailer/parser.go:618-660`): strips a `[1m]` suffix, applies a 3-entry alias map (`opusplan`→`claude-opus-4-1`, `sonnet`→`claude-sonnet-4-6`, `haiku`→`claude-haiku-4-5`), strips a `-\d{8}$` date suffix, then a most-specific-first `Contains` switch. ⚠️ Those hardcoded aliases pin `sonnet`/`haiku` to *specific versions* — an upstream model bump silently mis-prices until they're updated.
2. `capacity.modelAliases` (`core/pkg/capacity/aliases.go`), applied in `GetModelCapacity`. **Exact match only, no fuzzy.** Synced from codeburn via `/ir:refresh-aliases`. A model id missing here prices at **$0**.

**Config fallback is deliberately narrow** (`tailer_config.go:51-66`): only `claude-code` (`~/.claude/settings.json`), `pi` (`~/.pi/agent/settings.json`), `codex` (`~/.codex/config.toml`). **Every other adapter returns `""`** — no config is read for vibe, kiro-cli, opencode, antigravity, aider, or gemini-cli. This is intentional: #1019 found a vibe session inheriting an unrelated claude-code model from what used to be a catch-all default. Corollary: for those six, **if the model isn't in the transcript (or sibling store), there is no fallback at all.**

### Timestamps

`ParseTimestamp` (`core/pkg/tailer/parser.go:825-839`) tries `raw["timestamp"]` as RFC3339, then `2006-01-02T15:04:05.000Z`, then a positive `float64` as Unix seconds — and otherwise **returns `time.Now()`**. There is no error path. An upstream timestamp-format change is therefore **invisible** (no error, no log) but silently re-anchors every event to wall-clock, corrupting `SessionStartAt`, `ElapsedSeconds`, and `MessagesPerMinute`, and making replay goldens non-deterministic.

Adapters with no timestamps in-band at all (**vibe**, **aider**, and every kiro event except `Prompt`) always take this path — their backfilled history reads as "just now".

### Context utilization

`ComputeContextUtilization` (`tailer_metrics.go:548-579`). Window precedence: `contextWindowOverride` > `capacityMgr.GetModelCapacity(model).ContextWindow`. Thresholds: ≥90 critical, ≥80 warning, ≥60 caution.

**There is no 32k sentinel fallback** — `GetModelCapacity` returns a zero value on miss (`capacity.go:97-99`), and `ContextWindowUnknown` is what marks that zero as "unknown" rather than "not yet computed".

### User-blocking tools — a hardcoded, Claude-flavored list

The list is `AskUserQuestion`, `ExitPlanMode`, `question`, hardcoded in **two** places (deliberate duplication to avoid a domain import):

- `core/pkg/tailer/tailer_config.go:34-40` — feeds `SawUserBlockingToolClosedThisPass`
- `core/domain/session/metrics.go:380-382` — the **classifier-facing** one, via `NeedsUserAttention()`

> **The duplication is pinned, not just documented.** Both predicates are probed
> against one shared table (`core/internal/contracttesting/userblocking`) by a
> paired `TestUserBlockingListsAgree` in each package, so editing one list
> without the other turns that side red. The duplication is deliberate — pin it,
> don't "fix" it by extracting a shared constant; the tailer's copy exists
> precisely to avoid the domain import.

The names are Claude Code's, but the mechanism is name-matching, so **any adapter that emits one of those names gets user-blocking detection**. Codex earns it by *aliasing*: it synthesizes a fake tool call named `ExitPlanMode` for its `<proposed_plan>` block (`codex/parser.go:294-300`).

**Adapters that reach `waiting` only via text heuristics, never via an open tool:** mistral-vibe (its tool is `exit_plan_mode`, snake_case), kiro-cli (lowercase tool names — #588), antigravity (plan gates are live UI, never persisted), aider. For these, an upstream *addition* of a plan-approval gate is a non-event until the name is aliased in.

Adjacent hardcoded list: `isPermissionGatedEditTool` (`metrics.go:388-397`) matches `edit|write|multiedit|notebookedit` **case-insensitively**, because adapters disagree on casing.

### Optional parser seams — who implements what

| Seam | Implementers |
|---|---|
| `TranscriptParser` (required) | all |
| `RawLineParser` | aider |
| `idleFlusher` | aider |
| `rotationResetter` (`ResetForRotation`) | vibe |
| `queuedTurnSplitter` | vibe |
| `TranscriptPathAware` | vibe, kiro-cli, antigravity |
| `pendingContributor` | claude-code |
| `ParserStateProvider` | claude-code, codex |
| `ReplayStoreStager` | antigravity |

`core/pkg/tailer/parser.go:381-509`.

> **This table is enforced, not asserted.** `TestParserSeamCensus`
> (`core/adapters/inbound/agents/maps_test.go`) type-asserts every seam against
> every parser in `agents.All()`, one subtest per row. An adapter that gains or
> loses a seam fails the matching row. Each seam is consumed by a type assertion
> in the tailer, so implementing one silently changes runtime behavior — which is
> why the row set is worth pinning rather than restating.

---

## State Classification Logic

⚠️ **corrected (#1090):** this was documented as a 5-step order. The real body (`core/application/services/state_classifier.go:22-87`) evaluates **seven** rules.

| # | Condition | Result |
|---|---|---|
| 0 | `PermissionPending` (hook signal — first, because it doesn't depend on `HasOpenToolCall`) | **waiting** |
| 0b | `CompactInProgress` (PreCompact hook, #657) | **working** |
| 1 | `NeedsUserAttention()` (open user-blocking tool) | **waiting** |
| 1b | `OpenToolStalled` (#488 transcript fallback when the hook is unreachable) | **waiting** |
| 2 | `IsAgentDone()` → `classifyAgentDone` | **ready**, or **waiting** if `IsWaitingForUserInput()` |
| 3 | `isUserInterruptReady(...)` (ESC/denial) | **ready** |
| 4 | default | **working** |

`transitionTo` (`:92-97`) makes every rule a no-op when already in the target state.

**`IsAgentDone`** (`core/domain/session/metrics.go:429-459`), in order:
1. `HasOpenToolCall` → **false** (overrides everything, including `turn_done`)
2. `HasLiveBackgroundProcess` → **false** (#445)
3. `LastEventType == "turn_done"` → **true** (the primary path)
4. Fallback: `LastEventType ∈ {"assistant", "assistant_output"}` → true

That fallback is **not universal**, and two adapters depend on its absence:
- **Codex** must not use it (`:451-455`) — it emits a preliminary `assistant_message` before tool calls, which would flip ready→working→ready every turn.
- **opencode** emits `assistant_message`, which the fallback does not match — so **opencode is 100% dependent on `turn_done`**. Any break in its turn_done path is unrecoverable rather than degraded.

**Waiting cues** (`core/domain/session/waiting_cue.go`) read **only `LastAssistantText`**, which is capped at the **trailing 200 runes** (`TruncateAssistantText`). `ExtractQuestionSnippet` scans the whole (truncated) text, first-question-wins; `ExtractWaitingCue` walks only the last 1–2 sentences against ~20 regexes. Deliberately recall-biased.

### `turn_done`: marker vs. heuristic

The single highest-value thing to know per adapter. **Four adapters get an explicit signal from upstream; the rest infer it**, and every inference has the same two failure modes: a trailing text-only message mid-turn fires a **premature ready**, and a turn ending on a tool call **sticks in `working` forever**.

| Adapter | `turn_done` source |
|---|---|
| claude-code | **explicit** — a `turn_done` event |
| codex | **explicit** — `event_msg` payload `task_complete` (canonical) or `turn_aborted` (ESC/mid-flight error, emitted *instead of* `task_complete`) |
| pi | **explicit** — assistant message with `stopReason == "stop"` |
| opencode | **explicit** — `step-finish` with a terminal `reason` |
| mistral-vibe | *heuristic* — assistant message with no `tool_calls` |
| kiro-cli | *heuristic* — text-only `AssistantMessage` (no `toolUse`) |
| antigravity | *heuristic* — `PLANNER_RESPONSE` with no `tool_calls` |
| gemini-cli | *heuristic* — non-empty content **and** zero tool calls |
| aider | *synthesized* — `idleFlusher` after 1500ms idle; the only adapter |

**There is no inactivity sweep on `working`** for the heuristic adapters (except aider's idle flush) — a session that never emits its terminal line stays `working` indefinitely. That is why the heuristic adapters are the ones to scrutinize on any upstream turn-shape change: the four explicit adapters degrade loudly, the rest degrade silently.

---

## Supported Agents

### 1. Claude Code (`claude-code`)
- **Transcript path**: `~/.claude/projects/<project-dir>/<uuid>.jsonl`
- **Env override**: `CLAUDE_CONFIG_DIR`
- **The watcher is RECURSIVE, not flat.** Claude Code writes subagent transcripts to `<project-dir>/<session-uuid>/subagents/agent-*.jsonl` (live since 2026-06-12), and these are picked up as child SessionStates by design. Sibling dirs also exist: `tool-results/`, `workflows/`, `session-memory/`
- **Process binary name**: `claude` (detected via `pgrep -x claude`)
- **Process CWD**: used to match process to project (via `lsof`)
- **`ExcludeArgv`**: `IsInfraArgv` — excludes the `--bg-spare` pool helper (#727). One of only two adapters that declare an argv exclusion (the other is gemini-cli).
- **Config**: `~/.claude/settings.json` (model fallback)
- **PID tracking**: YES (kqueue EVFILT_PROC for exit detection)
- **Subagent detection**: ⚠️ **corrected (#1090) — file-based, not tool-call-based.** `parser.go:907-920` documents `<parent>/subagents/agent-*.jsonl` as the **single source of truth**, which is why `CountOpenSubagents()` deliberately `return 0` — counting open `Agent` tool_use entries as well would double-count every running subagent. The function is kept only as a seam, in case a future revision reintroduces subagents that write no transcript.

#### Transcript parsing dependencies
- **JSONL event structure**: each line is a JSON object with role/type fields
- **Event types recognized**: `user`, `assistant`, `tool_use`, `tool_result`, `turn_done`
- **`turn_done` event**: primary signal that agent finished its turn — one of only four adapters with an explicit upstream marker (see the marker-vs-heuristic table above)
- **Tool call structure**: `tool_use` blocks with `name` field; matched against `tool_result`
- **User-blocking tools**: `AskUserQuestion`, `ExitPlanMode` — trigger immediate waiting state
- **`is_error` on tool_result**: indicates ESC/rejection (maps to ready state)
- **`permissionMode` field**: passthrough only, no classifier branching — **new or renamed values are non-events** (v2.1.200 renamed "default" to "manual"; nothing noticed). Census across 320 local transcripts, v2.1.210, 2026-07-15.
- **Assistant text**: last assistant message checked for trailing `?` (waiting heuristic)
- **Token/cost fields**: `input_tokens`, `output_tokens`, `cache_read_tokens`, `cache_creation_tokens`
- **Model name field**: normalized (e.g., `sonnet` -> `claude-sonnet-4-6`)
- **Hooks**: the hook matcher (`hookinstaller.go:34`) hardcodes `Bash|Write|Edit|MultiEdit|NotebookEdit|WebFetch|mcp__.*|AskUserQuestion|ExitPlanMode`. Note hooks post to a **hardcoded port 7837**, so hook-delivered observations can't be re-recorded against a dev daemon on an alt port.

#### Process-level dependencies
- Binary named `claude` found via `pgrep -x claude`
- CWD readable via `lsof -p <pid> -Fn` (macOS)
- Process exit detectable via kqueue or `kill -0`

### 2. OpenAI Codex (`codex`)
- **Transcript path**: `~/.codex/sessions/<YYYY>/<MM>/<DD>/<uuid>.jsonl`
- **Env override**: `CODEX_HOME`
- **Config**: `~/.codex/config.toml` (model fallback)
- **Process monitoring**: NONE
- **Transcript format**: JSONL (similar event structure)
- **`turn_done`**: explicit — emitted for exactly two `event_msg` payloads (`parser.go:170-190`): **`task_complete`** (the canonical "turn finished" signal) and **`turn_aborted`** (cancelled via ESC or errored mid-flight — Codex emits it *instead of* `task_complete`, so without it an interrupted turn never settles). All other `event_msg` payloads are metadata and are skipped.
- **Must not use** the `assistant`/`assistant_output` fallback in `IsAgentDone` — Codex writes an intermediate assistant message before calling a tool, so the fallback would flicker working→ready→working every turn (`metrics.go:451-455`).
- **User-blocking**: earns it by aliasing — synthesizes a tool call literally named `ExitPlanMode` for `<proposed_plan>` (`parser.go:294-300`).

### 3. Pi Coding Agent (`pi`)
- **Transcript path**: `~/.pi/agent/sessions/--<cwd-dashes>--/<timestamp>_<uuid>.jsonl`
- **Env override**: `PI_CODING_AGENT_SESSION_DIR`
- **Config**: `~/.pi/agent/settings.json` (model fallback)
- **Process monitoring**: NONE
- **Transcript format**: JSONL (similar event structure)
- **`turn_done`**: explicit — an assistant message with **`stopReason == "stop"`**; any other `stopReason` (toolUse, etc.) is mid-turn `assistant` (`parser.go:116-122`).
- **Cost**: the only adapter that sets `ProviderCostUSD` from a provider-reported figure (`tailer/parser.go:375`); everything else is estimated from the capacity price map.

### 4. Gas Town Orchestrator (`gastown`)
- **Detection**: `GT_ROOT` environment variable + `gt` binary
- **CLI commands polled** (verified against `poller.go`, 2026-07-15 — exactly four):
  `gt rig list --json`, `gt polecat list --all --json`, `gt dog list --json`, `gt boot status --json`
  - **`gt convoy list --json` is NOT polled.** Convoy survives only in a comment (`adapter.go:4`) and permission text (`permission.go:38`). Changes to convoy's JSON are irrelevant — nothing reads it.
- **Role derivation**: from path segments under `$GT_ROOT`. Full roleMeta set is mayor, deacon, witness, refinery, polecat, crew, **boot, dog** (the latter two are already defined in `gastown/types.go`)
- **JSON output schema**: rig objects with polecats, crew fields. Unknown fields are ignored by `json.Unmarshal`, so additive upstream fields are harmless. **New enum values are also inert** — but *not* because of a defensive default, which is the assumption that produced a false finding here once. `polecat.state` is passed through raw (`poller.go:366`, overridden at `:371` only when a live session matches the polecat's worktree) and then **never reaches any client**: `workerInfo` (`grouped.go:45-52`) has no `State` field, so `addCodebaseWorkers` (`grouped.go:326-343`) can't copy it. The dashboard always shows `SessionState.State` from irrlicht's own classifier, never gastown's string. `Worker.State`/`GlobalAgent.State` are dead fields; the only reader is a broadcast change-diff (`gastown/adapter.go:211-215`). So an unknown state (v1.2.0's `review-needed`) surfaces nowhere. If orchestrator state is ever wanted in the UI, **that plumbing is the work** — not a defensive default for the raw string.

#### What breaks it
- Any of the four polled `gt` commands changing its JSON shape, or being renamed/removed → orchestrator polling breaks (HIGH). Additive fields and new enum values are safe (see above).
- `$GT_ROOT` path-segment layout changing → role derivation breaks.
- Changes to `gt convoy list --json` → **non-event**, nothing reads it.

### 5. Mistral Vibe (`mistral-vibe`)

- **Transcript path**: `<root>/<session-dir>/messages.jsonl`, plus a **sibling `meta.json`** (tier (b) — load-bearing, see below). `<root>` is `[session_logging].save_dir` when set, else `$VIBE_HOME/logs/session`, else `~/.vibe/logs/session` — see Env override and Config below.
- **Session ID = the directory name, and irrlicht reads no naming shape.** ⚠️ **corrected (#1090):** there is no glob and no regex. `sessionIDFromPath` (`adapter.go:56-65`) accepts a file iff its basename is exactly `messages.jsonl`, then takes `filepath.Base(filepath.Dir(path))` as the ID. Upstream *does* mint `session_<ts>_<shortid>`, but **nothing in irrlicht parses it** — a bare `<session-id>` dir would work identically. Do not write that irrlicht depends on the timestamp pattern.
- **Env override**: **`VIBE_HOME`** (#1095, `adapter.go`) → `<VIBE_HOME>/logs/session`. **Absolute paths only** — a relative or `~`-prefixed value is logged and ignored, so irrlicht is narrower than upstream, which also `expanduser().resolve()`s those. One caveat of the two that `KIRO_HOME` below carries: irrlicht watches **exactly one** root. Vibe is the **exception to the boot-time-snapshot rule** — since #1115 its root resolves through `FilesUnderRoot.DirFunc` at watcher-build time (which the `PermissionService` reaches only after an observe grant, so the `config.toml` read happens under consent), so it re-resolves on every grant rather than freezing at `Agent()`. Upstream honors it in **source but not in its docs** (v2.19.1 `vibe/core/paths/_vibe_home.py`) — which is why the adapter was hardcoded until #1095; the old "Vibe documents no env var" comment read the docs, not the source. `adapter.go`'s `vibeHomeEnvVar` is the single assertion of this fact.
- **Process**: `agent.CommandPattern{Regex: "(^|/)vibe( |$)|mistral-vibe/bin/python"}` → `pgrep -f` over the **full command line** (`adapter.go:43`, `agent.go:30`). Vibe is a Python console-script with no `setproctitle`, so `ExactName{"vibe"}` would never fire. No `ExcludeArgv`.
- **PID discovery**: `DiscoverPIDByCWDAndCmdLine` (`pid.go:16-21`) — cmdline regex, then narrow by CWD.
- **CWD comes only from `meta.json`.** The JSONL carries none. **No sidecar → no cwd → `DiscoverPID` early-returns `0, nil` → no PID bind**, and the session is exposed to the unbound-ghost reaper.
- **Config**: **exactly one key** — `[session_logging].save_dir` from `$VIBE_HOME/config.toml`, read to locate the session root (#1115, `config.go`). Nothing else in the file is parsed, and the read is table-aware (a bare `save_dir` under any other table is ignored). Model fallback stays explicitly disabled (#1019 — a vibe session used to inherit an unrelated *claude-code* model from the old catch-all); this key feeds the watcher root, not the tailer.

#### `meta.json` (sidecar) dependencies
Read on `turn_done` only; memoized by `(mtime, size)`; failures return last-good state.
- `environment.working_directory` → CWD (and thus PID binding)
- `config.active_model` → model
- `config.auto_compact_threshold` → context window fallback
- `config.models[].alias` → per-model context window. ⚠️ **Sharp edge:** resolution matches `active_model` against `models[].alias`, **not** `models[].name` — in live 2.19.1 configs `name` is `"mistral-vibe-cli-latest"` (an unrelated product label) while `alias` is `"mistral-medium-3.5"`.
- `stats.context_tokens` → `Tokens.Total`
- `stats.session_prompt_tokens` / `stats.session_completion_tokens` → **cumulative**; the parser emits the delta
- **Deliberately ignored**: `stats.input_price_per_million`, `output_price_per_million`, `session_cost`. Cost is priced from the capacity map instead (`mistral-medium-3.5` → `mistral/mistral-medium-3-5`, `aliases.go:205`). Changes to those three fields are non-events.

#### Transcript parsing dependencies
- **`role`** is mandatory — empty/unknown ⇒ the whole line is dropped. Only `user`, `assistant`, `tool` are handled.
- **`content` must be a plain string** — a switch to OpenAI-style content-block arrays would silently yield empty text.
- **`injected: true`** ⇒ skip (the shell-`!`-escape wrapper). Any *new* injection type that should open a turn would be dropped.
- **`tool_calls[]`** with `function.name` (OpenAI shape; a flat `name` is tolerated); `tool_call_id` closes the call.
- **No timestamps** — every event takes the `time.Now()` fallback.
- **`turn_done` is heuristic**: assistant message with a non-empty `tool_calls[]` ⇒ keep working; assistant with **no** `tool_calls` ⇒ `turn_done`.
- **Todos**: `function.name == "todo"` exactly, `arguments` as a JSON string, `action == "write"`.
- **`ResetForRotation`** — vibe is the **only** adapter implementing it, because 2.19.1's ACP `/rewind` rewrites `messages.jsonl` **in place**. It's also the only `queuedTurnSplitter` (#988, the in-memory `message_queue.py` drain).
- **User-blocking: unreachable.** Vibe's tool is `exit_plan_mode` (snake_case), absent from the hardcoded list. Even if aliased, vibe flushes `messages.jsonl` **per-turn, not per-message** — the call and its result land on disk together, *after* the user already answered, so the open-tool window never exists on disk.
- **Backfill quirk**: the sidecar retains only final cumulative totals, so the first `turn_done` on a backfill emits the whole session's tokens and later turns emit nothing. Session total is correct; the per-turn split is lost.

#### What breaks it
- Rename `messages.jsonl`, move the root, drop the `.jsonl` extension, or flatten to `<root>/<id>.jsonl` (which would collapse every session onto the ID `session`) → **silent total blackout**.
- Any XDG relocation, or a **third** relocation seam landing alongside `$VIBE_HOME` and `[session_logging].save_dir` → same. (Both of those are handled — see Env override and Config above.)
- **Three `save_dir` shapes remain unhandled, all silent blackouts** (#1115 fixed only the user-config one):
  - **A project config**: `<cwd>/.vibe/config.toml` **replaces** the user config wholesale (it does not merge — `_harness_manager.py:48-57`) when that cwd is trusted per `$VIBE_HOME/trusted_folders.toml`. It's keyed to the directory vibe was launched from, and the daemon has no such cwd — **unresolvable**, not merely unimplemented, since `FilesUnderRoot` watches one root. ⚠️ Note it does **not** walk up: the file must sit in the *exact* launch dir.
  - **`VIBE_SESSION_LOGGING`** (JSON, e.g. `{"save_dir":"/x"}`) outranks the TOML, but lives in *vibe's* process env, not the daemon's.
  - **A relative `save_dir`** resolves against vibe's launch cwd (`expanduser().resolve()`), so it names a different dir per launch. irrlicht logs and ignores it; `~`-relative **is** honored, since a config file is not shell-expanded.
- **`[session_logging].enabled = false`** → vibe writes no transcripts at all (`session_logger.py:46-54`); irrlicht watches the default root and sees nothing. Correct, but indistinguishable from "no sessions".
- ⚠️ **Upstream's dormant config-layer stack** (`core/config/layers/`, `builder.py`) is gated behind `enable_config_orchestrator = False` (`vibe_schema.py:290`) as of 2.19.1. If it ever ships enabled, precedence changes and `save_dir` merges as a **whole-table replace** — re-verify against source.
- Drop/rename `meta.json` or `environment.working_directory` → no PID bind, no model, no tokens, no context window, in one stroke.
- Adopt `setproctitle`, or launch as `python -m vibe.cli` → the cmdline regex never fires.
- Emit a trailing summary/telemetry line after the final assistant message → premature `ready`.
- Rename the `agents/` subfolder → subagents still appear, but **top-level and unlinked**.

### 6. Kiro CLI (`kiro-cli`)

- **Transcript path**: `~/.kiro/sessions/cli/<uuid>.jsonl`, plus a **`<uuid>.json` sidecar** (tier (b)). Session ID = filename stem.
- **`<uuid>.lock`**: exists upstream, ⚠️ **but irrlicht never reads it** — zero hits across `core/`. Liveness is `pgrep`, not the lock. Upstream could delete the lock mechanism entirely with **zero** impact. Non-event.
- **Env override**: **`KIRO_HOME`** (#1074, `adapter.go:38-47`) → `<KIRO_HOME>/sessions/cli`. **Absolute paths only** — a relative or `~`-prefixed value is logged and ignored. Two caveats: it's a **boot-time snapshot** (changing it after the daemon starts does nothing until restart), and irrlicht watches **exactly one** root — mixing `KIRO_HOME`-set and unset sessions means only one set is ever seen.
- **Process**: `agent.ExactName{"kiro-cli"}` → `pgrep -x`. Works because `kiro-cli chat` keeps `comm="kiro-cli"` on the parent; children (`kiro-cli-chat`, `bun tui.js`) and the always-running `kiro_cli_desktop` companion don't match.
- **PID discovery**: `DiscoverPIDByCWD` — **CWD-match, not writer-match**, because kiro doesn't hold the `.jsonl` open between writes (lsof-based discovery is impossible). Falls back to the sidecar's top-level `cwd` when cwd is unknown.
- **Headless writes no session file at all** — `kiro-cli chat --no-interactive` is **invisible to the daemon**. Only the TUI persists a transcript, and even then **lazily**, after the first prompt: a launched-but-unprompted kiro is process-visible but transcript-invisible. *(Attested by a live probe recorded in fixture prose, not by in-repo code.)*
- **Config**: none read.

#### `<uuid>.json` (sidecar) dependencies
⚠️ **corrected (#1090): this is not a general "metrics source".** It supplies model + context window only, and the read is a **strict token-walk by exact key name** — renaming any level returns nil, and **model shows unknown and the context bar disappears, silently**.
- `session_state.rts_model_state.model_info.model_id` → model. In practice this is `"auto"`, which no alias resolves — which is why the explicit `context_window_tokens` override is load-bearing.
- `session_state.rts_model_state.model_info.context_window_tokens` → context window
- `session_state.rts_model_state.context_usage_percentage` → **tokens are SYNTHETIC**: `Total = pct/100 × window`, back-computed purely so `ComputeContextUtilization` reproduces kiro's own percentage. `Input`/`Output`/`CacheRead`/`CacheCreation` stay **zero**. The sidecar's real `input_token_count`/`output_token_count` are 0 in kiro 2.5/2.6.
- **Cost is never produced.** The sidecar carries `metering_usage: [{value, unit: "credit"}]` and the adapter **ignores it**. Non-event.
- Read once per `turn_done`, never mid-turn; memoized by `(mtime, size)`.

#### Transcript parsing dependencies
Envelope: `{"version":"v1","kind":…,"data":{"content":[…]}}`. **`version` is read but never checked** — a v2 bump breaks nothing.
- **Dispatch on `kind`**: `Prompt` → user; `AssistantMessage` → assistant or turn_done; `ToolResults` → tool_result; **`Clear` → skip**; anything else → skip.
- **`turn_done` is heuristic**: a text-only `AssistantMessage` (`len(toolUses) == 0`). The single most fragile rule in the adapter.
- **Error semantics**: `status != "success"` ⇒ `IsError`. This is the *harness* verdict, not the command's — a **non-zero shell exit is still `success`**, and a user-cancelled (Esc) tool is `error`. Kiro has no `cancelled` status.
- **Blocks**: `toolUse`/`text`; `toolUse.data.toolUseId` + `.name`; `toolResult.data.toolUseId` + `.status`.
- **Todos**: `todo_list` with `command ∈ {create, complete}`, `tasks[].task_description`, `completed_task_ids[]`. IDs are assumed to be 1-based create order **as strings** — numeric IDs would silently drop every completion.
- **Timestamps**: only `Prompt` carries `data.meta.timestamp` (epoch seconds). Everything else falls back to parse time, so a backfilled session's `LastMessageAt` reads "just now".
- **User-blocking: unreachable, and it's a known bug.** Kiro emits lowercase tools (`write`, `execute_bash`, `todo_list`), none of which are in the allowlist — so while a permission picker is pending, the open toolUse classifies as `working`, never `waiting` (**#588**). Kiro's `waiting` comes only from the trailing-`?`/text-cue heuristic.
- **Interrupt quirk**: Escape mid-turn produces a *synthetic* text-only AssistantMessage reading literally `"Response was interrupted by the user"` — which the turn_done heuristic counts as a **normally completed turn**.

#### Session lifecycle
- **`/clear` does NOT rotate** — same file, same UUID, one appended `Clear` event, which the parser **skips**. `/clear` is therefore **entirely invisible** to irrlicht. Not a defect: no trace exists to observe.
- **`/chat new` DOES rotate** (live-verified 2.6.0, #935) — mints a new `<uuid>.jsonl` **in the same process**, synchronously (~1s), and the old `.lock` disappears. Surfaces as a genuinely new session row.
- `--resume-id <uuid>` re-opens the **same** `.jsonl` under a new PID — one session, two lifetimes.

#### What breaks it
- Root moves off `~/.kiro/sessions/cli`, or `KIRO_HOME` semantics change → blackout. (Date-*sharding* would survive — the watcher recurses; a *rename* would not.)
- Extension changes from `.jsonl`, or the filename stops being the session UUID stem.
- The `chat` parent stops keeping `comm="kiro-cli"` (a re-exec, or a Node/Bun wrapper making argv[0] `node`) → `pgrep -x` cannot see it; would need a `CommandPattern`.
- Sidecar key-path renames → model + context bar vanish silently. Top-level `cwd` removal → PID binding fails.
- **Highest risk**: any change to the turn-end shape. A final AssistantMessage carrying a trailing `toolUse` (telemetry, auto-`todo_list` completion, a citation block) ⇒ **hangs in `working` forever**. A mid-turn text-only AssistantMessage (streaming a preamble) ⇒ **premature turn_done** per line.
- `status` gaining a real `cancelled` value ⇒ cancellations stop flagging `IsError`.

### 7. opencode (`opencode`)

The odd one out twice over: **SQLite-backed**, and the **only full tailer bypass**.

- **Storage**: `~/.local/share/opencode/opencode.db` — hardcoded `$HOME`-relative (`adapter.go:24`, `watcher.go:107`). **`$XDG_DATA_HOME` is not honored.** Driver: `modernc.org/sqlite` (pure Go, no CGo).
- ⚠️ **corrected (#1090) — "WAL-watched" is misleading.** fsnotify watches the **parent directory** (`watcher.go:186-189`) and accepts a `Write` whose basename is `opencode.db` **or** `opencode.db-wal` (`:200-206`). **The WAL's contents are never read** — there is no frame parsing. Every read opens the **main DB** read-only (`?mode=ro&_journal=WAL&_timeout=500`). The `-wal` suffix survives only as (a) an fsnotify trigger name and (b) a routing sentinel baked into `TranscriptPath`.
- ⚠️ **"Polling" is a misnomer** despite the package doc saying so. The steady state is **event-driven with a 500ms debounce** (`defaultMinScanGap`) — the debounce exists to break the feedback loop where irrlicht's own read-only open touches `.db-shm`. The only real ticker is a 5s wait that runs **only while the DB does not yet exist**.
- **Change detection** = fsnotify event → debounced **full SQL re-query**. No diffing.
- **Config**: ⚠️ **none read by the adapter** — confirmed: the package's only `filepath.Join` is `$HOME`+`dbRelPath`, and there are no file reads at all. *Footnote:* `~/.local/share/opencode/auth.json` **is** read elsewhere (`quotainherit.go:377`) for quota inheritance and cost attribution, keying on `openai-oauth`/`anthropic-oauth`. That's a credentials file outside the adapter and **does not affect detection or classification**.
- **Process detection is a hard discovery gate**, not just a PID nicety: `agent.ExactName{"opencode"}` → `pgrep -x`. `maybeEmitNewSession` **refuses to emit a session unless a live opencode process owns that session's `directory`** (`watcher.go:373-380`) — otherwise every non-archived DB row would re-emit as live on each daemon start. **No live process ⇒ no session, regardless of DB content.**
- **PID discovery**: `DiscoverPIDByCWD` — deliberately not lsof-based (opencode opens/closes the DB per write). Known trade-off: two instances in the same CWD are indistinguishable.
- **Orphan reaping** uses "no process of this adapter owns the CWD" rather than mtime staleness, since a shared WAL makes mtime meaningless.

#### Schema dependencies — the break surface
Three tables. Any rename/drop of a listed column is a hard break.

| Table | Columns read |
|---|---|
| `session` | `id`, `directory`, `time_updated`, `parent_id`, `time_archived` |
| `part` | `id`, `data`, `session_id`, `message_id`, `time_updated`, **`time_created`** |
| `message` | `id`, `data` |

```sql
-- discovery (watcher)
SELECT id, directory, time_updated, parent_id FROM session
WHERE time_archived IS NULL AND time_updated >= ?
ORDER BY time_updated DESC LIMIT 200
-- events (watcher orders by time_updated; metrics by time_created — both load-bearing)
SELECT p.id, p.data, p.time_updated, m.data as message_data
FROM part p JOIN message m ON p.message_id = m.id
WHERE p.session_id = ? AND p.time_updated >= ?
ORDER BY p.time_updated ASC, p.id ASC
```
Note the two paths order by **different columns** — dropping either fails asymmetrically (discovery works, metrics silently empty, or vice versa). Discovery is capped at **`LIMIT 200`** recently-updated sessions — silent truncation past that.

**Inside `part.data`**: `type ∈ {step-start, step-finish, text, tool}`; `reason`; `tokens.{input,output,total}`; `tokens.cache.{read,write}`; `cost`; `text`; `state.status ∈ {pending,running,completed,error}`; `callID`; `tool`; `state.input.todos[].{content,status}` for `tool == "todowrite"`.
**Inside `message.data`**: `role`, `model.modelID` (fallback top-level `modelID`), `error`.

#### Turn termination — three independent producers
1. **`step-finish`** with `reason ∈ {stop, interrupted, length, error, content-filter}` → `turn_done`. (`tool-calls` is the non-terminal reason.)
2. **`message.data.error`** non-empty — opencode emits **no** `step-finish reason="error"` for an aborted turn, only a bare `step-start`, so without this the session sticks in `working` until process exit (#493).
3. Known residual gap: a turn that errors before emitting **any** part has no row for the join to carry, and is not surfaced — the remaining half of #493.

⚠️ **Two things that make opencode uniquely fragile:**
- The terminal-reason set is **duplicated** in `parser.go:122-144` **and** `watcher.go:474-478`. An upstream reason rename must be patched in **both**.
- It is **100% dependent on `turn_done`** (see State Classification above) — so any break in the paths below is unrecoverable, not degraded.

#### Metrics
Per-step (not cumulative) tokens from `part.data.tokens`; `cost` is a top-level float on the part; model from `message.data.model.modelID`, defaulting to `"unknown"`. `CacheCreation` is accumulated into the contribution but has no `Cum*` field. `ComputeMetricsTimeline` returns nil for provider-backed adapters — **opencode has no metrics timeline**.

#### What breaks it
- Move/rename the DB, honor `$XDG_DATA_HOME`, shard per-project, or version-suffix it → blackout (the watcher waits on a path that never appears).
- Rename the `session` table or its columns → `querySessions` errors, is **logged and swallowed** → no sessions, no user-visible error.
- Rename the OS process (`opencode-tui`, or wrapping under `node`/`bun`) → **a perfectly good DB with zero detected sessions.**
- Move off WAL (e.g. back to a rollback journal): writes land in `opencode.db-journal`, matching **neither** filter arm. Main-DB writes still fire on some ops, so this degrades to **erratic rather than dead** — worse to diagnose.
- A `directory` value that isn't the process CWD (e.g. storing a project root while running from a subdir) → the `LiveCWDs` exact-string match misses → no session.
- Rename `state.status` or `callID` → tool calls open and never close → `HasOpenToolCall` blocks `IsAgentDone` even with a valid turn_done.

### 8. Antigravity (`antigravity`)

**One adapter covers both the CLI and the IDE**, and discovery is transcript-first.

- **Multi-root**: `~/.gemini/antigravity-cli/brain` (CLI, primary) **and** `~/.gemini/antigravity/brain` (IDE), via `FilesUnderRoot.ExtraDirs` → **two fswatcher instances**. Note the watched roots are the `brain/` dirs, one level *below* the product dirs. Rooting at the shared `~/.gemini` parent is **not an option** — it would collide with the Gemini CLI adapter's `~/.gemini/tmp`.
- **Transcript path**: `<root>/brain/<conv-id>/.system_generated/logs/transcript.jsonl`. The sibling **`transcript_full.jsonl`** (unfiltered view) is skipped by the exact-basename check — which is what keeps it one session per conversation.
- **Session ID is path-based** (the `<conv-id>` dir), because the default stem derivation would return the constant `"transcript"` for **every** session and collapse them all into one.
- **Config**: none read.
- **Discovery is transcript-first** — PID=0 sessions are first-class. But three process-side facts matter:
  - A process scanner always runs (`agent.ExactName{"agy"}`) and mints `proc-<pid>` **pre-sessions**, retired when the real transcript session for the same cwd appears.
  - `DiscoverPID` binds by process-name + cwd. No argv filtering — `agy` is a standalone native binary.
  - **`RequireKnownHost: true` — antigravity is the ONLY adapter that opts in** (#791, to exclude CodexBar's non-interactive `agy`). A session whose PID's ancestry doesn't resolve to a known terminal/IDE is **rejected outright and the rejection cached**. It **fails open when no PID is found** (which is what keeps IDE PID=0 sessions alive), and `IsKnownInteractiveHost` is **darwin-only** — other platforms return true.

#### The token/model store (tier (b))
- **Path**: `<root>/conversations/<conv-id>.db` — a **sibling SQLite database holding protobuf blobs**, *not* the transcript. `<conv-id>` is the join key; the path is resolved by climbing exactly **five** `filepath.Dir` levels, so **inserting one directory anywhere in the chain silently resolves to a wrong path**.
- **Query**: `SELECT data FROM gen_metadata ORDER BY idx DESC LIMIT 1` — table/column names hardcoded.
- **Decode**: a hand-rolled, dependency-free protobuf walker — **no `.proto` ships with Antigravity**, so the field numbers are reverse-engineered and documented as stable only "across the 0.5.x CLI":
  - `#1` → usage submessage; `#4`/`#5` varint → **prompt tokens (context occupancy)**; `#19` string → **canonical model id** (`gemini-3.1-pro-low`). `#21` (display name) is deliberately *not* read — only the dash form resolves in the capacity map.
- **Applied on `turn_done` only**; emits `Tokens.Total` **only** — `Input`/`Output`/`CacheRead`/`CacheCreation` all stay zero, and **cost is structurally absent**.
- **Cache key is `(mtime, size, walSize)`** — the WAL *size* is in the key because live `agy` writes land in the WAL before checkpoint; without it an active session's context bar freezes. Size (not mtime) avoids our own read-only opens invalidating the cache.
- **Cost/output/cache tokens are underivable — #716 closed not-planned.** The token block carries unlabeled, per-turn-varying varints with **no ground truth to validate a guess**: `agy --help` exposes no usage surface, `cli.log` has zero token counts, the transcript has no usage keys, and the blobs have no ASCII labels. `#5` is identifiable *only* because it grows monotonically with context fill. Guessing would surface plausible-but-wrong cost. **Reopen condition**: `agy` ships a usage surface, or publishes the `gen_metadata` proto. (The separable internal half — capturing/serving the `.db` in replay — shipped as #766.)
- Degradation is graceful: a missing/locked/unrecognized store leaves the event unchanged.

#### Transcript parsing dependencies
- **Dispatch** on `(source, type)`: `type == "USER_INPUT"` → user (**not gated on `source`**); `MODEL/PLANNER_RESPONSE` → assistant; `source == "MODEL"` (any other type) → **tool result**; else skip. All five `SYSTEM/*` types fall to skip — **including `SYSTEM/ERROR_MESSAGE`, which is silently dropped**.
- ⚠️ The `source == "MODEL"` catch-all is **fail-open**: a *new* MODEL type that isn't a tool result would be **misread as one**.
- **`turn_done` is heuristic**: a `PLANNER_RESPONSE` with `len(tool_calls) == 0` (usually empty content). **No idle-flush fallback** — if the terminal line never lands, the session never settles.
- **Tool calls**: `tool_calls: [{name, args}]` — those are the **only two keys**. **Upstream supplies no tool-call ID**, so IDs are synthesized as `"<step_index>-<i>"` under a strictly **sequential single-open-tool model**. Parallel tool execution would mis-attribute results.
- **Tool error detection is a literal English substring**: `"The command failed"`.
- **Model** comes *only* from a regex over a `<USER_SETTINGS_CHANGE>` block in USER_INPUT content (`Model Selection…from X to Y`), updated on **every** occurrence (mid-session `/model` switches). It's then superseded by the store's canonical id on turn_done — so **before the first turn_done, a session shows no model**.
- **CWD** comes only from a `run_command` call's `Cwd` arg, with the sandbox scratch dir rejected by a `Contains(".gemini/antigravity")` + `Contains("/scratch")` match. Fallback: the `<root>/history.jsonl` index (keys `conversationId`/`workspace`).
- **`created_at`** is parsed as **strict RFC3339**; anything else silently falls back to `time.Now()`.
- **Never read, despite being present**: `status` (on every line), `thinking`, `truncated_fields`.
- **User-blocking: none.** Plan-approval gates are live UI prompts, **never persisted to the transcript**, so no `waiting` episode is ever written. `waiting` can only come from generic text cues.
- **Subagent linking** depends on the parent writing the child's conv-id into its transcript, found by a **substring scan** — safe only because conv-ids are random UUIDs (non-UUID ids would false-positive). Bounded by three heuristic constants upstream timing could invalidate: a 2-minute spawn window, a 40-sibling scan cap, and a 256KB tail.

#### What breaks it
- Root move/rename (dropping the `-cli` suffix, moving out of `~/.gemini`, adding a third surface) → **zero sessions, silently**.
- Any path-layout change (`brain/`, `.system_generated/`, `logs/`, or the literal `transcript.jsonl`) → zero sessions, **and** cascades into the store path, cwd resolution, and subagent linking at once.
- Promoting `transcript_full.jsonl` to the primary view → zero sessions.
- **Protobuf schema change** → `decodeGenMetadata` returns zeroes, **not an error**: the context bar silently disappears and the model falls back to the *display* name, which doesn't resolve in the capacity map. **Highest-risk silent failure**: a field number reused for a *different* varint would surface **plausible-but-wrong token counts with no detection.**
- Prose changes to `"The command failed"` (tool errors become successes) or the `<USER_SETTINGS_CHANGE>` sentence shape (model never harvested). Localization breaks both.
- Renaming `agy` → the pre-session scanner, PID bind, and terminal-jump break (transcript sessions survive at PID=0).

### 9. aider (`aider`)

**Markdown, not JSONL — and the only adapter with no file-discovery path at all.**

- **Transcript**: **`.aider.chat.history.md`** — a Markdown chat history, living in the **aider process's CWD**, not under `$HOME`.
- **Source model**: `agent.FilesUnderCWD` — **the only adapter**. It carries a **basename only**; it structurally cannot express a path. **No root, no glob, and no fswatcher is constructed** (`wiring.go:71-75`). Discovery is a **stat poll of `<pid's CWD>/<filename>`** by the process scanner (1s, backing off to 5s when the PID set is stable): first sight → new session, size change → activity.
- ⚠️ **Consequence: if aider's process isn't matched, the transcript is never found**, no matter that it's sitting on disk. Every other adapter's watcher would still see the file. **aider's process matcher is a hard dependency of transcript detection**, not just of PID features.
- **Process**: `agent.CommandPattern{Regex: "/aider"}` over the full command line — aider's real OS process is `python` invoking a console script (**the same problem as vibe**), so `pgrep -x aider` finds nothing. The leading slash anchors to the binary path and excludes wrappers (tmux, sh) that merely mention "aider". The `CommandPattern` path bypasses `pgrep -x` entirely (`wiring.go:99-103`), so the adapter's `ProcessName` const is never matched against running processes.
- **Config**: ⚠️ **corrected (#1090): `~/.aider.conf.yml` is read nowhere in irrlicht** — zero matches repo-wide. It's purely how *the user* configures aider. **Corollary: if `> Model:` is absent from the transcript, irrlicht has no fallback model source for aider at all.**
- **Session ID = `proc-<pid>`** — **session identity is the aider process, not the file.** An append-only `.md` needs no session boundary because the boundary *is* the process lifetime. Aider's own `# aider chat started at …` delimiter is **explicitly discarded**.

#### Markdown parsing dependencies — the full break surface
Lines arrive `TrimSpace`'d, so leading indentation is already gone.

| Marker | Effect |
|---|---|
| `# aider chat started` (prefix) | ignored |
| **`"#### "`** (literal prefix, exactly 4 hashes + space) | **user message — opens the turn** |
| `^>\s*Aider v(\S+)` | agent version |
| `^>\s*(?:Main\s+)?[Mm]odel:\s*(\S+)` | model (`> Weak model:` deliberately excluded) |
| `^>\s*Tokens:\s*([\d.]+\s*[kKmM]?)\s*sent,\s*([\d.]+\s*[kKmM]?)\s*received(.*)$` | assistant message + usage |
| `\$([\d.]+)\s*message` | `ProviderCostUSD` (the `$… session` cumulative figure is ignored) |
| `^>\s*Applied edit to\s+` | tool_result named `Edit` |
| `^>\s*Running\s+` | tool_result named `Bash` |
| `^>\s*\S*(?:Error\|Exception)(?:[: ]\|$)` | turn_done (error abort) |
| any other `>` line | ignored |
| any non-`>`, non-`####` line | buffered as assistant prose |

⚠️ **Asymmetry worth knowing:** every `>` marker tolerates spacing via `\s*`, but **`"#### "` is a rigid `HasPrefix` with no regex and no tolerance**. Three or five hashes, or `####` without the trailing space, silently becomes assistant prose. **It is the most brittle string in the adapter, and it's the one that opens every turn.**

- **`turn_done` is synthesized**: aider is the **only** adapter implementing `idleFlusher`, at **1500ms**. ⚠️ 1500ms is a **floor, not the observed latency** — the SessionDetector polls working sessions every 5s, so the real working→ready delay is ~5s. A second in-band path (`flushErrorTurn`) covers LLM-layer errors, because aider emits no `> Tokens:` line on failure.
- **`> Tokens:` is deliberately NOT turn_done** — multiple model calls per `####` turn are normal under `--yes-always`.
- **Tokens**: `k`/`K` → ×1000, `m`/`M` → ×1e6. **Silently returns 0 on parse failure** — no error, no log.
- **No timestamps in the markdown** → every event is stamped `time.Now()` at parse time, so replayed/backfilled history buckets wrong.
- **Tool vocabulary is hardcoded to exactly `Edit` and `Bash`.** New tool types (web search, MCP) produce no events at all.
- **Gaps**: aider's parser does **not** implement `ResetForRotation` despite holding session-scoped state (`model`, `turnOpen`, `assistantBuffer`, `toolSeq`). And two aider processes in the **same CWD** yield two `proc-<pid>` sessions both tailing the **same** file → **duplicated tokens and cost**.

#### What breaks it
- Rename/move `.aider.chat.history.md`, or make it configurable to a non-CWD location → total detection loss.
- **Ship as a frozen/native binary, or any packaging where `/aider` leaves the command line** (PyInstaller, `python -m aider`, a renamed console script) → no PID → **no transcript probe at all** → session never appears. **No fswatcher fallback, unlike vibe.**
- Change the `####` heading level → every prompt becomes prose, `turnOpen` never opens, no turn lifecycle.
- Drop/reformat `> Tokens: … sent, … received` → kills tokens, cost, model attribution, **and** `assistant_message` in one stroke. Localization or thousands-separators (`1,234`) also defeat `[\d.]+`.
- ⚠️ **The subtlest one:** aider printing **periodic keepalive/spinner/progress lines** into the `.md` would reset the idle anchor every tick and **suppress `IdleFlush` indefinitely → permanent `working`**. This requires no change to any marker string, just extra output.

### 10. Gemini CLI (`gemini-cli`) — ⚠️ unmaintained

**As of 2026-07-15 this adapter is no longer actively maintained and is skipped in release sweeps** (this section is the authoritative statement of that; `SKILL.md` only points here). It still ships and still monitors live sessions, so it is listed for completeness — but upstream Gemini CLI changes are **not** tracked, and a break-risk analysis is deliberately out of scope.

- **Transcript path**: `~/.gemini/tmp/<project>/chats/session-<timestamp>-<8hex>.jsonl`
- **Adapter source**: `core/adapters/inbound/agents/geminicli/` — consult it directly if this adapter ever needs attention again.
- **Reopen condition**: someone resumes maintenance, or a user reports gemini-cli sessions misbehaving. Until then, an upstream Gemini change is not a finding.

> ⚠️ **#1068 is not the reason, and is not a deprecation notice** — don't cite it as either. It's a *watch item* about the native SEA binary possibly becoming default (which would break `DiscoverPID` and the heap-bump exclusion), closed as completed with the verdict "No impact today; no code change required". The maintenance decision is separate and out-of-band.

---

## What Breaks Irrlicht (Impact Categories)

| Category | Examples | Severity |
|----------|----------|----------|
| **Transcript path change** | Directory moved, nesting changed, file extension changed | CRITICAL — sessions not discovered |
| **Root relocation via a new env var** | Upstream ships `$VIBE_HOME`-style override for an adapter that hardcodes its root | CRITICAL — silent blackout; the watcher waits on a path that never appears |
| **DB schema change** (opencode, antigravity) | Table/column rename, protobuf field renumbering | CRITICAL — errors are logged and swallowed, or decode silently returns zeroes |
| **Transcript format change** | JSONL schema altered, event types renamed/removed | HIGH — state classification fails |
| **Turn-end shape change** | Trailing tool call on the final message, or a text-only message mid-turn | HIGH — the 4 heuristic adapters (vibe, kiro-cli, antigravity, gemini-cli) stick in `working` forever, or flicker to `ready` early. aider is spared the stick by its idle flush; the 4 explicit adapters are unaffected. |
| **Tool system change** | Tool names renamed, tool_use/tool_result structure changed | HIGH — waiting/subagent detection breaks |
| **Process change** | Binary renamed, wrapped under `node`/`python`, `setproctitle` adopted, CWD no longer accessible | HIGH — PID tracking fails; **total loss for aider** (no file fallback) and **opencode** (live-process discovery gate) |
| **Prose/marker rewording** | `"The command failed"`, `"Request cancelled"`, `> Applied edit to`, `#### ` | HIGH — English-literal dependencies; localization breaks them silently |
| **Sidecar/store schema change** | vibe `meta.json`, kiro `<uuid>.json`, antigravity `.db` | HIGH — model/tokens/cwd vanish silently; **cwd loss also kills PID binding** |
| **File rotation without shrink** | In-place rewrite at same-or-larger size, inode swap at the same path | HIGH — silently mis-tailed; the tailer is size-only, with no inode awareness |
| **Config path/format change** | Settings file moved or reformatted | LOW — only affects model fallback, and only for claude-code / pi / codex |
| **New session type** | New agent mode, new transcript location | MEDIUM — sessions invisible until adapter added |
| **New tool category** | New user-blocking tools added | MEDIUM — inert until aliased into `AskUserQuestion`/`ExitPlanMode`/`question` |
| **Permission system change** | New permission modes, removal of permission-mode events | MEDIUM — `PermissionMode` surfacing affected |
| **CLI output change** | Gas Town `gt` command output format changed | HIGH — orchestrator polling breaks |
| **Timestamp format change** | RFC3339 → epoch millis, etc. | MEDIUM — silently falls back to `time.Now()`; corrupts elapsed/rate metrics and replay determinism |
| **New model id** | Model not in `capacity/aliases.go` | LOW — prices at $0; fix via `/ir:refresh-aliases` |
