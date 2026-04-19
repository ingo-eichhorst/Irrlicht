# Changelog

All notable changes to Irrlicht are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and this project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Release artifacts (DMG, PKG, universal `irrlichd` binary, checksums) are
attached to each [GitHub release](https://github.com/ingo-eichhorst/Irrlicht/releases).

## [Unreleased]

## [0.3.5] — 2026-04-19

### Fixed
- Replay: sidecar timeline for `/continue` sessions that span multiple
  daemon lifetimes is now split at `process_exited` boundaries instead
  of sharing one debounce state machine across both lifetimes. A gap fs
  event at the lifetime boundary no longer drives a ghost lifetime-2
  transition, and the single captured `processExitAt` no longer
  silences legitimate lifetime-2 coalesce fires (#144).
- macOS: desktop notifications now only fire on transitions *from*
  `working`, eliminating the brief waiting→ready notification noise that
  accompanied some same-turn user-blocking tool flows (#161).
- Detector: synthetic `working→waiting` emitted when fswatcher collapses
  a user-blocking tool's `tool_use` and `tool_result` into the same
  tailer pass, so the brief waiting episode is no longer lost (#150).
- Installer: curl install preserves provenance metadata and registers
  the app with LaunchServices so Finder opens work out of the box
  (#158).
- Detector: subagents no longer get stuck on the parent's
  task-notification path; the parent's authoritative completion signal
  now unsticks orphaned children deterministically (#134).
- Security: `irrlichd` network exposure locked down — the daemon binds
  loopback-only by default (#94).

### Added
- Install: curl installer at `irrlicht.io/install.sh` — one-liner
  installs the app, daemon, and LaunchAgent on macOS.
- Install: rerunning the installer cleanly removes the previous install
  before laying down the new bits.
- Web: "Raw" tab in the session inspector surfaces the underlying API
  response for debugging detector behavior.

### Changed
- Site: hero spacing tightened and the curl install command moved into
  the primary CTA slot.

### Distribution
- Skill `ir:release` now guards against stale Swift binaries and missing
  SwiftPM resource bundles — the two regressions that shipped a
  10-day-old / crash-at-launch build in v0.3.4 can no longer happen
  silently.

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

[Unreleased]: https://github.com/ingo-eichhorst/Irrlicht/compare/v0.3.5...HEAD
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
