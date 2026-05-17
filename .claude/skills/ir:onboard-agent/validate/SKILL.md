---
name: ir:onboard-agent/validate
description: >
  Per-cell validation (Stage 5). Runs `expected-validate` against
  one (agent, scenario) recording: checks every phase in
  `expected.jsonl` resolves correctly and surfaces per-phase
  pass/fail. Invoked as `/ir:onboard-agent validate <agent> <scenario>`.
  Cheap, deterministic — re-runnable across daemon versions and
  recording archives.
---

# Validation (Stage 5)

Runs the spec-grounded validator against one cell's latest recording
(or a specific archive) and reports `N/M phases passed` with per-
phase reasons. The viewer's "Spec expectations" panel reads the
same JSON output.

Validation runs **automatically** at the end of every
`/ir:onboard-agent record` via `promote-recording.sh`. Re-run by
hand when:

- Inspecting an archive (drift-detection loop).
- After editing `expected.jsonl` to confirm the change still
  validates against the latest recording.
- Investigating a viewer panel showing unexpected phase pass/fail.

> **Stage 5 of the cell lifecycle.** Inputs come from earlier
> stages:
> - Stage 3 (spec) → [`../spec/SKILL.md`](../spec/SKILL.md). The
>   `expected.jsonl` phases are the assertions.
> - Stage 4 (recording) → [`../record/SKILL.md`](../record/SKILL.md).
>   The `events.jsonl` is the input being validated.
>
> End-to-end walkthrough → [`../cell-lifecycle.md`](../cell-lifecycle.md).

## Invocation

```
/ir:onboard-agent validate <agent> <scenario>
```

Validates the latest top-level recording in
`replaydata/agents/<agent>/scenarios/<scenario>/`. To validate an
archived recording, run the binary directly:

```bash
go run ./tools/agent-onboarding/cmd/expected-validate \
  replaydata/agents/<agent>/scenarios/<scenario>/recordings/<TS>_irrlichd-<ver>
```

## Output

Exit code:

- `0` — all phases pass.
- `1` — at least one phase fails (unless the spec sets `known_failing: true`).
- `2` — validator error (malformed `expected.jsonl`, missing files,
  parser crash).

stdout: human-readable per-phase report.

stderr: structured JSON for the viewer's Spec expectations panel —
shape `{phases: [{phase, status, reason, matched_event_idx}, ...]}`.

## Decision tree

| Result                              | Action                                                |
|---                                   |---                                                    |
| All phases pass                      | Done. Update matrix to `irrlicht_observes: "yes"`.    |
| Some fail, `known_failing` set       | Documented daemon gap. Stays in the spec. File issue. |
| Some fail, no `known_failing`        | Choose: tighten recipe / fix daemon / fix spec        |
| All pass, but `known_failing` set    | Gap closed. Drop the flag immediately.                |
| Validator error (exit 2)             | Spec is malformed; fix `expected.jsonl`.              |

### Three honest reasons a phase fails

1. **Recipe doesn't exercise the spec** — the driver's actions
   don't produce the asserted events. Tighten timing, add a `keys`
   step for picker navigation, etc. Re-record.
2. **Daemon drifted from spec** — the agent emits the events but
   the daemon parses/classifies them wrong. File an issue, possibly
   fix the daemon. DO NOT silently rebase `expected.jsonl` to match
   the regression.
3. **Spec is overspecified** — asserts something not in the spec's
   wording (e.g. exact step counts, internal flag names). Loosen
   the assertion to match what `.specs/agent-scenarios.md` actually
   says.

### `known_failing: true` in meta

Set this in `expected.jsonl`'s meta line when the spec describes
behavior the daemon doesn't yet deliver. The validator still runs
the spec; `replay-fixtures.sh` logs the failure rather than
erroring out. Required signal for documented daemon gaps. Drop the
flag the moment the gap closes — the test will then error to remind
you (an all-pass run while `known_failing: true` is itself a
failure signal).

## Drift-detection loop

The viewer's recording-history dropdown lets you select any
archived recording and re-validate it against the current spec. Two
outcomes worth investigating:

- **Frozen pass rate matches fresh evaluation.** No drift. The
  daemon's behavior on that archive is what we expected.
- **Frozen passed but fresh fails.** The daemon went backward (or
  the spec moved forward without re-recording the archives). Read
  the per-phase reason — if the failure is in a phase the spec
  recently changed, re-record. If the failure is in an unchanged
  phase, suspect a daemon regression and bisect commits.
- **Frozen failed but fresh passes.** Either the spec was relaxed
  (intentional) or the validator's matcher changed. Diff
  `expected.jsonl` against the archived spec wording — if the spec
  legitimately tightened, the archive may need re-recording; if
  the matcher changed, document it in the commit that touched the
  validator.

## When to re-run

- After `/ir:onboard-agent record` (automatic via
  `promote-recording.sh`).
- After editing `expected.jsonl` to confirm the latest recording
  still passes the new spec.
- When investigating a phase mark on the viewer's Spec
  expectations panel.
- After a daemon version bump, against archives, to surface drift
  before users see it on the dashboard.
- Periodically across the full tree: `tools/replay-fixtures.sh`
  runs the validator against every cell's latest + archives in one
  shot.

## What this mode does NOT do

- It does not produce a recording — that's
  [`../record/SKILL.md`](../record/SKILL.md). If `events.jsonl` is
  missing the validator errors out with "no recording present."
- It does not author the spec — that's
  [`../spec/SKILL.md`](../spec/SKILL.md). The validator reads
  `expected.jsonl`; if it's missing the validator returns a
  zero-phase pass which is a meaningless green.
- It does not modify `expected.jsonl` based on failures. Manual
  edits only. Auto-rebasing on regressions is exactly the trap
  this whole pipeline avoids.
