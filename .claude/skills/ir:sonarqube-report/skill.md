---
name: "ir:sonarqube-report"
description: "On-demand SonarQube Cloud (SonarCloud) code-quality report — fetches open issues for this repo's connected project, sorted worst-first, with concrete file:line locations, rule keys, and fix guidance. Runs entirely locally via tools/sonarqube-report.sh (SONAR_TOKEN lives in a local .env, not a CI secret). Use when the user says '/ir:sonarqube-report', 'run a sonarqube report', 'sonar report', 'what needs fixing', 'show me the sonarqube issues', or wants concrete, actionable code-quality findings (as opposed to /ir:codescene-report's hotspot/trend view, which doesn't exist anymore)."
---

# SonarQube Report (on demand)

SonarQube Cloud's issue search gives concrete, actionable findings — rule
key, file, line, message, and estimated fix effort — which is exactly
what CodeScene's hotspot API doesn't expose (see the removed
`ir:codescene-report` skill's history for why that mattered). This skill
fetches those issues on demand and hands back a short, readable summary.

Unlike CodeScene, there's no GitHub Actions round-trip: `SONAR_TOKEN` lives
in this repo's local `.env` (gitignored, never a CI secret), so
`tools/sonarqube-report.sh` runs directly.

## Prerequisite

`.env` at the repo root must have `SONAR_TOKEN`, `SONAR_ORGANIZATION`, and
`SONAR_PROJECT_KEY` set (see `.env.example`). If any are missing, the
script fails fast with a clear message naming which one — report that
directly rather than guessing values.

Also: SonarQube Cloud has no equivalent of CodeScene's `run-analysis`
trigger. Fresh data depends on a scanner (`sonar-scanner` / a CI action)
having already run against this project — this skill only *reads*
whatever the last scan uploaded. If no CI scan is wired up yet, say so
rather than treating an empty/stale result as "no issues."

## Workflow

### 1. Fetch

```bash
tools/sonarqube-report.sh
```

This defaults to `issues/search` scoped to the project, `resolved=false`
(only currently-open issues), sorted by severity descending (worst first),
page size 100.

### 2. Read the result

From each entry in the `issues` array, pull out:

- `component` — strip the `<SONAR_PROJECT_KEY>:` prefix to get the
  repo-relative file path
- `line` (or `textRange.startLine`–`textRange.endLine` if `line` is absent
  — some rule types, e.g. file-level ones, have no single line)
- `rule` — the rule key (e.g. `go:S1192`)
- `message` — usually already actionable on its own (e.g. "Reduce the
  number of returns of this function from 6 to at most 3")
- `effort` — estimated remediation time (e.g. `"2h1min"`)
- `impacts` — array of `{softwareQuality, severity}` (the current Clean
  Code taxonomy: MAINTAINABILITY / RELIABILITY / SECURITY ×
  INFO/LOW/MEDIUM/HIGH/BLOCKER); prefer this over the older top-level
  `severity`/`type` fields when summarizing

`paging.total` is the full open-issue count — the fetched page (100) may
not be all of them; say so if `paging.total > 100`.

### 3. Deeper detail on one rule (optional)

If the user wants the full remediation writeup for a specific rule, not
just its one-line message:

```bash
tools/sonarqube-report.sh "rules/show?key=<rule>&organization=<org>"
```

### 4. Summarize

Report back concisely:

```
SonarQube report — <repo>
<paging.total> open issues (showing worst <N>)

  <severity>  <file>:<line>  [<rule>]
    <message>  (effort: <effort>)
  ...
```

Sort worst-first (already the case if the default fetch was used) and
show at most 10 — this is meant to be a quick read, not the full dump. If
the user wants to dig into one file or rule, re-run step 1 with narrower
`componentKeys`/`rules` query params, or step 3 for the rule's full
writeup.

## Constraints

- **Don't file issues or open PRs from this skill.** This is an on-demand
  look. If the user wants a finding turned into follow-up work, that's a
  separate, explicit ask.
- **Read-only.** Never call an endpoint that would mutate issue state
  (assign, resolve, add comment) — this skill only reads.
- **No local/offline fallback if `.env` is missing.** Don't invent or
  guess `SONAR_ORGANIZATION`/`SONAR_PROJECT_KEY` values — surface the
  script's `env var required` error directly.
