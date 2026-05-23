---
name: ir:onboard-agent/implement
description: >
  Agent #3 of the ir:onboard-agent pipeline. Takes one (agent, scenario)
  cell from an assessed `applicable: yes` verdict all the way to a
  committed recording: authors the spec + per-adapter recipe, drives the
  live agent CLI under a recording daemon, validates against the spec,
  promotes the recording, and commits — leaving no dirty tree. Bundles
  the recipe / spec / record / validate building blocks behind one
  contract with retry, driver-gap, and degrade-to-applicable:false
  handling. Invoked as `/ir:onboard-agent implement <agent> <scenario>
  [--re-record]`.
---

# implement (Agent #3)

> **You are running as a focused subagent with no parent context.**
> Everything you need is here or in the building-block docs named below.
> Don't ask the dispatcher anything. This stage DRIVES A LIVE AGENT CLI
> and SPENDS API TOKENS — but auth is set up out-of-band, so run it
> without ceremony (no key checks, no "this will spend money" prompts).
> When done, return only the "Return contract" block.

## What this does

Carries one cell from `applicable: yes` to a committed recording. It
bundles four building-block stages — you READ their docs for mechanics
and follow them here:

- Stage 2 recipe → [`../recipe/SKILL.md`](../recipe/SKILL.md)
- Stage 3 spec → [`../spec/SKILL.md`](../spec/SKILL.md)
- Stage 4 record → [`../record/SKILL.md`](../record/SKILL.md)
- Stage 5 validate → [`../validate/SKILL.md`](../validate/SKILL.md)
- End-to-end map → [`../cell-lifecycle.md`](../cell-lifecycle.md)

## Inputs

- `<agent>` — adapter slug (`claudecode`, `codex`, `pi`, `aider`,
  `opencode`).
- `<scenario>` — scenario id (matches `scenarios[].name` /
  `coverage_id`).
- `--re-record` (optional) — skip recipe + spec authoring; reuse the
  existing `by_adapter.<agent>` recipe and committed `expected.jsonl`,
  and just capture a fresh recording. Use after a daemon change.

## Preconditions

1. **An assessment exists.**
   `replaydata/agents/<agent>/scenarios/<scenario>/assessment.json` must
   be present. If it isn't, spawn `assess` first (`Agent` tool,
   `subagent_type: general-purpose`, prompt = "Read and execute
   `.claude/skills/ir:onboard-agent/assess/SKILL.md` for agent=<agent>
   scenario=<scenario>; return your summary."). Wait for it, then read
   the written `assessment.json`.
2. **The verdict is actionable.** Refuse unless `assess`'s `applicable`
   is `yes` (`agent_supports ∈ {yes, partial}` AND `irrlicht_observes ∈
   {yes, partial}`). For `no`/`n/a`, STOP — return status
   `applicable_false`, change nothing, note the frozen reason from the
   assessment body. There is no recipe or recording for a frozen cell.
3. **The daemon is recording.** This stage uses `--attach` against the
   user's running `irrlichd --record` (the dashboard stays connected and
   the scenario shows up live). `pgrep -x irrlichd` must return a PID and
   its recordings dir must be non-empty, else `run-cell.sh`'s precheck
   refuses — that's an `infra_fail`, not a cell problem.
4. **`--re-record` only:** `by_adapter.<agent>` (with `script` or
   `prompt`) AND `expected.jsonl` must already exist. If either is
   missing, return `infra_fail` ("nothing to re-record; run without
   --re-record first").

## Full-mode pipeline

### 1. Author the spec FIRST (Stage 3)

Follow [`../spec/SKILL.md`](../spec/SKILL.md) to write
`replaydata/agents/<agent>/scenarios/<scenario>/expected.jsonl`.
Spec-before-recipe is deliberate: the recipe's plain-English `verify`
items map onto concrete spec phases, so the spec must exist first.

### 2. Author the recipe (Stage 2)

Follow [`../recipe/SKILL.md`](../recipe/SKILL.md) to write
`by_adapter.<agent>` into
`.claude/skills/ir:onboard-agent/scenarios.json`. **Template from
claudecode's recipe for the same scenario** when one exists — copy its
shape and adapt the step grammar / quirks to `<agent>`. Validate JSON:
`jq '.' .claude/skills/ir:onboard-agent/scenarios.json > /dev/null`.

### 3. Driver-gap pre-flight (before any recording)

Compare the recipe's step types to what the agent's interactive driver
implements:

```bash
SK=.claude/skills/ir:onboard-agent
# step types the recipe needs:
jq -r '.scenarios[] | select(.name=="<scenario>") | .by_adapter["<agent>"].script[]?.type' \
  $SK/scenarios.json | sort -u
# step types the driver handles (case labels):
grep -oE '^\s*(send|slash|wait_turn|sleep|interrupt|keys|restart|resume|sigkill|exit_clean|reset_session)\)' \
  $SK/scripts/drive-<agent>-interactive.sh | tr -d ' )' | sort -u
```

If any needed step type is **not** in the driver's set →
**`driver_gap`**: this is a developer task (extend the driver), out of
scope here. Set `by_adapter.<agent> = {"applicable": false, "notes":
"<scope_note naming the missing step type, e.g. 'aider driver lacks
exit_clean'>"}`, commit recipe(applicable:false) + spec + assessment,
and return `driver_gap`. **Do NOT record, and do NOT retry against a
known-missing primitive.** (Headless `prompt` cells have no step types
and can't hit this.)

### 4. Commit the authored artifacts

`expected.jsonl` lives under `replaydata/agents/`, and `precheck.sh`
refuses to record with a dirty `replaydata/` tree. So commit the spec +
recipe now — this is also what keeps the tree clean per the contract:

```bash
git add .claude/skills/ir:onboard-agent/scenarios.json \
        replaydata/agents/<agent>/scenarios/<scenario>/expected.jsonl \
        replaydata/agents/<agent>/scenarios/<scenario>/assessment.json
git commit -m "feat(onboard): author <agent>/<scenario> recipe+spec"
```

(For `--re-record` mode, skip steps 1–4 entirely — the recipe + spec are
already committed and the tree is clean.)

### 5. Record (Stage 4)

```bash
SK=.claude/skills/ir:onboard-agent
$SK/scripts/run-cell.sh --attach <agent> <scenario>
# capture the staging dir from the final "manifest: <path>" line:
#   STAGING = dirname of that manifest path
```

Read `<STAGING>/run-manifest.json`. Classify the outcome — when in
doubt run `$SK/scripts/lib/classify-failure.sh <STAGING>` (codes:
`cli_not_found`, `cli_too_old`, `auth_failed`, `daemon_dirty`,
`working_tree_dirty`, `transcript_missing`, `timeout`, `unknown`):

| Outcome | Action |
|---|---|
| manifest `verdict: STAGED` (success) | proceed to step 6 |
| `timeout` / `transcript_missing`, **first** time | **retry once** — re-run the same `run-cell.sh --attach`. Often a lazy-transcript nudge or trailing-sleep timing issue. |
| `timeout` / `transcript_missing`, **second** time | degrade → **`applicable_false`** (see below) |
| `cli_not_found` / `cli_too_old` / `auth_failed` / daemon-not-running | **`infra_fail`** — environment problem, not a cell verdict. Don't retry, don't mark applicable_false. Tree is already clean (spec+recipe committed). Return. |

**Retry exactly once.** Never loop a third attempt.

**Degrade to `applicable_false`:** the recipe can't reliably elicit the
behavior. Set `by_adapter.<agent>.applicable = false` and add `notes`
with a `scope_note` explaining what failed (e.g. "two consecutive
drive timeouts — agent never reaches the waiting episode the spec
asserts"). Commit recipe(applicable:false) — the spec stays as the
documented target. Return `applicable_false`.

### 6. Promote + validate (Stages 4→5)

```bash
tools/promote-recording.sh <STAGING> <agent> <scenario>
```

This archives the previous recording, copies the staged
`events.jsonl` + `transcript.jsonl` + `manifest.json` into the scenario
root, and **re-runs `expected-validate`** (Stage 5). It exits non-zero
if validation fails but **leaves the new files in place**. Read the
manifest's `expected_pass_rate` — that is the authoritative
`pass_rate` for your return.

- `pass_rate == 100%` (or all failures are `known_failing`) → status
  `pass`.
- `pass_rate < 100%` and not `known_failing` → still commit (the
  recording is real captured data and `replay-fixtures.sh` should flag
  the drift), status `pass`, but the `notes` MUST say **"VALIDATION
  DRIFT — N/M phases; needs editorial review (do not assume green)."**
  Do NOT rebase `expected.jsonl` to make it pass — that's the trap the
  whole pipeline avoids; resolving real drift is a separate maintainer
  task.

For `--re-record`, also sanity-check structural determinism vs the
archived previous recording (per [`../record/SKILL.md`](../record/SKILL.md)):
state_transition order, distinct session_id count, and `process_exited`
count must match. UUIDs / PIDs / timestamps / token counts may differ.
Note any structural change in the return.

### 6b. Refresh the replay byte-identity golden (mandatory)

`promote-recording.sh` writes the recording but NOT the
`transcript.jsonl.replay.json.golden` that `TestFixtureReplayByteIdentity`
(core/cmd/replay) pins — so without this step a fresh recording leaves
`go test ./core/...` red. Regenerate this scenario's golden(s):

```bash
SK=.claude/skills/ir:onboard-agent
$SK/scripts/refresh-golden.sh <agent> <scenario>
```

This regenerates goldens, then discards every golden change that isn't
under `replaydata/agents/<agent>/scenarios/<scenario>/` — so it covers the
new top-level recording AND any archived `recordings/<ts>/` transcript,
while leaving other adapters' pre-existing golden drift untouched (don't
mask it; that's a separate maintainer task). It's idempotent — on a
`--re-record` that reproduced byte-identical output it reports "no golden
change". Do NOT hand-edit goldens or run a bare `UPDATE_REPLAY_GOLDENS=1`
across the whole tree (that commits other adapters' drift).

### 7. Commit the recording (mandatory before returning)

```bash
# The scenario dir now includes the recording AND its refreshed golden(s).
git add replaydata/agents/<agent>/scenarios/<scenario>/
git commit -m "feat(onboard): record <agent>/<scenario> (<pass_rate>)"
git rev-parse --short HEAD   # → commit_sha for the return
```

**Always commit before returning.** A batched run (the dispatcher
calling `implement` across many cells) must never leave a dirty tree
under `replaydata/agents/` — the next cell's `precheck.sh` would refuse.
If you somehow reach the end with uncommitted changes you can't justify,
commit them with an explanatory message rather than leaving them
dangling.

> Repo commit convention: end commit messages with the trailer
> `Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>`.

## Return contract

Return ONLY this (≤6 lines), no transcripts:

```
status: pass | applicable_false | driver_gap | infra_fail
commit_sha: <short sha>            # the recording commit (or the recipe/spec/applicable_false commit)
pass_rate: <N/M phases>            # "n/a" for non-pass statuses
agent: <agent>   scenario: <scenario>   mode: full | re-record
notes: <one or two sentences — drift flag, scope_note, retry count, or infra reason>
```

Status meanings:

- **`pass`** — recording captured, promoted, and committed. `pass_rate`
  is the truth; a sub-100% rate with status `pass` means
  committed-but-drifting (flagged in notes).
- **`applicable_false`** — recipe can't elicit the behavior after one
  retry, or the cell was assessed `no`/`n/a`. `by_adapter.<agent>` marked
  `applicable: false` with a `scope_note`; recipe + spec + assessment
  committed; no recording.
- **`driver_gap`** — the recipe needs a step type the agent's driver
  doesn't implement. Recipe marked `applicable: false`; committed; no
  recording; no retry.
- **`infra_fail`** — environment problem (CLI missing/old, auth, no
  recording daemon). Nothing about the cell verdict changed; tree left
  clean.

## Anti-patterns

- **Don't retry more than once.** One retry on timeout/transcript-missing,
  then degrade. Never loop.
- **Don't retry a driver gap.** A missing step type won't appear on a
  re-run. Detect it in the pre-flight and return `driver_gap` immediately.
- **Don't record on a dirty `replaydata/` tree.** Commit spec + recipe
  first (step 4) — `precheck.sh` refuses otherwise.
- **Don't rebase `expected.jsonl` to make a failing validation pass.**
  A real drift is a signal to commit-and-flag, not to paper over.
- **Don't author a recipe for an `applicable: no`/`n/a` cell.** Refuse at
  the precondition check.
- **Don't return without committing.** A dirty tree breaks the next
  cell in a batch.
- **Don't write the coverage matrix.** `agent-scenarios-coverage.json` is
  the maintainer's editorial truth; surface drift in `notes`, don't edit it.
- **Don't skip the golden refresh (step 6b), and don't blind-regenerate.**
  Always run `scripts/refresh-golden.sh <agent> <scenario>` after promote —
  a fresh recording without its golden leaves `go test ./core/...` red. But
  never run a bare `UPDATE_REPLAY_GOLDENS=1` across the whole tree or
  hand-edit goldens: that commits other adapters' pre-existing drift instead
  of leaving it for its own maintainer task.
