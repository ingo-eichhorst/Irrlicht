---
name: ir:onboarding-factory
description: >
  Maintain the canonical scenario × agent fixture matrix for irrlicht. A slim
  dispatcher that routes intent to four focused subagents — `create-scenario`
  (add a matrix row), `create-agent` (add a matrix column), `assess` (judge one
  cell across the three pillars and write its spec), and `record` (drive the
  live agent and verify every websocket observation). Every read and every
  write goes through the `of` factory CLI (tools/onboarding-factory) — the skill
  itself never touches `replaydata/`. Each subagent returns a ≤6-line summary so
  the parent keeps its context for strategic decisions instead of drowning in
  per-cell tool output. Use when the user says "/ir:onboarding-factory",
  "onboard agent", "add a scenario", "assess fixtures", "record fixtures", or
  "regenerate recordings".
---

# Irrlicht onboarding factory — dispatcher

This skill maintains the **scenario × agent fixture matrix**: for each
behavior (a scenario row) and each agent CLI (a column), it judges whether the
agent supports it, whether the daemon can observe it, and whether the recording
harness can drive it — then captures a live recording and verifies it.

**One iron rule: the factory owns `replaydata/`.** The skill is pure
orchestration. It NEVER reads or writes catalog files, cell metadata, specs, or
recordings by hand — every operation is an [`of`](#the-of-cli) call, and `of`
validates schema + referential integrity before it touches disk. If you find
yourself about to `jq`, `Edit`, or `Write` something under `replaydata/`, stop:
there is an `of` verb for it.

**Why a dispatcher of subagents.** A full onboarding sweep runs hundreds of
shell invocations and dumps every `events.jsonl` / `transcript.jsonl` into the
transcript — enough to exhaust the parent's context long before the work is
done. So each cell's work is ONE `Agent` call that burns its OWN context and
returns a short summary. The parent decides *which* cells to touch; the
subagents do the work and report back. See
[`return-contract.md`](return-contract.md) for the shared ≤6-line envelope.

## The four verbs

| The user wants to… | Verb | Subagent |
|---|---|---|
| track a NEW behavior the matrix lacks | `create-scenario <slug>` | [`create-scenario`](create-scenario/SKILL.md) |
| onboard a brand-NEW agent CLI (a column) | `create-agent <slug>` | [`create-agent`](create-agent/SKILL.md) |
| judge whether `<agent>` does `<scenario>` + write its spec | `assess <agent> <scenario>` | [`assess`](assess/SKILL.md) |
| capture + verify a live recording for a cell | `record <agent> <scenario>` | [`record`](record/SKILL.md) |
| see matrix status (no cost) | `/ir:onboarding-factory` (no args) | nothing — `of status` inline |
| run an orchestrator scenario (gastown) | `<orch> [<scenario>]` | inline (hermetic — see below) |

There is no separate `extend-driver` verb: a driver gap is surfaced by `assess`
(the **driver** pillar = `gap:<primitive>`) and closed inside `record`, which
ports the missing step from the reference driver before it drives.

## Dispatching a subagent

For `create-scenario`, `create-agent`, `assess`, and `record`, spawn ONE
`general-purpose` Agent and let it run the corresponding self-contained
`SKILL.md`. Brief it minimally — the SKILL.md carries the full contract:

```
Agent(
  subagent_type: "general-purpose",
  description: "<verb> <agent>/<scenario>",
  prompt: "Read and execute .claude/skills/ir:onboarding-factory/<verb>/SKILL.md.
           Inputs: agent=<agent> scenario=<scenario>.
           Follow it exactly and return ONLY the summary it specifies."
)
```

Collect each subagent's ≤6-line summary into a table for the user. Do **not**
run the per-cell mechanics yourself — that is what blows the context budget.

## Deriving the work-list — from `of status`, never from prose

When the user asks for a sweep ("assess every scenario for codex", "record all
pending claudecode cells"), compute the cell list **from the factory, never
from a prior subagent's summary.** The per-cell summaries are deliberately lossy
(≤6 lines, to protect parent context); enumerating the next stage's work from
that prose is how a cell silently drops — a real regression once left a scenario
sitting un-assessed and un-recorded because one summary omitted it. Ask the
authoritative source instead:

```bash
of status --agent <agent> --json     # every cell + its display_state, route, disposition, 3 pillars
of status --json                      # the whole matrix
```

The cells whose `display_state` is **`pending-record`** are the record
work-list; cells with no assessment yet are the assess work-list. Dispatch
**one Agent per cell**, then re-run `of status` to confirm none remain before
reporting "done". `display_state` ∈ `observed` (terminal),
`pending-record` (→ assess or record), `blocked-daemon` / `blocked-driver` /
`unobservable` / `n.a.` (terminal — documented, no recording), `unknown`
(→ assess). A sweep is finished only when every cell is terminal.

## Parallelism + ordering rules for sweeps

- **`assess` fans out.** It is read-only (web + file research, no daemon), so
  dispatch assess cells in parallel waves to save wall-clock.
- **`record` is serialized.** It drives a live CLI under the single
  `--attach` daemon; concurrent recordings on one daemon interleave. Run record
  cells one at a time.
- **Commit assessments before the first record.** `assess` writes cell
  `metadata.json` + `expected.jsonl` via `of`, dirtying `replaydata/`; the
  recording precheck refuses a dirty `replaydata/` tree. So land the assessment
  commits first, then record.
- After a `record` subagent returns, the recording is already committed (part
  of its contract). Don't re-stage, re-diff, or re-commit — just relay the
  summary.

## Human-blocking prerequisites

Some recordings need a human action the subagent cannot perform (e.g. switch a
subscription to an API key, install a CLI, set an env var). A subagent NEVER
asks the dispatcher a question — it runs to a terminal outcome and returns
`status: prereq_blocked` with the concrete blocker in `notes`. The dispatcher
surfaces that line to the human and moves on to the next cell. `of record
prereq-check --agent <agent>` lists an agent's known prerequisites up front.

## Orchestrators (inline — no subagent)

Orchestrator scenarios (gastown) are hermetic: no live CLI, no auth, no token
cost. The driver replays canned inputs through the Go test harness and diffs
goldens, so the parent runs it directly:

```bash
replaydata/orchestrators/gastown/driver.sh <scenario>
```

Read the emitted `run-manifest.json` (`verdict: OK | CHANGED | ERROR`). For a
`CHANGED` cell, diff staged vs committed goldens; the maintainer reviews and
commits orchestrator goldens by hand (unlike agent cells, which `record`
commits).

## The `of` CLI

`of` is the factory binary (`go run ./cmd/of` from `tools/onboarding-factory`,
or the built `of` binary). The verbs the skill drives:

```
of status   [--agent a] [--scenario s] [--runs] [--json]   coverage / run-log
of validate [--json]                                        schema + referential integrity
of coverage [--json]                                        derived rollup
of scenario add|update --name n [--id i] [--description d]
                       [--process-file f] [--acceptance-file f]
of agent add --id i --name n --provider p [--min-version v] [--prereq p]...
of cell write --agent a --scenario s --file metadata.json [--folder f]
of cell spec  --agent a --scenario s --file expected.jsonl [--folder f]
of verify --agent a --scenario s [--json]
of record prereq-check --agent a
of record run    --agent a --scenario s [--attach] [--dry-run]
of record verify --agent a --scenario s
```

Exit codes: `0` ok, `1` validation/operation failed, `2` usage error. Every
write verb validates-then-writes atomically and forces the foreign keys
(`scenario_id`), so a subagent cannot create a dangling cell.

## Anti-patterns

- **Don't touch `replaydata/` directly.** No `jq -i`, no `Edit`, no `Write`
  under `replaydata/`. Use the `of` write verbs — they are the only sanctioned
  writer and they validate first.
- **Don't run per-cell mechanics in the parent.** Research, driving CLIs,
  authoring specs, verifying recordings — all of that belongs inside a
  subagent. The parent routes and summarizes.
- **Don't enumerate a sweep's next stage from subagent prose.** Derive it from
  `of status`. Lossy summaries drop cells.
- **Don't record on a dirty `replaydata/` tree, and don't re-commit after
  `record`.** Commit assessments first; a clean tree is the record handoff
  signal and `record` commits its own recording.
- **Don't run an isolated recording daemon while production `irrlichd` is up.**
  Use `of record run --attach`; the precheck enforces this.
