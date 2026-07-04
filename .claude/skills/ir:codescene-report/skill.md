---
name: "ir:codescene-report"
description: "Trigger an on-demand CodeScene code-health report and summarize it — overall score, trend, and the worst refactoring-target hotspots. Wraps the manual codescene-report.yml workflow so you don't have to drive the GitHub Actions UI by hand. Use when the user says '/ir:codescene-report', 'run a codescene report', 'check code health', 'codescene improvement run', or wants an up-to-date look at code health outside the automatic per-PR check."
---

# CodeScene Report (on demand)

Irrlicht's per-PR "CodeScene Code Health Review" check (via the CodeScene
GitHub App, project 82148) is advisory-only — see AGENTS.md's Testing
section. It is *not* the mechanism for periodic improvement work. This
skill is: fetch the current report, on demand, and hand back a short,
readable summary — no PR, no waiting on Actions UI.

The README's CodeScene badge already shows the live overall score
(refreshed on every push to `main` by `codescene-badge.yml`) — this skill
is for going deeper: trend plus the actual hotspot files.

## Workflow

### 1. Trigger the overview

```bash
gh workflow run codescene-report.yml -R ingo-eichhorst/Irrlicht
```

The workflow defaults to the `analyses/latest` endpoint (project-wide
summary + current/month/year code health score).

### 2. Find the run

`workflow_dispatch` doesn't return a run ID, so poll for it:

```bash
sleep 4
gh run list -R ingo-eichhorst/Irrlicht --workflow=codescene-report.yml \
  --limit 1 --json databaseId,status,createdAt
```

If `status` isn't yet `completed`, wait and check again (a couple of
retries is normal — the job itself takes well under a minute).

### 3. Wait for completion and pull the JSON

```bash
gh run watch <databaseId> -R ingo-eichhorst/Irrlicht --exit-status
gh run view <databaseId> -R ingo-eichhorst/Irrlicht --log | grep "Fetch CodeScene report"
```

Strip the `report\tFetch CodeScene report\t<timestamp>` prefix (and the
ANSI-colored command-echo line) from each line to recover the raw JSON
body. From the `analyses/latest` shape, pull out:

- `id` — the analysis id, needed for step 4
- `high_level_metrics.current_score` / `month_score` / `year_score`
- `summary` — commits, files, authors_count, issues_classed_as_defects

### 4. Trigger the hotspot list

Using the `id` from step 3:

```bash
gh workflow run codescene-report.yml -R ingo-eichhorst/Irrlicht \
  -f endpoint="analyses/<id>/technical-debt?refactoring_targets=true"
```

Repeat steps 2–3 to fetch this run's output. It returns a `result` array
of files with `refactoring_target`, `code_health_score`, `friction`,
`friction_last_month`, `loc`, and `revisions`.

### 5. Summarize

Report back concisely:

```
CodeScene report — <repo> @ <analysis timestamp>
Code health: <current_score>/10  (month: <month_score>, year: <year_score>)
<commits> commits · <files> files · <issues_classed_as_defects> defects flagged

Top refactoring targets (lowest health first):
  <score>/10  <file>            friction <friction> (last month <friction_last_month>), <loc> loc, <revisions> revisions
  ...
```

Sort hotspots by `code_health_score` ascending and show at most 8 — this
is meant to be a quick read, not the full dump. If the user wants deeper
detail on one file, re-run step 4 pointed at a narrower endpoint (see
CodeScene's `/v2/projects/<id>/` API) or hand them the
`https://codescene.io/projects/82148/...` link from the check run instead
of reproducing it here.

## Constraints

- **Don't file issues or open PRs from this skill.** This is a read-only,
  on-demand look — the automatic per-PR check and the README badge already
  give continuous coverage, and all three are informational, not action
  items to auto-execute. If the user wants a hotspot turned into follow-up
  work, that's a separate, explicit ask.
- **Don't add scheduling.** "From time to time" means the maintainer
  invokes this manually when curious — no cron, no CronCreate. If that
  changes, it's a distinct decision to make explicitly.
- The CODESCENE_API_TOKEN this relies on lives only as a GitHub Actions
  secret — there is no local/offline path. Every invocation goes through
  the `codescene-report.yml` workflow.
