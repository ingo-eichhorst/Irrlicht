---
name: ir:onboard-agent/survey
description: >
  Per-agent scenario applicability survey. Reads each agent's docs, changelog,
  and source to propose a candidate `coverage[<agent>]` column for every
  scenario in `.specs/agent-scenarios.md`. Maintainer reviews + commits.
  Re-run on each agent version bump. Invoked as `/ir:onboard-agent survey
  <agent>`.
---

# Mode C: applicability survey

Produces a candidate per-scenario verdict (`yes` / `no` / `partial` / `unknown`)
for one agent, with citations, ready for the maintainer to merge into
`.specs/agent-scenarios-coverage.json`. The survey itself does **not** modify
the coverage matrix.

> **Stage 1 of the cell lifecycle.** This is the assessment phase — see
> [`../cell-lifecycle.md`](../cell-lifecycle.md) for how the verdict
> produced here feeds Stage 2 (recipe authoring via `recipe/`) and
> beyond.

## Invocation

```
/ir:onboard-agent survey <agent>
```

E.g. `/ir:onboard-agent survey claudecode`, `/ir:onboard-agent survey aider`.

The agent slug must match the adapter directory under
`core/adapters/inbound/agents/<name>/` if the agent is already onboarded;
otherwise it's a free-form slug for a brand-new agent (will be added to
`agents[]` in the coverage matrix later by the maintainer).

## Steps

### Step 1 — Gather inputs

```bash
scripts_dir="$( cd "$(dirname "$(realpath "${BASH_SOURCE:-$0}")")"/.. && pwd )"
"$scripts_dir/survey/run-survey.sh" prepare "<agent>"
```

The script prints to stdout: the agent slug, paths to the catalog (markdown
prose + JSON ID list), the canonical list of scenario IDs, and the full
survey prompt (`survey/prompts/applicability-survey.md`).

If the script errors with "agent-scenarios-coverage.json not found", the
maintainer's `.specs/` is missing. `.specs/` is gitignored — copy it from the
maintainer's main checkout (`/Users/ingo/projects/irrlicht/.specs/`) into
this working tree, or regenerate it from `.specs/agent-scenarios.md` if a
generator exists. Don't fabricate the catalog.

### Step 2 — Dispatch a research subagent

Spawn ONE general-purpose subagent (use the `Agent` tool with
`subagent_type: general-purpose`) and hand it the entire output of Step 1 as
the prompt. The subagent must:

1. Read the agent's official docs site, changelog, release notes, and (if
   open source) the relevant source files.
2. Decide a verdict for every scenario in the canonical ID list. Missing or
   extra IDs are a hard failure.
3. Cite at least one source per verdict (URL or file path with optional
   excerpt). Even `unknown` verdicts must cite what was searched.
4. Calibrate `confidence` honestly — `0.9+` only when the docs/code state
   the behavior explicitly.
5. Set `prerequisites_hint` for any scenario whose recording requires
   maintainer-only setup (paid account, signing cert, API key).
6. Print the final JSON document and stop.

The subagent's full instructions are in
`survey/prompts/applicability-survey.md` (Step 1 already inlined the prompt
for the dispatch).

### Step 3 — Capture the candidate

Write the subagent's JSON output to a candidate path under `.build/survey/`:

```bash
ts="$(date -u +%Y%m%dT%H%M%SZ)"
cand=".build/survey/<agent>-${ts}.json"
mkdir -p .build/survey
# (write the subagent's JSON output to $cand)
```

The candidate path is local-only; never committed.

### Step 4 — Validate

```bash
"$scripts_dir/survey/run-survey.sh" validate "$cand"
```

Validates against `survey/schema/survey-result.schema.json` AND cross-references
every key in `scenarios` against the canonical scenario ID list. Common
failures:

- **scenario id not in catalog: `<id>`** — the subagent invented an ID. Tell it
  to use only IDs from the canonical list in Step 1.
- **scenario id missing from survey: `<id>`** — the subagent skipped a scenario.
  Ask it to add a verdict (often `"unknown"` with a brief note if the agent's
  docs don't speak to it).
- **`<sid>`: sources must be a non-empty array** — verdict lacks citations.
  Reject and re-prompt.

On validation failure, return to Step 2 with the specific errors as feedback;
do NOT hand-edit the candidate.

### Step 5 — Commit to `.specs/`

```bash
"$scripts_dir/survey/run-survey.sh" commit "<agent>" "$cand"
```

Writes `.specs/agent-survey-<agent>.json` (backs up any prior version to
`.json.bak`). This file is gitignored — it stays in the maintainer's working
tree until they transcribe verdicts into `.specs/agent-scenarios-coverage.json`.

### Step 6 — Surface low-confidence cells for review

After commit, list the cells the maintainer should hand-check first:

```bash
jq -r '.scenarios | to_entries[]
       | select(.value.confidence < 0.7)
       | "\(.key)  [\(.value.agent_supports), conf=\(.value.confidence)]  \(.value.notes // "")"' \
  .specs/agent-survey-<agent>.json
```

For each, show the cited sources so the maintainer can spot-check without
re-fetching:

```bash
jq -r '.scenarios | to_entries[]
       | select(.value.confidence < 0.7)
       | "\(.key):\n" + (.value.sources | map("  - \(.kind): \(.ref)") | join("\n"))' \
  .specs/agent-survey-<agent>.json
```

### Step 7 — Diff vs the committed coverage matrix

If `.specs/agent-scenarios-coverage.json` already has a column for this
agent, show only cells whose proposed `agent_supports` differs from the
committed value:

```bash
agent=<agent>
jq -r --slurpfile cov .specs/agent-scenarios-coverage.json --arg a "$agent" '
  .scenarios | to_entries[] as $e
  | $cov[0].scenarios | map(select(.id == $e.key))[0] // null as $c
  | select($c != null and $c.coverage[$a].agent_supports != $e.value.agent_supports)
  | "\($e.key): committed=\($c.coverage[$a].agent_supports), proposed=\($e.value.agent_supports) (conf=\($e.value.confidence))"' \
  .specs/agent-survey-<agent>.json
```

Empty output = the proposal matches the committed matrix; nothing to merge.

## When to re-run

- Agent ships a major version (e.g. claudecode `2.0 → 2.1`, codex new release
  channel). Update `agent_version` in the survey output.
- A scenario is added or substantially reworded in `.specs/agent-scenarios.md`.
  `run-survey.sh validate` will fail with "scenario id missing from survey"
  on the next run, which is the forcing function.

## What this mode does NOT do

- It does not modify `.specs/agent-scenarios-coverage.json`. That's the
  maintainer's job after review.
- It does not record fixtures. That's a separate flow
  (`run-cell.sh` + `promote-recording.sh`).
- It does not run the agent CLI. The survey is documentation-only.
