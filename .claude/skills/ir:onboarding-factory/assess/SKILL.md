---
name: ir:onboarding-factory/assess
description: >
  Judge one (agent, scenario) cell across the three pillars — agent capability,
  daemon sensor capture, driver capability — on cited evidence, then author the
  cell's recipe and machine-checkable spec. Writes the cell metadata via
  `of cell write` and the spec (expected.jsonl) via `of cell spec`. No live
  recording. Invoked as `/ir:onboarding-factory assess <agent> <scenario>`.
---

# assess

> **You run as a focused subagent with no parent context.** Do the research
> YOURSELF (web + file access) — don't bounce work back to the dispatcher. This
> verb spends NO API tokens on agent CLIs and runs NO recording. When done,
> return only the "Return contract" block.

## What this produces

For one cell it writes two artifacts, both through the factory (never by hand):

1. **The cell** — `of cell write` writes
   `replaydata/agents/<agent>/scenarios/<id>_<scenario>/metadata.json`: the
   three-pillar verdict + confidence + a note, the full reasoning + caveats +
   sources, and the per-agent **recipe** (the driver step sequence `record`
   will run).
2. **The spec** — `of cell spec` writes `expected.jsonl` in the same folder:
   the machine-checkable phases AND the observation assertions (model / cost /
   tokens / agent) that `record`'s verify step checks against the recording.

The route the dispatcher reads off `of status` is DERIVED from the three
pillars — see the routing table in [`../return-contract.md`](../return-contract.md).

## The three pillars (judge each, cite each)

Read the pillar definitions in [`../return-contract.md`](../return-contract.md).
Three rules govern every verdict:

1. **Honest verdicts, anchored to evidence.** `agent=yes` only when the
   docs/code state the behavior explicitly; `no` when something fundamental
   blocks it; `unknown` over a guess. Name the RIGHT owner on the daemon pillar:
   `bug` (product — file an issue) vs `incapable` (architecture) vs the driver
   pillar's `gap:<primitive>` (tooling) each route differently. Don't park
   ambiguity in `bug` the way a catch-all `partial` once was a dumping ground.
2. **Caveats over downgrades.** If the canonical spec is met but a narrow detail
   is gappy, keep `daemon=full` and put the gap in `caveats`. A caveat is NOT a
   `bug`: reserve `bug` for a spec-required observable the daemon mis-handles.
3. **Cite primary sources.** Agent docs, official changelog, agent source,
   irrlicht adapter source, and — for `bug`/`incapable` — the recording's
   `events.jsonl`. Tutorials and blogs don't count. Even an `unknown` verdict
   cites what you searched.

**Evidence rule.** Any verdict other than `daemon=full` / `driver=ready` MUST be
anchored in `sources[]` to a cited event or code/doc reference — never a
plausible-sounding mechanism. The bar for `bug` / `incapable` / `gap:*` is a
concrete citation; finer granularity multiplies the chance of mislabeling.

## Steps

### 1. Read the scenario spec

```bash
of status --scenario <scenario> --json   # the cell's current pillars + route
of scenario show --name <scenario>        # the scenario's description + process + acceptance_criteria
```

Capture the **user-observable signal** the scenario asserts (from its
`acceptance_criteria` + `process`) — the state arc, counts, links, metrics. Each
is a candidate assertion you judge the daemon pillar against.

### 2. Confirm the agent's surface BEFORE writing `agent=no`

The bar for `agent=no` is higher than "`<agent> --help` doesn't mention it" —
many features live inside the REPL as slash commands or hooks, not top-level
flags. Before locking in `no`:

1. **`strings <agent-binary> | grep -iE "<feature>|/<slash>"`** for the
   feature's keywords — slash syntax, telemetry event names, preamble
   constants, error strings. This catches REPL-only features `--help` never
   lists. (The canonical miss: `claude --help` lacks `--goal`, but
   `strings $(which claude) | grep -i goal` surfaced the `/goal` autonomous-loop
   command — flipping the verdict to `yes`.)
2. **Search the agent's docs / changelog / source repo** for the same
   keywords. Vendor docs lag the binary; the binary's strings are authoritative
   for "what shipped."
3. If the scan still finds nothing, `agent=no` is honest — and `sources[]` MUST
   cite the empty binary scan so future audits don't re-litigate it.

### 3. Read the adapter transport (grounds the daemon pillar)

```
core/adapters/inbound/agents/<agent>/
  agent.go        # Source variant (FilesUnderRoot / FilesUnderCWD / ProcessOwnedStore), ProcessMatcher, PID discovery
  <parser>.go     # which event kinds the daemon can emit from this agent
```

For each user-observable signal from Step 1, ask "what event in `events.jsonl`
would prove this?" then "does this adapter's parser produce that event today?"

- yes for all, handled correctly → `daemon=full`.
- a trace exists but the daemon mis-handles a spec-required observable →
  `daemon=bug` (cite the event; the cell records `known_failing`, and `record`
  hands an `issue:` payload back for the dispatcher to file).
- no trace at all (cloud session with no local file; behavior the 3-state model
  can't represent) → `daemon=incapable`.

**Observation vs emission.** "The agent performs the behavior" (`agent`) and
"the signal reaches the Source the daemon tails" (`daemon`) are DIFFERENT
questions — don't let a plausible parser read collapse them. A `daemon=full`
derived purely from reading the parser is PROVISIONAL for any property about
what the agent *writes* to its transcript (streaming, partial flushes,
ordering, atomicity): the parser may handle a trace the agent never emits. When
the verdict hinges on emission you can't confirm from docs, keep `confidence`
low, say so in `caveats`, and let `record` promote it from provisional to
settled — a live recording is the only thing that can.

### 4. Author the recipe + judge the driver pillar

Write the per-agent recipe (the driver step sequence) that elicits the
behavior, specializing the scenario's agent-agnostic `process`. Template from
claudecode's recipe for the same scenario when one exists. For a cell asserting
the full lifecycle arc, prefer an INTERACTIVE recipe when the agent's headless
mode exits at turn completion (the process must outlive the daemon's observation
window, or the settle/teardown phases validate as missing).

Every interactive (`script`) recipe MUST carry the two fields the driver reads
positionally: **`timeout_seconds`** (the per-cell turn budget in seconds — size
it to the scenario; 120 is the floor) and **`settings`** (the agent settings
blob, or `{}` when none). `of cell write` defaults both when omitted and `of
validate` REJECTS a script recipe missing `timeout_seconds` — its absence once
reached a driver as the literal `null` and crashed it. Headless (`prompt`) and
`applicable:false` recipes don't need them.

Then judge the **driver** pillar against the agent's interactive driver:

```bash
source tools/onboarding-factory/scripts/lib/recipe-lint.sh
driver_step_types_from_file replaydata/agents/<agent>/driver-interactive.sh
```

If the recipe needs a step type the driver lacks (`keys`, `reset_session`,
`restart`, `sigkill`, …) → `driver=gap:<primitive>`. This is tooling work, NOT
an observability limit — don't let a driver gap masquerade as `incapable`. The
cell stays a real cell with a real recipe; `record` ports the missing step from
the reference driver before it drives. (First rule out a false gap: an
inline-argument slash command like `/model <id>` is a `slash` step, not a `keys`
gap.)

### 5. Author the spec (expected.jsonl)

Write the machine-checkable spec as JSONL. The first line is the meta object;
subsequent lines are phases:

```jsonl
{"schema_version":1,"notes":"<what this asserts>","observations":{"model":"<id>","cost_nonzero":true,"tokens_nonzero":true,"agent":"<agent>"}}
{"phase":"birth","anchor":"start", ...}
{"phase":"settle","from":"working","to":"ready", ...}
```

(`of cell spec` forces `scenario_id` onto the meta line — you don't write it.)

- **Phases** assert the user-observable arc only: state transitions, distinct
  session counts, parent-links, lifecycle. No internal flags, event kinds,
  reasons, or rule numbers. Anchor the birth by the adapter's session model:
  - **Single-birth adapters** (a stable session_id from launch — e.g.
    claudecode): anchor the FIRST phase to `"start"` UNPINNED so a transient
    `proc-<PID>` presession row can't steal the birth and cascade failures.
  - **Presession adapters** (a transient `proc-<PID>` row reconciles into a real
    session with a DIFFERENT session_id — codex, gemini-cli): model the birth as
    TWO phases and anchor every post-birth phase to the REAL session, never the
    presession row (which never goes `working`):
    ```jsonl
    {"phase":"presession_birth","expected_state":"ready","relative_to":"start"}
    {"phase":"session_birth","expected_state":"ready","relative_to":"presession_birth","new_session":true}
    ... every later phase: "same_session_as":"session_birth" ...
    ```
    Collapsing these into a single birth (so `session_birth` lands on the
    presession proc-row, which never goes `working`) is the miss that verified
    nearly every multi-phase presession cell PARTIAL until `record` re-anchored
    it. Template from the codex sibling spec.
- **Observations** assert the websocket metric vector the verify engine checks —
  exact-match categorical fields (`model`, `agent`), non-zero + tolerance for
  `cost`/`tokens`. This is the widened verify the factory added: a recording is
  verified on token/usage/cost/model, not just lifecycle state.
  **Don't hard-pin a doc-guessed `model`.** Vendor docs lag the binary, and a
  wrong `model` exact-match fails every recording until `record` corrects it
  (assessed `gemini-3-flash-preview`; reality `gemini-3.5-flash`). Pin `model`
  only when you can confirm it from the live surface (the binary's strings, a
  shipped config default); otherwise leave it provisional and let `record` fill
  it from the recording. Never pin a `cost` figure — assert non-zero only.
- For a `daemon=bug` cell, set `known_failing` in the meta and keep the spec
  asserting the CORRECT behavior — never weaken it to match the bug.

### 6. Write both artifacts through the factory

```bash
# metadata.json: the verdict lives in details.assessment; recipe in details.recipe
of cell write --agent <agent> --scenario <scenario> --file /tmp/<agent>-<scenario>.metadata.json
# expected.jsonl: the spec
of cell spec  --agent <agent> --scenario <scenario> --file /tmp/<agent>-<scenario>.expected.jsonl
of validate
```

The metadata.json shape. **`details.assessment` is the verdict of record** — it
MUST carry the three pillar enums + `confidence` alongside the reasoning, because
the matrix reads its routing/disposition straight from there. The `metadata`
overview tier is DERIVED: `of cell write` mirrors the pillars + confidence from
`details.assessment` into it, so you don't hand-write (or risk drifting) the
overview copy — fill `notes`/version fields there and leave the pillars to the
mirror. (`of cell write` also forces `scenario_id`.)

```json
{
  "metadata": {
    "notes": "<one-line excerpt of the verdict>",
    "agent_cli_version": "<x.y.z>", "daemon_version": "<x.y.z+sha>"
  },
  "details": {
    "assessment": {
      "schema_version": 1, "scenario_id": "<scenario>", "agent": "<agent>",
      "agent_supports": "yes", "daemon_capability": "full", "driver_capability": "ready",
      "confidence": 0.8,
      "body": "## Verdict ...markdown reasoning...",
      "caveats": ["..."],
      "sources": [{"kind":"url|file","ref":"...","note":"..."}]
    },
    "recipe": { "timeout_seconds": 120, "settings": {}, "script": [ {"type":"send","text":"..."}, {"type":"wait_turn"}, {"type":"sleep","seconds":10} ] }
  }
}
```

### 7. Surface recording prerequisites — but do NOT commit

If recording this cell needs a human action (auth switch, env var, mock,
unavailable provider) name it — it becomes `prereqs` in your return and the
dispatcher relays it to the human. If the cell is recordable now, `prereqs:
none`.

**Do NOT `git commit`.** You ran in a parallel assess wave, and N subagents
committing the one worktree at once race (scrambled attribution, stranded
resets). Write both artifacts via `of` and return — the **dispatcher** stages
and commits each cell serially after the wave (it knows your cell from the
dispatch). Leaving `replaydata/` dirty for the parent is correct here.

**If the route is `frozen`, you're done after the write** — the metadata
documents *why* the cell is frozen and what would unblock it; no spec phases are
needed beyond the meta. **`driver-gap`** and **`record` / `record-known-failing`**
all keep the recipe + spec so `record` can proceed (the driver-gap cell records
the moment `record` ports the missing step).

## Return contract

Return ONLY this (≤6 lines). Shared semantics + envelope rules live in
[`../return-contract.md`](../return-contract.md):

```
verdict: agent=<v> daemon=<full|bug|incapable|n/a> driver=<ready|gap:*> (confidence <n>)
route: record | record-known-failing | driver-gap | frozen
summary: <one sentence — the load-bearing reason, citing the anchoring event/code for any non-full/non-ready verdict>
wrote: metadata.json + expected.jsonl (via of cell write / of cell spec) — UNCOMMITTED; the dispatcher commits
prereqs: <human action recording needs, or "none">
```

## Anti-patterns

- **Don't write `replaydata/` by hand.** `of cell write` + `of cell spec` are
  the only writers; they validate and force the FK.
- **Don't reach for `bug`/`incapable` for a narrow gap.** Spec met → `daemon`
  stays `full`; use `caveats`.
- **Don't conflate the two observability axes.** A missing driver step is
  `driver=gap:<prim>` (tooling), never `daemon=incapable` (architecture) —
  mislabeling routes the fix to the wrong owner.
- **Don't fabricate sources.** An empty/honest `sources` with low `confidence`
  beats a fake citation that poisons future re-assessments.
- **Don't pin a doc-guessed `model`/`cost`.** Confirm `model` from the live
  surface or leave it provisional for `record` to fill; assert `cost` non-zero,
  never a figure. A doc-derived model string fails every recording's verify.
- **Don't set `confidence` ≥ 0.9 from general knowledge.** That band is for "the
  docs literally say this" / "the source has the exact behavior." `0.7–0.85` is
  the honest band for a thorough multi-source read.
- **Don't run a recording.** That's `record`'s job; this verb is doc + code
  research plus the spec.
