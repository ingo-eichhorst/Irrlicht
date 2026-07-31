---
name: ir:code-review
description: Review a working diff for defects and report findings worst-first — the repo-owned, agent-invocable counterpart to the built-in `/code-review`, which is `disable-model-invocation` and can only be run by a human. Scales from a single read to an adversarially-verified pass, and checks this repo's own conventions (three-state model, hexagonal layering, permission gating, defect-tests-seen-red) on top of ordinary correctness. Invoked as `/ir:code-review [effort] [base]`. Triggers on "/ir:code-review", "review the diff", "review my changes", "review this branch", or when `ir:exec` Phase 5 reaches its review gate.
---

# Review the Working Diff

```
/ir:code-review [effort] [base]
```

| Argument | Values | Default |
|---|---|---|
| `effort` | `low` · `medium` · `high` · `xhigh` · `max` | `medium` |
| `base` | any git diff spec | `origin/main...HEAD` |

Both are positional and both optional: `/ir:code-review`, `/ir:code-review high`,
and `/ir:code-review low origin/main...HEAD` are all valid. A bare effort word is
an **effort**, never a PR number — this skill reviews a local diff and never
takes a PR argument.

## Why this skill exists

The built-in `/code-review` is marked `disable-model-invocation`: the `Skill`
tool refuses it and it cannot be reached from `Bash` either. It is a
human-triggered path. That left `ir:exec`'s review gate unrunnable (#1205),
so this is the repo's own reviewer — same job, agent-invocable, and aware of
conventions in `AGENTS.md` that a general-purpose reviewer wouldn't check.

It is **not** a replacement for the real thing. For a large or risky diff,
a human running `/code-review <tier> origin/main...HEAD` — or `/code-review
ultra` for the cloud multi-agent pass — is still the stronger review, and
recommending that is a legitimate outcome of this skill.

## 1 · Resolve the diff

```bash
git fetch origin main                      # refresh the base before diffing
git diff --stat <base>                     # scope
git diff <base>                            # the review surface
```

Pass `base` explicitly when reviewing inside a worktree, because both of the
convenient defaults mislead there: the local `main` ref is stale (never updated
when other PRs merge, so `main...HEAD` drags in already-merged hunks), and once
the branch has been pushed with `git push -u`, `@{upstream}...HEAD` is ~empty
(upstream now points at the pushed branch, so it reviews nothing).
`origin/main...HEAD` after a fetch is the honest surface.

Review **only** what the diff changed, plus whatever the change puts at risk.
An unrelated pre-existing wart is not a finding.

## 2 · Effort ladder

Effort buys depth, and above `low` it buys an adversarial verify pass. Each tier
includes everything below it.

| Effort | Find | Verify pass |
|---|---|---|
| `low` | One careful read of the whole diff; obvious correctness and convention breaks | none — report findings unverified |
| `medium` | Trace each changed code path to its callers; check the tests that cover it | one refutation attempt per finding |
| `high` | Probe blast radius beyond the diff; confirm tests genuinely exercise the new behaviour rather than merely passing | 3 independent refutation attempts per finding |
| `xhigh` | Re-derive the change's intent from the issue/PR and check the diff actually achieves it; hunt edge cases by construction | 3 attempts, each from a distinct lens (correctness · security · does-it-reproduce) |
| `max` | Everything above, plus a completeness critic: what did this review not look at? | 3 distinct-lens attempts, then re-review whatever the critic surfaced |

Never escalate past the effort you were given. If the diff clearly warrants
more, say so in the report instead of silently spending it.

## 3 · Dimensions

Defects first — this skill hunts bugs. Quality cleanups are secondary here
(`/simplify` owns that pass, and `ir:exec` runs it separately).

- **Correctness** — logic errors, off-by-one, nil/zero-value handling, error
  paths that swallow or mis-wrap, concurrency (races, unsynchronised shared
  state, aliasing of returned structs), resource leaks, boundary conditions.
- **Repo conventions** (`AGENTS.md` Key Conventions — a break here is a real
  finding, not a nit):
  - Three session states only — `working`, `waiting`, `ready`. No cancelled state.
  - Hexagonal import direction: `domain/` → `ports/` → `adapters/` →
    `application/services/`. `domain/` and `ports/` must not import outward;
    `application/services/` reaches `adapters/inbound/` only through `ports/`.
  - Errors are logged via the `Logger` interface, not propagated with `fmt.Errorf`.
  - Child sessions (subagents, background agents) link via `ParentSessionID`.
  - Every adapter read or modification is consent-gated behind one of its
    declared `Permissions` — nothing exercised while pending or denied.
  - Format-specific transcript parsers live in the agent adapter package, not
    in shared tailer code.
- **Test coverage** — does a test actually reach the changed behaviour? A
  defect fix needs a test that was **seen red** before the fix existed; a test
  that passes on `main` either doesn't reach the defect or the diagnosis is
  wrong. Flag a green-that-was-never-red as a finding.
- **Efficiency** — needless O(n²) over a hot path, repeated work in a loop,
  redundant I/O or process scans.
- **Simplification / altitude** — only when the diff reimplements something
  the repo already has, or sits at the wrong layer.

## 4 · Verify (skipped at `low`)

An unverified finding is a guess. For each candidate, try to **refute** it:
construct the concrete input and state that would trigger it, then look for the
guard, caller check, or type constraint that already prevents it.

- **Default to refuted when uncertain.** A finding you can't demonstrate is
  noise, and noise is what makes reviews get ignored.
- At `high` and above, run the attempts independently and kill the finding when
  a majority refute it.
- Survivors carry a verdict: **`CONFIRMED`** when you traced a concrete path to
  the failure, **`PLAUSIBLE`** when the reasoning holds but you could not prove
  the trigger reachable.

Applies to your own dismissals too, per `AGENTS.md`: *"already handled
upstream"* or *"self-heals"* needs the same evidence as the finding it kills —
cite what you read, or mark it assumed.

## 5 · Report

Rank **most-severe first**. Every finding carries:

| Field | Content |
|---|---|
| `file` | repo-relative path |
| `line` | 1-indexed line it anchors to |
| `short_summary` | ≤60 chars — the claim alone, no rationale |
| `summary` | one sentence stating the defect |
| `failure_scenario` | concrete inputs/state → wrong output or crash |
| `category` | `correctness` · `convention` · `test-coverage` · `efficiency` · `simplification` |
| `verdict` | `CONFIRMED` / `PLAUSIBLE` — omit at `low`, where no verify pass ran |

If the `ReportFindings` tool is available, call it once with the surviving
findings (empty array if none survived) and do not also print them as prose.
Otherwise return the same fields as your final text — when running as a
subagent, that text **is** the return value to the caller, so it must be the
findings themselves, not a description of having reviewed.

Always state the effort you actually ran at, and say **"no findings"**
explicitly when that's the outcome — a silent report is indistinguishable from
a skipped gate.

**Do not fix anything.** Report only; the caller owns the worktree and decides
what to act on. Where the diff is large or risky enough that this pass isn't
enough, say so and recommend a human `/code-review`.
