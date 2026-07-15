---
name: "ir:agent-releases"
description: "Check latest releases of coding agents monitored by irrlicht (Claude Code, OpenAI Codex, Pi, Gas Town, Aider, OpenCode, Gemini CLI, Kiro CLI, Antigravity, Mistral Vibe) and report new features that impact session monitoring. Use when user says 'agent releases', 'check releases', 'agent updates', '/ir:agent-releases', or wants to know if upstream agent changes affect irrlicht."
---

# Agent Release Monitor for Irrlicht

Check latest releases of supported coding agents, compare against previously tracked releases, and report only new changes that impact irrlicht's session monitoring.

## Workflow

### 1. Load Context

Read these reference files (relative to this skill's directory):
- `references/monitoring-surface.md` — what irrlicht monitors and what breaks it
- `references/tracked-releases.md` — previously analyzed releases (skip these)

### 2. Fetch Latest Releases

Sources below were verified on 2026-07-15. Prefer the named URL over a search — several
agents have moved repos or have release pages that lag reality, and the guesses this skill
used to carry cost real time.

**Changelogs are a starting point, not evidence.** The 2026-07-15 run's two highest-severity
findings (vibe's in-place `messages.jsonl` rewrite, vibe's `read`→`read_file` rename) were
**undocumented upstream**, and its two most serious Claude Code findings came from inspecting
`~/.claude/projects` on this machine, not from any release note. For anything that would be
HIGH if true, confirm at source (clone + diff at tags, or `raw.githubusercontent.com`) or on
disk. Conversely, do not report a HIGH from changelog prose alone — the same run produced four
plausible-but-false findings that source/disk checks killed.

Where an agent is installed locally, **check the local install and its real files** — that is
the strongest evidence available, and it caught things no changelog mentioned. Note the version
you verified against, since it may lag the newest release.

> ⚠️ **`grep` in this repo is ugrep with `-I`** and will silently return 0 matches on
> transcript `.jsonl` files (long lines read as binary). Use `python3`, `rg -a`, or
> `git grep` when censusing transcripts — a naive `grep` nearly buried three real findings.

#### Claude Code
- Fetch: `https://raw.githubusercontent.com/anthropics/claude-code/main/CHANGELOG.md` (full, authoritative)
- Also: `https://docs.anthropic.com/en/docs/claude-code/changelog`, GitHub releases API for tag dates
- Note: version accounting is imperfect — some versions have changelog entries but no release tag, and some have a tag but **no entry** (e.g. v2.1.177). Do not guess at missing entries.
- Focus on: transcript format, tool names, permission system, process lifecycle, new agent modes, subagent changes
- **Check `~/.claude/projects` directly** — the transcript store is recursive (`<session>/subagents/`, `tool-results/`, `workflows/`, `session-memory/`), and `relocated` / `worktree-state` events are not documented anywhere

#### OpenAI Codex
- Check: `https://api.github.com/repos/openai/codex/releases` (paginated) — the releases page is authoritative
- **`CHANGELOG.md` is a stub** that only links to the releases page; there is no changelog content there
- Verify against source in `codex-rs/` (`rollout/`, `protocol/`, `features/`, `thread-store/`) rather than trusting release prose
- Focus on: transcript format, session directory structure, config file changes
- Standing watch: the `paginated_threads is not supported yet` guard in `app-server/src/request_processors/thread_processor.rs`, and `ThreadHistoryMode`'s `#[default]` — see #1067

#### Pi Coding Agent
- **Repo moved twice**: `badlogic/pi-mono` → `earendil-works/pi-mono` → **`earendil-works/pi`**
- Fetch: `https://raw.githubusercontent.com/earendil-works/pi/main/packages/coding-agent/CHANGELOG.md`
- Also: `.../packages/coding-agent/docs/session-format.md` (authoritative entry-type list), `docs/rpc.md`, `docs/sessions.md`
- npm scope: `@earendil-works/pi-coding-agent`
- ⚠️ **The GitHub releases page and npm registry render misleading 2024/2025 dates.** Trust CHANGELOG.md.

#### Gas Town
- Check: `https://github.com/gastownhall/gastown` releases + CHANGELOG (`steveyegge/gastown` redirects there)
- Diff tagged source for the polled surfaces — release prose does not mention JSON schema changes
- Focus on: the four polled `--json` surfaces (`rig list`, `polecat list --all`, `dog list`, `boot status`), roles, `GT_ROOT`
- **Additive fields are safe** (`json.Unmarshal` ignores unknowns); **new enum values are the risk** — they pass through raw and degrade silently in the UI

#### Aider
- Fetch: `https://raw.githubusercontent.com/Aider-AI/aider/main/HISTORY.md` (curl it — a summarizer returned a stale read of this file)
- **PyPI is authoritative for dates**: `https://pypi.org/pypi/aider-chat/json`. The GitHub Releases page **lags badly** (newest tag v0.86.0 from 2025-08-09 while PyPI had 0.86.2).
- Focus on: chat history file format (`.aider.chat.history.md`), CLI process naming, config file changes
- **Aider is effectively dormant** — no release since 2026-02-12, 5 commits in the 2.5 months before 2026-07-15. Safe to check every few runs rather than every run.

#### OpenCode
- Check: `https://github.com/anomalyco/opencode/releases` — **`sst/opencode` redirects here** (repo transferred; same project/history)
- npm package remains `opencode-ai`; binary still `opencode`
- Focus on: session **SQLite DB** schema (`~/.local/share/opencode/opencode.db`), process name, config file changes
- Note: sessions have been SQLite (drizzle) since ~2026-02-14, **not** JSON files. Legacy JSON storage still exists in `packages/opencode/src/storage/storage.ts` but `session.ts` no longer routes through it — do not mistake the leftover for live storage.
- The DB filename is channel-scoped upstream (`opencode-<channel>.db`); stable channels (`latest`/`beta`/`prod`) get plain `opencode.db` — see `packages/core/src/database/database.ts`

#### Gemini CLI — ⚠️ unmaintained, skip by default
- **As of 2026-07-15 this adapter is no longer actively maintained. Skip it in sweeps** unless the user explicitly asks for it. The adapter still ships and still monitors live sessions, but upstream changes are not tracked and the standing watches below are not being acted on. See `references/monitoring-surface.md` §10.
- Check: `https://github.com/google-gemini/gemini-cli/releases` — **authoritative; the repo has no root `CHANGELOG.md`** (404s on `main`)
- Releases very frequently (nightlies + previews) — focus on stable, group the rest
- Best evidence is a local clone + `git diff <old-tag>..<new-tag>` on `packages/core/src/config/storage.ts` and `packages/core/src/services/chatRecordingService.ts`
- Focus on: session transcript format under `~/.gemini/tmp/`, process naming (`bin/gemini`), heap-bump worker re-exec behavior
- Standing watch: SEA/native binary becoming default (`argv[0] === argv[1]`, env `GEMINI_CLI_NO_RELAUNCH=true`), and the `adk.agentSessionSubagentEnabled` flag — see #1068

#### Kiro CLI
- Check: **`https://kiro.dev/changelog/cli/`** (and per-version pages like `/2-4/`) — authoritative
- ⚠️ **`https://github.com/kirodotdev/Kiro/releases` has ZERO published releases** (issues-only repo). Do not conclude "no releases" from it.
- Focus on: session transcript format under `~/.kiro/sessions/cli/`, sidecar metadata schema, process naming, session-rotation semantics (`/clear` vs `/chat new` vs `/rewind`)
- Note: the docs claim SQLite storage in `~/.kiro/`; **disk says otherwise** (`.jsonl`/`.json`, no `.db`). Treat as loose wording unless disk confirms — but re-check if sessions go missing.
- `session_created_reason` in the sidecar is a first-class rotation discriminator (observed: `subagent`, `rewind`)

#### Antigravity
- **Antigravity IS publicly documented** — fetch **`https://antigravity.google/changelog`**
- ⚠️ It is an Angular SPA: WebFetch is blocked and the static HTML is empty. The data lives in the **`main-*.js` bundle** as two arrays — `engineSections` (Antigravity CLI tab) and `ideSections` (IDE tab). Extract from the bundle.
- The changelog is entirely user-facing and **never mentions storage internals** — verify transcript/DB layout on disk instead
- Focus on: transcript path `<root>/brain/<conv-id>/.system_generated/logs/transcript.jsonl`, the sibling `conversations/<conv-id>.db` store, multi-root Source (`~/.gemini/antigravity/` + `~/.gemini/antigravity-cli/`)
- Note: `agy --version` reports a 1.x number that does **not** track the changelog's 2.x engine numbering

#### Mistral Vibe
- Fetch: `https://raw.githubusercontent.com/mistralai/mistral-vibe/main/CHANGELOG.md`; also PyPI `https://pypi.org/pypi/mistral-vibe/json`
- ⚠️ **The changelog is insufficient and has been misleading here.** Tool names derive from class names (`BaseTool.get_name()` → snake_case), so a class rename silently renames a tool — the v2.19.0 `read`→`read_file` rename appears **nowhere** in the changelog. **Verify tool names by tracing classes at tags** (blobless clone), corroborated against `CHAT_AGENT_TOOLS`.
- Focus on: transcript format under `~/.vibe/logs/session/<session-id>/messages.jsonl`, the sibling `meta.json` sidecar schema (`config.active_model`, `config.auto_compact_threshold`, `stats.context_tokens`), and process naming (a Python console-script, so the command line — not the process name — is what matches)
- Also watch `SessionLogger` write behavior — v2.19.1 introduced in-place rewrites (`os.replace()`), which is a tailer hazard, not a format change

### 3. Analyze Impact

For each NEW release not already in `references/tracked-releases.md`, check every change against the monitoring surface.

⚠️ **An upstream change is not an irrlicht problem until you've checked irrlicht's own
adapter code.** The 2026-07-15 sweep reported several findings that were already fixed,
already tolerated by construction, or targeted fields no consumer reads — because it
reasoned from changelogs/monitoring-surface.md alone (see `references/tracked-releases.md`'s
"Rejected Findings" section for the specifics). Before writing up any candidate finding:
grep/read the relevant adapter source for how irrlicht actually handles the surface in
question, and cite the file:line that proves the gap is real (or that it isn't). If you
can't point at a specific line where the described behavior would break, don't report it.

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
