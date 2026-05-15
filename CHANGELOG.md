# Changelog

All notable changes to Irrlicht are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and this project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Release artifacts (DMG, PKG, universal `irrlichd` binary, checksums) are
attached to each [GitHub release](https://github.com/ingo-eichhorst/Irrlicht/releases).

## [Unreleased]

## [0.4.3] — 2026-05-15

> **Note:** Assets re-cut later the same day. The original v0.4.3 binary statically linked `Intents.framework` (via `FocusMonitor.swift`'s direct `INFocusStatusCenter` calls), which made macOS TCC preflight `kTCCServiceListenEvent` at process startup and AMFI/TCC SIGABRT the ad-hoc-signed binary with `launchd POSIX 153` on every end-user install. The re-cut moves `FocusMonitor` to dynamic dispatch (`NSClassFromString` + `SecCodeCopySigningInformation` gate) so Intents.framework is never loaded on ad-hoc builds. DND-aware notification silencing is paused until Developer-ID signing lands (#233); restoration tracked in #357. Release-skill regression fix that prevents recurrence: #356.

### Added
- **macOS: autostart Irrlicht.app at login, on by default** (#343) — On first launch the app registers itself as a login item via `SMAppService.mainApp` so the menu-bar overlay is up before you open your first terminal. A new toggle in Preferences flips the setting, and the choice is persisted to `UserDefaults`; a one-time gate (`didApplyDefaultLoginItem`) ensures the default never re-enables itself after you turn it off. The XPC call to launchd runs on a detached `userInitiated` task so the toggle animation stays smooth on slower Macs. The unsigned-then-signed-build dev edge case is called out inline in `applyDefaultIfNeeded` so future maintainers know why the gate exists.

### Fixed
- **macOS: focus VS Code windows on other Spaces** (#344, #348) — When VS Code (or Cursor / Windsurf) was fullscreen on a different macOS Space, clicking a session row in the overlay was a silent no-op. AX's `kAXWindowsAttribute` omits cross-Space fullscreen windows for Electron hosts, so the title-matching activator never saw the target window. The fix enumerates the app's Window menu instead — that list is always complete — and AX-presses the title-matching item so macOS performs the Space switch and window raise atomically. Window-menu titles are recognized across the major macOS-supported locales (en/de/fr/es/it/pt/nl/sv/da/no/fi/pl/cs/ru/tr/ja/zh-Hans/zh-Hant/ko) so the path works for non-English users too. Hardening from /simplify review: drop the second-to-last-menu positional fallback (could trigger a destructive action in non-Cocoa-standard apps), unwrap `menuBarRef` before `CFGetTypeID`, and collapse the imperative lookup into `first(where:) + compactMap`.
- **daemon: session disappears when a second Claude is opened in the same VS Code window** (#345, #347) — Opening a second Claude Code session inside the same VS Code window briefly leaves the new process in the parent CWD before it `cd`s into its worktree. The scanner minted a `proc-<NEW>` pre-session for that CWD; the claudecode adapter's CWD-based PID discovery — with no transcript yet, so the metadata-based filter is bypassed — then returned the *neighbor* process's PID, and `HandlePIDAssigned`'s same-PID cleanup deleted the legitimate neighbor's session row. The row reappeared later via the activity-driven recovery path, which presented as a confusing flicker. Fix: pre-session IDs already encode the PID by construction (`fmt.Sprintf("proc-%d", pid)` in `processlifecycle/scanner.go`), so the daemon now short-circuits adapter-level discovery for them and calls `HandlePIDAssigned` directly with the parsed PID. The short-circuit sits above the `ProcessWatcher == nil` / `discoverFn == nil` guards so it's robust against future adapters that have a process matcher but no `PIDForSession`. Real sessions (UUID IDs with a transcript path) continue through the adapter unchanged. E2E regression test uses `sync/atomic.Int32` for the discovery-call counter so a future regression races visibly under `-race`.

### Docs
- **Landscape page refresh against live GitHub data** (#346) — `site/landscape/index.html` and `site/landscape/compare/index.html` regenerated from a fresh `gh api` sweep of 38 tracked agents (May 15, 2026 snapshot). Aider and OpenCode flip from `planned` to `live` in the landscape table to match their existing adapters under `core/adapters/inbound/agents/`. Two repo renames propagated: Pi `badlogic/pi-mono` → `earendil-works/pi` (the v0.74 move) and Warp `warpdotdev/Warp` → `warpdotdev/warp`. Two plausibility-rule trips noted with explicit reasoning: Warp jumped 26.5k→58.5k stars after open-sourcing its codebase (description and language metadata flipped from null to "Warp is an agentic development environment, born out of the terminal." / Rust / AGPL-3.0); Ruflo grew 33.1k→51.3k on viral promotion. The `ir:agent-releases` skill's `tracked-releases.md` adds 22 new versions across Claude Code (v2.1.120–v2.1.142), Codex (v0.125–v0.130 stables), Pi (v0.71–v0.74), and Gas Town (v1.0.1, v1.1.0), with five upstream items flagged for verification against the live adapters (all five verified post-release; findings recorded on #312 and a new ticket filed at #349 for Pi's `PI_CODING_AGENT_SESSION_DIR` env-var support).

## [0.4.2] — 2026-05-15

### Changed
- **macOS app: drop legacy file-polling, retire vaporware `IRRLICHT_DISABLED` env var** (#337) — `IRRLICHT_DISABLED` was never wired into any code path, and `IRRLICHT_USE_FILES` gated a fallback path in `SessionManager` that the WebSocket transport has fully replaced; both are removed. ~165 lines of dead Swift in `SessionManager.swift` go with them (file watcher, debounce/periodic timers, `loadExistingSessions`, `createInstancesDirectoryIfNeeded`); init unconditionally uses WebSocket. Equivalent orphan reaping still happens daemon-side via `PIDManager`'s `syscall.Kill(pid, 0)` sweep — no safety net was lost. The macOS app no longer reads from the daemon-owned `instancesPath`; that ordering dependency is now documented inline.

### Docs
- **configuration: document four real env vars with concrete recipes** (#337) — `site/docs/configuration.html` drops the `USE_FILES` / `DISABLED` rows and adds rows for the four env vars that exist in code but were undocumented: `IRRLICHT_UI_DIR`, `IRRLICHT_BIND_ADDR`, `IRRLICHT_MDNS`, `IRRLICHT_DEBUG`. A "When to use these" section walks three real recipes — LAN phone access (with both shell-env and `launchctl setenv` flows so it works whether the daemon is shell-launched or auto-spawned by the macOS app), "Dashboard UI not found" recovery (showing the full four-place auto-detect order so the override slots in clearly), and a debug state dump. The original `IRRLICHT_DEBUG=1 open -a Irrlicht` example was broken on macOS — LaunchServices spawns GUI apps without inheriting shell env — and is replaced with three working alternatives (direct binary invocation, `open --env`, `launchctl setenv`).
- **architecture, SECURITY: drop vaporware kill-switch bullet, cross-link network-exposure docs** (#337) — `site/docs/architecture.html` no longer mentions the `IRRLICHT_DISABLED` kill switch (which never existed). `SECURITY.md` cross-links the network-exposure paragraphs to `configuration.html` and to the planned hub-mode design.

### Distribution
- **Release skill: enforce long-line paragraphs in release notes / PR body** (#335) — GitHub renders release-body markdown with the GFM "breaks" extension, so every soft line break inside a paragraph or bullet becomes `<br>`. The v0.4.0 and v0.4.1 release bodies were hand-wrapped at ~75 cols and shipped as a stack of short ragged lines on the release page (both since fixed via `gh release edit --notes-file`). Step 2 of `/ir:release` now carries an explicit line-wrap rule explaining the difference between GFM-with-breaks (release notes, PR body, issue body) and standard CommonMark (`CHANGELOG.md`); Step 8 switches the example from `--notes` to `--notes-file` pointing at a tempfile, so the body is reviewable, re-runnable, and the long lines survive shell escaping.
- **assets: version reference screenshots** (#336) — `assets/session_limits.png` and `assets/straeter_light.png` are now versioned alongside the other reference shots used in README drafts and social posts.

## [0.4.1] — 2026-05-14

### Fixed
- **kitty: click-to-focus lands on the right window and tab** (#326) — Three failures in the kitty click-to-focus path, fixed end-to-end. (1) When kitty is launched from a shell whose env contains `TERM_PROGRAM=vscode` (e.g. a VS Code integrated terminal), kitty inherits that value because kitty itself does not set `TERM_PROGRAM` (upstream kitty #4793); the daemon captured the inherited value and the click was routed to VS Code's activator. `ReadLauncherEnv` now overrides `TermProgram` to `"kitty"` whenever `KITTY_WINDOW_ID` is set and process ancestry confirms kitty.app is a parent — same precedence pattern as the existing `VSCODE_PID` / `TERMINAL_EMULATOR` overrides. (2) With multiple kitty processes running, `AppActivator.activate(bundleID: "net.kovidgoyal.kitty")` always picked one — typically the oldest — and a post-`kitten focus-window` re-activate fired *async*, outside the menu-bar click context, racing macOS yield-focus rules and producing the "raises then drops back" symptom. The daemon now whitelists `KITTY_PID`; a new `Launcher.KittyPID` field is plumbed through to the Swift app; `KittyActivator` calls `NSRunningApplication(processIdentifier:).activate(options: [])` synchronously inside the click handler (right kitty instance, no race) and dispatches `kitten @ focus-window` async with no follow-up re-activate. (3) Apple-signed agents like `pi` (and `/bin/zsh`) hide their env from sysctl, so `KITTY_*` env vars never reached their sessions — every click hit bundle fallback. Three new darwin-only helpers in `osutil_darwin.go` derive these fields without reading the agent's env: `kittyAncestryPID` walks `ppid` to find kitty.app, `kittyListenOnFor` probes the canonical `/tmp/kitty-{kitty_pid}` socket, and `kittyWindowIDForPID` shells `kitten @ ls` and finds the window whose `foreground_processes` contain the session PID. `backfillLauncher` was extended so pre-existing sessions get all four kitty fields refreshed on daemon restart (was previously TTY-only). Docs gain a "Terminal hosts → Kitty" section explaining the required `allow_remote_control yes` + `listen_on unix:/tmp/kitty-{kitty_pid}` kitty.conf config — macOS does not support Linux-style abstract sockets so a filesystem path is required.

### Security
- **kitty: uid-check on `/tmp/kitty-{PID}` socket probe** — `/tmp` is world-writable, so `kittyListenOnFor` could be tricked into trusting a pre-planted Unix socket at the canonical path and sending `kitten @ ls` to a hostile listener. `kittyListenOnFor` now stats the candidate socket and skips any whose owner uid doesn't match `os.Getuid()` — kitty binds with its own credentials, so a foreign-owned socket at the canonical path is either stale or hostile. Test coverage in `osutil_darwin_test.go` exercises the current-uid, foreign-uid (root-gated), non-socket, missing-file, and zero-PID branches.

### Changed
- **kitty: cache ancestry walk in `ReadLauncherEnv`** — The kitty-via-vscode `pi` worst-case path was walking the parent-process chain up to three times (each walk shells `ps` up to `maxAncestry` times): once to verify the inherited `TERM_PROGRAM`, once to recover `term_program` from a hardened-env agent, once again to extract kitty's PID. A new `resolveHostFromAncestry` returning `(termProgram, hostPID)` is memoized inside `ReadLauncherEnv` via a closure, so the chain is walked at most once. `resolveTermProgramFromAncestry` and `kittyAncestryPID` become thin wrappers; call-site behavior is unchanged.

### Docs
- **api-reference, contributing: catch v0.4.0 sweep gaps** (#332) — `site/docs/api-reference.html` still referenced `agentCfgs` in the `GET /api/v1/agents` blurb; renamed to `allAgents` to match `cmd/irrlichd/main.go`. `site/docs/contributing.html` adapter-PR checklist still said "Implements the `AgentWatcher` interface"; replaced with the current `Agent()` / `agent.Agent` / `allAgents` contract.

### Distribution
- **Release skill hardening** (#332) — Step 6 checksum recipe now includes `irrlichd-darwin-universal.tar.gz` (`site/install.sh` verifies it on the curl `--daemon-only` path; omitting it shipped a release where the standalone daemon installer failed the integrity check). Step 7b drops `--delete-branch` from `gh pr merge --squash` with an explanation, so the release branch remains addressable post-squash. Step 4b trigger table gains rows for adapter-package edits (flag `AGENTS.md`) and `main.go` slice/wiring renames (flag `api-reference.html` + `contributing.html`) so future Phase-A-style shape changes can't slip past doc sweeps.

## [0.4.0] — 2026-05-14

### Added
- **macOS: per-event notification sound picker** (#253) — Preferences gains a separate row per notification event (ready / waiting / context-pressure) with its own enable toggle, sound picker (Ping / Chime / Funk / Whoosh / Sosumi / None / Speak aloud / Custom), and preview button. Custom audio (`aiff/wav/mp3/m4a/caf`) is imported into `~/Library/Sounds/`, transcoding `mp3`/`m4a` to LPCM-in-CAF via `AVAudioFile` so `UNNotificationSound` will play it. "Speak aloud" routes title+body through `AVSpeechSynthesizer`, pinned to en-US, and exposes three voice variants (Default / Zoe-Premium / Jamie-Premium); if a premium voice isn't installed, the row renders an inline "Install … in System Settings" button that deep-links to Accessibility → Spoken Content. Defaults: all three events enabled; Ready=Funk, Waiting=Ping, Context=Sosumi. Existing preferences preserved on upgrade.

### Fixed
- **Claude Code: don't bounce ready→working on post-turn `away_summary`** (#329) — Claude Code writes a `system/away_summary` recap ~3 minutes after a turn ends. The parser correctly marked it `Skip=true`, but the fswatcher's mtime trigger still ran the full classification pipeline, and the force-bounce in `processActivity` saw the stale `LastEventType` from the prior `turn_done` and flipped the ready session back to working indefinitely. The tailer now surfaces a `NoSubstantiveActivity` signal when a pass consumed new content but produced no state-relevant change (every line `Skip=true`, no `SubagentCompletions`, no `TaskSnapshot`); the detector short-circuits the force-bounce / re-classify path on that signal while still refreshing `LastEvent`, `EventCount`, `UpdatedAt`, and broadcasting so the UI's "last activity" stays current. Regression fixture `17-issue-329-away-summary` reproduces the bug; six other fixtures' same-timestamp `ready→working→ready` flicker pairs disappear post-fix.
- **Claude Code: detect AskUserQuestion / ExitPlanMode via `PreToolUse` hook** (#307) — Claude Code can lag flushing the assistant `tool_use` block to JSONL for minutes after rendering an `AskUserQuestion` / `ExitPlanMode` overlay; the transcript-driven detector never saw the open tool call, so the session sat in `working` while the user stared at the prompt. A `PreToolUse` hook scoped to `AskUserQuestion|ExitPlanMode` fires synchronously when the model emits the tool_use and flips `permissionPending`; the existing `PostToolUse` matcher is widened to include both tools so the same edge clears the flag when the user answers. The hook handler also rejects `PreToolUse` for any tool name outside the allow-list, so a hand-edited matcher covering `Bash` (etc.) can't flip every tool call to `waiting`. Legacy installs are migrated in place by `upgradeStaleHookMatchers`.
- **Codex: treat `<proposed_plan>` as user-blocking like `ExitPlanMode`** (#322) — Codex's Plan Mode ends a turn with a `<proposed_plan>…</proposed_plan>` block — semantically identical to Claude Code's `ExitPlanMode`. The block arrives as plain assistant text, so the classifier never saw an open user-blocking tool and fell through `IsAgentDone()` → `ready`, leaving the dashboard green while the agent was actually blocked on the user. When an assistant message contains a fully-closed `<proposed_plan>…</proposed_plan>` block, the codex parser now synthesizes a virtual `ExitPlanMode` tool-use — same user-blocking path as Claude Code; the existing `ClearToolNames` hook on user messages closes it when the user replies. Detection scans raw content blocks directly because the shared `ExtractAssistantText` helper truncates to the last 200 runes.
- **Daemon: reject zombie sessions with missing cwd** (#321) — A daemon restart within 2 minutes of `claude --resume` against a session whose worktree had been deleted re-admitted the session as a ghost: the transcript-mtime check treated the refreshed mtime as live, PID discovery failed, and the steady-state sweep only cleaned it up ~75s later. Admission now checks cwd existence alongside the stale-transcript guard at both `onNewSession` and `seedAlivePIDs`; a missing cwd directory is unambiguous, since no live process can run in a directory that no longer exists.

### Changed
- **#159 Phase A — Agent declaration replaces `agents.Config`** — The legacy `agents.Config` struct + its five per-adapter `Config()` constructors and four map helpers (`ParserMap`, `PIDDiscoverMap`, `ProcessNameMap`, `MetricsProviderMap`) are removed. Each adapter now exports a single `Agent()` constructor returning the new sealed-sum declaration: `Agent = Identity × Process × Source`, where `Process` is `ExactName | CommandPattern`, `Source` is `FilesUnderRoot | FilesUnderCWD | ProcessOwnedStore`, and `FileParser` (when applicable) is `JSONLineParser | RawLineParser`. The daemon consumes `[]agent.Agent` directly, with per-projection helpers (`Parsers`, `PIDDiscoverers`, `ProcessNames`, `SubagentCounters`, `MetricsProviders`) in `adapters/inbound/agents/maps.go`. The variant-dispatched watcher wiring in `cmd/irrlichd/wiring.go` replaces the per-adapter loop and omits two useless fswatchers (aider rooted at `~/.aider`, opencode rooted at the SQLite directory) that emitted nothing in production. Phase A also lands an M0 contract-test layer (`SessionState` on-disk, 7 `PushMessage` shapes, `GET /api/v1/agents`) to guard the public surface across the remaining phases.
- **#159 Phase A — `Watcher` port replaces `AgentWatcher`; identity carried on the merge pipeline** — The inbound watcher port gains an `Identity()` method and `WithIdentity()` builder; each per-watcher drain goroutine in `SessionDetector.Run()` captures identity once and wraps every event with it, so `agent.Event.Adapter` is removed. The old `AgentWatcher` interface is deleted. `NewSessionDetector` panics at construction when any watcher's `Identity()` is the zero value, so sessions can no longer be bootstrapped with empty `state.Adapter` (previously papered over by a fallback in `onNewSession`). `metrics.New` takes a single `Registry` struct (parsers, subagents, providers, fallback by name) instead of four positional maps.

### Docs
- **`/ir:release` skill — adapt to PR-required `main` + fix tap-publish race** (#306) — `main` is now protected by a "Changes must be made through a pull request" repo rule, so the old skill's `git push origin main --tags` was rejected with `GH013` during the v0.3.13 release. The release flow now stages on a short-lived `release/v$NEW_VERSION` branch, opens a PR with the drafted release notes, squash-merges, hard-resets local `main` to `origin/main`, and tags the merged commit. Step 6.5 now patches the in-repo cask template via `sed` and leaves the sibling tap untouched until Step 8.5, so the external publish can't be poisoned by a pre-existing local commit in the sibling tap that trips `update-cask.sh`'s "nothing to commit" guard.

### Distribution
- All release artifacts as before: signed universal `Irrlicht.app` (DMG + PKG + curl-installer ZIP) plus `irrlichd-darwin-universal.tar.gz` for the `--daemon-only` curl install. Checksums in `checksums.sha256`.

## [0.3.13] — 2026-05-11

### Fixed
- **OpenCode: suppress ghost sessions when no opencode process is live** (#22e10ef) — the OpenCode watcher's startup scan emitted `EventNewSession` for every non-archived row whose `time_updated` fell within `maxAge`, regardless of whether `opencode` was actually running. On every daemon restart, every historical session in the DB became a "live" row in the menu bar with no path that ever removed them. Now gates emission on a live opencode process owning the session's CWD via `processlifecycle.LiveCWDs(processName)`. Sessions in the DB with no live process are tracked (cursor seeded so historical activity isn't back-filled if the process later starts) but not surfaced; a new `emitted` flag enables `EventNewSession` to fire on the dormant→live transition. Also drops the "skip first call" branch in `emitRemovedForArchivedSessions` so pre-startup archives get cleaned up correctly.
- **OpenCode: clean up carryover ghost state on startup** (#fec4a59) — the previous fix gates *new* emissions on a live process, but users upgrading from v0.3.12 whose state directory already contains ghost session JSON files weren't helped: `syscall.Kill` skips `PID=0` and `isStaleTranscript` short-circuits to false for `?session=` paths. Adds a third branch to `isStartupZombie`: `PID=0` sessions whose `TranscriptPath` is DB-backed and whose adapter has a registered process name are deleted iff no live process of that name owns the session's CWD. Safety: only deletes when `liveCWDs` returns a definitive non-nil result; on lookup error or unregistered adapter, the session is kept.

### Changed
- **OpenCode: GC stale cursors, drop dead initialArchiveCleanupWindow** (#ec2fe04) — `gcExpiredCursors` drops cursor entries whose `lastObserved` predates `maxAge` so the cursor map can't grow without bound for users who accumulate many sessions but rarely run the CLI. Tracked separately from `cur.lastTS` so a session whose `time_updated` bumps without new parts isn't wiped prematurely. Reverts the unused first-call cutoff tweak.
- **OpenCode: consolidate DB-backed predicate, cache liveCWDs lookup** (#85f4bb4) — DRYs the "is this path DB-backed?" check (one source of truth in `helpers.go`) and caches `liveCWDs` per adapter inside `CleanupZombies` so M ghost candidates sharing an adapter pay one `pgrep` fork, not M. Tightens the `isStartupZombie` testable surface.
- **Release: keep homebrew tap from silently lagging** (#299) — the tap was stranded at v0.3.8 across four releases because Step 8.5 no-op'd silently when `IRRLICHT_TAP_DIR` was unset. `update-cask.sh` now auto-discovers a sibling `../homebrew-irrlicht` clone before bailing and hard-fails on `--push` without a tap dir instead of exiting 0; the skill's Step 8.5 verifies the published cask version after publish and prints a loud WARNING on mismatch.

### Docs
- **README: supported platforms table** (#301, #302, #303) — adds a Platforms table with CLI access references and links the macOS access cell to the releases page.
- **Landing: mark OpenCode alpha; teach release skill the landing-page grid** (#1c78f83) — `site/index.html` listed OpenCode as `planned` even though the adapter shipped in v0.3.12; the parallel grid on the landing page was missed because the Step 4b sweep only enumerated `site/docs/*.html`. Extends the dynamic enumeration to scan `site/*.html` and adds explicit trigger-table rows for adapter maturity-stage changes and new platforms so future stage promotions can't slip through.
- **README: note codeburn alongside other quota & cost trackers** in the positioning section.

## [0.3.12] — 2026-05-09

### Added
- **OpenCode adapter** (#255) — first agent on the new SQLite-backed monitoring path. OpenCode stores all session data in a single WAL-mode database rather than JSONL files; the adapter ships an fsnotify WAL watcher polling `session`/`part` tables, a parser mapping step-finish / text / tool rows to normalized events, a `MetricsProvider` that bypasses the JSONL tailer for cost + token snapshots, CWD-based PID discovery, parent-child session linking via `parent_id` from the DB, and `EventRemoved` emission on `session.time_archived`. Closes #100.
- **`ir:triage` skill** (#283) — strictly diagnostic GitHub-issue triage skill that scores each issue against a 6-axis readiness rubric (Scope / Specification / Verifiability / Context / Independence / Reversibility) and lands it at `ready-for-agent` or `needs-info` with a one-line justification per label decision. Never invents acceptance criteria or sketches implementation; bulk sweep skips already-triaged issues but explicit `/ir:triage #N` always re-triages and edits the prior comment in place.

### Fixed
- **History bar right-anchors when states overflow `bucketCount`** (#286) — `HistoryBarView`'s anchor math only worked when `states.count <= bucketCount`. After #249 lowered `bucketCount` from 150 → 60, the 150-state test fixture started rendering an all-green bar because `offset` collapsed to 0 and the loop drew the *oldest* states inside the canvas while the newest tail was clipped past the right edge. Now takes `states.suffix(bucketCount)` and recomputes offset against the visible slice.
- **Claude Code: reconcile phantom `in_progress` from `task_reminder` snapshots** (#289) — Claude Code occasionally emits a `TaskUpdate` against a stale taskId and never sends a follow-up `completed`, so the UI hung at `n / total` forever. The tailer now treats the `task_reminder` attachment as authoritative — after the existing TaskDelta loop, any local `in_progress` whose ID is missing from the snapshot is demoted to `completed`, and snapshot status wins on any present-with-divergent-status case. Closes #282.
- **macOS: sync `apiGroups` on local session delete + reset** (#287) — local deletes/resets only updated `sessions` (menu bar) and `sessionMap` and skipped `apiGroups` (list view), so a deleted session lingered in the list until rehydration and a reset row stayed `working` in the list while the menu bar already showed `ready`. Mirrors the WS handler and adds `SessionState.withState(_:)` so all 10 optional fields (children, role, subagents, adapter, launcher, …) survive the round-trip instead of being silently dropped by field-by-field reconstruction.

### Changed
- **Co-locate adapter display name + icons with Go adapters** (#284) — adds `DisplayName` + `IconSVGLight`/`IconSVGDark` to `agents.Config` and a new `GET /api/v1/agents` endpoint serving them. Adapter is now the single source of truth for its own branding; adding a new adapter is a Go-only change. The macOS app and web dashboard look up name and icon from the registry — the five Swift `<adapter>SVG` functions and two switch statements in `SessionState.swift` are gone. Web dashboard renders adapter SVGs via `<img src="data:image/svg+xml;base64,...">` so the browser image-loading sandbox blocks scripts even if the daemon binary is tampered with. `AgentRegistry` is `@MainActor`-isolated for Swift 6 strict-concurrency cleanliness. Closes #260.

### Docs
- **Adapter interfaces documented with exact Go signatures** (#292) — `site/docs/adapters.html` gains an "Adapter Interfaces" section with the actual `agents.Config`, `tailer.TranscriptParser` (plus the optional `RawLineParser`, `IdleFlusher`, `PendingContributor`, `ParserStateProvider` hooks), `agent.PIDDiscoverFunc`, and `agent.Event` / `inbound.AgentWatcher` types, with file paths so readers can jump from doc to source.
- **Release skill sweeps every docs page on each release** (#293) — `/ir:release` Step 4b now enumerates `site/docs/*.html` and top-level READMEs dynamically (rather than from a hardcoded list) and walks each against the release diff so new pages cannot be silently missed.

### CI
- **Coverage workflow surfaces badge update failures** (#281) — validates `GIST_SECRET` / `COVERAGE_GIST_ID` up front, captures the gist `PATCH` response so a non-2xx fails the job with the actual error body instead of dying silently inside `curl -sf … > /dev/null`, and adds connect/max-time + retry settings so transient 5xx and stalled handshakes don't hang the step.

### Tests
- **Replay: refresh stale opencode `baseline-hello` golden** (#285) — golden was committed in #255 with a populated `source_transcript` field that the test zeros before comparison; regenerated via `UPDATE_REPLAY_GOLDENS=1` to bring opencode in line with the other 4 adapters.

## [0.3.11] — 2026-05-02

### Fixed
- **Serve stale LiteLLM cache instead of zeroing all costs** (#275) — when the model-pricing cache was older than 24h, every cost calculation silently fell to zero (and `omitempty` dropped `estimated_cost_usd` from output entirely). Stale pricing is now served to non-daemon callers (replay tool, CLI, tests); `IsCacheStale` keeps its job of driving the daemon's background refresh.
- **Aider: keep turn open across multiple `> Tokens:` lines** (#274) — under `--yes-always`, aider auto-accepts file-add prompts and re-prompts the model within one user turn. The parser now treats `> Tokens:` as end-of-one-model-call (emitting `assistant_message`) and synthesizes the `turn_done` via an idle-flush hook, so sessions don't flip to `ready` mid-turn.
- **Aider: emit turn_done on LLM-layer error** (#273) — when aider prints a `> litellm.BadRequestError: …` blockquote without a `> Tokens:` line, the session no longer hangs in `working` forever.
- **Tailer: drop bufio.Scanner cap so JSONL lines >2 MB don't wedge sessions** (#271) — large transcript lines used to silently stop being processed once they exceeded the default 64 KB scanner buffer.
- **Close 13 Code Scanning alerts** (#266).
- **macOS: use brand off-flame for idle/empty state** (#248).

### Changed
- **Performance: shrink mobile payload, unblock render path** (#272) — image optimization and CSS deferral for the marketing site; meaningful drop in mobile LCP/CLS.
- **Daemon serves dashboard from disk, drops `//go:embed`** (#267) — runtime walk-up search for `platforms/web/index.html` so the dashboard can be hot-edited in dev and shipped as a separate file in production bundles.
- **Session history streams over WebSocket; bit-pack 60-bucket rings** (#249) — replaces polling with live updates and a more compact wire format.
- **Centralize per-adapter transcript extension** (#251).

### Distribution
- **`onboard-agent` covers claudecode/codex multi-turn + interrupted-turn** (#269) — fixture matrix gains coverage for two real-world replay scenarios.

### Docs
- **Maturity-stage rubric and adapter onboarding section** (#264).

### Tests
- **Replay: zero `source_transcript` so goldens are worktree-portable** (#250).

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

[Unreleased]: https://github.com/ingo-eichhorst/Irrlicht/compare/v0.4.3...HEAD
[0.4.3]: https://github.com/ingo-eichhorst/Irrlicht/releases/tag/v0.4.3
[0.4.2]: https://github.com/ingo-eichhorst/Irrlicht/releases/tag/v0.4.2
[0.4.1]: https://github.com/ingo-eichhorst/Irrlicht/releases/tag/v0.4.1
[0.4.0]: https://github.com/ingo-eichhorst/Irrlicht/releases/tag/v0.4.0
[0.3.13]: https://github.com/ingo-eichhorst/Irrlicht/releases/tag/v0.3.13
[0.3.12]: https://github.com/ingo-eichhorst/Irrlicht/releases/tag/v0.3.12
[0.3.11]: https://github.com/ingo-eichhorst/Irrlicht/releases/tag/v0.3.11
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
