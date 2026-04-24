---
name: "ir:agent-landscape"
description: "Scan the web for coding agents and agent orchestrators, track GitHub stars and trends, rank by popularity+momentum, and publish a report to the irrlicht site. Shows which agents irrlicht already supports. Use when user says 'agent landscape', 'scan agents', 'coding agent tracker', 'agent popularity', '/ir:agent-landscape', or wants to see the competitive landscape of coding agents."
---

# Agent Landscape Tracker

Discover, track, and rank coding agents and agent orchestrators. Publish a styled HTML report to the irrlicht site at `site/landscape/index.html` and an in-depth comparison at `site/landscape/compare/index.html`.

## Non-negotiable rules

Past runs of this skill invented data — fabricated star counts, wrong repo paths, copied descriptions instead of reading the actual README, and fake historical snapshots (e.g. "2026-01-01" entries for agents the skill only started tracking in April). Follow these rules to prevent a repeat:

1. **Never invent a value.** Every `stars`, `language`, `license`, `description`, `funding_millions_usd`, `estimated_users`, or historical snapshot must come from a concrete source you can quote (the gh CLI, a specific WebFetch URL, a specific search result). If you don't have a source, write `null` and move on.
2. **Never copy an old value forward.** If you can't re-verify a field on this run, set it to `null` instead of leaving whatever was in the file from last time.
3. **Always use `gh api` for GitHub data, not WebFetch.** `gh api repos/OWNER/REPO` returns canonical stars, language, license, description, archived flag, and follows renames. `WebFetch` on github.com returns JS-rendered pages that routinely give stale numbers.
4. **Honor GitHub's repo-rename redirects.** `gh api` returns a `full_name` field. If `full_name != OWNER/REPO` that you requested, the repo has been transferred. Update `github_repo` to the new canonical path in `agent-data.json`.
5. **Never write a historical snapshot you didn't measure.** `stars_history` may only contain entries this skill actually measured. Do not back-fill "~3 months ago" or "~1 month ago" rows from memory or estimate.
6. **Plausibility check before writing.** Before writing a new stars value, compare to the prior snapshot. If the delta is >30% in <30 days for a repo with >5k stars, investigate before trusting it — it's more likely a bad query than real growth. Common causes: cached HTML, wrong repo, joke repo inflating itself via README marketing.
7. **Descriptions come from the repo's own `description` field**, not from product marketing pages. If a description contains attribution (e.g. "Anthropic's X" or "Google's Y"), verify the GitHub owner matches the claim. `badlogic/pi-mono` is not Anthropic's. `block/goose` is not Block's anymore (it was transferred). `cursor/cursor` is not the editor source (it's the issue tracker).
8. **Archived repos go in the "archived" list, not the ranked table.** `gh api` returns `archived: true` — respect it.
9. **Short-window growth is not 1-month or 3-month growth.** Until the repo has a snapshot ≥30 days old, the growth column must read "Recent growth since <date> (Nd ago)", not "1M" or "3M". The HTML generator already enforces this.

## Data File

All state persists in `references/agent-data.json` (relative to this skill directory). Schema:

```json
{
  "last_updated": "2026-04-24",
  "agents": [
    {
      "name": "Aider",
      "github_repo": "Aider-AI/aider",
      "category": "agent",
      "website": "https://aider.chat",
      "description": "AI pair-programming tool in your terminal.",
      "stars": 43874,
      "language": "Python",
      "license": "Apache-2.0",
      "interface": "CLI",
      "pricing": "Free (open source)",
      "stars_history": [
        {"date": "2026-04-24", "stars": 43874},
        {"date": "2026-04-12", "stars": 43202}
      ],
      "alternative_metrics": null,
      "irrlicht_support": "none",
      "first_seen": "2026-04-05"
    }
  ]
}
```

Field definitions:
- `github_repo` — canonical `OWNER/REPO` from `gh api` `.full_name`. Rewrite on repo rename.
- `stars` — current GitHub stars from `gh api` `.stargazers_count`. `null` if no public repo.
- `language`, `license` — straight from `gh api` (`.language`, `.license.spdx_id`). `null` if missing.
- `description` — from `gh api` `.description`. Lightly cleaned if too long, but never replaced with third-party marketing copy. For non-GitHub agents, use the project's own homepage.
- `interface` — curated string (CLI / IDE extension / Desktop app / Web / SDK / Enterprise SaaS / CLI (tmux) / etc.).
- `pricing` — curated string (Free (open source) / Freemium / Paid / Enterprise / Paid (usage-based)).
- `stars_history` — snapshots **this skill actually measured**. Newest first. Never back-fill.
- `alternative_metrics` — for non-GitHub agents. Object with any of `funding_millions_usd`, `acquisition_price_millions_usd`, `estimated_users`, `source`, `measured_date`. Set to `null` when `github_repo` is set.
- `irrlicht_support` — `"live"` (adapter exists in `core/adapters/inbound/`), `"planned"`, or `"none"`.
- `category` — `"agent"` or `"orchestrator"`.
- `first_seen` — ISO date when this entry was first added. Never change it once set.

### Snapshot strategy

Snapshots accumulate over successive runs. No back-filling.

On each run:
1. Append today's `{date, stars}` to `stars_history` for each GitHub agent.
2. Retain all prior real snapshots (no trimming until we have > 12 entries).
3. If today already has an entry, overwrite it (same-day re-run).
4. Dates are the run date, not a month/week anchor.

Growth is only computed on demand in the HTML generator using whichever snapshots exist. Until there is a snapshot ≥30 days old, growth is shown as "Recent growth since <date> (Nd ago)" rather than "1M" / "3M".

### NEW badge

An agent is "NEW" only if both:
- `first_seen` is strictly later than the earliest `first_seen` across all tracked agents (so the initial seed batch never gets tagged), **and**
- `first_seen` is within the last 90 days.

## Deny list

`references/deny-list.txt` lists agent/orchestrator names to explicitly exclude. One name per line; `#` lines are comments. Entries with matching `name` field are skipped during discovery and removed from `agent-data.json` if present.

## Irrlicht support mapping

The canonical source of truth is `core/adapters/inbound/agents/` + `core/adapters/inbound/orchestrators/`. As of this writing:

- `live`: Claude Code (`anthropics/claude-code`), OpenAI Codex (`openai/codex`), Pi (`badlogic/pi-mono`), Gas Town (`gastownhall/gastown`).
- `planned`: Gemini CLI, Cursor, Amp, Claude Squad, OpenCode, Qwen Code, Crush, Continue, Goose, Aider, Kilo Code, Paperclip, Ruflo, Multiclaude, SWE-agent.
- Everything else: `none`.

Before each run, `ls core/adapters/inbound/agents/` and `ls core/adapters/inbound/orchestrators/` to confirm the list is still correct. If the set of live adapters has changed, update this section and apply the change to `agent-data.json`.

## Workflow

### 0. Preflight

```bash
cd /Users/ingo/projects/irrlicht
gh auth status
ls core/adapters/inbound/agents
ls core/adapters/inbound/orchestrators
```

Abort if `gh` is not authenticated — every other step depends on it.

### 1. Load existing data

Read `references/agent-data.json`. Read `references/deny-list.txt` and prune any matching entries from the data. If `agents` is empty, read `references/seed-agents.md` for the initial list.

### 2. Refresh every GitHub agent via `gh api`

For every agent with `github_repo`, run:

```bash
gh api repos/<github_repo> --jq '{repo: .full_name, stars: .stargazers_count, language: .language, license: .license.spdx_id, description: .description, archived: .archived}'
```

Batch them in a single bash loop so you don't issue 40 separate tool calls. Example:

```bash
jq -r '.agents[] | select(.github_repo != null) | .github_repo' references/agent-data.json \
| while read -r repo; do
    gh api "repos/$repo" --jq '{queried: "'"$repo"'", repo: .full_name, stars: .stargazers_count, language: .language, license: .license.spdx_id, description: .description, archived: .archived}'
  done
```

For each result:
- If `repo != queried`, GitHub has renamed the repo — update `github_repo` to `repo`.
- If `archived: true`, move the agent out of the ranked tables into a separate "archived" list (or drop it if it's been replaced by another product — note the reason in a commit message).
- Update `stars`, `language`, `license`, `description` from the API response.
- Append today's `{date, stars}` to `stars_history` (overwriting if today already exists).
- Run the plausibility check from rule #6 above.

### 3. Search for new agents (optional; only when explicitly requested or quarterly)

Use WebSearch queries such as:
- `"best AI coding agents <year>"`
- `"new coding assistant CLI terminal <year>"`
- `"agent orchestrator framework <year>"`
- `"claude code alternatives"` / `"gas town alternatives"`

Skip anything that isn't a coding-specific agent or orchestrator (IDE themes, linters, general chatbots). Skip anything on the deny list.

For each new discovery:
- Resolve the canonical repo with `gh api` (see step 2) before writing anything.
- Set `first_seen` to today.
- Fill `interface` and `pricing` manually based on the project's own homepage.

### 4. Refresh non-GitHub agents (alternative_metrics)

For each agent with `github_repo: null`, re-check the following and update `measured_date` to today:

- Funding: `"<name>" funding round <year>` — look for the most recent round on TechCrunch / Bloomberg / Crunchbase.
- Acquisition: `"<name>" acquisition <year>` — if acquired, set `acquisition_price_millions_usd` and note in the description.
- User count: `"<name>" monthly active users` or `"<name>" VS Code marketplace installs`.

Only write numbers you have a source for. Set `source` to a short citation (outlet + date). If no data found, set `source` to a honest `"no public data found"` string.

Common pitfalls:
- Devin ≠ Cognition's total funding. Record under Devin since Devin is Cognition's product, but note "Cognition AI parent" in the source.
- Amp ≠ Sourcegraph Cody. Cody was rebranded to Amp; Amp is spinning out as a standalone company. Do not list both.
- Windsurf was acquired by Cognition (Dec 2025). Its funding field should be null; its `acquisition_price_millions_usd` should be set.

### 5. Save `agent-data.json`

Write the updated data. Sort agents by category (agent first), then by stars descending, then by name ascending. Set `last_updated` to today.

### 6. Generate HTML

Run the HTML generator:

```bash
python3 .claude/skills/ir:agent-landscape/assets/generate.py   # if checked in
# else: use the generator documented below
```

If the generator script is not checked in, the latest reference implementation lives in the commit that last touched `site/landscape/`. Use it as-is rather than re-writing it from scratch.

The generator must:
- Rank GitHub agents by `log10(stars + 1)` when no ≥30-day snapshot exists (no fake trend bonus).
- Add a trend component only when at least one snapshot is ≥30 days old.
- Show "Recent growth since `<prior snapshot date>` (Nd ago)" in the growth column header when no 30-day snapshot exists; switch to "1M / 3M growth" only when real snapshots at those horizons exist.
- Tag NEW per the rule above (later than earliest `first_seen` and within 90 days).
- Place non-GitHub agents in an unranked "No public repo" group below the ranked table.
- Include a footnote: "* License and pricing are sourced from the GitHub API and public web pages. Verify on the project's own website before relying on them."

### 7. Update landing-page nav

Ensure `site/index.html` has the Landscape nav link (nav bar + footer). Idempotent; skip if present.

### 8. Summary

Print:
- Count of tracked agents, how many github vs non-github.
- Any repos where `gh api` returned a renamed `full_name` (report the rename so the user can commit it).
- Any agents marked `archived: true` (report even if you've moved them out of the ranked list).
- Any plausibility-check failures (stars delta >30% in <30 days for a 5k+ repo) — do NOT silently write them; ask the user whether to trust the number.
- Top 5 by current stars per category.
- Diff summary: `git diff --stat site/landscape/` and `git diff --stat .claude/skills/ir:agent-landscape/references/agent-data.json`.
