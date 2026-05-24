---
name: ir:onboard-agent/record
description: >
  Per-cell recording (Stage 4). Drives the agent CLI through the
  authored recipe against a live `irrlichd --record` daemon, captures
  `events.jsonl` + `transcript.jsonl`, then archives the previous
  capture and promotes the fresh one. Invoked as
  `/ir:onboard-agent record <agent> <scenario>` (alias for the
  unkeyed form). Spends real API tokens; budget wall-clock minutes
  per cell.
---

# Recording (Stage 4)

Runs the recipe's `script` against a live agent CLI in a tmux
window, captures the daemon's lifecycle events + the agent's
transcript, archives the previous top-level recording with full
provenance, and promotes the new one as the latest.

> **Stage 4 of the cell lifecycle.** Prerequisites:
> - Stage 1 (assess) — [`../assess/SKILL.md`](../assess/SKILL.md).
>   `agent_supports != "no"`, otherwise the pipeline is frozen.
> - Stage 2 (recipe) — [`../recipe/SKILL.md`](../recipe/SKILL.md).
>   `scenarios.json -> scenarios[].by_adapter[<agent>]` must have a
>   `script` array.
> - Stage 3 (spec) — [`../spec/SKILL.md`](../spec/SKILL.md).
>   `expected.jsonl` must exist; `promote-recording.sh` re-runs the
>   validator after the recording and refuses to promote if it
>   fails.
>
> Stage 5 (validation) — [`../validate/SKILL.md`](../validate/SKILL.md)
> — runs automatically at the end of this stage; can also be re-run
> by hand.
>
> End-to-end walkthrough → [`../cell-lifecycle.md`](../cell-lifecycle.md).

## Invocations

```
/ir:onboard-agent record <agent> <scenario>          # isolated daemon
/ir:onboard-agent record --attach <agent> <scenario> # use the user's running daemon
/ir:onboard-agent <agent> <scenario>                 # alias for the first form
```

### Isolated mode (default)

The skill spawns its own `irrlichd --record` on port 7837 with
`IRRLICHT_RECORDINGS_DIR` pointed at a staging dir under
`.build/refresh/<agent>/<scenario>-<TS>/`. The user's primary
daemon (if any) is untouched. Clean teardown on success or failure.

### Attached mode (`--attach`)

Reuses the user's already-running `irrlichd --record` instead of
spawning a fresh one. The dashboard stays connected — the
scenario's session shows up live alongside the user's other work.
Requirements:

- `pgrep -x irrlichd` returns a PID (precheck refuses otherwise).
- The daemon was started with `--record` (else its recordings dir
  is empty and the precheck refuses).
- Optional: `IRRLICHT_RECORDINGS_DIR=<path>` if the daemon's
  recording dir isn't the default `~/.local/share/irrlicht/recordings/`.

After the driver returns, the script sleeps 6 seconds (5 s
periodic flush + 1 s slack) then curates the staged fixture from
the daemon's recording file. The daemon is never signalled; it
keeps observing whatever else the user has open.

## Output

```
replaydata/agents/<agent>/scenarios/<scenario>/
  events.jsonl          # daemon lifecycle events (latest)
  transcript.jsonl      # agent transcript (latest)
  manifest.json         # daemon version, agent CLI version,
                        # recipe hash, expected pass rate, start ts
  recordings/<TS>_irrlichd-<ver>/
    events.jsonl        # previous latest, archived
    transcript.jsonl
    manifest.json       # frozen provenance for the archive
```

The `recordings/` archive accumulates one folder per re-record. The
viewer's recording-history dropdown reads this directory so the
operator can play back any historical capture and re-validate it
against the current spec (drift-detection loop).

## Preconditions

The skill's precheck (`scripts/precheck.sh`) refuses to run if:

- Working tree has uncommitted replaydata changes (re-records
  should be deliberate commits, not snuck into an unrelated
  branch).
- Agent CLI isn't on PATH (`command -v <agent-bin>` fails).
- For `--attach` mode: no running `irrlichd --record` is found.

The operator's job:

- Agent CLI is authenticated. The agent's stderr surfaces auth
  failures; the recording finishes empty in that case.
- For lazy-transcript adapters (claudecode, aider), the recipe's
  `script` already includes a 1-token nudge (recipe author's job).

## Steps (what `run-cell.sh` does)

1. **Precheck.** `scripts/precheck.sh <agent>` — clean tree, CLI on
   PATH, daemon running (attach mode), build dev daemon if needed.
2. **Daemon launch.** Isolated mode only: start
   `core/bin/irrlichd --record` on port 7837 with the staging dir.
3. **Drive.** `scripts/drive-<agent>-interactive.sh <scenario>`
   reads the recipe's `script` and walks each step (`send`,
   `wait_turn`, `sleep`, `interrupt`, `restart`, ...) against the
   agent in tmux. The driver tracks `SESSION_UUIDS[]` across
   `restart`s for multi-variant recordings.
4. **Curate.** `tools/curate-lifecycle-fixture.sh -d <staging>/replaydata/agents/<agent>/scenarios/<scenario>/`
   pulls events for the session UUID(s) out of the daemon's
   recording file and writes them as
   `<staging>/events.jsonl` + `<staging>/transcript.jsonl`.
5. **Daemon shutdown.** Isolated mode only: SIGINT → 6 s →
   SIGTERM → SIGKILL. Attach mode: skipped.
6. **Promote.** `tools/promote-recording.sh <staging> <agent> <scenario>`:
   - Moves current top-level recording into
     `recordings/<old-start-ts>_irrlichd-<old-daemon-ver>/`
     with its own `manifest.json` (frozen provenance + the
     expected-validate pass rate at the time of archiving).
   - Copies staged files into top-level.
   - Writes a new top-level `manifest.json`.
   - Re-runs `expected-validate` against the new recording.
     **Exits non-zero if validation fails**, leaving the new files
     in place so the maintainer can inspect drift before deciding
     to roll back.

## Determinism check (after a fresh recipe authored)

For a newly-authored recipe, re-record twice and diff structurally:

```bash
/ir:onboard-agent record --attach <agent> <scenario>   # capture A
# promote
/ir:onboard-agent record --attach <agent> <scenario>   # capture B
# diff capture B against capture A:
#   - state_transition order (must match)
#   - distinct session_id count (must match)
#   - process_exited count (must match)
```

Structural drift between two consecutive recordings means the recipe
has variance that will bite someone in six months. Tighten the
recipe (more sleep, different step ordering) before committing.

Things that may legitimately differ between runs (don't tighten for
these): wall-clock timestamps, UUIDs, PIDs, token counts, cost,
cache-read counts.

## Rollback

If a promotion's revalidation fails and you want to revert to the
prior latest:

```bash
SCENARIO_DIR=replaydata/agents/<agent>/scenarios/<scenario>
LATEST_ARCHIVE=$(ls -1d $SCENARIO_DIR/recordings/*/ | tail -1)
mv "$LATEST_ARCHIVE"/{events,transcript,manifest}.json[l]* "$SCENARIO_DIR"/
```

## When to re-record

- Daemon version bumped and the new code touched the relevant
  parser / classifier. Re-record to lock in the latest baseline;
  archives still validate the prior daemon.
- Agent CLI shipped a major version that changes transcript shape
  (new line types, new metadata).
- A user reports a regression and the dashboard shows behavior the
  current fixture can't reproduce — re-record to capture it as a
  failure, then file an issue against the daemon (don't update
  expected.jsonl).

## What this mode does NOT do

- It does not author the recipe — that's
  [`../recipe/SKILL.md`](../recipe/SKILL.md). Without a recipe in
  `scenarios.json`, this skill exits with "no recipe for cell."
- It does not author the spec — that's
  [`../spec/SKILL.md`](../spec/SKILL.md). Without `expected.jsonl`,
  promote-recording.sh skips validation and just stamps the
  manifest with `expected_pass_rate: "no spec"`.
- It does not modify `.claude/skills/ir:onboard-agent/agent-scenarios-coverage.json`. The
  matrix rollup is the maintainer's editorial truth.
- It does not read the catalogs into model context — so there is no
  `slice-cell.sh` step here (unlike `recipe`/`assess`/`spec`).
  `run-cell.sh` extracts just this cell's recipe from `scenarios.json`
  with `jq` at the script level; no whole-catalog read happens.
