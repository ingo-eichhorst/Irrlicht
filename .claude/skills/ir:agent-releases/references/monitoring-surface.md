# Irrlicht Monitoring Surface

Irrlicht is a daemon that monitors coding agent sessions. It watches transcript files and processes to classify sessions into the states declared by `session.CanonicalStates()` (`core/domain/session/session.go`) — currently **working**, **waiting**, **ready**, **error**. Any upstream agent change that alters the items below can break detection.

This file exists to brief a release-sweep analysis (`/ir:agent-releases`) on what it should actually look for. `core/adapters/inbound/agents/` ships **twelve agent adapters** (`all.go`'s `All()` — claude-code, codex, pi, aider, opencode, kiro-cli, gemini-cli, antigravity, mistral-vibe, copilot, hermes, junie), and this file has **eleven numbered sections**: ten of those twelve adapters **plus the Gas Town orchestrator**, which is a different layer entirely (`core/adapters/inbound/orchestrators/gastown` — it polls a CLI, has no `Source` variant, and so is absent from the discovery table below). **Copilot and Hermes have no section of their own yet** — they appear only in the inline tables below, so a release sweep must not treat "no section" as "not monitored".

*(#1090's title says "4 of 10 agent adapters" and its body lists nine names "+ the gastown orchestrator". At that time: nine adapters + one orchestrator = ten sections; there were not ten agent adapters. Noted because the miscount is exactly the kind of thing this file is supposed to stop — and it recurred. Per `git log -p` on this file, the count was right at "ten sections / nine adapters"; copilot (#1256) and hermes (#1325) then each incremented it **without adding a section**, and junie inherited that accumulated drift (it did add `### 11`), leaving a claim of thirteen against a file holding eleven. Count with `grep -cE '^### [0-9]+\.'` on this file instead of incrementing the previous number.)*

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
| `agent.FilesUnderRoot` | claude-code, codex, pi, gemini-cli, mistral-vibe, kiro-cli, antigravity, copilot, junie, muse, dsh | fswatcher over a `$HOME`-relative root |
| `agent.FilesUnderCWD` | aider | **No watcher at all** — the process scanner stat-polls `<pid's CWD>/<filename>` |
| `agent.ProcessOwnedStore` | opencode, hermes | Dedicated SQLite watcher; full tailer bypass |

`core/domain/agent/source.go`; wired in `core/cmd/irrlichd/wiring.go` (whose `ProcessOwnedStore` arm dispatches on adapter name — opencode and hermes each bring their own store watcher — and **panics** for any store adapter not listed there).

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
| **(a)** Tailer only | claude-code, codex, pi, gemini-cli, aider, copilot, junie | No second file feeds the parser. (claude-code is still not *purely* transcript-driven — its hooks deliver `PermissionPending`/`CompactInProgress` out-of-band, and `settings.json` supplies the model fallback.) |
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

**Adapters that reach `waiting` only via text heuristics, never via an open tool:** mistral-vibe (its `exit_plan_mode` IS now in the shared list as of #1256, but vibe flushes per-turn so the open-tool window never exists on disk — see its section below), kiro-cli (lowercase tool names — #588), antigravity (plan gates are live UI, never persisted), aider. For these, an upstream *addition* of a plan-approval gate is a non-event until the name is aliased in.

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

The single highest-value thing to know per adapter. **Six adapters get an explicit signal from upstream; the rest infer it**, and every inference has the same two failure modes: a trailing text-only message mid-turn fires a **premature ready**, and a turn ending on a tool call **sticks in `working` forever**.

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
| copilot | **explicit** — `assistant.turn_end`. Note copilot closes the turn after *every* tool call and immediately opens a continuation, so a turn_end is only terminal when no `assistant.turn_start` follows without an intervening `user.message` |
| aider | *synthesized* — `idleFlusher` after 1500ms idle; the only adapter |
| junie | **explicit** — `TaskState`, written once when the task finalizes (after failures too — state `COMPLETED` follows `AgentTaskFailedEvent`), so it is the turn boundary, not a success verdict |

**There is no inactivity sweep on `working`** for the heuristic adapters (except aider's idle flush) — a session that never emits its terminal line stays `working` indefinitely. That is why the heuristic adapters are the ones to scrutinize on any upstream turn-shape change: the explicit adapters degrade loudly, the rest degrade silently.

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
- **`turn_done` event**: primary signal that agent finished its turn — one of the six adapters with an explicit upstream marker (see the marker-vs-heuristic table above)
- **Tool call structure**: `tool_use` blocks with `name` field; matched against `tool_result`
- **User-blocking tools**: `AskUserQuestion`, `ExitPlanMode` — trigger immediate waiting state
- **`is_error` on tool_result**: indicates ESC/rejection (maps to ready state)
- **`permissionMode` field**: passthrough only, no classifier branching — **new or renamed values are non-events** (v2.1.200 renamed "default" to "manual"; nothing noticed). Census across 320 local transcripts, v2.1.210, 2026-07-15.
- **Assistant text**: last assistant message checked for trailing `?` (waiting heuristic)
- **Token/cost fields**: `input_tokens`, `output_tokens`, `cache_read_tokens`, `cache_creation_tokens`
- **Model name field**: normalized (e.g., `sonnet` -> `claude-sonnet-4-6`)
- **Hooks**: the `PermissionRequest`/`PostToolUse`/`PostToolUseFailure` matcher (`hookinstaller.go`, `hookMatcher`) is match-all since #1861 — it used to be a nine-entry tool allowlist, which silently dropped a permission prompt on any tool outside it, so **a new tool name is a non-event now and was a regression before**. The `Notification` matcher (`hookMatcherNotification`) is an explicit `notification_type` allowlist, so **a new type meaning "a human is blocked" IS an event and needs adding there** — diff it against the bundle's own type list, not against dialog names, which are a different vocabulary. Read both current values from the constants rather than from this line: they are pinned by tests (`TestHookMatcher_TakesClaudeCodesLiteralShortCircuit`, `TestNotificationTypesDrivingState_MatchesTheInstalledMatcher`) and a value copied into prose here drifts silently. Since #1178 the hook endpoint follows the daemon's own `IRRLICHT_BIND_ADDR`, so hook-delivered observations *can* be recorded against a dev daemon on an alt port.

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
- **User-blocking: unreachable.** Vibe's tool is `exit_plan_mode` (snake_case). It was absent from the hardcoded list until #1256 added it (for GitHub Copilot, which uses the same name); that alias does not help vibe, because vibe flushes `messages.jsonl` **per-turn, not per-message** — the call and its result land on disk together, *after* the user already answered, so the open-tool window never exists on disk.
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

The odd one out twice over: **SQLite-backed**, and the **only full tailer bypass**. One adapter covers **both majors** of opencode: it reads the 1.x schema (`session`/`message`/`part`) and the 2.x schema (`session_v2`/`session_message`), and installs one of two hook plugins. The 2.x support landed as #2104's phases P1–P4 (#2112, #2113, #2114, #2115); the 2.x material is in the subsections headed "opencode 2.x". Everything else in this section applies to both majors unless it says 1.x.

- **Storage**: ⚠️ **corrected (#2109) — `$XDG_DATA_HOME` IS honored since #2106.** `StorePath()` (`adapter.go:83-92`) resolves the store the way opencode does:
  - `$OPENCODE_DB` when set: an absolute value as is, a relative one inside the data dir, and `:memory:` falls back to the default;
  - else `opencode.db` in the data dir: `$XDG_DATA_HOME/opencode` (absolute values only, through `agentpaths`; `dataDir`, `:100-107`), else `~/.local/share/opencode`.
  - A non-stable release channel's `opencode-<channel>.db` is **not** followed (`:80-82`).
  - The watcher and the hook receiver both key sessions through this one resolver. Driver: `modernc.org/sqlite` (pure Go, no CGo).
- **Schema switch, on every scan.** `storeIsV2` (`watcher.go:413-419`) asks `sqlite_master` for a `session_v2` table. With it the 2.x reader runs (`scanV2`), without it the 1.x reader (`watcher.go:338-345`); `ComputeMetrics` makes the same choice (`metrics.go:39-50`).
  - **The 1.x reader never runs against a 2.x store** (#2105, `watcher.go:372-385`). A fresh 2.x store has no 1.x tables, and one 2.x migrated keeps them frozen.
  - The schema is re-read on every scan because a running daemon sees a 1.x store turn into a 2.x one the moment opencode 2.x first opens it. A switch logs once and drops every cursor (`:392-404`). A schema lookup that fails skips that scan.
- ⚠️ **corrected (#1090) — "WAL-watched" is misleading.** fsnotify watches the **parent directory** (`watcher.go:220-223`) and accepts a `Write` whose basename is the store's **or** the store's plus `-wal` (`isStoreWrite`, `:281-287`). **The WAL's contents are never read** — there is no frame parsing. The `-wal` suffix survives only as (a) an fsnotify trigger name and (b) a routing sentinel baked into `TranscriptPath` (`transcriptPathFor`, `adapter.go:117-119`).
- ⚠️ **corrected (#2109) — the DSN.** Every read opens the **main DB** through `core/pkg/sqlitero` (`file:<path>?mode=ro&_timeout=500`, `sqlitero.go:82-86`). The `?mode=ro&_journal=WAL&_timeout=500` this line used to quote carried `_journal=WAL`, which is a write (`sqlitero.go:68-75`).
- ⚠️ **"Polling" is a misnomer** despite the package doc saying so. The steady state is **event-driven**, at most one scan per 500 ms (`defaultMinScanGap`, `watcher.go:21`). The gap breaks the feedback loop in which irrlicht's own read-only open touches `.db-shm`.
  - Since #2106 **one armed timer** runs every scan after the first (`watcher.go:225-266`), so a write inside the gap is scanned when the gap ends. Before that, such a write was dropped until some later write, and a turn end reached the daemon 8.7 s and 17 s late in the two live runs that comment records.
  - The only real ticker is a 5 s wait (`pollForDB`, `watcher.go:810-826`). It starts **only when the data directory is missing at startup**, and ticks until the database file exists. When the directory exists but the database does not, `waitForDB` waits on fsnotify instead (`:776-802`).
- **Change detection** = fsnotify event → debounced **full SQL re-query**. No diffing.
- **Config**: ⚠️ **corrected (#2109) — opencode's own config is never read, but the adapter does read two files.** It writes and re-reads **its own plugin** (`hookinstaller.go`, behind `hooks`), and it reads the `version` of the newest session in the store to choose that plugin (`storeversion.go`, behind `database`). Both are covered in the 2.x subsections.
  - *Footnote:* `~/.local/share/opencode/auth.json` **is** read elsewhere (`readOpenCodeInheritKey`, `quotainherit.go:413-430`) for quota inheritance: only the `openai-oauth` block's type and the account-id claim decoded from its access token. ⚠️ **corrected (#2109):** the `anthropic-oauth` block is **not** read, because it carries no account id. The path is `$HOME`-relative and does **not** follow `$XDG_DATA_HOME`. It **does not affect detection or classification**.
- **Process match**: ⚠️ **corrected (#2109) — no longer `ExactName{"opencode"}` / `pgrep -x`.** Since #2106 it is an `agent.CommandPattern` over the whole command line, anchored on argv[0]: `^([^ ]*/)?opencode(2|\.exe)?( |$)` (`processCmdPattern`, `adapter.go:29-51`). It matches `opencode` (Homebrew), `opencode2` (the npm package's bin) and `opencode.exe` (that package's binary).
  - It is anchored because an unanchored pattern matched driver scripts that merely named the binary. It uses `( |$)` because macOS `pgrep` has no `\s`. The cost: an argv[0] containing a space does not match.
- **Clients and the 2.x service.** Of the matched processes, `IsServiceArgv` (`serviceargv.go:22-36`) picks out opencode 2.x's background service: `serve` is the first positional token after argv[0], and `--service` follows it before any `--`. It skips the values of `--server`, `--session`/`-s`, `--prompt`, `--log-level` and `--completions`, so `opencode run serve --service` is a client. Every other match is a **client** (a TUI or `opencode run`).
  - The service is declared as `Process.SessionHostArgv` (`agent.go:54-67`), so the scanner mints **no `proc-<pid>` row** for it.
- **Discovery gate (1.x)**: `maybeEmitNewSession` **refuses to emit a session unless a live client owns that session's `directory`** (`watcher.go:500-512`). Otherwise every non-archived DB row would re-emit as live on each daemon start. **No live client ⇒ no 1.x session, regardless of DB content.** The 2.x gate is different; see the store-reader subsection.
- **PID discovery**: `DiscoverPID` (`pid.go:24-31`) looks first for a **client** whose cwd is the session's directory (`DiscoverPIDByCWDAndCmdLineExcludingArgv`, the service excluded), else the **2.x service** (`servicePIDFor`). It is deliberately not lsof-based, because opencode opens and closes the DB for each write.
  - Known trade-offs: two clients in the same CWD are indistinguishable, and a client-less session in a directory a client runs in binds to that client (`pid.go:20-23`).
- **Orphan reaping** uses "no process of this adapter owns the CWD" rather than mtime staleness, since a shared WAL makes mtime meaningless.
  - Known residual (#2106, `agents/maps.go:93-101`): the startup zombie sweep matches `pgrep -x opencode`, so it counts the 2.x service and misses an `opencode2` client.

#### Schema dependencies (1.x) — the break surface
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

#### Turn termination (1.x) — three producers, one gap
1. **`step-finish`** with `reason ∈ {stop, interrupted, length, error, content-filter}` → `turn_done`. (`tool-calls` is the non-terminal reason.)
2. **`message.data.error`** non-empty — opencode emits **no** `step-finish reason="error"` for an aborted turn, only a bare `step-start`, so without this the session sticks in `working` until process exit (#493).
3. **The hook plugin's `session.idle`**, when `hooks` is granted (`hooks.go:289-305`): it releases any permission hold and reports an authoritative turn end. See the hook-plugin subsection.

Known residual gap: a turn that errors before emitting **any** part has no row for the join to carry, and is not surfaced by the store — the remaining half of #493.

⚠️ **Two things that make opencode uniquely fragile:**
- On 1.x the terminal-reason set is **duplicated** in `parser.go:140-172` **and** `watcher.go:613-626` (`isTerminalPart`). An upstream reason rename must be patched in **both**. (2.x has no such pair: both paths key on the one `idle` constant.)
- It is **100% dependent on `turn_done`** (see State Classification above), and so is the 2.x branch, whose steps are all `assistant_message`. Any break in a turn-end path is unrecoverable, not degraded.

#### Metrics (1.x)
Per-step (not cumulative) tokens from `part.data.tokens`; `cost` is a top-level float on the part; model from `message.data.model.modelID`, defaulting to `"unknown"`. `CacheCreation` is accumulated into the contribution but has no `Cum*` field. `ComputeMetricsTimeline` returns nil for provider-backed adapters — **opencode has no metrics timeline**.

#### opencode 2.x: the store reader (#2106, #2107)
`watcher_v2.go`, `parser_v2.go`, `metrics_v2.go`. Two tables. Any rename/drop of a listed column is a hard break.

| Table | Columns read |
|---|---|
| `session_v2` | `id`, `directory`, `time_updated`, `parent_id`, `time_archived` (watcher); `model`, `cost`, `tokens_input`, `tokens_output`, `tokens_cache_read`, `tokens_cache_write` (metrics); `version`, `time_created` (the hook version gate) |
| `session_message` | `id`, `session_id`, `type`, `data`, `time_updated` (watcher and metrics); `seq` (metrics order) |

```sql
-- discovery (watcher_v2.go:69-76)
SELECT id, directory, time_updated, parent_id FROM session_v2
WHERE time_archived IS NULL AND time_updated >= ?
ORDER BY time_updated DESC LIMIT 200
-- activity (watcher_v2.go:138-144): the cursor is (time_updated, id)
SELECT id, type, time_updated FROM session_message
WHERE session_id = ? AND time_updated >= ?
ORDER BY time_updated ASC, id ASC
-- metrics (metrics_v2.go:27-44): totals from the session row, the rest folded in seq order
SELECT directory, model, cost, tokens_input, tokens_output, tokens_cache_read, tokens_cache_write
FROM session_v2 WHERE id = ?
SELECT type, data, time_updated FROM session_message WHERE session_id = ? ORDER BY seq ASC
-- hook version gate (storeversion.go:51-52)
SELECT version FROM session_v2 ORDER BY time_created DESC, id DESC LIMIT 1
```
Again the two paths order by different columns: the watcher by `time_updated`, metrics by `seq`. Discovery keeps the **`LIMIT 200`** cap.

- **Row types.** `session_message.type ∈ {user, assistant, idle}`: one row per prompt, one per assistant **step**, and one `idle` row per turn end (`parser_v2.go:11-37`). No 1.x part type uses these names, so the row's type alone picks the parser branch (`parser.go:67` tries `parseV2Message` first).
- **Assistant rows are rewritten in place** while the step streams: same id, same `seq`, a later `time_updated` (36 of 36 rows on the sandbox store, `watcher_v2.go:11-20`). That is why the activity cursor is `(time_updated, id)` and not `seq`.
- **Inside `data`** (`parser_v2.go:17-25`, `:71-173`):
  - `user`: `text`.
  - `assistant`: `model.id`; `content[]` items of type `text` (`text`) and `tool` (`id`, `name`, `state.status ∈ {streaming, running, completed, error}`); `tokens.{input,output,reasoning}` and `tokens.cache.{read,write}`; `error`.
  - `idle`: `outcome ∈ {succeeded, interrupted, failed}`; `error` (`type`, `message`, `status`).
- **Turn end = the `idle` row**, whatever its outcome (`turn_done`). It also closes any tool the last stored step still showed running. A `failed` outcome becomes the session's error, taken from the idle row's own `error`, else from the last step's (`parser_v2.go:121-163`). The watcher marks a scan terminal on the same constant (`typ == v2TypeIdle`, `watcher_v2.go:156`).
- **Admission is on activity, not on first sight** (`watcher_v2.go:22-34`, `:98-131`).
  - The first scan that sees a session only seeds its cursor. The session is emitted when a `session_message` row is inserted or rewritten **after** that, and only while `admissionOnce` (`:86-96`) answers yes: **a 2.x service is running**, or **a live client owns the session's directory**.
  - Consequences: a migrated 1.x row, never written again, is never admitted; a daemon start does not admit the up to `maxAge` (5 days) of sessions the store lists; and after a daemon restart the reader does not re-admit an idle 2.x session until its next write.
  - What a user sees after a restart differs by session. One the daemon was already tracking is restored from the daemon's own saved state (`seedFromDisk`, `session_detector_lifecycle.go:506-520`) and kept while its PID lives (`SeedPIDs`, `pid_manager.go:2092-2096`); #2114's live QA saw restored service-bound rows last until the TTL. **One created while the daemon was down stays hidden until its next write.**
- ⚠️ **`time_suspended` is not a close** (`watcher_v2.go:36-42`). 2.x set it on a TUI's session 8 ms after creating it, held it through the turn and cleared it at the idle. A reader that skipped suspended rows lost every TUI session for exactly the span it was working. Only an **archived or deleted** row is gone: `emitRemovedForGoneV2Sessions` checks each tracked session the scan did not list (`:167-197`).
- **Metrics.** Token totals and cost are `session_v2`'s **own columns**, not a sum of the steps, because they also count calls no step row carries: three sandbox steps summed to 2219 input tokens where the session row said 2254, the difference being the title call (`metrics_v2.go:13-20`). The model comes from the steps, else from the `id` in `session_v2.model` (`:65-71`). 2.x reports no per-step total, so a step's `Total` is the sum of its five counts (`parser_v2.go:165-173`).

#### opencode 2.x: the shared service (#2107)
- **One service per user.** 2.x runs a background service, `opencode serve --service`, whose cwd is `$HOME` (`serviceargv.go:3-7`). The stable service binds a fixed port, 49374, which lets only one run at a time (`pid.go:36-38`).
  - irrlicht identifies it **by argv alone** (`IsServiceArgv`). It reads neither the port nor `service.json`, which carries the service's password and is covered by no consent (`pid.go:44-45`).
- **Who owns a session.** A session a client runs binds to that client. A session **no client runs** — one made through the service's HTTP API — lives inside the service and binds to the **service's PID** through `servicePIDFor` (`pid.go:46-55`), and only while its `session_v2` row is live (`v2SessionLive`, `:76-88`).
  - With **more than one** `serve --service` running (another release channel, say), no row says which hosts a session, so it binds **none** rather than guess.
  - **Desktop-app and web-app sessions are assumed to be client-less too.** The code says so (`watcher_v2.go:25-26`, the consent copy at `agent.go:93-96`), but their process model is **unmeasured** (#2114's residue).
- **Shared PID.** `OwnsSharedPID` (`pid.go:57-70`) is the adapter's `Process.SharedPIDOwner` (`agent.go:49-53`): the service PID is shared by any number of client-less roots while their rows live, and an archived or deleted one gives it up. **A client's PID is never shared**, so two roots on one TUI keep 1.x's exclusive supersession.
  - PIDManager runs it wherever it would otherwise retire a same-PID duplicate: at PID assignment (`pid_manager.go:1064-1066`) and in the seed-time and periodic dedupes (`:2781`, `:3211-3212`). A retirement on an adapter that declares a `SharedPIDOwner` is recorded as `transcript_removed` (`retireSamePIDDuplicate`, `:2805-2808`).
  - A **service exit ends every root bound to it**: `HandleWatcherExit` fans out to every holder of the PID (`pid_manager.go:745-760`).
- **How a service-bound session ends.** The service outlives every session it hosts, and a client detaching writes nothing distinctive to the store, so its live PID is no evidence a session is open.
  - The **ready-session TTL** is the end rule: 30 min by default, `IRRLICHT_READY_SESSION_TTL` to change it (`core/domain/config/config.go:25`, `:194`).
  - The mechanism: `idlesOnHost` (`pid_manager.go:1947-1966`) withdraws the live-PID protection from a root bound to its adapter's `SessionHostArgv` host when the adapter declares no `ReleasedPID`; `reapsIdleTopLevel` (`:1934-1942`) then ends it once it has been `ready` (or PID-less) past the TTL. A busy service-bound root is not reaped. Client-bound roots keep the protection.
  - An archived or deleted row also ends it, through the store reader.
  - A service-bound root captures **no launcher**: the service's environment is whichever client spawned it (`agent.go:61-66`).

#### Hook plugin — one file, two renderings (#1719; 2.x: #2108)
- **Path**: `<config>/plugin/irrlicht.js`, where `<config>` is `$XDG_CONFIG_HOME/opencode` (absolute values only), else `~/.config/opencode` (`hookinstaller.go:362-391`). `$OPENCODE_CONFIG_DIR` is deliberately not consulted: it **adds** a directory, and the default one is scanned regardless (`:371-378`). opencode's own config file is never touched.
- **Consent**: written behind `hooks` (`KindModify`). The receiver also needs `database` before it reads a body (`hooks.go:226-242`).
- **Delivery**: address-free. The file runs `irrlichd hook-post opencode` and writes `{hook_event_name, session_id}` to its stdin, plus `event_id` from the 2.x plugin; the beacon reads the daemon's published address at fire time (`plugin_v2.js:41-47`, `:101-114`). The receiver drops any session id without opencode's `ses` prefix (`hooks.go:107-112`, `:249-255`) and composes the session key itself (`transcriptPathFor`), never from the body.

| | 1.x `plugin.js` | 2.x `plugin_v2.js` |
|---|---|---|
| Runs in | every opencode session | the shared service, **once per project directory it has open** (`plugin_v2.js:24-30`) |
| Forwards | `permission.asked`, `permission.replied`, `session.idle` (`hookinstaller.go:155-159`) | `permission.asked`, `permission.replied`, `session.execution.succeeded`, `session.execution.failed`, `session.execution.interrupted` (`hookinstaller.go:205-211`, `plugin_v2.js:58-64`) |
| Turn end | `session.idle` | `session.execution.*`; 2.x published **no** `session.idle` in any turn #2104 drove (measured; `hookinstaller.go:174-178`) |
| Repeats | none; no `event_id` sent | every event arrives **once per instance**; the plugin forwards opencode's own event id as `event_id` |

- **Dispatch** (`hooks.go:282-313`): `permission.asked` → `HandlePermissionPromptHook` (waiting); `permission.replied` → `ReleasePermissionPromptHold`; `session.idle` or any `session.execution.*` → release the hold, then `HandleStopHook` (an authoritative turn end). Any other name goes to `IgnoreUnknownEvent`, counted and reported once.
- **Dedupe by `event_id`** (`hooks.go:169-212`, `:257-277`): a ring-backed set of the **1024** most recent ids. An id longer than **128** bytes is dropped, not remembered (opencode 2.1.4's are 30). It applies only when `event_id` is set, so a 1.x payload is dispatched every time. It runs **before** dispatch, so a repeated `permission.asked` arriving after its reply cannot hold the session in waiting again.
- **Why the hook matters**: a pending permission request is held in memory and **never written to the store** (`hooks.go:34-42`, read from the 1.x bundle; for 2.x, measured in #2104 item 6 and not re-derived from code here). Without `hooks` granted there is **no waiting at all**, and a turn ends only on the store's own signal.
- **Excluded on purpose** (`hookinstaller.go:126-154`, `:196-205`): the `permission.ask` *hook*, whose output can answer the prompt; `session.status`; `tool.execute.before`/`.after`; `message.part.updated`. For 2.x also `session.execution.started`, because the store's rows already open the turn, and **`form.*`**, an unverified surface.
- `session.execution.failed` was **not observed live**; its name was read out of the 2.1.4 bundle (`hookinstaller.go:184-189`).

#### opencode 2.x: the version split — which plugin, if any (#2105, #2108)
Declared as `ManagedUserFile.Variants`, one `[Min, Max)` range per artifact, inside an envelope `VersionGate` of `[1.14.50, 3.0.0)` (`agent.go:126-159`). Selection is `SelectVariant` (`core/domain/agent/declaration.go:550-603`).

| Installed opencode | What is written | Bound (`hookinstaller.go`) |
|---|---|---|
| below 1.14.50 | nothing | `minOpenCodeVersion` (`:236`) |
| 1.14.50 up to, not including, 2.0.0 | the 1.x `plugin.js` | `maxOpenCodeVersion` = 2.0.0 (`:248`), P1's ceiling |
| 2.0.0 up to, not including, 2.1.4 | nothing; an earlier file of ours is removed | `minOpenCodeV2Version` = 2.1.4 (`:254`) |
| 2.1.4 up to, not including, 3.0.0 | the 2.x `plugin_v2.js` | `maxOpenCodeV2Version` = 3.0.0 (`:260`) |
| 3.0.0 and up | nothing; an earlier file of ours is removed | the envelope ceiling |
| unreadable | a current install is kept; otherwise nothing | `VersionUnknown` |

- **P1's ceiling** (#2105). 2.0.0 is the first version known to reject the 1.x plugin: 2.x logs `Plugin must export a default definition with an id and an effect or setup function` for it on every load (`hookinstaller.go:238-248`). At or above a ceiling, `runClosureEffect` writes nothing and removes only a file carrying irrlicht's sentinel (`permission_service.go:959-966`, `removeAfterCeilingRefusal`, `permission_version_gate.go:208`).
- **Rewrite in place.** Both renderings share the path, the sentinel and the beacon line, so each reads the other's file as **ours but stale** (`hookinstaller.go:293-299`, `:419-429`). The #1372 re-verify loop rewrites a 1.x file under a 2.x opencode within one pass (`VerifyHooksInstalledV2`, `:473-478`), and 2.x hot-reloads it.
- **Where the version comes from** (`resolveInstalledVersion`, `permission_version_gate.go:137-169`):
  - First the store: `newestObservedCLIVersion` (`storeversion.go:33-45`) reads the `version` of the newest-**created** session in `session_v2`, else in `session`. It orders by creation because a 2.x migration imports 1.x sessions with their old version and timestamps. It is read only while `database` is granted (`ObservedRequires`, `agent.go:134-140`).
  - Then, only when that answer does not parse or selects no variant, the probe `opencode --version` (`shouldConfirmByProbe`, `:254-263`). For a variant declaration the probe can only **raise** the store's answer, never lower it (`:152-166`).

#### opencode 2.x: known limits a user will notice
- **Waiting lingers 6–11 s after a permission reply.** This is the cross-adapter #1366 waiting-exit dwell: `waitingExitDwell` is 6 s (`core/domain/session/state_dwell.go:28`), plus up to one 5 s `staleWorkingRefreshInterval` (`session_detector.go:41`). Only the waiting→working edge is debounced (`state_dwell.go:101-106`).
  - So a post-reply turn shorter than the dwell goes straight from waiting to ready. That was **measured** in #2115's live run (turns ending 3.4 s and 4.6 s after the reply), not derived from code.
- **Session forms (`form.*`) are not forwarded**, so a form that blocks the user does not show as waiting.
- **The desktop and web-app process model is unmeasured.** Those sessions are assumed to be client-less.
- **A session appears on its first write after the watcher first sees it, not at creation**, and a session created while the daemon was down stays hidden until its next write. One the daemon was already tracking is restored from its own saved state.

#### What breaks it
- Move/rename the DB **outside `StorePath`'s rules** (e.g. a release-channel `opencode-<channel>.db`, per-project sharding, a version suffix) → blackout (the watcher waits on a path that never appears).
- Rename the `session` table or its columns → `querySessions` errors, is **logged and swallowed** → no 1.x sessions, no user-visible error.
- Rename the executable so argv[0] is no longer `opencode`, `opencode2` or `opencode.exe` (`opencode-tui`, or running under `node`/`bun`) → **a perfectly good DB with zero detected sessions.**
- Move off WAL (e.g. back to a rollback journal): writes land in `opencode.db-journal`, matching **neither** filter arm. Main-DB writes still fire on some ops, so this degrades to **erratic rather than dead** — worse to diagnose.
- A `directory` value that isn't the client's CWD (e.g. storing a project root while running from a subdir) → the live-client exact-string match misses → no 1.x session; on 2.x the session is admitted only while a service runs, and then binds to the service rather than the client.
- Rename `state.status` or `callID` (1.x) → tool calls open and never close → `HasOpenToolCall` blocks `IsAgentDone` even with a valid turn_done.
- **2.x:** drop or rename `session_v2` → `storeIsV2` answers false and the **1.x reader runs**: on the frozen 1.x tables of a migrated store, or on no tables at all in a fresh one.
- **2.x:** rename `session_message` or its `type` values, or stop writing the `idle` row → no store turn end. With `hooks` granted, the `session.execution.*` hook still ends the turn.
- **2.x:** change the service's argv (`serve --service`) → `IsServiceArgv` misses it: the service is treated as a client in `$HOME`, gets a `proc-<pid>` row, and client-less sessions find no service to admit or bind them.
- **2.x:** a second `serve --service` running beside the first → client-less sessions bind to no PID (`pid.go:46-55`).
- **2.x:** a plugin-format change, or renamed bus events → the plugin loads nothing or forwards nothing, and waiting disappears silently, because the store has none. From 3.0.0 nothing is written, by design.
- **2.x:** `session_v2.version` no longer written → plugin selection falls back to the `opencode --version` probe.

### 8. Antigravity (`antigravity`)

**One adapter covers both the CLI and the IDE**, and discovery is transcript-first.

- **Multi-root**: `~/.gemini/antigravity-cli/brain` (CLI, primary) **and** `~/.gemini/antigravity/brain` (IDE), via `FilesUnderRoot.ExtraDirs` → **two fswatcher instances**. Note the watched roots are the `brain/` dirs, one level *below* the product dirs. Rooting at the shared `~/.gemini` parent is **not an option** — it would collide with the Gemini CLI adapter's `~/.gemini/tmp`.
- **Transcript path**: `<root>/brain/<conv-id>/.system_generated/logs/transcript.jsonl`. The sibling **`transcript_full.jsonl`** (unfiltered view) is skipped by the exact-basename check — which is what keeps it one session per conversation.
- **Session ID is path-based** (the `<conv-id>` dir), because the default stem derivation would return the constant `"transcript"` for **every** session and collapse them all into one.
- **Config**: none read.
- **Discovery is transcript-first** — PID=0 sessions are first-class. But three process-side facts matter:
  - A process scanner always runs (`agent.ExactName{"agy"}`) and mints `proc-<pid>` **pre-sessions**, retired when the real transcript session for the same cwd appears.
  - `DiscoverPID` binds by process-name + cwd. No argv filtering — `agy` is a standalone native binary.
  - **`RequireKnownHost: true` — antigravity is the ONLY adapter that opts in** (#791, to exclude CodexBar's non-interactive `agy`). A session whose PID's ancestry doesn't resolve to a known terminal/IDE is **rejected outright and the rejection cached** — and never evicted, so a wrong rejection lasts the daemon's lifetime. It fails open in three places, not one: when **no PID is found** (which is what keeps IDE PID=0 sessions alive), when the ancestry walk **could not be completed** (#1513 — an unreadable/timed-out `ps`), and when the walk's **bundle-id probe was never answered** (#1524 — a `plutil` over its own 2s ceiling). Only a walk that RAN and found no allow-listed host still rejects; that is #784 and it is unchanged. `IsKnownInteractiveHost` is **darwin-only** — other platforms return true.

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

### 10. Gemini CLI (`gemini-cli`) — ⚠️ scheduled for deprecation in irrlicht v0.7

> **Deprecation scheduled 2026-08-20 (at v0.5.10), effective v0.7.** The maintainer's account is
> tier-ineligible — `IneligibleTierError: This client is no longer supported for Gemini Code
> Assist for individuals… migrate to the Antigravity suite of products` — so no live turn, and
> therefore no re-recording, is possible on this machine. The adapter is not broken and did not
> fail: #1717 shipped its hook channel the same day and every gate passes. It is being retired
> because it cannot be **verified**, which is a different claim and the honest one. Sweeps
> continue as described below until v0.7. Reversible: `gemini-api-key` auth (a key from
> `aistudio.google.com/apikey`, with `security.auth.selectedType` switched away from
> `oauth-personal`) is a separate path from the retired Code Assist OAuth and was not attempted.

**Tracked in sweeps like any other adapter until then.** It carried an "unmaintained,
skip by default" marking from 2026-07-15 until #1717 reversed it: the flag was about
maintainer time, not viability. #1717's audit found the flag was blocking the strongest
hook-based state-detection candidate in the whole set — Gemini CLI ships `gemini hooks
migrate` ("Migrate hooks from Claude Code to Gemini CLI"), deliberate upstream evidence
of the Claude-Code-shaped envelope convergence the hook epic (#1355) depends on — and
shipped it: `core/adapters/inbound/agents/geminicli/hooks.go` installs `Notification`
(gated on `notification_type == "ToolPermission"`), `AfterTool` (broad release) and
`AfterAgent` (the turn-done push, retiring the heuristic fallback below for sessions that
grant the hooks permission).

- **Transcript path**: `~/.gemini/tmp/<project>/chats/session-<timestamp>-<8hex>.jsonl`
- **Adapter source**: `core/adapters/inbound/agents/geminicli/` — `hooks.go` and
  `hookinstaller.go` are the hook half; both carry the source citations (bundle line
  offsets, exact byte-identical claims at two installed versions) rather than restating
  them here.
- **Hook delivery is the beacon, not a native http hook**: source-read from the installed
  CLI's own bundle, `HookType` is `{Command, Runtime, Plugin}` — there is no
  JSON-expressible `Http` type — so `irrlichd hook-post gemini-cli`
  (`core/pkg/hookbeacon`) is what gets installed, never a bare `curl` line. That matters
  for a release check specifically: Gemini CLI's own exit-code-to-decision mapping turns
  ANY exit code other than 0 or 1 into `decision: "deny"` for a hook whose stdout didn't
  parse as JSON, which the scheduler then turns into a denied tool call or a blocked
  turn — a delivery type change here is a correctness question, not a compatibility one.
- **Version floor**: `minCLIVersion = "0.54.0"` in `hookinstaller.go`, deliberately set at
  the edge of what was directly source-verified (0.54.4 and 0.56.0 — published
  2026-08-07 and 2026-08-19, so a twelve-day, two-minor-release window, not a long
  stability record — byte-identical) rather than at the PR-archaeology date the hook system actually shipped
  (~2025-09-22, google-gemini/gemini-cli #9074-#9112) — see that constant's doc comment
  before assuming an earlier floor is safe to backdate.
- **Reopen condition for the OLD marking**: none — it will not return. A genuine future
  maintenance lapse is a new, separately-justified decision, not a reinstatement of this
  one.

> ⚠️ **#1068 is not a deprecation notice** — don't cite it as one. It's a *watch item*
> about the native SEA binary possibly becoming default (which would break `DiscoverPID`
> and the heap-bump exclusion), closed as completed with the verdict "No impact today; no
> code change required".

### 11. JetBrains Junie (`junie`)

- **Transcript path**: `~/.junie/sessions/<session-id>/events.jsonl`. The basename is constant, so **session ID = the parent `session-*` directory**, via `SessionIDFromPath` (`core/adapters/inbound/agents/junie/adapter.go`); the root's `index.jsonl`, sibling `state.json`/`transcript.md`, and any `events.jsonl` inside a `task-*` subdirectory all return `""` and are skipped — exactly one session per session directory.
- **Env override: none.** `$JUNIE_DATA` exists upstream but points at the version/install store under `~/.local/share/junie`, **not** at the session root (verified against a live install, 2026-08) — the root is a plain `$HOME` join. If Junie ships a real session-root override, that's the silent-blackout category below.
- **`turn_done` is an explicit marker**: `TaskState`, written once when the task finalizes — **after failures too** (state `COMPLETED` follows `AgentTaskFailedEvent`), so it is the turn boundary, not a success verdict. `TaskStartedEvent` → `turn_start` (copilot precedent, #1256): a resumed task can start with **no** `UserPromptEvent`, so it's the only signal the agent is working again.
- **Waiting**: no hooks — `INPUT_REQUIRED` in the `event.state` field of `SessionA2uxEvent` lines opens `PermissionRequestIDs` keyed on `stepId` (the copilot pattern); `UserAsyncResponseEvent`/`UserResponseEvent`/`CancelAgentEvent`/`TaskState` drain every open id as a safety net.
- **Schema is unversioned and reverse-engineered** from live captures (`parser.go` doc comment); unknown `kind`s are skipped, never fatal.
- **CWD**: `CurrentDirectoryUpdatedEvent` is the only in-transcript source (no top-level `cwd` field). A session that never started a task emits none — `ExtractCWDFromJunieSidecar` (`core/pkg/transcript/junie_cwd.go`) then falls back to the `~/.junie/processes/<pid>-<session-id>-<hash>.json` sidecar, accepting a file only when its JSON `sessionId` field matches.
- **PID**: the same process sidecars give a direct session→PID binding (`pid.go`) — stronger than the CWD scan sibling adapters need — but sidecars can outlive their process, so a PID is liveness- and command-pattern-checked (`(^|/)junie( |$)`) before being trusted.

#### What breaks it
- Renaming `events.jsonl`, or moving sessions out of `~/.junie/sessions/`, or changing the `session-` directory prefix → zero sessions, silently.
- A new session-root env override (see above) → silent blackout for users who set it.
- Renaming `TaskState`/`TaskStartedEvent` → turn lifecycle never opens/closes; renaming `INPUT_REQUIRED` or dropping `stepId` → no `waiting` episodes.
- Sidecar filename/JSON shape changes (`sessionId`/`projectPath`) → CWD fallback and PID binding degrade (sessions land in "unknown" project at PID=0).

---

## What Breaks Irrlicht (Impact Categories)

| Category | Examples | Severity |
|----------|----------|----------|
| **Transcript path change** | Directory moved, nesting changed, file extension changed | CRITICAL — sessions not discovered |
| **Root relocation via a new env var** | Upstream ships `$VIBE_HOME`-style override for an adapter that hardcodes its root | CRITICAL — silent blackout; the watcher waits on a path that never appears |
| **DB schema change** (opencode, antigravity) | Table/column rename, protobuf field renumbering | CRITICAL — errors are logged and swallowed, or decode silently returns zeroes |
| **Transcript format change** | JSONL schema altered, event types renamed/removed | HIGH — state classification fails |
| **Turn-end shape change** | Trailing tool call on the final message, or a text-only message mid-turn | HIGH — vibe, kiro-cli and antigravity (still purely heuristic, no independent signal) stick in `working` forever, or flicker to `ready` early. aider is spared the stick by its idle flush; the explicit adapters are unaffected. gemini-cli's own transcript parsing is still heuristic and equally exposed, but since #1717 a session with the hooks permission granted has an independent, transcript-shape-agnostic backstop — `AfterAgent` carries its own turn text and fires regardless of what the transcript looks like — so this row applies to it only when hooks are not granted (or the install has gone stale and `Verify` hasn't yet repaired it). |
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
