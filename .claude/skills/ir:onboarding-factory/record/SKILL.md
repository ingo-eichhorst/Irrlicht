---
name: ir:onboarding-factory/record
description: >
  Carry one assessed cell to a committed, verified recording: check
  prerequisites, port any missing driver step, drive the live agent CLI under a
  recording daemon via `of record run`, verify EVERY websocket observation
  (state + model + cost + tokens + agent) via `of record verify`, refresh the
  replay golden, and commit. Backflows a correction into the cell when the live
  recording disagrees with the assessment. Invoked as
  `/ir:onboarding-factory record <agent> <scenario>`.
---

# record

> **You run as a focused subagent with no parent context.** This verb DRIVES A
> LIVE AGENT CLI and SPENDS API TOKENS — auth is set up out-of-band, so run it
> without ceremony (no key checks, no "this will spend money" prompts). It must
> be serialized: only one `record` runs against the live daemon at a time. When
> done, return only the "Return contract" block.

## Preconditions

1. **The cell is assessed and on a recordable route.** Read it:
   ```bash
   of status --agent <agent> --scenario <scenario> --json
   ```
   - route `record` / `record-known-failing` → proceed.
   - route `driver-gap` → port the step first (Step 2), then proceed.
   - route `frozen` → STOP, return `status: frozen` (nothing to record).
2. **Prerequisites met.** `of record prereq-check --agent <agent>` lists the
   human actions a recording needs (auth mode, env vars, a mock, a local model
   server). If one is unmet and you can't satisfy it, STOP and return
   `status: prereq_blocked` naming the exact blocker — never ask the dispatcher.
3. **Clean `replaydata/` tree.** The recording precheck refuses a dirty
   `replaydata/` (re-records must be deliberate commits). If the assessment
   isn't committed yet, that's the dispatcher's ordering bug — return
   `status: infra_fail` with that note.
4. **A recording daemon is up.** Use `--attach` against the user's running
   `irrlichd --record` (the dashboard stays connected; the session shows up
   live). The precheck refuses if no `--record` daemon is found — `infra_fail`.
   A recording daemon must run with `IRRLICHT_PERMISSION_MODE=grant-all` —
   the consent-first gate (#570) otherwise leaves a fresh daemon monitoring
   nothing until its wizard is answered. (run-cell.sh / run-cell-multi.sh
   set it on the daemons they spawn.)

## Steps

### 1. Driver-gap → port the missing step (only if route is `driver-gap`)

Port the primitive named in the cell's `driver=gap:<primitive>` from the
reference driver into the agent's driver — the recipe is sound, only a step type
is missing:

```bash
grep -n '<primitive>)' replaydata/agents/claudecode/driver-interactive.sh \
                       replaydata/agents/codex/driver-interactive.sh
```

Adapt the three seams (tmux input; turn/effect detection — a
`reset_session`/`restart`/`resume` must detect the NEW session id, not the old;
the multi-session contract `session.uuids`/`transcript.paths` for primitives
that mint a new session). Add the primitive to the driver's `DRIVE_ELICITS`
constant so recipe-lint treats it as genuinely produced. Verify + commit the
driver alone:

```bash
bash -n replaydata/agents/<agent>/driver-interactive.sh
source tools/onboarding-factory/scripts/lib/recipe-lint.sh
driver_step_types_from_file replaydata/agents/<agent>/driver-interactive.sh | grep -qx '<primitive>'
git add replaydata/agents/<agent>/driver-interactive.sh
git commit -m "feat(onboard): teach <agent> driver the <primitive> step type"
```

If the primitive has no claudecode/codex reference, it's a NEW grammar element —
STOP and return `status: needs_design`. Don't invent one.

### 2. Record (live capture)

```bash
of record run --attach --agent <agent> --scenario <scenario>
```

`of record run` resolves the driver + orchestration script, prints the
prerequisites, and drives the agent under the recording daemon: it walks the
recipe in tmux and captures the daemon's `events.jsonl` + the agent's transcript
into a STAGING dir (`.build/refresh/<agent>/<folder>-<ts>/`). It does NOT touch
`replaydata/` — promotion is the next step. (`--dry-run` prints the resolved plan
without driving — useful to confirm wiring.)

Then promote the staged capture into the cell's `recordings/<name>/`:

```bash
tools/promote-recording.sh <staging-dir> <agent> <folder>
```

This copies `events.jsonl` + the transcript + a `manifest.json` into a new
`replaydata/agents/<agent>/scenarios/<folder>/recordings/<name>/`. It does NOT
write any artifacts cache into `metadata.json`: the on-disk `recordings/<name>/`
tree IS the record (the single source of truth). The replay golden is added by
Step 5; nothing else needs wiring.

**Retry exactly once** on a `timeout` / `transcript_missing` outcome (often a
lazy-transcript nudge or trailing-sleep timing issue). On a second failure, or
on a classified `cli_not_found` / `cli_too_old` / `auth_failed` /
daemon-not-running, return `status: infra_fail` (don't loop, don't mark the cell
un-doable — the environment is the problem). When unsure of the failure class,
classify the staging dir:

```bash
bash tools/onboarding-factory/scripts/lib/classify-failure.sh <staging-dir>
```

### 3. Verify EVERY observation

```bash
of record verify --agent <agent> --scenario <scenario>
```

This runs the go-test-style verify engine: the state-phase validation AND the
observation vector — exact-match `model`, non-zero + tolerance
`cost`/`tokens`, with a soft-diff of the full vector against the prior committed
recording (flagged, not failed, on live jitter). Report the per-field result in
`observations`. Hard spec-phase failures are real: a sub-100% pass that is NOT
`known_failing` still commits (the recording is real captured data and
`replay-fixtures.sh` should surface the drift) but the `notes` MUST say
"VALIDATION DRIFT — needs editorial review." **Never rebase `expected.jsonl` to
make a failing verify pass** — resolving real drift is a separate maintainer
task.

Things that legitimately differ run-to-run (don't tighten for these):
timestamps, UUIDs, PIDs, token counts, cost, cache-read counts. Structural
drift (state-transition order, distinct session count, `process_exited` count)
between two consecutive recordings means the recipe has variance — tighten it
(more sleep, different ordering) before committing.

### 4. Backflow — correct the cell if the recording disagrees

If the LIVE recording refutes the doc-based assessment (e.g. assessed
`daemon=full` but the transcript/store proved the signal isn't emitted →
`incapable`; or it's atomic so streaming never happens), correct the cell IN THE
SAME COMMIT — this is the backflow loop, not a cue:

```bash
of cell write --agent <agent> --scenario <scenario> --file /tmp/<agent>-<scenario>.corrected-metadata.json
```

Update the affected pillar, add a caveat citing the recording that proved it,
and set `observability_correction` in your return. For a `daemon=bug` cell, do
**not** run `gh issue create` — outward-facing writes are denied in your
subagent permission context, so it silently degrades to a cell note and files
nothing. Instead, write the issue body to a temp file and hand it back for the
**dispatcher** to file (with the user's consent):

```bash
cat > /tmp/<agent>-<scenario>.issue.md <<'EOF'
<cited events.jsonl evidence + what the spec requires>
EOF
```

Return it as the `issue:` payload (the path + a one-line title). Keep
`known_failing` set in the spec meta with the bug behavior cited in `notes`; the
issue number is wired into the cell by a later touch, once the dispatcher has
filed it.

### 5. Refresh the replay golden (mandatory)

A fresh recording without its `transcript.jsonl.replay.json.golden` leaves
`go test ./core/...` (the byte-identity replay test) red. Regenerate this
cell's golden(s) only — never a blanket `UPDATE_REPLAY_GOLDENS=1` across the
tree (that commits other agents' pre-existing drift):

```bash
tools/onboarding-factory/scripts/refresh-golden.sh <agent> <scenario>
```

It's idempotent — a `--re-record` that reproduced byte-identical output reports
"no golden change."

### 6. Commit the recording (mandatory before returning)

```bash
git add replaydata/agents/<agent>/scenarios/<id>_<scenario>/
git commit -m "feat(onboard): record <agent>/<scenario> (<pass_rate>)"
git rev-parse --short HEAD
```

**Always commit before returning** — a dirty `replaydata/` tree makes the next
cell's recording precheck refuse. `of validate` should pass after the commit; it
now also gates recording completeness — the newest recording must carry
`events.jsonl`, `manifest.json`, a transcript, and (for a jsonl transcript) its
`transcript.jsonl.replay.json.golden`.

> End commit messages with the trailer
> `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`.

## Return contract

Return ONLY this (≤7 lines). Shared semantics + envelope rules live in
[`../return-contract.md`](../return-contract.md):

```
status: pass | infra_fail | prereq_blocked | needs_design | frozen
commit_sha: <short sha>            # the recording commit (or driver commit), "n/a" otherwise
pass_rate: <N/M phases>            # "n/a" for non-pass statuses
observations: model=<ok|MISMATCH> cost=<ok|zero> tokens=<ok|zero>
observability_correction: <none | the live recording overrode the assess verdict — e.g. assessed daemon=full but the store proved no trace (→ incapable/bug)>
issue: <none | /tmp/<agent>-<scenario>.issue.md title="<one-line title>">   # daemon=bug only; the dispatcher files it
notes: <one or two sentences — drift flag, retry count, infra/prereq reason>
```

## Anti-patterns

- **Don't write `replaydata/` by hand.** `of record run` stages;
  `promote-recording.sh` copies the staged capture into `recordings/<name>/`;
  `refresh-golden.sh` writes the golden; `of cell write` does the backflow
  correction; the driver is a script under `replaydata/agents/<agent>/`. No
  `jq -i`, no hand-edited recordings or metadata. The on-disk recording is the
  single source of truth — there is no artifacts cache to maintain.
- **Don't retry more than once**, and **don't retry a driver gap** — a missing
  step won't appear on a re-run; port it (Step 1) or return.
- **Don't rebase `expected.jsonl`** to make a failing verify pass — flag the
  drift, don't paper over it.
- **Don't run `gh issue create`** — outward-facing writes are denied in your
  context, so it files nothing. Return the `issue:` payload and let the
  dispatcher file it with the user's consent.
- **Don't run an isolated daemon while production `irrlichd` is up** — use
  `--attach`.
- **Don't skip the golden refresh**, and **don't blanket-regenerate** goldens —
  refresh only this cell's.
- **Don't return without committing** — it breaks the next cell in a serialized
  sweep.
