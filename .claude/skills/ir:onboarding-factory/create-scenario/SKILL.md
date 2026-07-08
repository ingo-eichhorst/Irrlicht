---
name: ir:onboarding-factory/create-scenario
description: >
  Add a brand-new scenario ROW to the matrix: one agent-agnostic
  `{id, name, description, acceptance_criteria, process}` entry written through
  `of scenario add`. Researches how the behavior manifests across every
  onboarded agent (and what the daemon would observe) before synthesizing the
  agent-agnostic spec. No agent CLI invocation, no recording. Invoked as
  `/ir:onboarding-factory create-scenario <slug>`.
---

# create-scenario

> **You run as a focused subagent with no parent context.** Everything you need
> is in this file and the repo. Do the research yourself (web + file access) —
> don't bounce work back to the dispatcher. This task spends NO API tokens on
> agent CLIs and runs NO recording. When done, return only the summary in the
> "Return contract" section.

## What this does

Adds one new **agent-agnostic** scenario so later verbs (`assess`, then
`record`) have a row to fill. A scenario is defined ONCE, agnostic to any
particular agent; per-agent verdicts, recipes, specs, and recordings come later
from `assess` and `record`. After you run, the new scenario is a row of
unknown cells — nothing claimed about any agent yet.

This is the matrix-ROW counterpart to `create-agent`, which adds a COLUMN.

## The scenario schema (5 fields, nothing else)

`of scenario add` writes exactly this shape into
`replaydata/agents/scenarios.json`:

```
id                   <section>.<index>  — e.g. "2.22". Stable; orders the matrix.
name                 kebab slug         — e.g. "mid-turn-message-queued". The FK.
description          one paragraph      — what behavior, why it matters.
acceptance_criteria  markdown           — what a recording must show to pass.
process              markdown           — how to drive an agent to elicit it.
```

There is no `section`/`feature`/`requires`/`verify`/`idle_only` any more — those
were dropped in the factory cutover. Applicability is decided per-cell by
`assess`, not by a `requires` gate on the row.

## Inputs

- `<slug>` — kebab-case scenario name (stable; becomes the FK and the recording
  folder stem). E.g. `mid-turn-message-queued`.
- A one-paragraph description of the behavior. If the dispatcher didn't pass
  one, derive it from the slug and state your assumption in the summary.

## Steps

### 1. Pick the id

List the catalog through the factory (never read the file directly):

```bash
of status --json | jq -r '.scenarios[].id' | sort -t. -k1,1n -k2,2n
```

Group ids by their `<section>` integer (1 = session lifecycle, 2 = turn / tool
interaction, 3 = subagents, 4 = multi-session/workspace, 5 = metrics, 6 =
backchannel/control — infer the section from sibling scenarios). Pick the section that fits the behavior and
take the next free `<index>` in it. Confirm the slug isn't already present.

### 2. Research the behavior across every onboarded agent

This is the load-bearing step — the scenario must be agent-agnostic but
GROUNDED in how real agents behave and what the daemon can see. Find the
onboarded agents:

```bash
of status --json | jq -r '.agents[]'
```

If the research is broad, **fan out one research subagent per agent** (`Agent`
tool, `general-purpose`) — each reads that agent's docs/changelog/source and
the irrlicht adapter under `core/adapters/inbound/agents/<agent>/` and reports:
does the agent do this behavior, and what trace would it leave (transcript line,
store row, process event) that the daemon tails? Synthesize their findings
yourself — the written scenario is yours.

The point is to capture the **user-observable signal** (state badge, session
count, parent-link, a metric, a lifecycle arc) the behavior produces — never an
internal event kind or classifier rule. Acceptance criteria assert what a user
SEES.

### 3. Write `process` (markdown)

How to drive *an* agent to elicit the behavior, agent-agnostically. Reference
the step grammar the drivers understand (`send`, `wait_turn`, `sleep`,
`interrupt`, `restart`, `reset_session`, `keys`, `slash`, …) without pinning to
one agent's quirks. State the minimal sequence and the timing the behavior needs
(e.g. "a ≥10s trailing idle so an idle-flush settle is captured"). `assess`
later specializes this into a per-agent recipe.

### 4. Write `acceptance_criteria` (markdown)

What a recording must show for the cell to pass — user-observable only:

- the state arc (e.g. `ready → working → ready`, or ends `waiting` for a
  blocking question);
- counts (distinct sessions, open subagents) where relevant;
- links (parent ↔ child) and metrics (token/cost/model non-zero) the behavior
  implies.

Keep it structural and agent-agnostic. Do NOT assert internal flags, event
kinds, reasons, rule numbers, or tool-event timings — the per-agent
`expected.jsonl` spec (authored by `assess`) carries the machine-checkable
phases; this block is the human-readable contract.

### 5. Write it through the factory

Put the two markdown blocks in temp files and call `of`:

```bash
of scenario add --name <slug> --id <section>.<index> \
  --description "<one paragraph>" \
  --process-file /tmp/<slug>.process.md \
  --acceptance-file /tmp/<slug>.acceptance.md
```

`of` validates the id format, the kebab slug, and id/name uniqueness before it
writes. Then confirm the tree is consistent:

```bash
of validate
```

### 6. Commit

```bash
git add replaydata/agents/scenarios.json
git commit -m "feat(onboard): add <slug> scenario row"
git rev-parse --short HEAD
```

> End commit messages with the trailer
> `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`.

## Return contract

Return ONLY this (≤6 lines). Shared semantics + envelope rules live in
[`../return-contract.md`](../return-contract.md):

```
scenario_id: <slug>  (id <section>.<index>)
wrote: replaydata/agents/scenarios.json  (via of scenario add)
acceptance: <one-line summary of the state arc / counts you asserted>
commit_sha: <short sha>
next: assess <agent> <slug>  (per agent, to fill the row)
```

## Anti-patterns

- **Don't write `replaydata/` by hand.** Only `of scenario add` writes the
  catalog. No `jq -i`, no `Edit`.
- **Don't assess.** Every cell stays unknown — you declare the row exists, not
  any agent's verdict against it.
- **Don't assert internal mechanics in `acceptance_criteria`.** User-observable
  state/counts/links/metrics only; the machine spec is `assess`'s job.
- **Don't run a recording or invoke an agent CLI.** This is pure catalog
  authoring.
