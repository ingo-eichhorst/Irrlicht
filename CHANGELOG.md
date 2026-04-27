# Changelog

All notable changes to Irrlicht are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and this project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Release artifacts (DMG, PKG, universal `irrlichd` binary, checksums) are
attached to each [GitHub release](https://github.com/ingo-eichhorst/Irrlicht/releases).

## [Unreleased]

## [0.3.10] — 2026-04-27

### Fixed
- **Sweep zombie sessions on startup** (#242) — Claude Code sessions no longer linger in the menu bar / UI after the underlying `claude` process has exited. The daemon now reaps stale entries on launch.
- **Prune deleted sessions from `apiGroups` synchronously** (#244) — when a session is removed, the overlay now updates immediately instead of showing a stale row until the debounced rehydrate lands. Walks agents, child subagents, and nested groups; drops project groups that become fully empty (gas-town excepted, since it renders even with no rigs).

### Changed
- **Daemon: drop recycled-PID predicate from `CleanupZombies`** — simpler, more reliable startup-cleanup path. Groundwork for #242.

### Tests
- e2e regression test for the startup zombie sweep (#242).
- 5 new `SessionManagerApiGroupsTests` cases covering top-level / child / parent removal, gas-town empty-rigs survival, and unknown-id no-op (#244).

## [0.3.9] — 2026-04-27

### Added
- **Aider adapter** (#220, #224, etc.) — first agent shipped through the new `/ir:onboard-agent` discovery flow. Includes parser, tmux-driven interactive driver, scenario fixtures, and a pinned trailing-`?` waiting-state contract. Aider sessions show alongside Claude Code, Codex, Pi, and Gas Town with the same three-state vocabulary.
- **`/ir:exec` skill** — issue-driven plan generation. Reads a GitHub issue and produces a structured implementation plan to start a fresh worktree.
- **`coverage-viewer` dev webview** (#222) — local web UI for the agent × scenario fixture matrix; shows which lifecycle events each adapter has recorded.
- **`tui` capability + category taxonomy** — adapters can now declare `tui` as a discoverable capability so the canonical scenario matrix can target TUI-style agents.
- **`IRRLICHT_DEMO_MODE=1`** — daemon flag that disables ProcessWatcher and per-adapter AgentWatchers so `tools/seed-demo-sessions` can stage screenshot scenarios without live processes leaking into the dropdown.
- **Process discovery: `CommandLineMatch` + `TranscriptFilename` probes** — wrapper-launched agents (e.g. invoked via `pgrep -f`) and per-CWD agents that write their transcript next to the project are now detected without a kqueue race.
- **Transcript activity emission for CWD-resident transcripts** — processlifecycle now emits `transcript_activity` events for agents whose transcript lives next to the working directory.

### Fixed
- **Tailer survives `SendMessage` tool across `turn_done`** (#81) — Claude Code emits a `turn_done` between the assistant message and a follow-up `SendMessage` tool call; the tailer used to drop the second half. Sessions stay coherent across that boundary now.
- **Mid-paragraph question detection + snippet trim** (#236) — the waiting-state classifier used to require a question at the end of the assistant message. It now picks up questions mid-paragraph and trims the surfaced snippet for the menu bar block.
- **Skip rhetorical Q&A pairs in question detection** — `"Did X happen? Yes."`-style self-answered questions no longer flip a session to waiting.
- **Menu-bar button stays highlighted while panel is open** (#224) — the NSPanel migration in 0.3.8 lost the button-pressed appearance; restored with explicit highlight-on-show / unhighlight-on-close.
- **Tooltips restored after NSPanel migration** (#218) — switched from SwiftUI `.help()` (silently dropped inside NSPanel) to an `NSView`-bridged tooltip modifier.
- **History bars align with Context layout; modes renamed to "Min" with tooltips** (#210).
- **Cost display drops cents at ≥$100** (#214, #215) — `$132.41` was line-breaking the row; now renders `$132`.
- **Stale session ledger files GC'd** (#185) — the per-session ledger directory used to grow without bound; now cleaned alongside session expiry.
- **Claude Code hook errors silenced when daemon is down** (#221) — hooks no longer print noisy connection errors when irrlichd isn't running.
- **Replay byte-identity test excludes bare `events.jsonl`** — the bare events file is regenerated and shouldn't be part of the byte-identity check.
- **Coverage-viewer rejects path-traversal in API + uses `aider.Parser` after stub removal**.

### Changed
- **`/ir:onboard-agent` overhaul** (#199, #200) — moved from a hardcoded scenario list to a `features.json` + `replaydata` layout with 3-subagent discovery, reasoned merge, and cross-agent feature widening. Adds Codex + Pi drivers and scenario columns; gastown gets its own orchestrator scenario axis. Onboarding aider through this flow validated the design end-to-end.
- **Canonical scenario × adapter fixture matrix** (#228, #231) — covers the 7 actionable scenario × adapter cells plus `agent-question-pending` for claudecode/codex/pi. Adds `drive-pi-interactive.sh` and two pi script-based fixtures.
- **Dev scripts consolidated under `tools/`** — standalone tooling (HTTP viewers, fixture generators, homebrew-tap helper) now lives in top-level `tools/` rather than `core/cmd/`.
- **`tools/homebrew-tap/update-cask.sh` simplified** — single source of truth for cask updates; the in-repo template and external tap repo are bumped from one script.
- **Aider parser**: single regex match per line; documented interface contract.
- **e2e tests for processlifecycle crash, concurrent sessions, fswatcher** (#205) — extracts `IsCanonicalState` and `assertWatchersExited` helpers.

### Distribution
- **Homebrew cask via own tap** (#187) — `brew tap ingo-eichhorst/irrlicht && brew install --cask irrlicht` now resolves to the latest release. The cask is auto-bumped on each release via `tools/homebrew-tap/update-cask.sh`.

### Site
- **Landing page rewrite** — restructured around a "first 30 seconds" pain → state → install flow, with stage-tag legend, install stats strip, and expanded "why" section. New menu-bar explainer screenshot and dark-forest backdrop.
- **README** restaged for first-30-seconds skim, adapters tagged by stage, explainer image promoted to hero banner.
- **Design system reference** added under `tools/irrlicht-design-system/`.

## [0.3.8] — 2026-04-24

### Added
- **Menu bar rewrite: NSStatusItem + NSPanel** (#196) — replaces SwiftUI's `MenuBarExtra(.window)` with a hand-rolled `NSStatusItem` + `NSPanel` so content changes and panel resize land in the same runloop tick. Eliminates the one-frame background flash when a group is collapsed or expanded, and keeps the panel top pinned to the status item while height grows downward. Panel opens rightward from the icon with 10 pt continuous-curve rounded corners; screen-edge clamp + notch fallback cover narrow right-edge displays.
- **SessionListView column rebalance** (#196) — panel 350 → 380 pt, context bar 80 → 100 pt, cost column 40 → 36 pt; branch column shrinks to 44 pt when a subagent badge is present so the context bar's x-position stays constant across rows. `FlowLayout.placeSubviews` is now a two-pass layout so the 7 pt task-progress circles align with the taller "done/total" label.

### Fixed
- **Settings overlay background no longer transparent** — the `NSStatusItem` + `NSPanel` rewrite (#196) intentionally uses a transparent panel so the hosting controller's corner-radius clip can draw the rounded edges, which meant every SwiftUI branch had to paint its own background. `SessionListView` did (`windowBackgroundColor`), but the Settings pane didn't — the desktop wallpaper bled through. `SettingsView` now paints the same `windowBackgroundColor`, and a new `SettingsViewTests` pixel-opacity assertion samples the four corners + center to catch any future regression.
- **"…" overflow indicator beyond 5 menu-bar groups** (#193) — when more than five project groups are active, the menu bar icon shows a trailing "…" so you know the list is truncated rather than silently dropping the extras.
- **Replay harness mirrors daemon parent-hold, permission-pending, and orphan promotion** (#198) — the sidecar-driven replay was skipping three pieces of daemon logic (parent-child hold when subagents are active, permission-pending overlay from PermissionRequest/PostToolUse hooks, and stale-sweep promotion of children whose transcripts go quiet). Extended-check now passes on the subagent and permission-hook fixtures without regenerating their sidecars.

### Changed
- **Core ARS composite 8.0 → 8.2** (#195) — large internal refactor that splits `session_detector.go` into `_activity`/`_helpers`/`_lifecycle`/`_subagent` files, splits `cmd/replay/main.go` into `lifecycle`/`metrics`/`replay_sidecar`/`replay_transcript`/`extended_check`/`types`/`fixtures_test`, and factors `cmd/irrlichd/main.go` request handlers into `handlers.go`. Behavior unchanged; smaller files make each concern easier to locate and test.
- **Unified agent registration via `agents.Config`** (#199) — adding a new agent adapter previously required edits in three disconnected places (metrics `parserFor`, `main.go` `pidDiscovers` map, each adapter's `New()`). Now: one `Config()` constructor per adapter + one line in `main.go`'s `agentCfgs` slice. `PIDDiscoverFunc` moved to `domain/agent/` so `Config` can reference it without violating hexagonal layering. Metrics adapter inverted to accept its parser map from the caller (outbound no longer imports inbound).
- **Shared constants between daemon and replay** — `HookPermissionRequest`/`PostToolUse`/`PostToolUseFailure` in the claudecode adapter, exported `services.SubagentQuietWindow`, and `services.ForceReadyToWorkingReason` — so hook names, the 30 s stale-sweep window, and the force-ready-to-working reason string can't drift between the live classifier and the replay.

### Developer tooling
- **`/ir:onboard-agent` skill** (#199) — produces a canonical scenario × adapter fixture matrix. Scenarios are defined once, agent-agnostically, with a `requires: [capability]` list; adapters declare `Capabilities`, and matrix cells fall out automatically. Unifies the refresh, bootstrap, and new-agent-onboarding workflows behind a single driver.
- **`/ir:agent-landscape` hardened against hallucinations** (#191) — every agent in the landscape report is now verified against the GitHub API before publishing, and the skill refuses to emit entries it can't resolve.

### Site
- **Landing page: Terminals & IDEs column dropped** (#192) — the click-to-focus host list grew past what fit cleanly in the features grid; the column has been removed in favor of a single "works with your terminal" sentence.



### Added
- **Agent history bar with 1s/10s/60s granularity** — server-side pre-aggregates per-session state buckets (`working`/`waiting`/`ready`) under `/api/v1/sessions/history`, so clients can plot state timelines without bloating the WebSocket envelope. A single cycling mode button in the menubar lets you switch between context display and the three history granularities.
- **History persistence across daemon restarts** — history buffers are saved to `~/.local/share/irrlicht/history.json` every 60s and on shutdown, so the timeline survives a restart instead of resetting to empty.
- **Waiting-state question block in the session row** — when a session goes to `waiting`, the menubar row now shows the last assistant question (or the AskUserQuestion text) in an orange block beneath the row so you can see what's being asked without clicking in.
- **Claude Code task list progress** — `TaskCreate` / `TaskUpdate` tool calls are parsed and surfaced as a progress dot strip on the session card (purple outline for pending, purple filled for in-progress, green filled for completed) with a live "N/M" count.
- **Click-to-focus across 17 terminal/IDE hosts** — extending v0.3.6's launcher work, the click-through now covers Zed, Rio, Tabby, WaveTerm, Alacritty, Nova, cmux, Kitty (socket-based) and the JetBrains family in addition to iTerm2/Terminal.app/VSCode.
- **Web UI timeline seeded from persisted daemon history** — on page load the dashboard pulls `/api/v1/sessions/history?granularity=1` and paints the last 60 s immediately instead of starting empty and waiting for live ticks.

### Fixed
- **Menu-bar rows stop updating context/cost mid-session** — the Swift app's incremental WS→`apiGroups` patch path silently dropped updates whose session id wasn't in the hydration-snapshot `groupedSessionIds` set, and never walked `agent.children`, so child/subagent rows only refreshed every 30 s. Now `collectSessionIds` includes children, `patchGroup` patches children in place (and reattaches them when the parent is replaced, since WS payloads don't carry `children`), and a guard miss schedules a debounced rehydration instead of silently dropping. Covered by `SessionManagerApiGroupsTests`.
- **Accurate per-model cost estimation across all adapters and restarts** — the cost tracker now handles usage maps consistently for Claude Code, Codex, and the pi adapter, and survives daemon restarts without double-counting.
- **Offline-at-startup: LiteLLM capacity fetch retries with backoff** — the capacity table used for context-window classification no longer stays empty when the laptop is offline at daemon boot.
- **History timeline ticks flow right→left** — new ticks land in the rightmost bucket and older ones shift left as time passes; previously partial-fill bars grew left→right until they hit the cap. Applies to both the Swift `HistoryBarView` and the web dashboard canvas.
- **Waiting-state detection survives long assistant messages** — `ExtractAssistantText` now keeps the tail of long messages (with a leading ellipsis) so a question-mark at the end still trips the waiting classifier. `AskUserQuestion` tool calls with no text block now fall back to `questions[0].question`.
- **Menubar popover: dynamic height + collapse state survives apiGroups updates** — a single ScrollView with `.fixedSize(vertical:)` lets the popover size to content up to 560 pt; collapse state is lifted onto `SessionManager.collapsedGroupNames` so it persists across session-list refreshes.
- **Tasks: state resets on transcript rotation, stable across schema bump** — the in-memory task list is cleared on file rotation, and the ledger schema is bumped to v2 to force a re-scan so task history is consistent.
- **Menubar tooltips work inside MenuBarExtra panels** — switched from `.help()` (which silently drops inside MenuBarExtra) to an `NSView`-bridged `.tooltip(...)` modifier.
- **Launcher: ProcessRunner calls dispatched off the main thread** — focus/open operations no longer stall the UI.
- **Launcher: fullscreen Space handling** — correctly raises windows that live on a fullscreen Space.
- **Swift: `Task` model renamed to `SessionTask`** so it stops shadowing Swift's built-in concurrency `Task`.

### Changed
- **Parsers split per-adapter** — format-specific transcript parsers (Claude Code `AskUserQuestion`, `TaskCreate`/`Update`) live under the agent adapter packages instead of the shared tailer.
- Cost tracker usage-map extraction and ledger hot-path simplified; history granularity parsing cleaned up; `$0` cost toggle stays visible so the timeframe cycle button remains reachable.
- Task parsing switch flattened; status constants lifted to one place.

### Tests
- History persistence round-trip (save/load/missing-file/corrupt-file/version-mismatch) on `HistoryTracker`.
- `AskUserQuestion` text fallback (short message) and long-text tail storage on the Claude Code parser.
- Integration test for `GET /api/v1/sessions/history` (response shape + bad-granularity 400).
- `SessionMetrics.formattedCost` two-decimal regression (`$12.50`, `$105.00`).
- New `SessionRowView` snapshot suite — waiting-question block and ContextBar token-count label; `NSHostingView.appearance` pinned to `.darkAqua` so snapshots are deterministic regardless of the tester's system appearance.

## [0.3.6] — 2026-04-19

### Added
- Jump to the launching terminal or IDE on session click (#170) — clicking
  a session row (or a delivered desktop notification) now brings the
  originating iTerm2 tab, Terminal.app window, or AXTitle-matched editor
  to the front. Host resolution walks the session's ppid chain; iTerm2
  sessions are matched by UUID, Terminal tabs by tty, and generic apps by
  scoring window titles against the session CWD's deepest ancestor
  segment. Daemon captures the launcher env on first PID assignment
  (`$TERM_PROGRAM`, `$ITERM_SESSION_ID`, tty) so the lookup works even
  after the agent has been running for hours.

### Fixed
- `claude-code`: gate the PID negative filter on transcript mtime (#169)
  — after `/clear`, Claude leaves `~/.claude/sessions/<pid>.json`
  pointing at the old session for up to two minutes. The detector no
  longer holds onto the dead session; once the new transcript's mtime
  exceeds the stale metadata, the PID is reassigned and the old session
  is cleaned up immediately.
- Web UI: render sessions on initial load and drop the dead gastown
  endpoint (#167) — the initial-load handler now reads the bare-array
  response from `GET /api/v1/sessions` (previously it expected a
  `groups`/`orchestrator` wrapper, so the dashboard stayed empty until
  the 30s rehydrator or a WebSocket delta arrived). Also removes three
  stale references to the removed `/api/v1/orchestrators/gastown`
  endpoint.
- macOS: restore project-group reorder chevrons (#172) — the up/down
  chevrons in top-level project group headers came back; ordering is
  derived from `apiGroups` directly so the UI state stays in sync with
  persisted order.
- CLI: `irrlicht-ls` now `go run`s from the repo root via `--workspace`
  so the command works from any subdirectory (#175, carried forward from
  v0.3.5 mid-release).

### Changed
- macOS Launcher refactored into per-host activators behind a
  `HostActivator` protocol — `iTerm`, `Terminal.app`, and an
  `AXTitleMatchActivator` for generic apps each live in their own file.
  Window raising goes through the Accessibility API to raise a *specific*
  window rather than the frontmost one.
- Web UI initial-load path dropped a dead orchestrator assignment and a
  redundant array guard now that `/api/v1/sessions` returns an array
  directly.

### Tests
- End-to-end regression for #169: drives the real
  `claudecode.DiscoverPID` with a stale `~/.claude/sessions/<pid>.json`
  and asserts the full pipeline cleans up the old session inside the
  retry window.
- Snapshot tests for the project-group reorder chevrons; developer
  defaults are restored after the snapshot run.

### Distribution / Dev
- Persistent self-signed `"Irrlicht Dev"` identity — `ir:test-mac` now
  signs dev builds with a stable designated requirement so Accessibility
  and Automation grants survive rebuilds. Run
  `scripts/dev-sign-setup.sh` once to install it.

## [0.3.5] — 2026-04-19

### Added
- Per-group cost display with switchable time frames — project group headers
  now surface day / week / month / year cost totals via a timeframe toggle
  instead of a single hard-coded window (#83, #162).
- `curl | sh` installer at `irrlicht.io/install.sh` — one-line install pulls
  the latest release zip, verifies the sha256, and registers the app with
  LaunchServices. Rerunning removes any previous install cleanly.
- Raw tab in the web UI for inspecting the `/api/v1/sessions` JSON payload
  live.

### Changed
- Capacity data: LiteLLM is now the single source of truth for model
  context windows and pricing. The hand-maintained
  `core/pkg/capacity/model-capacity.json` table is removed; lookups go
  through a process-wide singleton that hot-reloads the LiteLLM cache
  when it's refreshed, so the daemon no longer needs a restart to see
  a new model. Fixes the 200K/1M flip on Claude Opus 4.7 and enables
  1M context for Sonnet 4.6 (#165).

### Fixed
- macOS: state-transition notifications only fire on `working → waiting`
  and `working → ready`. Previously a `waiting → ready` transition also
  fired a redundant "ready" notification (#161).
- Detector: sessions that ended on a user-blocking tool whose start was
  collapsed out of the transcript now get a synthetic `waiting` emitted
  so they don't linger as `working` forever (#150, #160).
- Replay: sidecar timelines now split at `process_exited` boundaries so
  `/continue` sessions with multiple process lifetimes don't report
  spurious extra transitions (#144, #163).
- Detector: subagents stop getting stuck when their parent session emits a
  task-notification (#134, #156).
- Installer: preserves extended attributes and registers with LaunchServices
  so the first launch isn't quarantined (#158).
- Security: `irrlichd` now binds to localhost only and rejects cross-origin
  WebSocket upgrades (#94, #155).
- Site: `curl | sh` install command wraps on narrow screens; hero spacing
  cleaned up.
- Tests: three stale `SessionManagerTests` unit tests updated to match
  the current `SessionState` decoder and the abbreviated
  `RelativeDateTimeFormatter` behavior (#166).

### Distribution / CI
- ARS badge workflow pinned to v0.0.9; `GOPROXY=direct` no longer required.
- `ir:release` skill guards against stale Swift binaries and missing SwiftPM
  resource bundles — the two root causes of the broken v0.3.4 bundle.

## [0.3.4] — 2026-04-14

### Added
- Gas Town full role support with recursive group nesting — first-class role
  registry surfaces all roles (mayor, deacon, witness, refinery, polecat,
  scribe, etc.) instead of treating them as ad-hoc strings, and codebase/rig
  groups can nest recursively for richer worker hierarchies (#154).
- Desktop notifications on macOS state transitions — system notifications
  fire when sessions transition into `waiting` or `ready`, with a Settings
  toggle to opt out (#147).
- Permission-pending state surfaced via Claude Code hooks — the daemon now
  consumes `PreToolUse` / `Notification` hooks to detect modal permission
  prompts directly, instead of inferring them from transcript heuristics
  (#108).
- Landscape page: 3-month growth trend lines, head-to-head comparison page,
  and alternative agent metrics for visitors evaluating the space.

### Fixed
- Push: broadcast buffer increased so bursty session updates no longer drop
  `waiting` transitions before clients can drain them (#152).
- Tailer: user-blocking tools (Bash, AskUserQuestion, …) are preserved
  across the `turn_done` sweep so an in-flight permission prompt is not
  cleared when the assistant briefly stops streaming (#148).
- Daemon: fswatcher event drops that occasionally missed user-blocking
  tool starts have been eliminated by switching to a non-blocking event
  pump (#143).
- macOS dev workflow: `ir:test-mac` now builds a real `.app` bundle and the
  bundle identifier was migrated to `io.irrlicht.app`, so dev and
  release builds no longer share state (#149).
- macOS: corrected dev fallback path for the bundled `irrlichd` binary so
  the menu bar app finds the daemon when running outside an installed
  bundle.
- Skill: `ir:test-mac` builds from the active worktree when invoked from
  one, instead of always rebuilding from the main checkout.

### Changed
- `replay-session` and `replay-lifecycle` consolidated into a single
  `replay` tool with subcommands, removing duplicated transcript-loading
  code and giving the harness one entry point (#141).

### Docs
- README problem section tightened to lead with the concrete user pain
  rather than the architecture.
- `CHANGELOG.md` added at the repo root and wired into the `ir:release`
  skill so every release updates it.

## [0.3.3] — 2026-04-11

### Added
- Cost display toggle in the macOS menu bar, off by default (#130).
- Full session lifecycle recording & replay — `ir:test-mac` writes a sidecar
  of presession/session/tail events; `replay` replays it byte-identically
  against the production tailer + classifier and fails on drift (#107, #138).
- Curated subagent fixtures, including an 11-background-agent transcript, so
  parent/child tracking regressions are caught offline.
- Tailer open-tool tracking replaced FIFO with an id-keyed map, removing
  ordering assumptions that broke under parallel tool calls (#117).

### Fixed
- Subagent count unified at the adapter level — in-process and file-based
  counts are reconciled inside the adapter so the daemon sees one authoritative
  value, eliminating impossible `count=0 / names=[...]` states (#132).
- Subagent quiet window bumped to 30s so long tool-call gaps no longer mark
  children idle prematurely.
- Orphaned subagents are fast-forwarded to `ready` when the parent turn ends,
  instead of hanging in `working`.
- Fast-forward path guarded so subagents whose transcripts are still being
  written aren't killed mid-stream.
- Parent is re-classified immediately when the liveness sweep removes its
  last child.
- FS watcher recursively observes newly-created subtrees so new project/session
  directories don't require a daemon restart.

### Docs
- README rewritten around real user pain and the competitive landscape.
- GitHub community health files added: `CODE_OF_CONDUCT.md`, `CONTRIBUTING.md`,
  `SECURITY.md`, issue and PR templates.

## [0.3.2] — 2026-04-07

### Fixed — Claude Code session state flicker (#102)
Four distinct bugs caused long-running Claude Code sessions to bounce between
`working`, `waiting`, and `ready`. All fixed:

- Stale-tool timer disabled for Claude Code — the 15s heuristic tripped on
  every multi-second Bash invocation. Permission-pending modals and long Bash
  are indistinguishable in the JSONL stream, so the false-positive rate
  swamped the signal. Claude Code now matches Pi's behaviour.
- Tailer open-tool tracking collapsed to a single source of truth. Parallel
  integer counters and a name slice could desync on orphan `tool_result`
  events (from `--continue` resumes or compact replays), producing impossible
  `has_open=false / open_tool_names=[Bash]` states that fooled the classifier.
  Now derived solely from the name slice.
- ESC interrupts distinguished from benign tool errors. The cancellation rule
  fired on any `tool_result.is_error=true` (grep misses, failed builds). A new
  `LastWasUserInterrupt` signal only fires on the literal
  `[Request interrupted by user` marker.
- `stop_reason` allow-list — only `null` is treated as intermediate streaming;
  `max_tokens` and `pause_turn` no longer trip `IsAgentDone()` mid-turn.
  Unknown future values default to "assume streaming".

### Fixed
- OpenCode agent registry corrected to `anomalyco/opencode`, marked as planned.
- Empty-state copy updated from "Claude Code" to the generic "coding agent".

### Added — Testing infrastructure
- Offline replay harness `core/cmd/replay` — any Claude Code, Codex,
  or Pi transcript runs through the production tailer + classifier in virtual
  time. A 500-hour session replays in under a second; every transition is
  logged with reason, metric snapshot, and trigger cause.
- Regression fixtures under `testdata/replay/<adapter>/` — four Claude Code,
  one Codex, one Pi — all post-fix flicker-clean.
- Local session scanner `scripts/find-flicker-sessions.sh` ranks transcripts
  across `~/.claude/projects`, `~/.codex/sessions`, and `~/.pi/agent/sessions`
  by flicker count for harvesting new regressions.

### Docs
- README updated: cost tracking, subagent visibility, corrected state detection.
- State machine cancellation section rewritten around `LastWasUserInterrupt`.
- API reference session metrics schema updated
  (`last_tool_result_was_error` removed, `last_was_user_interrupt` added).

## [0.3.1] — 2026-04-06

### Added
- Dynamic model capacity from LiteLLM — context window sizes and pricing are
  fetched from the LiteLLM API at daemon startup, replacing hardcoded fallbacks.
- Token usage metrics surfaced in debug mode for all models; percentage kept
  next to the context bar.
- Reorderable project groups in the macOS popup and menu bar.

### Changed
- Client-side session expiry setting removed — expiry is handled entirely by
  the daemon, simplifying macOS app settings.
- Stale-tool waiting is now adapter-driven rather than globally configured.

### Fixed
- Session state flicker between tool calls — the first streaming chunk of each
  new assistant message (`stop_reason=null`, new message ID) was misclassified
  as a final message, triggering false working→ready→working transitions.
- Stale-tool timeout raised from 5s to 15s to reduce false waiting transitions
  during long-running builds.
- Codex gpt-5.3 context window mapping corrected to 256K.
- Large appended transcript lines no longer skip events.
- Wrapped Codex transcript schema parsed correctly.
- Pi compaction events treated as activity for working-state detection.
- Subagent tool tracking and debug row display preserved correctly.
- False ready state during tool calls and subagent work prevented.

### Site
- Agent landscape page added with 63 tracked agents, 3-month trends, deny list.

## [0.3.0] — 2026-04-05

### Added
- Permission-pending detection — sessions with non-blocking tool calls awaiting
  user approval transition to `waiting` after a 5-second stale-tool timeout.
- Last-question display — when an agent asks a question and enters waiting
  state, the question text is captured and exposed via the API.
- Session state dots in the macOS menu bar and popover headers for at-a-glance
  status per session.
- Gas Town educational UI — role hierarchy visualization and active tool
  display for Gas Town orchestrator sessions.
- Web dashboard redesign with compact rows, DOM reconciliation for flicker-free
  updates, and a timeline heatmap.
- Adapter-specific PID lifecycle for Codex and Pi sessions.
- `ir:agent-releases` skill — tracks upstream coding agent releases and reports
  changes that impact irrlicht monitoring.

### Changed — Architecture
- Per-adapter transcript parsers — each agent adapter (Claude Code, Codex, Pi)
  now owns its own transcript parser instead of sharing a single parser,
  enabling format-specific handling and cleaner separation.

### Fixed
- PID assignment race condition — serialized PID assignment with state
  transitions so concurrent sessions can't claim the same process.
- Orphan session cleanup after `/clear`.
- Stuck `working` state from local command events (shell escapes).
- Daemon performance degradation when monitoring many concurrent sessions.
- Git root resolution for deleted worktree directories.
- Pi nested message parsing for `role`/`stopReason`/`content` fields.
- Pi PID discovery via command-line pattern matching.
- `assistant` included in `IsAgentDone` fallback for turn detection.

### Site
- Refined hero section — removed glow, shortened copy, cleaned up labels.
- Added counter.dev analytics.

## [0.2.4] — 2026-04-04

### Fixed
- Ready sessions are preserved while their Claude Code process is still alive,
  preventing false cleanup during idle periods.

### Added — Branding
- Will-o'-the-wisp macOS app icon.
- Will-o'-the-wisp SVG/PNG favicons across the landing page and documentation.
- SEO metadata (Open Graph, Twitter Card, canonical URL) on all site pages.

### Added — Distribution
- `ir:release` skill — automated release pipeline covering DMG, PKG, universal
  binary builds, changelog updates, and GitHub release creation.
- Branded DMG background asset for the installer experience.

### Docs
- `CLAUDE.md` development guide with project conventions, build commands, and
  architecture overview.
- `.gitignore` updated for stray build artifacts.

## [0.2.3] — 2026-04-04

### Added — Subagent lifecycle
- Subagent sessions detected and tracked — background and foreground subagents
  appear as child sessions linked to their parent, with real-time state updates
  across the daemon, CLI, and macOS app.
- Purple badge on the parent session row displays a live count of working
  subagents.
- Automatic cleanup — child sessions are removed when they finish (ready),
  when their parent session ends, or when their transcript goes stale.
- Cascade deletion — when a parent process exits, all child sessions are
  cleaned up in one sweep.

### Added — Hierarchical dashboard API
- Unified `GET /api/v1/sessions` response — sessions grouped into
  Orchestrator → Group → Agent → Children hierarchy, with `SubagentSummary`
  tracking total, working, waiting, and ready counts.

### Added — Reliability & multi-instance
- PID discovery retry with backoff (500ms, 1s, 2s) plus a CWD-based fallback,
  so sessions link to their process even when the transcript file isn't
  immediately open.
- Correct session assignment with multiple instances — running two Claude Code
  sessions in the same repo no longer causes one to steal the other's PID.
- No false `waiting` state during tool execution — only truly user-blocking
  tools (`AskUserQuestion`, `ExitPlanMode`) trigger waiting; Bash, Read, and
  Agent correctly show as working.

### Added — macOS app
- Custom purple will-o'-the-wisp flame icon.
- Debug mode — set `IRRLICHT_DEBUG=1` for verbose logging.
- Dev daemon support — the app connects to an already-running development
  daemon instead of spawning its own.
- Clean shutdown — `DaemonManager` properly terminates the embedded daemon on
  quit.
- Daemon version displayed in the app's settings area.
- WebSocket keepalive with periodic pings and auto-reconnect on failure.

### Changed — Architecture
- `SessionDetector` split into focused collaborators: `StateClassifier` (pure
  state transitions), `MetadataEnricher` (git/metrics), and `PIDManager`
  (process lifecycle). Each is independently testable.
- Process scanner and process watcher merged into a single `processlifecycle`
  adapter.

### CLI
- `irrlicht-ls`: `--format json`, `--id` prefix filtering, subagent hierarchy
  with indented child sessions and agent count badges.

### Distribution
- Branded DMG installer — dark-themed drag-to-install window with Irrlicht
  corporate identity, purple wisp glow, and dot grid pattern.
- Ad-hoc code signing with resolved `Info.plist` fixes the
  "damaged or incomplete" error on macOS.
- Ships as `.dmg`, `.pkg`, and standalone `irrlichd-darwin-universal`.

### Docs
- Will-o'-the-wisp favicon (SVG + PNG + ICO) across the landing page and all
  14 docs pages.
- Changelog, API reference (`DashboardResponse` + `SubagentSummary` schemas),
  session detection (subagent lifecycle), architecture (modular collaborators),
  and CLI tools pages all updated.

## [0.2.2] — 2026-04-04

### Added
- Embedded daemon in app bundle — `Irrlicht.app` is now a single artifact
  containing both the SwiftUI menu bar UI and the Go daemon. No separate
  services or LaunchAgents needed.
- `DaemonManager` — automatically spawns, monitors, and restarts the embedded
  daemon with exponential backoff.
- Session tooltips in the menu bar popover for extra context on hover.

### Fixed
- Sessions are removed immediately when their agent process exits, instead of
  lingering as `ready`.
- Stale transcript files from previous runs are no longer picked up as new
  sessions on startup.
- Old session deleted when `/clear` reuses the same PID, preventing duplicates.
- Daemon's own PID filtered from `lsof` so it no longer tracks itself as a
  session.
- Ready-session TTL cleanup — idle ready sessions auto-delete after 30 minutes
  (configurable in app settings).
- UI: horizontal settings/quit button layout with hover states, improved
  session group label sizing and centering.

### Site & docs
- Dark forest hero background with floating wisp animation.
- Project names and signal patterns rendered in hero dots.
- Responsive layout fixes for small screens.
- Landing page and documentation links added to README.
- Troubleshooting docs updated for the new session cleanup behaviour.

### Distribution
- DMG included alongside the existing PKG installer.

## [0.2.1] — 2026-04-03

### Fixed
- Session persistence — sessions with transcripts survive daemon restarts
  (kept as `ready` instead of deleted).
- Subagent sessions linked to parents via transcript path and shown as a
  count badge.
- MCP tool detection — browser automation and other `mcp__*` tools no longer
  flip state to waiting.
- ESC cancellation correctly detected from both working and waiting states
  using `is_error` on tool results.
- Worktree awareness — project name resolved through `git-common-dir`,
  `worktree-` prefix stripped from branches, CWD changes tracked mid-session.

### Added
- Codex support — recursive directory watching, model detection from config,
  token estimation with cost calculation.
- UI: state icons (hammer/hourglass/checkmark), combined cost per project
  group, context-pressure coloring on project names.

## [0.2.0] — 2026-04-03

### Added
- Codex adapter support — recursive directory watching, transcript parsing,
  model/token extraction for OpenAI Codex sessions.
- Cost estimation — per-model pricing data with estimated session cost
  displayed per-session and per-project group.
- Git worktree awareness — resolves main repo root, strips worktree branch
  prefixes, tail-reads transcripts for latest CWD.
- UI: dark-mode Codex icon, context-pressure coloring on project names.

### Distribution
- First bundled macOS installer `Irrlicht-0.2.0-mac-installer.pkg` containing
  the daemon, menu bar app, and auto-start LaunchAgent.

[Unreleased]: https://github.com/ingo-eichhorst/Irrlicht/compare/v0.3.10...HEAD
[0.3.10]: https://github.com/ingo-eichhorst/Irrlicht/releases/tag/v0.3.10
[0.3.9]: https://github.com/ingo-eichhorst/Irrlicht/releases/tag/v0.3.9
[0.3.8]: https://github.com/ingo-eichhorst/Irrlicht/releases/tag/v0.3.8
[0.3.7]: https://github.com/ingo-eichhorst/Irrlicht/releases/tag/v0.3.7
[0.3.6]: https://github.com/ingo-eichhorst/Irrlicht/releases/tag/v0.3.6
[0.3.5]: https://github.com/ingo-eichhorst/Irrlicht/releases/tag/v0.3.5
[0.3.4]: https://github.com/ingo-eichhorst/Irrlicht/releases/tag/v0.3.4
[0.3.3]: https://github.com/ingo-eichhorst/Irrlicht/releases/tag/v0.3.3
[0.3.2]: https://github.com/ingo-eichhorst/Irrlicht/releases/tag/v0.3.2
[0.3.1]: https://github.com/ingo-eichhorst/Irrlicht/releases/tag/v0.3.1
[0.3.0]: https://github.com/ingo-eichhorst/Irrlicht/releases/tag/v0.3.0
[0.2.4]: https://github.com/ingo-eichhorst/Irrlicht/releases/tag/v0.2.4
[0.2.3]: https://github.com/ingo-eichhorst/Irrlicht/releases/tag/v0.2.3
[0.2.2]: https://github.com/ingo-eichhorst/Irrlicht/releases/tag/v0.2.2
[0.2.1]: https://github.com/ingo-eichhorst/Irrlicht/releases/tag/v0.2.1
[0.2.0]: https://github.com/ingo-eichhorst/Irrlicht/releases/tag/v0.2.0
