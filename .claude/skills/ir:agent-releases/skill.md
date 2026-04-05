---
name: "ir:agent-releases"
description: "Check latest releases of coding agents monitored by irrlicht (Claude Code, OpenAI Codex, Pi, Gas Town) and report new features that impact session monitoring. Use when user says 'agent releases', 'check releases', 'agent updates', '/ir:agent-releases', or wants to know if upstream agent changes affect irrlicht."
---

# Agent Release Monitor for Irrlicht

Check latest releases of supported coding agents, compare against previously tracked releases, and report only new changes that impact irrlicht's session monitoring.

## Workflow

### 1. Load Context

Read these reference files (relative to this skill's directory):
- `references/monitoring-surface.md` — what irrlicht monitors and what breaks it
- `references/tracked-releases.md` — previously analyzed releases (skip these)

### 2. Fetch Latest Releases

Use WebSearch and WebFetch to find recent releases for each agent. Search in this order:

#### Claude Code
- Search: `"Claude Code" changelog OR release notes site:docs.anthropic.com OR site:github.com/anthropics`
- Also fetch: `https://docs.anthropic.com/en/docs/claude-code/changelog`
- Focus on: transcript format, tool names, permission system, process lifecycle, new agent modes, subagent changes

#### OpenAI Codex
- Search: `"OpenAI Codex CLI" OR "codex-cli" changelog OR release site:github.com/openai`
- Also check: `https://github.com/openai/codex` releases page
- Focus on: transcript format, session directory structure, config file changes

#### Pi Coding Agent
- Search: `"Pi coding agent" OR "pi-agent" changelog OR release site:github.com/anthropics`
- Note: Pi may have limited public release info. If no results found, note "no public releases found" and move on.

#### Gas Town
- Search: `"Gas Town" orchestrator OR "gastown" coding agent release`
- Note: Gas Town may be internal/limited. If no results found, note "no public releases found" and move on.

### 3. Analyze Impact

For each NEW release not already in `references/tracked-releases.md`, check every change against the monitoring surface.

**Only report a change if it falls into one of these categories:**
- Transcript path or directory structure changed
- Transcript JSONL event schema changed (new/renamed/removed event types)
- Tool system changed (tool names, tool_use/tool_result structure)
- Process binary name or CWD access method changed
- Config file path or format changed
- New session type or agent mode added
- New user-blocking tools introduced
- Permission system changed
- CLI output format changed (Gas Town)
- Subagent/child session mechanism changed

**Skip these — they do NOT impact monitoring:**
- New model releases (unless they change transcript format)
- UI/UX improvements, bug fixes, performance improvements
- New tool capabilities (unless new tool names need special handling like user-blocking)
- Documentation, pricing, or SDK changes

### 4. Generate Report

Output a markdown report:

```
# Agent Release Impact Report — <date>

## Summary
<1-2 sentences: how many agents checked, how many impactful changes found>

## Impactful Changes

### <Agent Name> — <version> (<release date>)

#### <Change Title>
- **What changed**: <concise description>
- **Why it matters**: <which irrlicht component breaks or degrades>
- **What to change**: <file(s) and rough scope of fix in irrlicht>
- **Severity**: CRITICAL / HIGH / MEDIUM / LOW

---

## No Impact
<Releases checked with no monitoring impact, one line each>

## Not Found
<Agents where no new release info was available>
```

If zero impactful changes: "No impactful changes found across all agents."

### 5. Update Tracking

After generating the report, update `references/tracked-releases.md` with all newly analyzed releases under the appropriate agent heading.

Format: `- <version> (<YYYY-MM-DD>) — <impact summary or "no impact">`

## Notes

- Be conservative: only flag changes that genuinely affect file watchers, transcript parsing, state classification, process detection, or orchestrator polling.
- When uncertain, include with note: "**Uncertain** — verify by checking <specific thing>."
- If a release page is inaccessible, note it and move on. Never fabricate release content.
- The report targets a developer who knows the irrlicht codebase. Be specific about components and files.
