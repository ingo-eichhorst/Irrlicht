# Usage-limit 429 leaves a session stuck in `error` — issue #1858

**The committed goldens in this folder pin the DEFECT, not the wanted behaviour.**
Read that first. A green `TestFixtureReplayByteIdentity` over this capture proves the
bug still reproduces; it does not prove the daemon is correct. When #1858 lands, both
goldens must move, and the shape of that move is the red-first evidence the fix is
required to show:

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

## The recorded (wrong) outcome

| capture | transitions | time in `error` |
|---|---|---|
| top-level | 3 | 4826.089 s (80m26s) — never leaves |
| subagent | 3 | 1810.219 s (30m10s) — never leaves |

The top-level session ran two more complete turns inside that window (10,790 output
tokens, ~$8.21 estimated spend) and stayed red for all of them.

## Why

`clearSessionErrorOnRecovery` retires a terminal error only on
`ParsedEvent.StartsNewUserTurn()` — a turn boundary cannot, because
`ClearedByTurnBoundary()` is true only for `ErrorPhaseRetrying`. The prompt that
actually resumes a usage-limited session is written by Claude Code itself and flagged
`isMeta: true` (`origin.kind` is `auto-continuation` for the parent, `human` for the
subagent), so `handleUserEvent` skips it before `ClearToolNames` is ever raised. The
one event that starts the recovery turn is therefore invisible to the clearing rule.

Full diagnosis, the `origin.kind` census, and the measured probe that confirms this
fixture discriminates: issue #1858.
