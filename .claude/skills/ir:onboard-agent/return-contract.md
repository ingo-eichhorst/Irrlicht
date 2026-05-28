# Subagent return contract (shared)

Every onboarding subagent (`scenario-create`, `assess`, `implement`,
`extend-driver`) returns ONE short block to the dispatcher — nothing else. The
dispatcher collects these into a table and never re-reads the per-cell tool
output, so the block is the whole interface. This doc defines the SHARED rules
and the cross-cutting fields ONCE (#508 #5); each verb's own `## Return
contract` section lists its exact field set and points here for shared-field
semantics.

## Envelope rules (all verbs)

- **Return ONLY the block.** No transcripts, no tool logs, no narration.
- **Stay within the line budget** stated in the verb's section (≤5–7 lines).
- **Never ask the dispatcher a question.** A subagent runs to a terminal
  outcome and reports it; if blocked, it returns the blocking status
  (`infra_fail` / `needs_design` / `needs-info`) with the concrete blocker in
  `notes`, not a question.
- **First line is the outcome key**: a `status:` / `route:` / `verdict:` /
  `scenario_id:` line the dispatcher keys on to decide the next step.

## Cross-cutting fields (defined here, used by more than one verb)

These mean the same thing wherever they appear — do not redefine them per verb:

- **`commit_sha`** — short SHA of the commit this verb made (recording, recipe,
  spec, driver, or scenario row). `n/a` when the verb committed nothing.
- **`notes`** — one or two sentences: the load-bearing reason, a drift flag, a
  scope note, a retry count, or an infra reason. Prose, not data.
- **`record_blocked`** (`assess`) — a non-axis reason a record-route cell is NOT
  recorded now: `infra` / `unit_test` / `driver_bug` / `upstream`. It OVERRIDES
  a `record`/`record-known-failing` route to `deferred` and makes the cell
  `applicable:false` with honest axes. The consistency gate REQUIRES this
  whenever a record-now cell is marked `applicable:false`. Distinct from
  `frozen` (an axis says irrlicht can't) and `driver-gap` (a missing step type).
- **`observability_correction`** (`implement`) — set when the LIVE recording
  disagrees with the doc-based `assess` verdict (e.g. assessed
  `daemon_capability:full` but the store/transcript proved the signal isn't
  emitted). A non-`none` value MUST be written back into `assessment.json` in
  the SAME commit (update the axis, bump `assessed_at`, add a citing caveat) —
  the backflow loop, not a cue. `none` when the recording matched.
- **`matrix_drift` / `unblocked_cells` / `next`** — forward pointers for the
  dispatcher: which coverage rows the result changes, which cells a ported
  primitive unblocks for `implement`, or the next verb to dispatch.

## Derived-rollup reminder

`agent-scenarios-coverage.json` is DERIVED from the assessments (#508 #2). Any
verb that changes an `assessment.json` axis or adds a `catalog[]` row leaves the
rollup stale — regenerate it with `( cd tools/agent-onboarding && go run
./cmd/matrix rollup )` and commit, or `matrix rollup --check` / the Go
`TestRollupInSync` fail CI. Never hand-edit the rollup's axes (only its
editorial `notes` / `legend` are hand-owned).

## Per-verb field sets

The canonical field list lives in each verb's own `## Return contract`:

| Verb | First-line key | Verb-specific fields |
|---|---|---|
| `scenario-create` | `scenario_id:` | `capability_ids`, `files_changed`, `verify`, `next` |
| `assess` | `verdict:` + `route:` | `summary`, `wrote`, `matrix_drift`, `record_blocked` |
| `implement` | `status:` | `commit_sha`, `pass_rate`, `mode`, `observability_correction` |
| `extend-driver` | `status:` | `primitive`, `agent`, `commit_sha`, `unblocked_cells` |
