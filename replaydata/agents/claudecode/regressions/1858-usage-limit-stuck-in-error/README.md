# Usage-limit 429 leaves a session stuck in `error` — issue #1858

**Fixed.** The goldens in this folder now pin the CORRECT recovery — the session
leaves `error` once Claude Code's own resume prompt lands, rather than staying red
for the rest of the session. Before the fix, the identical capture replayed to 3
transitions per file with the error never clearing at all; that before/after golden
diff (regenerated with the command below) is the red-first evidence for the defect.
It is preserved in the fix's PR description rather than re-committed here, so this
README does not itself pin a wrong answer for a future reader to trust by accident.

```
UPDATE_REPLAY_GOLDENS=1 go test ./tools/onboarding-factory/cmd/replay/... -count=1
```

## What the capture holds

Live session `e81ac16e-353f-47e2-aa0a-09f388e3610e`, claude-code 2.1.240, 2026-08-28.
Both files are contiguous, unedited slices of their source transcript — no lines were
cherry-picked, so the ordering the tailer sees is the ordering that shipped.

| file | source lines | what it shows |
|---|---|---|
| `recordings/2026-08-28-17-45-52_irrlichd-unknown/transcript.jsonl` | 1186–1280 of the parent transcript | the top-level session |
| `subagents/agent-a51e4b480796757bc.jsonl` | 315–345 of the subagent transcript | the subagent it had running |

Both hit the same HTTP 429 at 17:45:52, and both are resumed automatically once the
limit resets.

## The corrected outcome

| capture | transitions | time in `error` | clears at |
|---|---|---|---|
| top-level | 3 → 24 | 4826.089 s (80m26s) → 960.090 s (16m00.090s) | exactly the 18:01:53.464Z resume prompt |
| subagent | 3 → 5 | 1810.219 s (30m10s) → 1020.646 s (17m00.646s) | exactly the 18:02:52.912Z resume prompt |

The remaining ~16-17 minutes in `error` is the REAL usage-limit wait — the gap between
the give-up and Claude Code's resume prompt landing — not a residual bug (it matches the
issue's cited probe, ~16m00s / ~17m00s, almost exactly). The top-level session's
subsequent turns (10,790 output tokens, ~$8.21 estimated spend) now track normally
instead of staying red for all of them.

An earlier version of this fix cleared `t.sessionError` correctly but without marking
the pass substantive, so the OBSERVABLE transition lagged the resume prompt by ~10
seconds (until whatever transcript activity happened to arrive next) rather than firing
on it directly — caught by this PR's own review pass (`applySkippedEvent` in
`core/pkg/tailer/tailer.go`, mirroring the existing SessionError-arriving half of the
same function from #1799). `TestSessionError_AgentResumeIsSubstantive` pins it.

## Why it was broken, and why the fix is scoped the way it is

`clearSessionErrorOnRecovery` retires a terminal error only on
`ParsedEvent.StartsNewUserTurn()` — a turn boundary cannot, because
`ClearedByTurnBoundary()` is true only for `ErrorPhaseRetrying`. The prompt that
actually resumes a usage-limited session is written by Claude Code itself and flagged
`isMeta: true` (`origin.kind` is `auto-continuation` for the parent, `human` for the
subagent), so `handleUserEvent` skipped it before `ClearToolNames` was ever raised. The
one event that starts the recovery turn was therefore invisible to the clearing rule.

The fix adds a narrow, adapter-set `ParsedEvent.IsAgentResume` signal — NOT a change to
`StartsNewUserTurn`/`ClearToolNames` themselves. `origin.kind:"human"` is not exclusive
to a resume (Claude Code uses the identical wrapper for a genuine mid-turn
interjection), so treating it as a real turn start would also reset the task
estimate/summary/question and sweep open tool calls for an ordinary interjection
unrelated to any error. `IsAgentResume` is consumed only by a new
`SessionError.ClearedByAgentResume()` predicate gated on `ErrorPhaseTerminal` — the
mirror image of `ClearedByTurnBoundary`'s `ErrorPhaseRetrying` gate — so it is a no-op
on every pass without a standing terminal error and never touches the retrying-error
path at all.

Full diagnosis, the `origin.kind` census, and the measured probe that first confirmed
this fixture discriminates: issue #1858.
