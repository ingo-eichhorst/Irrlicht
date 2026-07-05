---
name: "ir:codescene-report"
description: "Trigger a fresh, on-demand CodeScene analysis of main, wait for it to complete, and summarize the result — overall score, trend, and the worst refactoring-target hotspots. Wraps the manual codescene-report.yml workflow so you don't have to drive the GitHub Actions UI by hand. Use when the user says '/ir:codescene-report', 'run a codescene report', 'check code health', 'codescene improvement run', or wants an up-to-date look at code health outside the automatic per-PR check."
---

# CodeScene Report (on demand)

Irrlicht's per-PR "CodeScene Code Health Review" check (via the CodeScene
GitHub App, project 82148) is advisory-only — see AGENTS.md's Testing
section. It doesn't run per-push either: CodeScene analyzes `main` on its
own schedule, so the score right after a merge can be a stale, pre-merge
number. This skill triggers a fresh full analysis of `main`, waits for it
to finish, and hands back a short, readable summary — no PR, no waiting on
the Actions UI.

The README's CodeScene badge already shows the live overall score
(refreshed on every push to `main` by `codescene-badge.yml`) — this skill
is for going deeper: a *current* trend plus the actual hotspot files.

## Workflow

### 1. Trigger a fresh analysis

```bash
gh workflow run codescene-report.yml -R ingo-eichhorst/Irrlicht -f action=trigger
```

This POSTs `run-analysis` for the project, then polls `analyses/latest`
inside the job until a new completed analysis appears (or a 15-minute
timeout elapses). CodeScene's docs say to turn off analysis scheduling if
this endpoint is used as a *continuous* trigger — this is a one-off manual
kick, never wire it into a cron/schedule.

If CodeScene is running slow that day (or you want a shorter timeout while
iterating), override the defaults: `-f poll_timeout_secs=1800` and/or
`-f poll_interval_secs=15`.

### 2. Find the run

`workflow_dispatch` doesn't return a run ID, so poll for it:

```bash
sleep 4
gh run list -R ingo-eichhorst/Irrlicht --workflow=codescene-report.yml \
  --limit 1 --json databaseId,status,createdAt
```

If `status` isn't yet `completed`, wait and check again (a couple of
retries is normal).

### 3. Wait for completion and pull the JSON

```bash
gh run watch <databaseId> -R ingo-eichhorst/Irrlicht --exit-status
gh run view <databaseId> -R ingo-eichhorst/Irrlicht --log | grep "Run CodeScene script"
```

Because the fresh analysis runs inside this job, `gh run watch` can block
for several minutes — that's expected, not a hang. Strip the
`report\tRun CodeScene script\t<timestamp>` prefix (and the ANSI-colored
command-echo line) from each line. For `action=trigger`, also drop any
line starting with `codescene-trigger:` — those are the script's own
stderr progress messages ("triggered a fresh analysis...", "completed
after ~180s...") and land in the same combined log as the JSON, but aren't
part of it. What's left is the raw JSON body.

Three outcomes:

- **Success** — the run exits 0 with the fresh analysis JSON.
- **Timeout** — the run still exits 0, but the JSON carries
  `"_irrlicht_stale_fallback": true`: the trigger didn't finish within the
  timeout, so this is the last completed analysis, not a new one. Say so
  plainly in the summary (step 5) rather than presenting it as current.
- **Analysis failure** — the run exits non-zero and the log says a new
  analysis "reported a failure status". Report this to the user directly
  as CodeScene itself failing the run, not a timing or token problem.
- **Token scope failure** — the run exits non-zero and the log says
  `run-analysis returned 403 Forbidden`. Report this to the user directly:
  `CODESCENE_API_TOKEN` needs to be re-scoped (from the CodeScene dashboard)
  to permit triggering analyses, not just reading them. Don't retry — a
  403 won't clear on its own.

From the `analyses/latest` shape, pull out:

- `id` — the analysis id, needed for step 4
- `high_level_metrics.current_score` / `month_score` / `year_score`
- `summary` — commits, files, authors_count, issues_classed_as_defects

### 4. Trigger the hotspot list

Using the `id` from step 3 (this is a plain fetch, not another trigger —
`action` defaults to `report`):

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
is meant to be a quick read, not the full dump. If `_irrlicht_stale_fallback`
was set in step 3, lead with a note that the fresh analysis didn't finish
in time and this is the last completed run instead. If the user wants
deeper detail on one file, re-run step 4 pointed at a narrower endpoint
(see CodeScene's `/v2/projects/<id>/` API) or hand them the
`https://codescene.io/projects/82148/...` link from the check run instead
of reproducing it here.

## Constraints

- **Don't file issues or open PRs from this skill.** This is an on-demand
  look — the automatic per-PR check and the README badge already give
  continuous coverage, and all three are informational, not action items
  to auto-execute. If the user wants a hotspot turned into follow-up work,
  that's a separate, explicit ask.
- **Don't add scheduling.** "From time to time" means the maintainer
  invokes this manually when curious — no cron, no CronCreate. CodeScene
  itself warns against using `run-analysis` as a continuous trigger; this
  skill's `action=trigger` step is a one-off, human-initiated kick only.
  If recurring triggers become desirable, that's a distinct decision to
  make explicitly.
- **Scoped to `main`, no branch/delta targeting.** `run-analysis` takes no
  branch parameter — it always analyzes the project's configured branch.
  Don't try to point this at a PR branch or a commit range.
- **Don't run overlapping triggers.** The script identifies "the fresh
  analysis" by `analyses/latest`'s id changing from a pre-trigger baseline
  — it has no way to confirm a newly-appeared analysis was actually caused
  by this invocation. Running a second `action=trigger` while one is still
  polling (or right as CodeScene's own schedule kicks off a run) risks
  attributing the wrong analysis to the wrong caller.
- The CODESCENE_API_TOKEN this relies on lives only as a GitHub Actions
  secret — there is no local/offline path. Every invocation goes through
  the `codescene-report.yml` workflow.
