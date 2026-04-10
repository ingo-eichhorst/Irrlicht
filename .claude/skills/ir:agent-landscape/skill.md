---
name: "ir:agent-landscape"
description: "Scan the web for coding agents and agent orchestrators, track GitHub stars and trends, rank by popularity+momentum, and publish a report to the irrlicht site. Shows which agents irrlicht already supports. Use when user says 'agent landscape', 'scan agents', 'coding agent tracker', 'agent popularity', '/ir:agent-landscape', or wants to see the competitive landscape of coding agents."
---

# Agent Landscape Tracker

Discover, track, and rank coding agents and agent orchestrators. Publish a styled HTML report to the irrlicht site at `site/landscape/index.html`.

## Data File

All state persists in `references/agent-data.json` (relative to this skill directory). Schema:

```json
{
  "last_updated": "2026-04-05",
  "agents": [
    {
      "name": "Aider",
      "github_repo": "paul-gauthier/aider",
      "category": "agent",
      "website": "https://aider.chat",
      "description": "AI pair programming in your terminal",
      "stars": 30000,
      "stars_history": [
        {"date": "2026-04-01", "stars": 30000},
        {"date": "2026-03-01", "stars": 28500},
        {"date": "2026-02-01", "stars": 27000}
      ],
      "irrlicht_support": "none",
      "first_seen": "2026-04-05"
    }
  ]
}
```

Field definitions:
- `stars` — current GitHub stars (null if no public repo)
- `stars_history` — array of `{date, stars}` snapshots, newest first. Always retain entries for the 1st of the current month and the 1st of 3 months ago; other entries may be trimmed. Keep up to 6 entries total.
- `irrlicht_support` — one of: `"live"`, `"planned"`, `"none"`
- `category` — one of: `"agent"`, `"orchestrator"`

## Deny List

`references/deny-list.txt` lists agent/orchestrator names to explicitly exclude (one per line). Skip any agent whose name matches a line in this file during discovery and HTML generation. Remove matching entries from `agent-data.json` if present.

## Irrlicht Support Status

These agents have irrlicht adapters (mark as `"live"`):
- Claude Code (`anthropics/claude-code`)
- OpenAI Codex CLI (`openai/codex`, display as "OpenAI Codex")
- Pi (`badlogic/pi-mono`)
- Gas Town

These are planned (mark as `"planned"`):
- Gemini CLI
- Cursor
- Amp
- Claude Squad
- OpenCode (`anomalyco/opencode`)

Everything else: `"none"`.

## Workflow

### 1. Load Existing Data

Read `references/agent-data.json`. If `agents` is empty, read `references/seed-agents.md` for the initial list of known agents.

Read `references/deny-list.txt`. Remove any agents whose name matches a deny-list entry from the loaded data. Skip denied names during discovery (step 2).

### 2. Search for New Agents

Use WebSearch to find coding agents and orchestrators not already tracked:
- `"best AI coding agents 2026"`
- `"new coding assistant CLI terminal 2026"`
- `"AI agent orchestrator framework 2026"`
- `"agentic coding tools"`
- `"claude code coding agent alternatives"`
- `"gas town coding agent orchestrator alternatives"`

Add any genuinely new coding agents or orchestrators found. Skip IDE themes, linters, or general-purpose AI chatbots — only track tools that write/edit code autonomously or orchestrate agents that do.

### 3. Fetch GitHub Stars

For each agent with a `github_repo`:

1. Use WebFetch to get `https://api.github.com/repos/{owner}/{repo}` (JSON response includes `stargazers_count`)
2. Set `stars` to current `stargazers_count`
3. Upsert a snapshot for the **1st of the current month** in `stars_history` (add if missing, update if present)
4. Ensure a snapshot for the **1st of 3 months ago** is retained — never trim it
5. Trim any other entries beyond 6 total, keeping newest first

For agents without a public GitHub repo, set `stars: null`.

If the GitHub API rate-limits (403), use WebSearch `"{agent name}" github stars` as fallback and parse the count from search results.

**Migration:** If an existing entry has the old `stars_prev`/`stars_date`/`stars_prev_date` fields instead of `stars_history`, convert: build `stars_history` from current + previous snapshot, then delete the old fields.

### 4. Compute Rankings

For each agent, compute a `score` using the **3-month star increase** from `stars_history`:

```
# Anchor dates (always use these exact points)
month_start     = first day of current month (e.g. 2026-04-01)
month_start_3m  = first day of 3 months ago  (e.g. 2026-01-01)

# Pick the snapshots closest to each anchor
current  = stars_history entry closest to month_start
baseline = stars_history entry closest to month_start_3m

stars_3m = current.stars - baseline.stars                # absolute 3-month gain
days     = days between baseline.date and current.date
trend    = stars_3m / days * 30                          # normalized to stars/month, 0 if < 2 snapshots

normalized_stars = log10(stars + 1)                      # dampen mega-repos
normalized_trend = log10(trend + 1)                      # monthly gain scale
score = 0.6 * normalized_stars + 0.4 * normalized_trend
```

Agents with `stars: null` get `score = 0` and sort to the bottom. Agents with only 1 snapshot get `trend = 0`.

Sort agents descending by score within each category.

### 5. Save Data

Write updated `references/agent-data.json` with the new snapshot.

### 6. Generate HTML Report

Generate `site/landscape/index.html` using the template structure in `assets/page-template.html`.

The page must:
- Match the irrlicht site design (dark theme, same CSS variables, fonts, nav)
- Show two tables: "Coding Agents" and "Agent Orchestrators"
- Each row: rank, name (linked to website/repo), stars (formatted with commas), trend arrow + monthly delta, irrlicht support badge, description
- Badges: green "live" / orange "planned" / grey "not tracked" matching the site's tag styles
- Show `last_updated` date in the header
- Include a "What is this?" intro explaining the page
- Be fully self-contained (inline CSS, no external dependencies beyond Google Fonts)

### 7. Update Landing Page Navigation

Add a nav link to the landscape page in `site/index.html` if not already present. Add it to both:
- The nav bar: `<li class="nav-hide-sm"><a href="landscape/">Landscape</a></li>` (before Docs)
- The footer: `<a href="landscape/">Agent Landscape</a>` (before Docs)

### 8. Summary

Print a concise summary:
- Total agents tracked, how many new this run
- Top 5 by score (name + stars + trend)
- Any agents where irrlicht support status may be outdated
