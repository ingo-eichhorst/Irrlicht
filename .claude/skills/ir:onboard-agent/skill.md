---
name: ir:onboard-agent
description: >
  Maintain the canonical scenario × adapter fixture matrix for irrlicht.
  A slim dispatcher that routes intent to three focused subagents —
  `scenario-create` (add a matrix row), `assess` (judge one cell), and
  `implement` (recipe→spec→record→validate→commit one cell) — plus
  `--new <slug>` discovery (add a matrix column) and a no-arg matrix
  status report. Each subagent returns a ≤5-line summary, so the parent
  keeps its context for strategic decisions instead of drowning in
  per-cell tool output. Use when the user says "/ir:onboard-agent",
  "refresh fixtures", "onboard agent", "regenerate recordings", "add a
  scenario", or "update replay fixtures".
---

# Irrlicht Agent Onboarding — dispatcher

This skill maintains the scenario × adapter fixture matrix across two
axes:

1. **Agent scenarios** (`scenarios.json -> scenarios[]`) — agent-agnostic
   declarations with `requires: [capability]`; per-adapter recipes under
   `by_adapter`. A cell is applicable when the adapter's
   `capabilities.json` satisfies the scenario's `requires`. Valid cells
   run a real CLI under a recording daemon and capture transcripts.
2. **Orchestrator scenarios** (`scenarios.json -> orchestrator_scenarios[]`)
   — per-orchestrator inputs under `by_orchestrator`, verified by a Go
   test against committed goldens. Hermetic; no live CLI, no auth.

**Why a dispatcher of subagents:** a single onboarding sweep used to run
hundreds of shell invocations and dump every `events.jsonl` /
`transcript.jsonl` into one transcript, exhausting the parent's context
long before the work was done. Now each mechanical operation is one
`Agent` call that exhausts its OWN context and returns a short summary.
The parent decides *which* cells to touch; the subagents do the work.

See [`README.md`](README.md) for the user-facing decision tree and a
worked example, and [`cell-lifecycle.md`](cell-lifecycle.md) for the
five-stage pipeline the subagents implement.

## Routing — pick the operation

| The user wants to… | Command | You dispatch |
|---|---|---|
| see matrix status (no cost) | `/ir:onboard-agent` (no args) | nothing — compute inline (below) |
| track a NEW behavior the matrix lacks | `scenario-create <slug>` | `scenario-create` subagent |
| judge whether `<agent>` supports `<scenario>` | `assess <agent> <scenario>` | `assess` subagent |
| capture how `<agent>` does `<scenario>` | `implement <agent> <scenario>` | `implement` subagent |
| re-record after a daemon change | `implement <agent> <scenario> --re-record` | `implement` subagent |
| onboard a brand-NEW agent CLI | `--new <slug>` | discovery (see below) |
| verify nothing regressed | `tools/replay-fixtures.sh` | nothing — pure script |
| run an orchestrator scenario | `<orch> [<scenario>]` | inline (see Orchestrators) |

The legacy per-stage verbs (`assess`/`recipe`/`spec`/`record`/`validate`)
still exist as building blocks under their own `SKILL.md` files;
`implement` bundles recipe→spec→record→validate behind one contract, so
you rarely invoke the middle three directly.

## Dispatching a subagent

For `scenario-create`, `assess`, and `implement`, spawn ONE
`general-purpose` Agent and let it run the corresponding self-contained
`SKILL.md`. Brief it minimally — the SKILL.md carries the full contract:

```
Agent(
  subagent_type: "general-purpose",
  description: "<verb> <agent>/<scenario>",
  prompt: "Read and execute .claude/skills/ir:onboard-agent/<verb>/SKILL.md.
           Inputs: agent=<agent> scenario=<scenario> [--re-record].
           Follow it exactly and return ONLY the summary it specifies."
)
```

When the user asks for a column/row sweep (e.g. "assess every scenario
for codex", "implement all never-recorded claudecode cells"), compute
the cell list from the matrix status below, then dispatch **one Agent
per cell**. Collect each subagent's ≤5-line summary into a table for the
user. Don't run the per-cell mechanics yourself — that's what blows the
context budget.

**Parallelism rule for sweeps.** `assess` cells are read-only (no daemon)
and may be fanned out in parallel waves to save wall-clock. `implement` /
record cells drive a live CLI under the single `--attach` daemon and MUST
be serialized — concurrent recordings on one daemon interleave. And a batch
of standalone `assess` runs writes `assessment.json` files under
`replaydata/`, dirtying the tree, so commit those assessments BEFORE the
first `implement` — `precheck.sh` refuses to record on a dirty `replaydata/`.

After dispatching `implement`, the recording is already committed (that
is part of its contract). Don't re-stage, re-diff, or re-commit; just
relay its summary.

## Matrix status (`list` — the no-arg path)

Compute and print the state of both matrices. No cost.

### Cross-reference check (run first)

Every `requires` id must exist in the canonical features list, else the
matrix is undefined:

```bash
SK=.claude/skills/ir:onboard-agent
comm -23 \
  <(jq -r '.scenarios[].requires[]' $SK/scenarios.json | sort -u) \
  <(jq -r '.features[].id' replaydata/agents/features.json | sort -u)
# any output = a scenario requires an unknown capability — block and report it.
```

Also confirm the two catalogs agree — `scenarios.json` vs the coverage
rollup — so no cell is orphaned or unmapped:

```bash
SK=.claude/skills/ir:onboard-agent
# coverage_ids referenced by scenarios.json but ABSENT from the rollup matrix:
comm -23 \
  <(jq -r '.scenarios[].coverage_id' $SK/scenarios.json | sort -u) \
  <(jq -r '.scenarios[].id'          $SK/agent-scenarios-coverage.json | sort -u)
# rollup ids with NO recipe row in scenarios.json (orphan coverage cells):
comm -13 \
  <(jq -r '.scenarios[].coverage_id' $SK/scenarios.json | sort -u) \
  <(jq -r '.scenarios[].id'          $SK/agent-scenarios-coverage.json | sort -u)
# either output = catalog drift: a name maps to a missing coverage cell, or a
# coverage cell has no recipe. Surface it; don't silently sweep around it.
```

Several `name`s legitimately share one `coverage_id` (recipe variants of the
same canonical cell) — the matrix axis is the coverage id, not the name.

### Agent matrix (`scenarios[]` × agent adapters)

A scenario is applicable to an adapter iff every id in `requires` maps to
`true` in that adapter's `capabilities.json -> features` (`false` and
`"unknown"` both block). Per-cell state:

- **OK** — fixture committed at
  `replaydata/agents/<adapter>/scenarios/<scenario>/{transcript,events}.jsonl`.
- **never-recorded** — applicable + `by_adapter.<adapter>` recipe exists,
  but no committed fixture. → `implement <adapter> <scenario>`.
- **missing-recipe** — applicable but no `by_adapter.<adapter>` entry. →
  `implement` (which authors it) once `assess` says `applicable: yes`.
- **unassessed** — no `assessment.json` yet. → `assess` first.
- **N/A (no <capability>)** — adapter's capabilities don't satisfy
  `requires`.

```bash
SK=.claude/skills/ir:onboard-agent
for a in claudecode codex pi aider opencode; do
  echo "== $a =="
  jq -r '.features | to_entries[] | "\(.key)=\(.value)"' replaydata/agents/$a/capabilities.json
done
ls replaydata/agents/*/scenarios/ 2>/dev/null
```

Print a table (rows = adapters, columns = scenarios) with a one-line hint
on every non-OK cell.

### Driver-capability pre-flight (before an `implement` sweep)

A cell can be applicable yet un-recordable because the agent's interactive
driver lacks a step type its recipe needs (`keys`, `resume`, `restart`,
`reset_session`, `exit_clean`, `sigkill`). Surface these UPFRONT so the
sweep routes them to a driver-extension task instead of spending an
`implement` round-trip per cell to rediscover the gap:

```bash
SK=.claude/skills/ir:onboard-agent
# step types the agent's interactive driver implements (its case labels):
grep -oE '^\s*(send|slash|wait_turn|sleep|interrupt|keys|restart|resume|sigkill|exit_clean|reset_session)\)' \
  $SK/scripts/drive-<agent>-interactive.sh | tr -d ' )' | sort -u
```

Cross-check that set against the step types each cell's recipe needs (or,
for unwritten recipes, the steps the behaviour implies — multi-session ⇒
`reset_session`/`resume`/`restart`; in-REPL picker navigation ⇒ `keys`).
Cells needing an absent step are `driver_gap` — a developer task to extend
the driver, not a recording. New agents start with the sparsest drivers
(e.g. opencode: `send`/`sleep`/`wait_turn` only), so this report matters
most at onboarding time.

### Orchestrator matrix (`orchestrator_scenarios[]` × orchestrators)

```bash
jq -r '.orchestrator_scenarios[] | "\(.name)\t\(.by_orchestrator.gastown.fixture_dir)\t\(.by_orchestrator.gastown.poll_ticks)"' \
  .claude/skills/ir:onboard-agent/scenarios.json
find replaydata/orchestrators/gastown/scenarios -name 'state-*.json' 2>/dev/null | sort
```

States: **OK** (goldens cover all `poll_ticks`), **never-recorded** (no
`golden/`), **missing-fixture** (no `input/`).

## Orchestrators (inline — no subagent)

Orchestrator scenarios are hermetic, so the parent runs them directly
(they're cheap and produce no live-agent token cost):

```bash
.claude/skills/ir:onboard-agent/scripts/drive-gastown.sh <scenario>
```

The script stages a writable copy, runs
`go test -run TestGastownReplay/<scenario> -update-goldens` against it,
and writes `run-manifest.json` with `verdict` (`OK`/`CHANGED`/`ERROR`)
+ the differing golden files. Read the manifest, diff staged vs
committed goldens for any `CHANGED` cell, and cross-check the scenario's
`verify` block. Unlike agent cells, the maintainer reviews and commits
orchestrator goldens by hand:

```bash
go test ./core/adapters/inbound/orchestrators/gastown/ \
        -run TestGastownReplay/<scenario> -update-goldens
git add replaydata/orchestrators/gastown/scenarios/<scenario>/ && git commit -m "..."
```

## Discovery mode (`--new <slug>`)

Adds a matrix COLUMN (a whole new agent), as opposed to
`scenario-create` which adds a ROW. Load
[`discovery-instructions.md`](discovery-instructions.md) and follow that
recipe — it researches the agent on the web, proposes a
`capabilities.json`, and walks the stub-adapter + smoke-recording gate.
No live agent CLI runs during discovery itself.

## Anti-patterns

- **Don't run per-cell mechanics in the parent.** Authoring recipes,
  driving CLIs, curating fixtures, diffing reports — all of that belongs
  inside an `implement` subagent. The parent routes and summarizes.
- **Don't re-commit after `implement`.** It commits as part of its
  contract; a clean tree is the handoff signal.
- **Don't dispatch `implement` for an `applicable: no`/`n/a` cell.** It
  refuses anyway — check the assessment first and save the round-trip.
- **Don't run with an isolated daemon while a production `irrlichd` is
  up.** Use `--attach` (the subagents do). `precheck.sh` enforces this.
- **Don't edit `agent-scenarios-coverage.json` from the parent.** It's
  the maintainer's editorial rollup; subagents surface drift in their
  summaries.
