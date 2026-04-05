# Tracked Agent Releases

This file tracks releases already analyzed. Update after each run to avoid re-reporting.

## Format

Each entry: `- <version> (<date>) — <one-line summary of impact or "no impact">`

## Claude Code
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
- v0.119.0-alpha.11 (2026-04-04) — no impact (alpha, no details available)
- v0.118.0 (2026-03-31) — no impact (Windows sandbox, device code auth, exec stdin)
- v0.117.0 (2026-03-26) — LOW: removed read_file/grep_files handlers, sub-agent path addresses, tui rename
- Pre-v0.117.0 (2025-05 to 2026-03) — no impact (session format stable, rollout-*.jsonl matches *.jsonl glob, YYYY/MM/DD nesting handled by recursive WalkDir)

## Pi Coding Agent
- v0.65.0 (2026-04-03) — CORRECTED: NO IMPACT (parser already skips session_switch/session_fork events; irrlicht uses filesystem events for lifecycle)
- v0.64.0 (2026-03-29) — no impact (prepareArguments hook, ModelRegistry change)
- v0.63.2 (2026-03-29) — no impact (ctx.signal for cancellation)
- v0.63.1 (2026-03-27) — no impact (label timestamps)
- v0.63.0 (2026-03-27) — no impact (session tree structure, internal API)
- v0.62.0 (2026-03-23) — HIGH/UNCERTAIN: "Session directory structure was modified" per changelog; verify if ~/.pi/agent/sessions path changed
- v0.61.x–v0.60.0 (2026-03) — details unavailable (changelog truncated)

## Gas Town
- v1.0.0 (2026-04-02) — MEDIUM: new Boot/Dog role not in irrlicht's roleMeta; verify role derivation
- v0.13.0 (2026-03-29) — no impact (new CLI commands not monitored, session-hygiene plugin removed)
- v0.12.1 (2026-03-15) — no impact (ACP protocol, gt assign/mountain, per-repo settings)
- v0.12.0 (2026-03-11) — no impact (event-driven polecat lifecycle, session_running field unchanged)
- v0.11.0 (2026-03-05) — LOW/UNCERTAIN: Beads Classic code paths removed; verify gt --json output still matches types.go structs
- v0.10.0 (2026-03-03) — no impact (circuit breaker, telemetry, mTLS; min beads v0.57.0)
- v0.9.0 (2026-03-01) — no impact (gt doctor --fix, env-vars check)
