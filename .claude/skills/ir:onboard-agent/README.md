# ir:onboard-agent

Developer guide for the scenario × adapter fixture-matrix skill.

## Which command do I want?

| I want to… | Run | Cost |
|---|---|---|
| see the matrix status | `/ir:onboard-agent` (no args) | free |
| **track a new behavior** the matrix doesn't have yet | `/ir:onboard-agent scenario-create <slug>` | free |
| **check if `<agent>` supports `<scenario>`** | `/ir:onboard-agent assess <agent> <scenario>` | free (research only) |
| **capture how `<agent>` does `<scenario>`** | `/ir:onboard-agent implement <agent> <scenario>` | spends agent CLI tokens |
| **re-record** after a daemon change | `/ir:onboard-agent implement <agent> <scenario> --re-record` | spends agent CLI tokens |
| **onboard a brand-new agent CLI** | `/ir:onboard-agent --new <slug>` | free (web research) |
| **verify nothing regressed** | `tools/replay-fixtures.sh` | free |

The first three are the everyday loop: **`scenario-create`** adds a matrix
ROW, **`--new`** adds a matrix COLUMN, **`assess`** judges one cell, and
**`implement`** fills it (recipe → spec → record → validate → commit).

## What this skill does

It maintains the canonical scenario × adapter fixture matrix in
`replaydata/agents/`. Scenarios are defined once, agent-agnostically;
each declares the capabilities it `requires`. Each adapter declares what
it supports in its `capabilities.json`. A cell is applicable when the
adapter's capabilities satisfy the scenario's requirements. The skill is
a slim dispatcher: it routes your intent to one of three focused
subagents, each of which exhausts its OWN context window and returns a
≤5-line summary — so a large sweep doesn't drown the parent session in
per-cell `events.jsonl` / `transcript.jsonl` output.

## The three subagents

Each is a self-contained prompt under its own directory; the dispatcher
spawns it with a one-line brief and relays its summary.

- **`scenario-create/SKILL.md`** — adds a new agent-agnostic scenario:
  a `scenarios[]` + `catalog[]` entry (top-level only, no recipe), a
  `scenario-meanings.md` glossary block, a coverage-matrix row with every
  adapter `unknown`, and a new `features.json` capability if needed.
  One-shot, no CLI, no recording. Returns `{scenario_id, capability_ids,
  files_changed}`.
- **`assess/SKILL.md`** — researches one cell and writes
  `assessment.json` (verdict + body + caveats + sources). Returns
  `{verdict, applicable, summary}`. If `applicable` is `no`/`n/a`, the
  cell is frozen — no recipe, no recording. (Also has `--column <agent>`
  / `--row <scenario>` batch-candidate modes.)
- **`implement/SKILL.md`** — the full pipeline for one cell: authors the
  spec + recipe, records against a live daemon, validates, promotes, and
  commits. Retries a drive timeout exactly once, then degrades to
  `applicable: false` with a `scope_note`; returns `driver_gap`
  immediately when the recipe needs a step type the agent's driver lacks
  (no retry); **always commits before returning**. Returns `{commit_sha,
  pass_rate, status}` where status is `pass | applicable_false |
  driver_gap | infra_fail`.

## Worked example — a new scenario, end to end

Say you want to track "the agent queues a message the user typed
mid-turn" for claudecode.

1. **Create the row** (free, one-shot):
   ```
   /ir:onboard-agent scenario-create mid-turn-message-queued
   ```
   Adds the scenario to `scenarios.json` + `catalog`, writes its
   `scenario-meanings.md` block, and adds a coverage row where every
   adapter (claudecode, codex, pi, aider, opencode) is `unknown`.
   Returns the `scenario_id` and the `requires` capability ids.

2. **Assess the cell you care about** (free):
   ```
   /ir:onboard-agent assess claudecode mid-turn-message-queued
   ```
   Researches claudecode's docs + the adapter transport, writes
   `assessment.json`, and returns e.g. `applicable: yes`. (If it returned
   `no`/`n/a`, you'd stop here — the assessment documents why.)

3. **Implement it** (spends tokens, needs a recording daemon):
   ```
   /ir:onboard-agent implement claudecode mid-turn-message-queued
   ```
   Authors `expected.jsonl` then the `by_adapter.claudecode` recipe,
   commits them, drives the live `claude` CLI under your running
   `irrlichd --record`, validates the recording against the spec,
   promotes it, and commits. Returns `{commit_sha, pass_rate: "8/8",
   status: pass}`.

4. **Later, after a daemon change**, refresh the recording without
   re-authoring anything:
   ```
   /ir:onboard-agent implement claudecode mid-turn-message-queued --re-record
   ```

To fill the same scenario across every applicable adapter, ask the
dispatcher to sweep — it computes the cell list and runs one `assess`
(then one `implement`) subagent per cell, collecting each summary.

## Has anything broken? — `tools/replay-fixtures.sh`

```
tools/replay-fixtures.sh
```

No agent, no tokens, no daemon. It walks every committed
`expected.jsonl` and re-validates the latest recording (and archives)
against the current daemon, then writes JSON + Markdown reports under
`replaydata/agents/_reports/`. **This is the CI gate** — it exits
non-zero on a regression, so CI catches a daemon change that drifts a
fixture before users see it on the dashboard. Run it after any change to
the daemon's parsers/classifier, and it's part of the "before marking a
ticket done" suite alongside `go test ./core/... -race -count=1`.

## Building blocks (stage-level mechanics)

`implement` bundles four stages; their per-stage `SKILL.md` docs are the
detailed mechanics, useful when a cell needs hand-holding or when you're
extending a driver:

- [`recipe/SKILL.md`](recipe/SKILL.md) — Stage 2: author the
  deterministic `by_adapter.<agent>` driver script (step grammar,
  multi-variant chaining, adapter quirks, determinism budget).
- [`spec/SKILL.md`](spec/SKILL.md) — Stage 3: author `expected.jsonl`
  (the phase DSL the validator runs against every recording).
- [`record/SKILL.md`](record/SKILL.md) — Stage 4: `--attach` mode,
  the run-cell → promote pipeline, the determinism re-record check.
- [`validate/SKILL.md`](validate/SKILL.md) — Stage 5: the
  `expected-validate` decision tree, `known_failing`, drift detection.
- [`cell-lifecycle.md`](cell-lifecycle.md) — how the five stages fit
  together, with a worked `claudecode/session-reset` walkthrough.
- [`scenario-meanings.md`](scenario-meanings.md) — the committed prose
  glossary every scenario must have a block in.
- [`discovery-instructions.md`](discovery-instructions.md) — the
  `--new <slug>` recipe for onboarding a brand-new agent CLI.

## Correctness guards

Not cost-related — these prevent broken runs:

- `pgrep -x irrlichd` refusal in isolated mode (port 7837 clash); the
  subagents use `--attach` against your running daemon instead.
- Git-clean check on `replaydata/agents/` before recording — which is
  why `implement` commits the spec + recipe before it records.
- CLI version minimum check against `scenarios.json.min_versions`.
- Wall-clock `timeout` per cell (hang protection).

## File layout

```
.claude/skills/ir:onboard-agent/
  skill.md                    — the dispatcher (routing + matrix status)
  README.md                   — this file
  scenario-create/SKILL.md    — agent #1: add a matrix row
  assess/SKILL.md             — agent #2: judge one cell (+ batch modes)
  implement/SKILL.md          — agent #3: fill one cell end-to-end
  recipe|spec|record|validate/SKILL.md — stage-level building blocks
  scenario-meanings.md        — committed scenario glossary
  scenarios.json              — canonical scenario catalogue
  agent-scenarios-coverage.json — rollup matrix (maintainer-owned)
  discovery-instructions.md   — --new (new agent column) recipe
  install-instructions.md     — per-adapter install + auth recipes
  scripts/
    run-cell.sh               — precheck → daemon → driver → curate → replay
    precheck.sh               — fail-fast preconditions
    drive-<agent>[-interactive].sh — per-adapter drivers
    discover-agent.sh         — discovery preamble renderer
    lib/{assert-staging-path,classify-failure}.sh

replaydata/agents/
  features.json               — canonical capability catalog
  <adapter>/
    capabilities.json         — per-adapter feature support
    scenarios/<scenario>/
      assessment.json         — Stage 1 artifact
      expected.jsonl          — Stage 3 spec
      events.jsonl            — Stage 4 lifecycle events (latest)
      transcript.jsonl        — Stage 4 agent transcript (latest)
      manifest.json           — daemon/CLI/recipe versions + pass rate
      recordings/<ts>_irrlichd-<ver>/ — archived prior recordings
```

## Troubleshooting

- **"another irrlichd is running"** — the subagents use `--attach`;
  ensure your daemon was started with `--record`. Isolated mode refuses
  while a production daemon is up.
- **"replaydata/agents/ has uncommitted changes"** — commit or stash
  first. `implement` commits its own spec/recipe before recording, so a
  dirty tree means a half-finished manual edit.
- **"claude X.Y.Z is below pinned minimum"** — update the CLI, or bump
  the minimum in `scenarios.json` if intentional.
- **`implement` returned `infra_fail`** — environment problem (CLI
  missing/old, no recording daemon, auth). The cell verdict is
  unchanged; fix the environment and re-run.
- **`implement` returned `driver_gap`** — the recipe needs a driver step
  type that agent doesn't implement. Extending the interactive driver is
  a developer task (out of scope for the skill).

## See also

- `tools/promote-recording.sh` — archives the previous recording and
  re-validates the new one (invoked by `implement`).
- `tools/curate-lifecycle-fixture.sh` — the underlying fixture curator.
- `core/cmd/replay/main.go` — the replay engine behind the reports.
