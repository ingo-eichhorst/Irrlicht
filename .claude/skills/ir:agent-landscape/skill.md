---
name: "ir:agent-landscape"
description: "Scan the web for coding agents and agent orchestrators, track GitHub stars and trends, rank by popularity+momentum, and publish a report to the irrlicht site. Shows which agents irrlicht already supports. Use when user says 'agent landscape', 'scan agents', 'coding agent tracker', 'agent popularity', '/ir:agent-landscape', or wants to see the competitive landscape of coding agents."
---

# Agent Landscape Tracker

Discover, track, and rank coding agents and agent orchestrators. Publish a styled HTML report to the irrlicht site at `site/landscape/index.html` and an in-depth comparison at `site/landscape/compare/index.html`.

## Data File

All state persists in `references/agent-data.json` (relative to this skill directory). Schema:

```json
{
  "last_updated": "2026-04-12",
  "agents": [
    {
      "name": "Aider",
      "github_repo": "paul-gauthier/aider",
      "category": "agent",
      "website": "https://aider.chat",
      "description": "AI pair programming in your terminal",
      "stars": 30000,
      "stars_history": [
        {"date": "2026-04-12", "stars": 30000},
        {"date": "2026-04-05", "stars": 29800},
        {"date": "2026-03-12", "stars": 28500},
        {"date": "2026-01-12", "stars": 27000}
      ],
      "alternative_metrics": null,
      "irrlicht_support": "none",
      "first_seen": "2026-01-05"
    }
  ]
}
```

Field definitions:
- `stars` — current GitHub stars (null if no public repo)
- `stars_history` — array of `{date, stars}` snapshots, newest first. Always maintain 4 anchor points: **today**, **~1 week ago**, **~1 month ago**, **~3 months ago**. When adding today's snapshot, keep the existing entry closest to each anchor and trim anything else. Keep up to 6 entries total.
- `alternative_metrics` — for agents without a public GitHub repo. Object with optional fields:
  ```json
  {
    "vscode_installs": 5200000,
    "npm_weekly_downloads": 120000,
    "funding_millions_usd": 150,
    "estimated_users": "1M+",
    "source": "VS Code Marketplace, Crunchbase",
    "measured_date": "2026-04-12"
  }
  ```
  Set to `null` for agents that have a GitHub repo.
- `irrlicht_support` — one of: `"live"`, `"planned"`, `"none"`
- `category` — one of: `"agent"`, `"orchestrator"`
- `first_seen` — ISO date when the agent was first added to tracking

### Snapshot Strategy

Snapshots are measured relative to the **current date** (the day the skill runs), NOT normalized to month boundaries.

Anchor points (always relative to today):
- **today** — fresh star count from GitHub API
- **~1 week ago** — closest existing snapshot to (today - 7 days)
- **~1 month ago** — closest existing snapshot to (today - 30 days)
- **~3 months ago** — closest existing snapshot to (today - 90 days)

When updating:
1. Add today's snapshot (or update if today already exists)
2. From remaining history, keep the single entry closest to each anchor
3. Discard duplicates and anything else beyond 6 total entries
4. Sort newest first

### "NEW" Badge

An agent is marked **NEW** if its `first_seen` date is less than 3 months before today (i.e., `today - first_seen < 90 days`). Display a "new" superscript badge next to its name in both the main table and comparison page.

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
3. Add a snapshot for **today** in `stars_history`
4. Trim history per the snapshot strategy above (keep closest to each anchor, max 6 entries)

If the GitHub API rate-limits (403), use WebSearch `"{agent name}" github stars` as fallback and parse the count from search results.

### 4. Fetch Alternative Metrics for Non-GitHub Agents

For agents where `github_repo` is null, gather alternative popularity signals:

1. **VS Code Marketplace installs** — if the agent has a VS Code extension, use WebSearch `"{agent name}" VS Code marketplace installs` and extract the install count
2. **npm weekly downloads** — if there's an npm package, use WebFetch `https://api.npmjs.org/downloads/point/last-week/{package}` 
3. **Funding** — use WebSearch `"{agent name}" funding raised` to find total funding in millions USD
4. **Estimated users** — use WebSearch `"{agent name}" users monthly active` for any public user counts

Store results in `alternative_metrics`. Set `source` to describe where the data came from. Set `measured_date` to today. Only populate fields you can actually find — leave others out of the object.

If you cannot find any alternative metrics, set `alternative_metrics` to an empty object `{}` with just `measured_date` and `source: "no public data found"`.

### 5. Compute Rankings

For each agent, compute a `score` using star data from `stars_history`:

```
# Anchor dates (relative to today)
today       = current date
date_1m     = today - 30 days
date_3m     = today - 90 days

# Pick the snapshots closest to each anchor (strict tolerance)
current  = stars_history entry closest to today
snap_1m  = stars_history entry closest to date_1m (null if none within 10 days)
snap_3m  = stars_history entry closest to date_3m (null if none within 21 days)

# Growth calculations
growth_1m_abs = current.stars - snap_1m.stars      (null if snap_1m missing)
growth_1m_pct = growth_1m_abs / snap_1m.stars * 100 (null if snap_1m missing or snap_1m.stars == 0)

growth_3m_abs = current.stars - snap_3m.stars      (null if snap_3m missing)
growth_3m_pct = growth_3m_abs / snap_3m.stars * 100 (null if snap_3m missing or snap_3m.stars == 0)

# Trend = average monthly gain over 3 months
days     = days between snap_3m.date and current.date
trend    = growth_3m_abs / days * 30               (0 if < 2 snapshots)

normalized_stars = log10(stars + 1)                 # dampen mega-repos
normalized_trend = log10(trend + 1)                 # monthly gain scale

# Score depends on whether we have enough history for a trend
if snap_3m is not null:
    score = 0.6 * normalized_stars + 0.4 * normalized_trend
else:
    score = 0.85 * normalized_stars              # no trend penalty for new entries
```

**Non-GitHub agents** (where `stars` is null) are **not ranked** alongside GitHub agents. They appear in a separate "No Public Repo" group below the ranked table, sorted by their best available alternative metric. Do not assign them a numeric rank — show `—` for rank.

### 6. Save Data

Write updated `references/agent-data.json` with the new snapshots and alternative metrics.

### 7. Generate HTML Report (Main Table)

Generate `site/landscape/index.html` using the template structure in `assets/page-template.html`.

The page must:
- Match the irrlicht site design (dark theme, same CSS variables, fonts, nav)
- Show two tables: "Coding Agents" and "Agent Orchestrators"
- Each row: rank, name (linked to website/repo), stars (formatted with commas), **3M growth**, irrlicht support badge, description
- **3M growth column**: Show absolute change and percentage, e.g. `+5,200 (12.3%)`. Use green for positive, red for negative, dim for zero/flat. Show `—` if no 3M data. (1M growth is only shown on the comparison page.)
- **NEW badge**: Show a styled "NEW" superscript next to the name for agents first seen < 90 days ago
- **Trend note**: Add a small note below the table header stating the actual baseline dates, e.g.: "1M growth measured vs YYYY-MM-DD snapshot; 3M growth measured vs YYYY-MM-DD snapshot." Use the real dates of the snapshots used, not approximate labels.
- For agents without GitHub stars but with `alternative_metrics`: show the most relevant metric instead of stars (e.g., "5.2M installs" or "$150M raised") with a footnote explaining the source
- Badges: green "live" / orange "planned" / grey "not tracked" matching the site's tag styles
- Show `last_updated` date in the header
- Include a "What is this?" intro explaining the page
- Link to the comparison page: "See the [detailed comparison →](compare/)" 
- Be fully self-contained (inline CSS, no external dependencies beyond Google Fonts)

### 8. Generate Comparison Page

Generate `site/landscape/compare/index.html` using the template in `assets/compare-template.html`.

This is an in-depth comparison page with a single unified table covering **all** agents and orchestrators. The table includes these columns:

| Column | Description |
|--------|-------------|
| Name | Agent name + link + NEW badge if applicable |
| Category | "Agent" or "Orchestrator" badge |
| Stars | Current GitHub stars (or alternative metric) |
| 1M Growth | Stars gained in the last month (absolute + %) |
| 3M Growth | Stars gained in the last 3 months (absolute + %) |
| Open Source | Yes/No |
| License | MIT, Apache-2.0, proprietary, etc. |
| Primary Language | Go, TypeScript, Python, Rust, etc. |
| Interface | CLI, IDE extension, Web, Desktop app |
| Pricing | Free, freemium, paid, etc. |
| Irrlicht | Support badge |

Data sourcing for comparison columns:
- **License** and **Primary Language**: fetch from GitHub API (`license.spdx_id`, `language` fields) for repos with `github_repo`. Use WebSearch for non-GitHub agents.
- **Interface** and **Pricing**: derive from existing `description` field + WebSearch if not obvious.
- **Open Source**: `true` if `github_repo` is not null and license is not proprietary.
The comparison page must:
- Use the same dark theme, nav, and fonts as the main page
- Be sortable by clicking column headers (use inline JavaScript, no external deps)
- Include a note at the top: "Growth figures measured relative to today ({{LAST_UPDATED}}). 1M baseline: YYYY-MM-DD. 3M baseline: YYYY-MM-DD." Use the real snapshot dates.
- Mark **License** and **Pricing** column headers with an asterisk (*). Add a footnote at the bottom of the table: "* License and pricing data sourced via web search and may not reflect the latest changes. Verify on the project's website."
- Show a "← Back to Landscape" link to the main table
- Be fully self-contained

### 9. Update Landing Page Navigation

Add a nav link to the landscape page in `site/index.html` if not already present. Add it to both:
- The nav bar: `<li class="nav-hide-sm"><a href="landscape/">Landscape</a></li>` (before Docs)
- The footer: `<a href="landscape/">Agent Landscape</a>` (before Docs)

### 10. Summary

Print a concise summary:
- Total agents tracked, how many new this run
- Top 5 by score (name + stars + 1M growth + 3M growth)
- Any agents where irrlicht support status may be outdated
- Count of agents with vs. without GitHub repos
- Note: "Comparison page generated at site/landscape/compare/index.html"
