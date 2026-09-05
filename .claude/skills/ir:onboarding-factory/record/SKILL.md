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
4. **A recording daemon is up — attached OR coexisting.** Which one is a
   decision you make per run, not a default:

   - **Attach** (`--attach`, against the user's running `irrlichd --record`)
     when you are **re-recording an adapter the running daemon already ships**,
     *and* that daemon is genuinely in `--record` mode. The dashboard stays
     connected and the session shows up live.
   - **Coexist** — an isolated daemon **built from this branch**, on its own
     `IRRLICHT_HOME` and port — when either half of that is false:

     ```bash
     IRRLICHT_ONBOARD_HOME=/tmp/irr-onb \
     IRRLICHT_ONBOARD_BIND_ADDR=127.0.0.1:7838 \
     IRRLICHT_PERMISSION_MODE=grant-all \
       of record run --agent <agent> --scenario <scenario>
     ```

     Setting `IRRLICHT_ONBOARD_HOME` is the one knob that selects coexist mode;
     the bind addr defaults to `127.0.0.1:7838` and the precheck refuses 7837
     (it would clash with production). Pick another free port if 7838 is taken.

     **Hook delivery reaching the coexisting daemon is not one adapter-neutral
     guarantee** (#1754). URL-delivery adapters (claudecode, codex, copilot)
     bake the daemon address into the installed entry at install time, so they
     always reach it. Beacon-delivery adapters — every adapter importing
     `core/pkg/hookbeacon`; as of this writing antigravity, gemini-cli, hermes,
     kiro-cli, opencode, pi and mistral-vibe, but that list is not the source
     of truth and will drift — `righome.BeaconAdapters` (derived from the
     import graph) is — carry NO address in the entry at all; `irrlichd
     hook-post` resolves the daemon from its OWN process environment at fire
     time, a child of the CLI's tmux pane. `of record run` / `run-cell.sh`
     already export and forward `IRRLICHT_BIND_ADDR` into that pane for you
     (`TestEveryBeaconAdapterDriverPassesTheDaemonAddress` in
     `tools/onboarding-factory/internal/righome` enforces it), so this needs no
     action through the normal path — but it is why driving a beacon-delivery
     adapter's CLI any other way silently posts every hook at production
     instead, and the resulting recording looks complete and healthy with zero
     hook events in it (#1735).

   **Onboarding a NEW adapter is always the coexist case**, by construction: the
   running `irrlichd` is an installed release whose binary has no such adapter
   compiled in, so it observes nothing no matter how healthy it looks. Coexist
   builds the daemon from the branch, which is the only build that contains the
   adapter you are recording. Both onboarding runs to date hit this from
   opposite directions — copilot's production daemon had no copilot adapter;
   hermes' production daemon was not in `--record` mode at all (`lsof` held no
   recordings file, and the newest one predated the run by a day).

   A recording daemon must run with `IRRLICHT_PERMISSION_MODE=grant-all` on
   **either** path — the consent-first gate (#570) otherwise leaves a fresh
   daemon monitoring nothing until its wizard is answered. (run-cell.sh /
   run-cell-multi.sh set it on the daemons they spawn.)

   **On the attach path you must set `IRRLICHT_ALLOW_SHARED_CONFIG_WRITES=1`
   too, and back the files up yourself first.** Since #1449 a grant-all daemon
   refuses by default to write the shared agent configs it manages
   (`~/.claude/settings.json`, `~/.codex/hooks.json`, …), because a dev daemon
   on a throwaway port doing that is what left a real machine's hook channel
   silently dead. The spawn path needs nothing from you — `spawn_record_daemon`
   snapshots every managed file and restores it on exit, which is what earns it
   the override. Attach reuses a daemon **you** started, so the snapshot is
   yours to make: run `irrlichd --print-managed-files`, copy those files
   somewhere, start the daemon with both variables, and put them back after.
   Skipping it does not fail loudly at the daemon — the permission still reads
   `granted` — so `run-cell.sh` checks the daemon's `unapplied_grants` before
   driving anything and refuses rather than recording a fixture in which the
   adapter merely looks incapable of reporting state.

## Steps

### 1. Driver-gap → port the missing step (only if route is `driver-gap`)

**First, read the assessment's own diagnosis — the analysis is already paid
for.** `of status --json` gives you only the gap's *name*
(`driver=gap:<primitive>`); the reasoning lives in the assessment **body**,
which routinely names the exact defect and the exact precedent to copy:

```bash
of status --agent <agent> --scenario <scenario> --json \
  | jq -r '.details.assessment.body, (.details.assessment.caveats // [])[]'
```

Quote what it says about the gap in your work before porting anything. (Real
case: `1-6`'s assessment had already pinned the rewind bug to `turn_count`
driving `EXPECTED_TURNS` backwards *and* named the `interrupt` step as the
one-line re-baseline precedent. The record phase never read it and burned a
full live run rediscovering the same thing days later.)

Then port the primitive named in `driver=gap:<primitive>` from the reference
driver into the agent's driver — the recipe is sound, only a step type is
missing:

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
# Re-recording a shipped adapter against a running --record daemon:
of record run --attach --agent <agent> --scenario <scenario>

# Onboarding a new adapter, or production isn't recording (Precondition 4):
IRRLICHT_ONBOARD_HOME=/tmp/irr-onb IRRLICHT_ONBOARD_BIND_ADDR=127.0.0.1:7838 \
IRRLICHT_PERMISSION_MODE=grant-all \
  of record run --agent <agent> --scenario <scenario>
```

`of record run` resolves the driver + orchestration script, prints the
prerequisites, and drives the agent under the recording daemon: it walks the
recipe in tmux and captures the daemon's `events.jsonl` + the agent's transcript
into a STAGING dir (`.build/refresh/<agent>/<folder>-<ts>/`). It does NOT touch
`replaydata/` — promotion is the next step. (`--dry-run` prints the resolved plan
without driving — useful to confirm wiring.)

**Before promoting, check the run actually FINISHED — on every outcome,
including `ok`.** `driver.exit-reason` is the driver's claim about itself, not
evidence about the recording. Of three broken copilot runs, **two reported `ok`
with a silently truncated recording**, which is worse than a timeout because
`ok` invites promotion: `2-6` was torn down 4s into its final turn (its
`events.jsonl` ends on `debounce_coalesced` with no final transition at all) and
`3-5` was killed 14s into three 25s children. Both were caught only because a
human read the events by hand.

```bash
jq -c '.driver_exit_reason, .completeness' <staging-dir>/run-manifest.json
# or re-run it directly:
bash tools/onboarding-factory/scripts/lib/completeness-check.sh <staging-dir>
```

A `suspect` verdict is **advisory, not fatal** — roughly 7% of committed
recordings legitimately end unsettled, so read the reasons and decide:

- The cell's scenario genuinely ends unsettled → promote, and say so in `notes`.
- Anything else → **do not promote**. Diagnose before re-running.

Scenarios *defined* to end unsettled are declared once, in the committed
catalog, and are waived automatically:

```bash
jq -r '.meta.ends_unsettled | join(", ")' replaydata/agents/scenarios.json
# session-end, token-quota-exhausted, user-esc-interrupt, subagent-orphan-cleanup
```

Add a scenario there only when its **definition** requires an unsettled ending
(the process is killed, the turn is interrupted, the quota dies, the orphan is
abandoned) — **not** because recordings of it have ended unsettled before.
Twelve scenarios currently trip the check somewhere in the corpus; waiving all
of them would turn it into exactly the green-and-vacuous pass this check exists
to prevent. For a genuine one-off, drop
`{"ends_unsettled": true}` into `<staging>/completeness-waiver.json` instead.

Also confirm by hand what the check deliberately does not mechanize: that the
transcript's turn count matches the recipe's `send` count. A short transcript
with a settled tail is still a truncated run.

**On a `timeout`, diff the transcript against the driver's turn accounting
BEFORE retrying.** A systematic driver bug will not fix itself on a re-run, and
each retry costs real credits — turn accounting is the single most defect-prone
seam in the driver (four separate defects in one run; see
`replaydata/_lib/drive/turn-count_test.sh` for the shapes).

Then promote the staged capture into the cell's `recordings/<name>/`:

```bash
tools/promote-recording.sh <staging-dir> <agent> <folder>
```

This copies `events.jsonl` + the transcript + a `manifest.json` into a new
`replaydata/agents/<agent>/scenarios/<folder>/recordings/<name>/`. It does NOT
write any artifacts cache into `metadata.json`: the on-disk `recordings/<name>/`
tree IS the record (the single source of truth). The replay golden is added by
Step 5; nothing else needs wiring.

**If `<agent>` declares a hooks permission and this recording carries no
`hook_received` event, promote-recording.sh prompts rather than promoting
silently** (#1754) — the exact failure mode #1735 took three attempts to
diagnose. When the scenario genuinely produces no hook (confirm against `of
coverage --hooks` if unsure), that's a real "yes, intended" — since you run
non-interactively, answer it with `IRRLICHT_PROMOTE_HOOKFREE_OK=1
tools/promote-recording.sh <staging-dir> <agent> <folder>` rather than
retrying blind against an unanswerable prompt. Anything else — a scenario that
SHOULD have produced a hook — is the bug this check exists to catch: go back
to Step 2's diagnosis rather than overriding it.

**Retry exactly once** on a `timeout` / `transcript_missing` outcome (often a
lazy-transcript nudge or trailing-sleep timing issue). On a second failure, or
on a classified `cli_not_found` / `cli_too_old` / `auth_failed` /
daemon-not-running, return `status: infra_fail` (don't loop, don't mark the cell
un-doable — the environment is the problem).

**Never retry `driver_session_leaked` blind (#1825).** The driver returned while
its agent was still running, so a retry starts a SECOND live agent in the same
workspace beside the first. Both write into the recording, and the fixture that
comes out is of two interleaved sessions rather than the scenario. Kill the
survivor the manifest names — `.tmux_teardown_detail` carries the session name,
so `tmux kill-session -t <name>` — then retry at most once. A leak that recurs
is a driver bug: go to that agent's `driver-interactive.sh` teardown, not to a
third run.

**`driver_teardown_unverifiable` is not evidence the run was clean** — it means
the check could not look at all (no `tmux` binary, an unreadable session list).
Return `status: infra_fail` and fix the host rather than promoting the staging
dir. `daemon_not_ready` and `replay_failed` are `infra_fail` too.

**`driver_pid_unrecorded` means the driver never started (#1828)** — the pid
wrapper died before it could exec the driver, and an unwritable `$STAGING` is
what makes that write fail. Nothing of this run is holding a tmux session, so
unlike `driver_session_leaked` there is no survivor to kill first. Check that
the staging dir is writable, then retry. Do not read it as a teardown verdict:
tmux was never asked anything.

When unsure of the failure class, classify the staging dir:

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
"VALIDATION DRIFT — needs editorial review."

**Editing `expected.jsonl` splits into two cases — one required, one forbidden.**
Decide which you are in *before* you touch the file:

- **Spec correction — required.** The daemon's behaviour changed, or this live
  recording refuted the assessment, so the spec is **stale**, not drifting.
  Correct it, and write the cause into the spec meta's `notes`: the issue or PR
  number plus the commit **subject**. Example: `"birth is now working, not
  ready — corrected per #1256, 'classify every new session against its own
  metrics'"`.
- **Spec rebase — still forbidden.** Editing phases to turn a red verify green
  with **no cited cause**. That is papering over drift, and resolving real drift
  is a separate maintainer task.

The test between them is whether you can name what changed *outside* the spec.
If you can't, you're rebasing. (Real case: `ae3257e6` legitimately changed when
a session is born `working` vs `ready`, making 19 pre-existing copilot specs
stale. The old blanket rule forbade the only correct action and offered no
alternative.)

**Cite the issue/PR + subject, never a bare branch SHA.** A pre-PR rebase
rewrites it: those same 19 specs cite `3cde4f8d`, which is on no branch and
will not resolve in a fresh clone. Add a SHA only after merge, if at all.

Things that legitimately differ run-to-run (don't tighten for these):
timestamps, UUIDs, PIDs, token counts, cost, cache-read counts. Structural
drift (state-transition order, distinct session count, `process_exited` count)
between two consecutive recordings means the recipe has variance — tighten it
(more sleep, different ordering) before committing.

### 4. Backflow — correct the cell if the recording disagrees

**When a cell looks wrong, read the RAW recording before blaming the daemon or
the adapter.** The curated `events.jsonl` is a *derived* artifact; the raw
daemon capture in the staging dir is what the daemon actually saw:

```bash
jq -c 'select(.type)' <staging>/recordings/*.jsonl | less   # raw daemon capture
```

Twice in one run the daemon recorded a cell perfectly and **curation** dropped
half of it — `1-5_session-reset` lost the pre-reset session because a reset
reuses its slot and the retired transcript never reached `session.uuids`, and
`4-2_multiple-agents-same-workspace` recorded five of the *user's own* sessions
because `run-cell-multi.sh` never exported `COPILOT_HOME`. Both looked like
adapter or daemon failures and were neither. Note that #1214 unified the two
rigs' daemon lifecycle but **not** their per-adapter env, which is how `4-2`
slipped — a multi-agent cell is worth this check specifically.

If the LIVE recording refutes the doc-based assessment (e.g. assessed
`daemon=full` but the transcript/store proved the signal isn't emitted →
`incapable`; or it's atomic so streaming never happens), correct the cell IN THE
SAME COMMIT — this is the backflow loop, not a cue. If the newly-discovered
`bug` looks like state going stale/missing right after a presession
reconciles into its real session id, check
[`../../../../tools/onboarding-factory/docs/KNOWN_SEAMS.md`](../../../../tools/onboarding-factory/docs/KNOWN_SEAMS.md)
before treating it as brand new:

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

It is also safe to run in a **loop** over several cells since #1333: it
snapshots which goldens were already dirty before regenerating and only undoes
what that invocation itself caused. It used to revert every out-of-scope golden
unconditionally, so each iteration silently discarded the previous cell's work —
which made commit-per-cell (Step 6) load-bearing for a reason the skill never
gave. Commit-per-cell is still the rule, for the reason Step 6 actually states;
it just isn't the only thing standing between a sweep and data loss any more.

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
- **Don't accept a guard that proves PRESENCE where it means FRESHNESS.** "A
  recording file exists" is not "a recording exists from this run"; "the
  prerequisites could be read" is not "the prerequisites are met"; "the
  validator ran" is not "the validator ran before the write". Three separate
  guards in this rig shipped the weaker check, and every one of them reported
  green while handing back another run's data. When you add or lean on a guard,
  say out loud which of the two it actually proves.
- **Don't rebase `expected.jsonl`** to make a failing verify pass — flag the
  drift, don't paper over it. Correcting a **stale** spec with a cited cause is
  a different act, and it is required rather than forbidden (Step 3).
- **Don't cite a bare branch SHA** in spec `notes` — a pre-PR rebase rewrites
  it. Cite the issue/PR number and the commit subject.
- **Don't run `gh issue create`** — outward-facing writes are denied in your
  context, so it files nothing. Return the `issue:` payload and let the
  dispatcher file it with the user's consent.
- **Don't run an isolated daemon on production's port while production
  `irrlichd` is up** — either `--attach`, or coexist on a separate
  `IRRLICHT_ONBOARD_HOME` + non-7837 port (Precondition 4). Coexisting is the
  *right* answer when onboarding a new adapter: the running release has no such
  adapter compiled in and would observe nothing.
- **Don't skip the golden refresh**, and **don't blanket-regenerate** goldens —
  refresh only this cell's.
- **Don't return without committing** — it breaks the next cell in a serialized
  sweep.
