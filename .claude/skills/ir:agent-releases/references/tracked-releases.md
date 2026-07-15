# Tracked Agent Releases

This file tracks releases already analyzed. Update after each run to avoid re-reporting.

## Format

Each entry: `- <version> (<date>) — <one-line summary of impact or "no impact">`

## Claude Code
- v2.1.143–v2.1.210 (2026-05-15 to 2026-07-14, 58 releases) — checked 2026-07-15. Impactful subset below; all others no impact (bug fixes, models, TUI, OTel, auth). Note v2.1.177 has a release tag but no changelog entry (content unknown); v2.1.151/v2.1.155 have entries but no tag.
- v2.1.208 (2026-07-14) — MEDIUM/UNCERTAIN: transcript size cut up to 79x by "pruning superseded file-history backups". If pruning rewrites/shrinks a LIVE .jsonl in place, offset-based tailing desyncs. Verify whether it prunes written history or only bounds new writes. Also `CLAUDE_CODE_PROCESS_WRAPPER` (opt-in self-spawn wrapper — could alter process tree; LOW)
- v2.1.200 (2026-07-03) — LOW (downgraded after verification): "default" permission mode renamed to "manual". `permissionMode` is a passthrough display field (parser.go:121 → session/metrics.go); no classifier branches on its value, so the rename can't break classification. NOTE parser.go:238's `trigger == "manual"` is the PreCompact trigger — unrelated, no collision. Doc comment at session/metrics.go:212 lists stale values. Also: AskUserQuestion no longer auto-continues (idle timeout now opt-in) — makes `waiting` more accurate but lengthens dwell times
- v2.1.198 (2026-07-01) — LOW (downgraded after verification): subagents run in background by default; Notification hook gains agent_needs_input/agent_completed. Does NOT affect subagent counting — CountOpenSubagents() deliberately returns 0 and counting is file-based via child SessionStates, not open Agent tool calls
- v2.1.196 (2026-06-29) — MEDIUM/UNCERTAIN: a misread transcript is now "set aside", never deleted — i.e. a live transcript can be renamed/moved out from under the watcher. Set-aside naming/location undocumented; verify it doesn't leave a stale duplicate or orphan
- v2.1.186 (2026-06-22) — MEDIUM: background subagent permission prompts now surface in the MAIN session (cross-transcript causality: a child's block drives the parent to `waiting`); "Esc denies just that tool" writes `is_error` on a tool_result, which irrlicht maps to ESC/rejection → ready while the session is not done
- v2.1.178 (2026-06-15) — MEDIUM: TeamCreate/TeamDelete tools REMOVED; teammates now spawn via the Agent tool's `name` param (indistinguishable from ordinary subagents). Adds `Tool(param:value)` permission rule syntax
- v2.1.172 (2026-06-10) — LOW: subagents can spawn subagents up to 5 levels deep; ParentSessionID linking must tolerate arbitrary depth. On-disk `subagents/` dirs first appear 2026-06-12
- v2.1.170 (2026-06-09) — MEDIUM (historical): FIX for sessions not saving transcripts AT ALL when launched from a shell inheriting Claude Code env vars (the shape of a daemon/background spawn). Explains any "session exists but no transcript" reports before 2026-06-09
- v2.1.169 (2026-06-08) / v2.1.157 (2026-05-29) — MEDIUM: `/cd` moves a session's cwd mid-session; EnterWorktree can switch worktrees mid-session. Root cause of the transcript-relocation behavior below. `claude agents --json` gains id/state (+ `waitingFor` in v2.1.162) — a supported read path for background-session state
- VERIFIED ON DISK 2026-07-15 (not changelog-derived) — transcripts RELOCATE across project dirs mid-session: new `relocated` (2104 events) and `worktree-state` (3046 events) types; a session started in the main repo now has its transcript ONLY under the worktree's project dir, with no copy left behind. Still under the watched root (so still seen), but MEDIUM/UNCERTAIN: verify the same session-id stem moving between project dirs doesn't produce a ghost/duplicate session
- VERIFIED ON DISK 2026-07-15 — NON-ISSUE, already handled: nested `~/.claude/projects/<proj>/<session>/subagents/agent-*.jsonl` (133 dirs, first 2026-06-12, still live today). The fswatcher is RECURSIVE (addExistingDirs walks subdirs + adds watches dynamically), and parser.go:907-920 documents this as the deliberate single source of truth for subagents. Sibling dirs also present: tool-results/, workflows/, session-memory/
- VERIFIED ON DISK 2026-07-15 — permissionMode census: auto=4984 (93%), plan=331, default=8, acceptEdits=3; `manual`/`bypassPermissions` never observed. Passthrough only — no impact, but session/metrics.go:212's comment listing "default"/"plan"/"bypassPermissions" is factually stale
- RESOLVED (was UNCERTAIN, v2.1.113) — native binary does NOT break process matching: `claude --version` 2.1.210, binary is Mach-O arm64, `pgrep -x claude` matches live PIDs
- STILL OPEN (was UNCERTAIN, v2.1.139/142) — dispatched/background session transcript path unconfirmed; circumstantial evidence points to the same store. Needs a live dispatch to settle
- v2.1.142 (2026-05-15) — MEDIUM/UNCERTAIN: new `claude agents` flags (--add-dir, --settings, --mcp-config, --plugin-dir, --permission-mode, --model, --effort, --dangerously-skip-permissions) for dispatched background sessions; verify whether dispatched sessions still write transcripts to `~/.claude/projects/<project>/<uuid>.jsonl` flat or to a new nested path
- v2.1.141 (2026-05-14) — no impact (`claude agents --cwd` scoping, `terminalSequence` hook output, /feedback last-24h/7d, plugin menu nav, Bedrock/MCP fixes)
- v2.1.140 (2026-05-13) — no impact (Agent tool case-insensitive subagent_type matching, /goal hook gating, settings hot-reload fix)
- v2.1.139 (2026-05-12) — HIGH/UNCERTAIN: `claude agents` view (Research Preview) GA's a unified list of every Claude Code session including background-dispatched; `/goal` command added; subagent API requests now carry `x-claude-code-agent-id`/`parent-agent-id` headers and OTEL attrs. Verify (a) whether dispatched/background sessions land in the same `~/.claude/projects/<project>/<uuid>.jsonl` watcher path, (b) whether subagent counting via open `Agent` tool still works or now relies on `agent_id` headers
- v2.1.138 (2026-05) — no impact (internal fixes)
- v2.1.137 (2026-05) — no impact (VSCode Windows activation fix)
- v2.1.136 (2026-05) — no impact (settings.autoMode.hard_deny, MCP /clear fix, plan-mode Edit rule fix)
- v2.1.129 (2026-05) — no impact (`--plugin-url`, force-sync-output, package-manager auto-update, plugin manifest experimental key, gateway model discovery opt-in, Ctrl+R history default, skillOverrides settings honoring, /context grid bugfix)
- v2.1.128 (2026-05) — no impact (`--plugin-dir` zip, `--channels` with API key, /mcp tool counts, MCP reserved name `workspace`, EnterWorktree uses local HEAD, parallel shell sibling cancellation fix)
- v2.1.126 (2026-04) — no impact (`claude project purge`, /model picker gateway discovery, Win PowerShell detection, Bedrock/Vertex/managed-domain fixes)
- v2.1.123 (2026-04) — no impact (single OAuth fix)
- v2.1.122 (2026-04) — no impact (Bedrock service tier, PR URL paste resume, OTel attrs, image resize bug)
- v2.1.121 (2026-04) — MEDIUM/UNCERTAIN: `CLAUDE_CODE_FORK_SUBAGENT=1` now works in non-interactive (`claude -p`/SDK) sessions, expanding the forked-subagent transcript-path question (re v2.1.117 — still needs verification of nested `~/.claude/projects/<project>/<session>/subagents/` dir)
- v2.1.120 (2026-04) — no impact (`claude ultrareview` non-interactive subcommand prints findings to stdout, no session; AI_AGENT env var for subprocesses; PgUp/PgDn hint; /rewind keyboard fix; `find` FD exhaustion fix)
- v2.1.119 (2026-04-23) — no impact (/config persistence, prUrlTemplate, misc fixes)
- v2.1.118 (2026-04-23) — no impact (vim visual mode, /usage merge, themes, hook MCP tools)
- v2.1.117 (2026-04-22) — MEDIUM/UNCERTAIN: CLAUDE_CODE_FORK_SUBAGENT=1 enables forked subagents; verify if transcripts land in nested ~/.claude/projects/<project>/<session>/subagents/ dir (watcher scans flat jsonl only)
- v2.1.116 (2026-04-20) — no impact (faster /resume, MCP startup, thinking spinner)
- v2.1.115 (2026-04-19) — no impact (no listed changes)
- v2.1.114 (2026-04-18) — no impact (permission dialog crash fix)
- v2.1.113 (2026-04-17) — HIGH/UNCERTAIN: CLI spawns native binary instead of bundled JS; verify pgrep -x claude + lsof -p still resolve session PID+CWD correctly on macOS native build
- v2.1.112 (2026-04-16) — no impact (Opus 4.7 auto mode fix)
- v2.1.111 (2026-04-16) — no impact (Opus 4.7 xhigh, /effort slider, /ultrareview, PowerShell rollout)
- v2.1.110 (2026-04-15) — no impact (/tui fullscreen, push notification tool not user-blocking, Monitor-adjacent changes)
- v2.1.109 (2026-04-15) — no impact (thinking indicator)
- v2.1.108 (2026-04-14) — no impact (prompt caching env vars, recap feature, Skill→slash commands)
- v2.1.107 (2026-04-14) — no impact (thinking hints)
- v2.1.105 (2026-04-13) — no impact (EnterWorktree.path param, PreCompact hook, plugin monitors manifest key; schema extensions only)
- v2.1.101 (2026-04-10) — no impact (/team-onboarding, OS CA trust, OAuth/Bedrock fixes)
- v2.1.98 (2026-04-09) — no impact (Monitor tool for streaming background scripts — not user-blocking; subprocess sandboxing; Bash permission hardening)
- v2.1.97 (2026-04-08) — no impact (focus view, /agents running indicator, misc fixes)
- v2.1.96 (2026-04-08) — no impact (Bedrock bearer token fix)
- v2.1.94 (2026-04-07) — no impact (Bedrock Mantle, Slacked header, plugin skill stability)
- v2.1.93 (not released or no changelog entry)
- v2.1.92 (2026-04-04) — no impact (permission policy, sandbox, removed commands)
- v2.1.91 (2026-04-02) — no impact (disableSkillShellExecution setting)
- v2.1.85 (2026-03-26) — no impact (hook output to disk, PreToolUse changes)
- v2.1.84 (2026-03-26) — no impact (PowerShell tool, plugin allowlist, hooks)
- v2.1.83 (2026-03-25) — no impact (env scrub, sandbox settings, hook events)
- v2.1.82 (2026-03-27) — no impact (MCP policy fix, bare mode fix)
- v2.1.81 (2026-03-20) — no impact (bare flag, plugin marketplace, subagent compaction fix)
- v2.1.80 (2026-03-19) — no impact (plugin timeout config)
- v2.1.79 (2026-03-18) — no impact (console auth, remote-control)
- v2.1.78 (2026-03-17) — no impact (streaming, thinking summaries, terminal passthrough)
- v2.1.77 (2026-03-17) — MEDIUM: Agent tool resume removed, SendMessage replaces it; verify subagent counting
- v2.1.76 (2026-03-14) — no impact (Elicitation hooks, sparse worktree)
- v2.1.75 (2026-03-13) — no impact (managed policy fix, color command)
- v2.1.74 (2026-03-12) — no impact (autoMemoryDirectory setting)
- v2.1.73 (2026-03-11) — no impact (deprecated output-style)
- v2.1.72 (2026-03-10) — MEDIUM: ExitWorktree/EnterWorktree tools added; verify if user-blocking
- v2.1.70 (2026-03-19) — LOW: AskUserQuestion disabled in channels mode
- v2.1.69 (2026-03-05) — no impact (hook agent_id, skill dir variable)
- v2.1.63 (2026-02-28) — no impact (listener leak fix)
- v2.1.59 (2026-02-26) — no impact (auto-memory feature)
- v2.1.51 (2026-02-24) — no impact (managed settings plist/registry)
- v2.1.50 (2026-02-20) — no impact (worktree flag, simple mode, Opus 4.6 1M context)
- v2.1.49–v2.1.0 (2026-02 to 2026-01) — no impact (skills, hooks, config, UI, plugin system)
- v2.0.76–v2.0.0 (2026-01 to 2025-12) — no impact (background agents, named sessions, plugin system, native extension, SDK rename)
- v1.0.126–v1.0.0 (2025-12 to 2025-10) — no impact (hooks, custom agents, skills, process title already "claude" since v1.0.17)
- v0.2.125–v0.2.21 (2025-10 to 2025-05) — no impact (tool renames, MCP, settings, compaction, etc.)

## OpenAI Codex
- v0.131.0–v0.144.4 (2026-05-18 to 2026-07-14, 23 stable releases) — checked 2026-07-15 against source on `main`, not release prose. Impactful subset below; all others no impact. Also v0.145.0-alpha.1–13 (through 2026-07-15) ship no release notes — not assessable.
- v0.144.0 (2026-07-09) — MEDIUM/WATCH: **paginated thread rollout format** (PR #30188). Threads with `history_mode = "paginated"` persist `ItemCompleted(item)` records instead of the legacy event stream; rollout/src/policy.rs shows UserMessage, AgentMessage, AgentReasoning, McpToolCallEnd, WebSearchEnd, PatchApplyEnd, ContextCompacted and SubAgentActivity are NOT persisted at all in that mode. Would fail SILENTLY (sessions parse as empty / turn-completion-less, not as an error). NOT biting today — triple-gated: `ThreadHistoryMode` is `#[default] Legacy`, no feature flag exists, and app-server rejects it with `method_not_found("paginated_threads is not supported yet")`. **Escalate to CRITICAL if that guard string disappears or `#[default]` moves to Paginated** — a clean thing to poll for
- v0.143.0 (2026-07-08) — LOW: new additive `world_state` RolloutItem variant persisted to JSONL (`{"type":"world_state","payload":{...}}`). Confirm the parser skips unrecognized `type` rather than erroring; same logic covers v0.140.0's additive `window_id` on CompactedItem
- v0.142.0 (2026-06-22) — LOW: rollout compression to `.jsonl.zst` extended from archived_sessions/ to the live `sessions/` tree (v0.137.0 introduced it for archived only). `.jsonl.zst` would not match a `*.jsonl` glob, BUT the design invariant (#25087) is that writers always append to plain `.jsonl` and compression only targets cold (>7d) rollouts — live sessions stay plain. Gated off: `local_thread_store_compression` is Stage::UnderDevelopment, default_enabled false
- v0.136.0 (2026-06-01) — LOW: `/archive` + `codex archive`/`unarchive` expose thread archiving to users (the RPC predates v0.130.0 — only the surface is new, which raises how often it happens). `fs::rename` moves sessions/YYYY/MM/DD/<f>.jsonl to a FLAT archived_sessions/<f>.jsonl, disappearing from the sessions/ walk. Manual + targets finished threads; resume/fork on archived is blocked, so a live monitored session won't be archived mid-flight
- v0.134.0 (2026-05-26) — LOW: profile v2 configs live in sibling `~/.codex/<name>.config.toml` files. `~/.codex/config.toml` itself unchanged, but a `--profile` user may declare `model` in a sibling, so the model fallback could read a stale value
- v0.140.0 (2026-06-15) — no impact, but notable: SQLite state is rebuilt FROM rollout data, confirming rollouts remain the durable source of truth
- CONFIRMED UNCHANGED: session path `~/.codex/sessions/YYYY/MM/DD/rollout-*.jsonl` (SESSIONS_SUBDIR still "sessions"), `rollout-*.jsonl` still matches a `*.jsonl` glob, config `~/.codex/config.toml`. **No release moved sessions off local disk** — the window's "remote thread store" work is remote execution/control (Noise relay, remote-control RPCs), and external-agent work is import INTO Codex
- v0.130.0 (2026-05-08) — no impact (plugin details/share metadata, `codex remote-control` headless app-server entrypoint, paginated thread item views, Bedrock console-login auth, view_image multi-env resolution; no session-storage changes)
- v0.129.0 (2026-05-07) — no impact (TUI Vim mode, resume/fork picker redesign, raw scrollback, /ide context injection, workspace-aware /diff, plugin workspace sharing, hooks before/after compaction + PreToolUse context, experimental goals; rollout JSONL schema unchanged)
- v0.128.0 (2026-04-30) — no impact (persisted /goal model-tool + TUI, `codex update`, permission profiles with built-in defaults, marketplace plugin install + remote bundle caching, external agent session **import** (ingest direction; doesn't change Codex's own session storage), MultiAgentV2 thread-cap config)
- v0.125.0 (2026-04-25) — no impact (Unix socket transport, pagination-friendly resume/fork, sticky environments, remote thread store, model provider discovery, `codex exec --json` reasoning tokens, rollout tracing of tool/code-mode/session/multi-agent relationships)
- v0.125.0-alpha.3 (2026-04-24) — no impact (pre-release)
- v0.125.0-alpha.2 (2026-04-24) — no impact (pre-release)
- v0.124.0 (2026-04-23) — no impact (TUI reasoning controls, multi-env app-server sessions, Bedrock SigV4, stable hooks, ChatGPT Fast tier)
- v0.123.0 (2026-04-23) — no impact (built-in Bedrock provider, /mcp verbose, realtime handoffs, gpt-5.4 default)
- v0.120.0–v0.122.x (2026-04-05 to 2026-04-22) — no impact (no session-format or path changes detected)
- v0.119.0-alpha.11 (2026-04-04) — no impact (alpha, no details available)
- v0.118.0 (2026-03-31) — no impact (Windows sandbox, device code auth, exec stdin)
- v0.117.0 (2026-03-26) — LOW: removed read_file/grep_files handlers, sub-agent path addresses, tui rename
- Pre-v0.117.0 (2025-05 to 2026-03) — no impact (session format stable, rollout-*.jsonl matches *.jsonl glob, YYYY/MM/DD nesting handled by recursive WalkDir)

## Pi Coding Agent

> **Repo moved AGAIN**: `earendil-works/pi-mono` → **`earendil-works/pi`**. The pi-mono CHANGELOG raw URL now 404s. Authoritative sources:
> `raw.githubusercontent.com/earendil-works/pi/main/packages/coding-agent/CHANGELOG.md` and `.../docs/session-format.md`.
> npm scope `@earendil-works/pi-coding-agent` is unchanged. The GitHub releases page and npm registry render misleading 2024/2025 dates — trust CHANGELOG.md.

- v0.74.1–v0.80.7 (2026-05-16 to 2026-07-14, 30 releases) — checked 2026-07-15. **Nothing requires an irrlicht change.** Session dir structure, header (`version: 3`), config path (`~/.pi/agent/settings.json`) and tool_use/tool_result emission all unchanged. Impactful subset below; all others no impact (providers, models, TUI, keybindings, HTTP fixes)
- v0.80.4 (2026-07-09) — LOW/UNCERTAIN: "custom metadata in JSONL headers" touches the session header line irrlicht reads. Appears additive/optional (header stays `version: 3`), but the field name/shape is NOT in session-format.md — weakest finding of the batch, unverified in source
- v0.78.0 (2026-05-29) — LOW: `--name`/`-n` adds a new additive `session_info` JSONL entry type: `{"type":"session_info","id":...,"parentId":...,"timestamp":...,"name":"..."}`. Carries a display name only, no message/tool content. Only breaks if the parser errors on unknown types rather than skipping (it already skips session_switch/session_fork/session_shutdown, so almost certainly fine). Optional: surface `name` as a session title. Confirmed in session-format.md commit ce554ad
- v0.76.0 (2026-05-27) — LOW: `--session-id` lets callers supply an exact project-local session ID, so the `<timestamp>_<id>.jsonl` id segment may no longer be a UUIDv7. Dir and filename SHAPE unchanged, so FilesUnderRoot still picks it up; only bites if irrlicht parses the filename stem as a UUID. Safer to read `id` from the header line
- RESOLVED (was UNCERTAIN, v0.73.0) — **incremental bash streaming does NOT affect open-tool-call tracking.** Streaming rides RPC events on stdout (`tool_execution_start`/`_update`/`_end`, correlated by toolCallId); the JSONL transcript still persists exactly ONE terminal ToolResultMessage per call. session-format.md's entry-type list contains no `tool_execution_*` type
- STILL OPEN (v0.71.0) — `PI_CODING_AGENT_SESSION_DIR` remains unhonored; no release in the window touched it. (A v0.77.0 "custom session directories" fix does NOT exist — that was a summarizer artifact, checked and discarded.) Related, out-of-window: issue #320 (open) — the SDK passes `agentDir` to SessionManager.create(), writing sessions to `~/.pi/agent/` instead of `~/.pi/agent/sessions/`. SDK-embedded Pi only, not the CLI
- v0.74.0 (2026-05-07) — LOW: repository moved from `badlogic/pi-mono` to `earendil-works/pi-mono`; `@earendil-works/*` package scopes. Filesystem-monitoring path unchanged (`~/.pi/agent/sessions/...`); only affects landscape repo references
- v0.73.1 (2026-05-07) — no impact (patch)
- v0.73.0 (2026-05-04) — MEDIUM/UNCERTAIN: **incremental bash output streaming** — Bash tool output now appears while commands run. Verify whether this changes `tool_result` event emission (multiple partial events vs single terminal event) for irrlicht's open-tool-call tracking. Also: Xiaomi MiMo API billing + regional Token Plan providers (no monitoring impact), compact read rendering (UI only)
- v0.72.1 (2026-05-02) — no impact (patch)
- v0.72.0 (2026-05-01) — no impact (Xiaomi MiMo Token Plan provider, `thinkingLevelMap` replaces `reasoningEffortMap` (custom-provider API), `shouldStopAfterTurn` agent loop callback, custom provider `baseUrl` overrides)
- v0.71.1 (2026-05-01) — no impact (patch)
- v0.71.0 (2026-04-30) — MEDIUM: **`PI_CODING_AGENT_SESSION_DIR` env var added** — overrides the default `~/.pi/agent/sessions/...` location. Pi adapter's `FilesUnderRoot{Dir, ...}` watches the fixed home-relative path; sessions in a custom dir are invisible. Consider honoring this env var (or documenting that custom locations aren't discovered). Also: Cloudflare/Moonshot providers, Mistral Medium 3.5 (no monitoring impact); built-in Gemini CLI / Antigravity providers removed
- v0.70.6/.5/.4/.3/.2/.1 (2026-04-24 to 2026-04-28) — no impact (patch series)
- v0.70.0 (2026-04) — no impact (--no-builtin-tools preserves extension tools)
- v0.69.0 (2026-04) — no impact (TypeBox 1.x validation, internal)
- v0.68.0 (2026-04) — no impact (Tool[]→string[] tool selection is extension-author API; PI_CODING_AGENT env — irrlicht doesn't process-monitor Pi; reason/targetSessionFile on session_shutdown — parser already skips)
- v0.67.1 (2026-04-09) — no impact (Earendil startup announcement, telemetry ping)
- v0.67.0 (2026-04-08) — no impact (changelog formatting bug, release itself unremarkable)
- v0.66.0 (2026-04) — no impact (minor, no session-surface changes)
- v0.65.0 (2026-04-03) — CORRECTED: NO IMPACT (parser already skips session_switch/session_fork events; irrlicht uses filesystem events for lifecycle)
- v0.64.0 (2026-03-29) — no impact (prepareArguments hook, ModelRegistry change)
- v0.63.2 (2026-03-29) — no impact (ctx.signal for cancellation)
- v0.63.1 (2026-03-27) — no impact (label timestamps)
- v0.63.0 (2026-03-27) — no impact (session tree structure, internal API)
- v0.62.0 (2026-03-23) — HIGH/UNCERTAIN: "Session directory structure was modified" per changelog; verify if ~/.pi/agent/sessions path changed
- v0.61.x–v0.60.0 (2026-03) — details unavailable (changelog truncated)

## Gas Town

> **Repo**: `github.com/gastownhall/gastown` (`steveyegge/gastown` redirects there).
> **Correction to monitoring-surface.md**: irrlicht does NOT poll `gt convoy list --json`. poller.go polls exactly four: `rig list`, `polecat list --all`, `dog list`, `boot status`. Convoy survives only in a comment (adapter.go:4) and permission text (permission.go:38) — convoy's JSON did change in v1.2.0 (`labels []string`), and it is irrelevant because nothing reads it.

- v1.2.0 (2026-05-30) — LOW: `polecat list --json` gains 11 additive fields (cleanup_status, active_mr, branch, verdict, reason, reusable, safe_to_nuke, needs_recovery, needs_mq_submit, mq_status, counts_toward_capacity, reuse_status) — harmless, Go's json.Unmarshal ignores unknown fields, and the 5 fields irrlicht reads (rig, name, state, issue, session_running) are untouched. The real effect is **one new polecat state value: `review-needed`** (full set: working, idle, done, review-needed, stuck, stalled, zombie). poller.go:366 assigns `pcWorker.State = pc.State` as a raw passthrough, so it surfaces as a state string irrlicht has never emitted — unstyled/unlabeled in the UI. Semantically "work finished, awaiting a verdict"; worth surfacing rather than passing through raw. Verified by diffing tagged source v1.1.0..v1.2.0
- v1.2.1 (2026-06-06) — no impact (patch; zero changes to any polled --json surface, command name, role, or env var)
- CONFIRMED UNCHANGED (v1.1.0→v1.2.1): `gt rig list --json` rigInfo is byte-identical to irrlicht's rigState; `gt dog list --json` no schema diff; `gt boot status --json` identical; all four commands + `--json` still exist; binary still `gt`; `GT_ROOT` still the env var (`GT_TOWN_ROOT` appears in v1.2.1 commits but already existed in v1.1.0 — not new)
- CLOSED (was the open v1.0.0 item) — **the Boot/Dog role gap does not exist**: gastown/types.go already defines RoleBoot ("🥾", Watchdog for the Deacon) and RoleDog ("🐕", Cross-rig infrastructure worker) with full roleMeta entries. v1.2.x adds no further roles; full set is mayor, deacon, witness, refinery, polecat, crew, boot, dog. ("Compactor dog" from v0.12.0 is a dog *instance name*, not a role.)
- v1.1.0 (2026-05-07) — no impact (convoy completion + cross-rig dep resolution notifications; BEADS_DIR routing to avoid pthread deadlock with bd 1.0+; ResolveProcessNames Args plumbing; no `gt list --json` schema changes)
- v1.0.1 (2026-04-25) — no impact (Bitbucket Cloud merge-queue integration, custom-groq-opus cost tier, per-role effort, stuck-agent-dog auto-restart, deacon model-escalation.json, formula --set/interactive support, dolt commit freshness, reaper close_reason logging; orchestration internals only)
- (2026-04-24 check) — no public releases after v1.0.0 found; Libraries.io pseudo-version v1.0.1-0.20260420010148-db74d7567d58 is a Go-module commit ref, not a release
- v1.0.0 (2026-04-02) — MEDIUM: new Boot/Dog role not in irrlicht's roleMeta; verify role derivation
- v0.13.0 (2026-03-29) — no impact (new CLI commands not monitored, session-hygiene plugin removed)
- v0.12.1 (2026-03-15) — no impact (ACP protocol, gt assign/mountain, per-repo settings)
- v0.12.0 (2026-03-11) — no impact (event-driven polecat lifecycle, session_running field unchanged)
- v0.11.0 (2026-03-05) — LOW/UNCERTAIN: Beads Classic code paths removed; verify gt --json output still matches types.go structs
- v0.10.0 (2026-03-03) — no impact (circuit breaker, telemetry, mTLS; min beads v0.57.0)
- v0.9.0 (2026-03-01) — no impact (gt doctor --fix, env-vars check)

## Aider

> Baseline established 2026-07-15. **PyPI is authoritative for dates** — the GitHub Releases page lags badly (newest tag v0.86.0, 2025-08-09).

- **No releases 2026-05-01 → 2026-07-15.** Latest is v0.86.2 (2026-02-12), ~5 months stale. Only 5 commits exist in the whole window (2026-05-15 → 2026-05-22: an ANTHROPIC_MODELS expansion and bash tree-sitter repomap tags — both out of scope), then nothing for ~8 weeks
- HISTORY.md's unreleased `main` section reviewed in full: model support, exception mapping, repo-map tags, a `/ok` shortcut, a symlink-loop fix. Nothing touches chat-history format, config paths, process naming, or session types
- CONFIRMED UNCHANGED: `.aider.chat.history.md` (markdown transcript), `.aider.conf.yml`, Python console-script naming. The `pid_bind`→session-start and `/run`-is-not-a-tool_result quirks remain valid
- **Signal: aider is effectively dormant. Low future monitoring priority — consider checking it every few runs rather than every run.**

## OpenCode

> Baseline established 2026-07-15. **Repo transferred: `sst/opencode` now redirects to `anomalyco/opencode`** (same project/history). npm package remains `opencode-ai`; binary still `opencode` despite the org move.

- v1.14.31–v1.18.1 (2026-05-01 to 2026-07-14, 58 releases) — **no release changed session storage location, process name, or config path.** Impactful subset below
- v1.15.0 (2026-05-15) → #33993 (2026-06-26) — MEDIUM/UNCERTAIN: Effect-based core event system + versioned event projectors; the public event surface is now a generated manifest (`packages/core/src/public-event-manifest.ts`), and **"stop legacy v2 event emission" landed 2026-06-26**. This is the single most plausible way an irrlicht event consumer breaks in this window. `packages/schema/src/legacy-event.ts` still exists and message error schemas persist (session-message.ts → UnknownError / Session.Error.Unknown, ToolStateError), but the exact `message.data.error` path irrlicht consumes was NOT traced end-to-end. **Verify before trusting the known quirk.**
- v1.14.49 (2026-05-13) — LOW: a global `opencode.jsonc` is now auto-created when no config exists, so fresh installs get `~/.config/opencode/opencode.jsonc`, not `opencode.json`. Low risk: `.jsonc` was already accepted pre-window (2026-04-27) and both extensions are read (resolution order `.jsonc` then `.json`); only the auto-created default changed. Config directory unchanged
- v1.15.13 (2026-05-30) — LOW: config now resolves upward from the opened location to the worktree. Only matters if irrlicht resolves effective config itself rather than reading the global file
- v1.16.0/v1.16.2 (2026-06-05) — LOW: DB migration normalize_storage_paths (Windows-only); session move between workspaces; `run --replay`
- NON-ISSUE, verified 2026-07-15 — **the adapter already reads SQLite**, matching upstream: metrics.go/watcher.go open `~/.local/share/opencode/opencode.db` read-only and watch the WAL via fsnotify. (Upstream flipped JSON→SQLite ~2026-02-14, #10597 — well before this window; a naive grep suggesting a 2026-05-31 flip was wrong, that was only an import-path move + dropped session-diff JSON writes.)
- LOW/WATCH — the DB filename is **channel-scoped upstream**: `opencode-<channel>.db`, per database.ts `path()`. Verified this does NOT bite stable installs: the code returns plain `opencode.db` when InstallationChannel ∈ {latest, beta, prod} or OPENCODE_DISABLE_CHANNEL_DB is set, and this machine's install (1.14.50) has plain `opencode.db`. irrlicht hardcodes `dbRelPath = ".local/share/opencode/opencode.db"`, so a **non-stable channel install (e.g. `opencode-dev.db`) would be invisible**, as would an `OPENCODE_DB` override

## Gemini CLI

> Baseline established 2026-07-15. The repo has **no root CHANGELOG.md** (404s) — GitHub Releases is authoritative. Evidence below is from `git diff v0.40.1..v0.50.0` on a local clone, not release prose.

- v0.41.0–v0.50.0 (2026-05-05 to 2026-07-08, stable) + ~60 nightlies/previews through v0.52.0-nightly.20260715 — **no impact.** `packages/core/src/config/storage.ts` changed by exactly one hunk (unrelated getGlobalMemoryFilePath removal); transcript-path and session-id construction in chatRecordingService.ts is **byte-identical** across the window: same `<projectTempDir>/chats/`, same subagent nesting under sanitized parentSessionId, same `${SESSION_FILE_PREFIX}${timestamp}-${sessionId.slice(0,8)}.jsonl` (main) / `${sessionId}.jsonl` (subagent)
- v0.42.0 (2026-05-12) + v0.43.0 (2026-05-22) — MEDIUM/WATCH: SEA (Single Executable Application) relaunch reworked (#26130/#26261/#26333), and since v0.43.0 every release attaches `gemini-darwin-{arm64,x64}-unsigned.zip`. Standard Node mode is UNCHANGED (child still carries `bin/gemini` in argv), but **SEA mode spawns `[process.execPath, ...scriptArgs]` → `argv[0] === argv[1]`, no `node` and no `bin/gemini`** — which would break both irrlicht's node-based DiscoverPID and its heap-bump-worker exclusion. **Not the default today**: npm @google/gemini-cli@0.50.0 still ships `bin: {gemini: "bundle/gemini.js"}`, and the binaries are unsigned with low download counts. Two clean discriminators if it goes mainstream: `argv[0] === argv[1]`, and env `GEMINI_CLI_NO_RELAUNCH=true` on the child
- v0.44.0 (2026-05-27) — LOW/WATCH: AgentSession subagent protocol scaffolding (#25302/#25303/#26665/#26937/#26948/#26934) behind experimental flag `adk.agentSessionSubagentEnabled`, default off. If it flips on, `chats/<parentUUID>/<childUUID>.jsonl` is the layout most likely to move. Nothing changed on disk yet — verified
- v0.47.0 (2026-06-18) — LOW: #27770 deletes the current session on interactive exit when it never gained "resumable content". A transcript irrlicht has bound to can be **deleted at exit** for start-and-quit sessions — possible ghost/stat-error path, not a format change. Also adds an Antigravity CLI transition banner + migration commands (#27676/#27765) steering users toward Antigravity CLI, where irrlicht already has an adapter
- v0.41.0 (2026-05-05) — LOW: `--session-id` lets the caller pin the session UUID (rejects path traversal, conflicts with --resume). Filename-stem derivation unchanged. Accuracy note: the stem equals the session id only for SUBAGENT files; main sessions embed a truncated 8-char id — pre-existing, not a change
- NON-FINDING (checked, ruled out): `migrateFromFileStorage` (#27229) touches oauth-credential-storage.ts (OAuth creds → keychain), not sessions

## Kiro CLI

> Baseline established 2026-07-15. `github.com/kirodotdev/Kiro/releases` has **zero published releases** (issues-only repo) — `kiro.dev/changelog/cli/` is authoritative.

- v2.3.0–v2.12.0 (2026-05-12 to 2026-07-09). No release after 2.12.0 as of 2026-07-15
- v2.4.0 (2026-05-20) — **HIGH: `/rewind` now BRANCHES into a new session**, contradicting irrlicht's frozen "rewind edits in-place" adapter fact (characterized in the PR #590 era, an older CLI). Docs verbatim: *"When you run /rewind, Kiro creates a new session that starts from the turn you select. The original session stays intact."* Rewind is now a session-rotation event like `/chat new`: a rewind mints a new session id + transcript + sidecar, and the original never rotates. **Confirmed on disk, not just docs**: of 76 sidecars in ~/.kiro/sessions/cli/, one (65396b33-…, 2026-06-05) carries `"session_created_reason": "rewind"` — and its cwd is under irrlicht's OWN onboarding rig (`.build/refresh/kiro-cli/1-6_checkpoint-rewind-…`), so **a fixture may already encode the stale in-place assumption**. `session_created_reason` is a first-class on-disk discriminator; observed values `subagent` (75×) and `rewind` (1×), vocabulary not fully enumerable from this sample
- v2.3.0 (2026-05-12) — MEDIUM: **`KIRO_HOME` env var relocates ~/.kiro**, sessions included. Docs verbatim: *"Overrides the ~/.kiro directory used for global agents, prompts, skills, steering, settings, and sessions."* irrlicht hardcodes `defaultRootDir = ".kiro/sessions/cli"` (kirocli/adapter.go:20), so with KIRO_HOME set, session discovery silently finds nothing. Cheap fix, directly analogous to irrlicht's own IRRLICHT_HOME
- v2.8.0 (2026-06-17) — MEDIUM/UNRESOLVED: CLI V3 early access — opt-in `kiro-cli --v3` runs a new unified harness shared with Kiro IDE/Web (spec-driven flow, capability-based permissions, standalone hook file format, tag-based agent config). A different harness plausibly implies a different session store, and **neither the changelog nor the docs say where V3 stores sessions**. Needs a live `--v3` run. **Do not assume V3 reuses ~/.kiro/sessions/cli/**
- v2.6.0 (2026-06-05) — LOW: `/transcript save` export; `/title`; persistent model/effort prefs
- v2.5.0 / v2.7.0 / v2.9.0 / v2.10.0 / v2.11.0 / v2.12.0 — no impact (thinking display, /goal, queue steering, Entra ID, config hot-reload, MCP auth, ASCII mode)
- CONFIRMED UNCHANGED: `~/.kiro/sessions/cli/<uuid>.jsonl` + `<uuid>.json` sidecar + `<uuid>.lock`; sidecar schema across all 76 sidecars (created_at, cwd, session_created_reason, session_id, session_state, title, updated_at) with metrics paths intact (session_state.rts_model_state.model_info.{model_id,model_name,context_window_tokens}, .context_usage_percentage, user_turn_metadatas[].{input_token_count,output_token_count,metering_usage}); process `/opt/homebrew/bin/kiro-cli` (the separate /usr/local/bin/kiro is the IDE launcher)
- CAVEAT — **docs claim SQLite, disk says otherwise**: kiro.dev/docs/cli/chat/session-management/ says *"Storage: Local database (~/.kiro/)"* / *"SQLite database in ~/.kiro/"*, which would be a CRITICAL storage migration — but `find ~/.kiro` returns no .db/.sqlite, the .jsonl/.json layout is fully populated through 2026-07-12, and no changelog entry mentions a storage move. Treated as loose wording (or IDE-specific), NOT a migration. Flagged, not asserted
- CAVEAT — **disk evidence only covers ~2.4→2.6**: local kiro-cli is 2.6.0, so even Jul-12 files were written by 2.6.0. **The sidecar/transcript schema for 2.7.0–2.12.0 is unverified locally.** Given the known pattern that live tests contradict frozen verdicts, do not treat 2.7–2.12 as clean on file evidence alone
- Also new since ~2.6.0: a third per-session file `<uuid>.history` (plain-text prompt history). irrlicht tails `*.jsonl`, so inert — noted only in case of glob widening. Dating inferred from mtime (LOW confidence on the introducing version)

## Antigravity

> Baseline established 2026-07-15. **Correction to the skill's premise**: Antigravity is NOT undocumented — there is an official public changelog at `antigravity.google/changelog`. It is an Angular SPA (WebFetch blocked, static HTML empty); the data lives in the `main-*.js` bundle as two arrays, `engineSections` (Antigravity CLI tab) and `ideSections` (IDE tab).

- CLI 2.0.1–2.3.0 (2026-05-19 to 2026-07-13, 7 releases) and IDE 2.0.3–2.1.1 (2026-05-21 to 2026-06-22, 3 releases) — **no release documents a transcript path change, transcript format change, `conversations/<conv>.db` schema/location change, or a new session type.** The changelog is entirely user-facing; internal storage is never mentioned
- CLI 2.1.4 (2026-06-11) — LOW/WATCH: *"see all nested subagents belonging to the main conversation instead of only the subagents that are one level deep"* — multi-level nesting is now a product concept, so transcripts may carry subagent entries more than one level deep (irrlicht links children via ParentSessionID). On-disk shape change unverified
- CLI 2.3.0 (2026-07-13) — LOW/WATCH: `/btw` gained *"persistence across conversation switches"* (it was introduced in 2.1.4 as an *"ephemeral, single-response agent"*). Ephemeral→persisted is exactly the transition that tends to mint new on-disk entry types; whether /btw turns are written to transcript.jsonl is unverified. Same release: *"Stopped background tasks from continuing to run after a conversation is archived"* — implies an archived-conversation state; whether it's visible on disk (and whether irrlicht would keep reporting such a session live) is unverified
- CLI 2.2.1 (2026-06-25) — no monitoring impact, but **operationally relevant**: *"Enabled automatic saving of refreshed OAuth tokens to the OS keyring to reduce authentication prompts."* This speaks directly to the known dev-machine pain where agy's cached OAuth token expires and silently fails keyring refresh — 2.2.1+ may resolve it
- VERIFIED ON DISK 2026-07-15 (local install): both roots exist (`~/.gemini/antigravity/`, `~/.gemini/antigravity-cli/`) so the multi-root Source is still valid; transcript path `<root>/brain/<conv-id>/.system_generated/logs/transcript.jsonl` still valid; sibling store `<root>/conversations/<conv-id>.db` still valid. `~/.gemini/antigravity/` holds only global_skills/ and skills/ (no brain/, no conversations/) — the IDE side of the adapter is unexercised on this machine
- Observation, not a release finding: `~/.gemini/antigravity-cli/conversation_summaries.db` sits at root level; dbmetrics.go only knows `conversations/<conv>.db`. Cannot date the file or attribute it to a release — flagged only as something the adapter doesn't read
- CAVEAT: local `agy --version` reports **1.1.1**, which does not track the changelog's 2.x engine numbering — the local install may lag the 2.3.0 line, so the path verification above reflects the installed build, not necessarily 2.3.0

## Mistral Vibe

> Baseline established 2026-07-15. Package `mistral-vibe` on PyPI, repo `mistralai/mistral-vibe`. **The changelog alone is insufficient and in one case misleading** — the two highest-impact findings below are undocumented or under-described there. All findings anchored to source at specific tags (blobless clone, v2.9.4…v2.19.1).

- v2.9.4–v2.19.1 (2026-05-05 to 2026-07-08, 23 releases)
- v2.19.1 (2026-07-08) — **HIGH: `messages.jsonl` can now be atomically REWRITTEN/TRUNCATED in place.** `SessionLogger._overwrite_messages_sync` is brand new in 2.19.1 (verified absent in 2.19.0 and every prior tag). The writer now branches: if message count grew AND a `last_message_fingerprint` boundary check passes → append (`open("a")`, as before); otherwise → **full rewrite to a temp file + `os.replace()`**. Triggers: rewind-in-place (shrink), compaction rewriting the prefix, or any legacy session with no fingerprint. Before 2.19.1 rewind FORKED to a new session dir, so the file was effectively append-only. **Now the file can change inode and shrink mid-session**: a tailer holding an open FD silently reads a deleted inode forever; a (path, offset) tailer gets offset > filesize and either misses or garbles events. **Single most likely breakage in the whole window.** Verified by source-reading, not by driving a live rewind — the frequency of the fingerprint-mismatch path (vs. rewind only) is unconfirmed; worth a live rewind + compaction recording
- v2.14.0 (2026-06-04) — **HIGH: tool renames.** Tool names derive from class name → snake_case (`BaseTool.get_name()`), traced across every tag and corroborated against CHAT_AGENT_TOOLS: `SearchReplace`→`Edit` (tool `search_replace` → **`edit`**, args `{file_path, content}` → `{file_path, old_string, new_string, replace_all}`); `ReadFile`→`Read` (tool `read_file` → **`read`**, args `path` → `file_path`, the arg change persists); `write_file` becomes create-only (refuses overwrite)
- v2.19.0 (2026-07-03) — **MEDIUM: `read` renamed BACK to `read_file`** — a round-trip, and **entirely absent from the changelog** (found only in source; future silent renames are plausible). Net effect: name-keyed handling must accept BOTH `read_file` and `read` — transcripts from 2.14.0–2.18.4 carry `read`, while ≤2.13.0 and ≥2.19.0 carry `read_file`. **Fixtures recorded mid-window encode the transient name.** Same release: `--worktree NAME` changes session cwd. NON-issue: webfetch.py→web_fetch.py and websearch.py→web_search.py are FILE-only renames — classes unchanged, so tool names web_fetch/web_search never changed
- v2.19.1 also — LOW/gated: managed/experimental bash **overrides `get_name()` to return `"bash"`**, masquerading as the normal bash tool with a different args schema (PTY sessions). Skills now load as a **synthetic tool call**, so tool_use entries can appear without a model request
- v2.9.4 (2026-05-05) — LOW: `/clear` no longer chains `parent_session_id` — directly touches the field irrlicht uses for parent-child linking
- v2.10.0 (2026-05-19) / v2.19.0 — LOW: `--add-dir` (multi-root) and `--worktree` change the session's working directory; `~/.vibe/worktrees/` is a new dir (not under logs/session/, no path collision). Affects cwd/project resolution only
- v2.13.0 (2026-05-29) / v2.15.0 (2026-06-12) — LOW: compaction now injects summaries rather than replacing the conversation, and re-injects prior user messages — changes message-sequence shape, and is one of the paths that can trip 2.19.1's full-rewrite branch. v2.15.0 also adds experimental before_tool/after_tool hooks
- v2.18.3 (2026-06-30) — LOW: project-level `.vibe/config.toml` now persists config changes
- CONFIRMED UNCHANGED: `SESSION_LOG_DIR = VIBE_HOME/"logs"/"session"`, `VIBE_HOME = ~/.vibe` (overridable via `$VIBE_HOME`); `METADATA_FILENAME = "meta.json"` + `MESSAGES_FILENAME = "messages.jsonl"` as siblings; session dir naming `{prefix}_{timestamp}_{short_id}` (**note: NOT a bare `<session-id>`**; 2.19.1 swapped `session_id[:8]` for `shorten_session_id()`, default `[:8]` — functionally identical); meta.json `config.active_model` / `config.auto_compact_threshold` / `stats.context_tokens` all intact, only new field is the additive `last_message_fingerprint`; **process detection safe** — still a pure Python console-script (`vibe = "vibe.cli.entrypoint:main"`), no native binary, no entrypoint rename
- WATCH: `AnyVibeConfig = VibeConfig | VibeConfigSchema` — a config-schema migration is in flight (new vibe_schema.py alongside _settings.py). meta.json's `config` is `base_config.model_dump()` of EITHER; both currently carry top-level active_model and auto_compact_threshold, so it's safe today, but the dumped shape is now union-typed
- PRE-EXISTING BUG (not a window regression, worth knowing): `config.auto_compact_threshold` is the *global default*; the **effective** threshold is per-model (`config.get_active_model().auto_compact_threshold`). `_apply_global_auto_compact_threshold` pushes the global only into models that didn't set it explicitly, so **the top-level value diverges from the effective one when a per-model override exists**. If irrlicht reads only the top-level field it may already be wrong for override configs
