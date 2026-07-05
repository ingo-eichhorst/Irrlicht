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
  "regenerate recordings". For unattended / overnight runs ("push through",
  "onboard everything", "implement everything that can be"), see the Overnight
  push-through mode section — implement every feasible unlock and deliver a
  report-as-PR, autonomously.
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

## Overnight push-through mode (the maintainer's default for unattended runs)

Triggered by "push through", "everything", "onboard fully", "overnight", or any
unattended / scheduled run. The goal is **a valid report and a reviewable PR by
morning, with every cell that *can* be observed actually observed** — not just
the easy ones. Run **fully autonomously**: never call `AskUserQuestion` /
`ExitPlanMode` / block on a prompt mid-run — make the sensible call, document it,
and keep going. Emit a one-line status at each stage boundary; the final PR
body *is* the morning report.

This is more than the standard sweep. Work three passes until the matrix stops
moving:

1. **Sweep** — assess every cell, then record every `pending-record` cell (the
   normal dispatcher flow described below).

2. **Unlock — implement what's missing.** Do **not** accept a frozen verdict at
   face value. For each non-`observed` cell (`blocked-daemon` / `blocked-driver`
   / `unobservable` / `unknown`), ask: *is the blocker a daemon / parser / driver
   feature that is feasible to build?* If yes, **build it** — this is the one
   kind of work the parent does directly (Go code under `core/`, never
   `replaydata/`, so the iron rule still holds): an adapter parser fix, a new
   daemon capability, a `Source` / discovery extension, a driver seam. And
   **empirically probe every `unobservable` verdict** — drive the agent into that
   edge state and read what actually lands on disk; assessments are sample-based
   and routinely under-call what's observable. Every feature gets **unit tests
   AND a live end-to-end check**. Then re-assess + re-record the now-unblocked
   cells (back through the subagents). Repeat pass 2 until nothing is feasibly
   unlockable. (Precedent: the antigravity run shipped PID-binding, subagent
   linking, and three parser fixes this way — moving ~7 cells from frozen to
   observed — while probing *confirmed* that permission/interrupt states are
   genuinely unobservable.)

3. **Freeze honestly — but never fake.** A cell stays frozen ONLY when the signal
   genuinely doesn't exist: the agent lacks the feature, the data isn't persisted
   anywhere readable (live-API-only / TUI-only), or it needs an upstream change.
   **Never ship a fragile guess to force a green cell** — e.g. decoding unlabeled
   protobuf with no ground truth to validate against. The test: *would a reviewer
   accept this as correct, or is it a plausible guess?* If a guess, freeze it,
   document the exact blocker, and file a follow-up issue.

Then **deliver**: get the full suite green (`go test ./core/... -race`, `go test
./tools/onboarding-factory/... -race`, `tools/replay-fixtures.sh`, `of
validate`), commit in clean logical commits, push, and open / update **one PR**.
The PR body is the report: cells observed (count + delta), cells frozen (grouped
by honest reason), features implemented (with their tests), follow-ups filed.
Update the relevant memory.

**Scope guard.** "Implement everything that can be implemented" means everything
**feasible and verifiable** — proven by tests + a live run. It does NOT mean
force-greening the impossible. A cell that is frozen for a real reason, with a
filed follow-up, is a valid outcome and belongs in the report — that is success,
not a gap.

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
`SKILL.md`. Brief it minimally — the SKILL.md carries the full contract — but
ALWAYS bind its working directory explicitly. A spawned agent does **not**
inherit the dispatcher's cwd: it starts in the main checkout even when you are
in a worktree. Pass the absolute repo root and make the subagent `cd` there and
assert the branch BEFORE any `git` or `of` write. Skipping this once landed 43
cells on `main` instead of the worktree. Compute the root once
(`git rev-parse --show-toplevel`) and substitute it for `<repo-root>`:

```
Agent(
  subagent_type: "general-purpose",
  description: "<verb> <agent>/<scenario>",
  prompt: "Read and execute .claude/skills/ir:onboarding-factory/<verb>/SKILL.md.
           Repo root: <repo-root>. FIRST cd there, then assert
           `git rev-parse --show-toplevel` == <repo-root> and
           `git branch --show-current` == <branch>; abort if either differs.
           Run every of/git command from that root.
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
**one Agent per assess cell**; **`record` cells may be batched a few per
subagent** (see the parallelism rules). Then re-run `of status` to confirm none
remain before reporting "done". `display_state` ∈ `observed` (terminal),
`pending-record` (→ assess or record), `blocked-daemon` / `blocked-driver` /
`unobservable` / `n.a.` (terminal — documented, no recording), `unknown`
(→ assess). A sweep is finished only when every cell is terminal.

## Parallelism + ordering rules for sweeps

- **`assess` fans out; the PARENT commits.** assess is read-only of the live
  system (web + file research, no daemon), so dispatch assess cells in parallel
  waves to save wall-clock. But an assess subagent only WRITES its cell (via
  `of cell write` / `of cell spec`) — it does **not** commit. N parallel
  `git commit`s on one worktree race, scramble attribution, and once stranded a
  wave mid-`reset`. After each wave returns, the parent stages and commits the
  cells serially (one commit per cell). Committing is version control, not
  authoring — the iron rule forbids the parent *authoring* `replaydata/`, not
  committing what the subagents already wrote through `of`.
- **`record` is serialized and self-commits.** It drives a live CLI under the
  single `--attach` daemon; concurrent recordings on one daemon interleave. Run
  record cells one at a time. Because record is serial there is no commit race,
  so the record subagent commits its own recording (part of its contract).
- **Batch a few serial cells per `record` subagent.** A record subagent records
  one cell at a time internally, so handing it ~4 cells per dispatch is far
  cheaper on dispatcher context/turns than strict one-subagent-per-cell, with no
  downside (the recordings still serialize). Keep batches small enough that the
  ≤7-line returns stay legible — one return block per cell in the batch.
- **Commit every assessment before the first record.** Cell `metadata.json` +
  `expected.jsonl` dirty `replaydata/`; the recording precheck refuses a dirty
  tree. So land all the parent-side assessment commits first, then record.
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

## Filing daemon-bug issues (parent step, with consent)

A `record` subagent CANNOT file GitHub issues — outward-facing writes are denied
in its permission context, so for a `daemon=bug` cell it returns an `issue:`
payload (a temp-file body + a one-line title) instead of filing it. When a
record return carries a non-`none` `issue:`, the dispatcher — not the subagent —
files it: show the user the title + body, and ONLY on their confirmation run

```bash
gh issue create --repo ingo-eichhorst/Irrlicht --label bug \
  --title "<title from the payload>" --body-file "<path from the payload>"
```

Report the new issue number to the user. Wiring the number back into the cell,
if wanted, is a follow-up `assess`/`record` touch (a subagent) — the dispatcher
never authors `replaydata/`.

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
- **Don't accept a frozen verdict without probing it (push-through mode).** An
  `unobservable` / `blocked-*` cell may just need a feature built or an
  empirical probe — see Overnight push-through mode. But don't swing the other
  way and **fake green**: never ship a guess (e.g. reverse-engineered unlabeled
  data) to force a cell observed. Freeze honestly + file a follow-up.
