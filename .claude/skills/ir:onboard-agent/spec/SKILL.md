---
name: ir:onboard-agent/spec
description: >
  Per-cell spec authoring (Stage 3). Translates the prose
  `.specs/agent-scenarios.md → Feature: …` block for one scenario
  into a structured `expected.jsonl` phase DSL that the validator
  runs against every recording. Writes
  `replaydata/agents/<agent>/scenarios/<scenario>/expected.jsonl`.
  Invoked as `/ir:onboard-agent spec <agent> <scenario-id>`. The spec
  is the source-of-truth benchmark: it never gets silently rebased
  on a regressed recording.
---

> **Private stage of `implement` (#508 #5).** recipe → spec → record → validate
> are internal building blocks `implement` runs in order — NOT top-level verbs.
> The dispatcher never routes here directly; `implement` reads this doc for
> mechanics. See `skill.md` → "Verb hierarchy".

# Spec authoring (Stage 3)

Reads the prose spec for one scenario and emits a structured
`expected.jsonl` file: one meta line + N phase lines that the
`expected-validate` binary checks every recording against. Phases
assert state transitions, daemon event kinds, dwell windows, and
negative invariants. Anchored relative to one another, never to
absolute timestamps, so the same file validates re-records
indefinitely.

> **Stage 3 of the cell lifecycle.** Author the spec BEFORE Stage 2
> (recipe) so the recipe's `verify` items line up with concrete spec
> phases. Other stages:
> - Stage 1 (assessment) → [`../assess/SKILL.md`](../assess/SKILL.md)
>   — single cell, or `--column <agent>` / `--row <scenario>` for
>   batch scans.
> - Stage 2 (recipe) → [`../recipe/SKILL.md`](../recipe/SKILL.md).
> - Stage 4 (recording) → [`../record/SKILL.md`](../record/SKILL.md).
> - Stage 5 (validation) → [`../validate/SKILL.md`](../validate/SKILL.md).
> - End-to-end walkthrough → [`../cell-lifecycle.md`](../cell-lifecycle.md).

## Invocation

```
/ir:onboard-agent spec <agent> <scenario-id>
```

- `<agent>` — the adapter slug (`claudecode`, `codex`, `pi`, `aider`,
  `opencode`). Different agents may need different phase names
  (e.g. lazy-transcript adapters add a `pid_bind` phase tied to
  `transcript_new`); the spec is per-cell, not agent-agnostic.
- `<scenario-id>` — kebab id from `.claude/skills/ir:onboard-agent/agent-scenarios-coverage.json`.

## Output

A single file at:

```
replaydata/agents/<agent>/scenarios/<scenario>/expected.jsonl
```

Schema: one meta line + N phase lines.

```jsonl
{"schema_version":1,"scenario_id":"<id>","source":".specs/agent-scenarios.md → Feature: <name>","notes":"..."}
{"phase":"session_birth","expected_state":"ready","relative_to":"start","max_delay_ms":1000,"text":"Session appears in ready within 1s"}
{"phase":"pid_bind","kind":"pid_discovered","relative_to":"start","max_delay_ms":1000,"text":"PID bound within 1s"}
{"phase":"first_turn_start","expected_state":"working","relative_to":"session_birth","text":"User prompt → working"}
{"phase":"idle_window","expected_state":"ready","relative_to":"first_turn_start","duration_at_least_ms":15000,"invariants":["no transcript_removed for primary session","no state_transition to working"],"text":"..."}
```

## Field semantics

| Field | Purpose |
|---|---|
| `phase` | Spec-grounded label (kebab or snake case). Used as anchor target for later phases. For multi-variant recordings prefix with `v1_`, `v2_` etc. so phases are unambiguous. |
| `expected_state` | `ready` / `working` / `waiting`. Asserts a state-band transition. |
| `kind` | Daemon event kind (e.g. `pid_discovered`, `process_exited`). Asserts a lifecycle event. **Exactly one** of `expected_state` or `kind`. |
| `relative_to` | Anchor phase name. `"start"` means recording start. All other values must reference a phase declared EARLIER in the file. |
| `max_delay_ms` | Phase event must arrive within this delay of the anchor. Omit for "any time after anchor". |
| `duration_at_least_ms` | For idle/dwell phases. Asserts the `expected_state` persists this long without flipping away. |
| `same_session_as` | Pin the matched event's `session_id` to a specific earlier phase's match. Use for arcs spanning multiple transitions on the SAME session when a co-occurring different session would otherwise satisfy the match. |
| `new_session` | Bool. When true, the matched event's `session_id` must NOT equal any previously-matched phase's session_id. Strongest observable proof that a /clear or fork created a fresh transcript. Mutually exclusive with `same_session_as`. |
| `invariants` | Plain-English negative assertions over the phase's time window. Two DSL forms: `"no <kind> for <session-noun>"`, `"no state_transition to <state>"`. Unknown forms are silently skipped (graceful degradation). |
| `trigger` | Optional documentation hint (`user_prompt`, `tool_call`, `interrupt`, `process_exit`). No validator semantics this iteration. |
| `text` | The spec's wording for the assertion. Operator-facing. |

## CRITICAL — what NEVER appears in `expected.jsonl`

- **Absolute `ts_offset_ms` values.** The whole point of
  expected.jsonl is that the same file validates every re-record
  regardless of when it ran. Express timing as `max_delay_ms`
  relative to a previously-declared phase, not as an absolute offset.
- **Numbers copied from a specific recording.** If you find yourself
  measuring events and writing offsets verbatim, step back — the
  spec describes a *bound* (within N ms of phase X), not the
  particular offset this recording happened to produce.

## Steps

### Step 1 — Slice the cell

```
.claude/skills/ir:onboard-agent/scripts/slice-cell.sh <scenario-id> <agent>
```

Read the `### <scenario-id>` scenario-meanings block from the output —
its **User-observable signal** lines are what each phase will assert (one
phase per signal). The slice also prints the recipe and coverage cell for
context. If a richer `.specs/agent-scenarios.md` happens to be present
(gitignored, usually absent), use its `Scenario:`/`Expected:` bullets for
extra precision; the scenario-meanings signals are sufficient without it.

### Step 2 — One phase per Expected bullet

Map each Expected bullet to a phase entry:

- "Session appears in ready within Ns" → `{"phase": "session_birth",
  "expected_state": "ready", "relative_to": "start", "max_delay_ms": N*1000}`
- "PID bound within Ns" → `{"phase": "pid_bind", "kind":
  "pid_discovered", "relative_to": "start", "max_delay_ms": N*1000}`
- "User prompt → working" → `{"phase": "turn_start",
  "expected_state": "working", "relative_to": "session_birth"}`
- "Turn ends in ready" → `{"phase": "turn_done", "expected_state":
  "ready", "relative_to": "turn_start"}`

When the scenario implies a **negative** invariant ("state never
entered working"), use the `invariants` array on the relevant dwell
phase rather than omitting the assertion.

### Step 3 — Multi-variant chaining

When the spec has multiple `Scenario:` / `Expected:` sub-blocks
under one Feature heading, the recipe (Stage 2) chains all variants
in one recording. Prefix phases with `v1_`, `v2_`, etc. so phases
are unambiguous, and anchor the first phase of each variant to the
previous variant's `_exit`:

```jsonl
{"phase":"v1_session_birth","expected_state":"ready","relative_to":"start","max_delay_ms":1000}
{"phase":"v1_turn_start","expected_state":"working","relative_to":"v1_session_birth"}
{"phase":"v1_turn_done","expected_state":"ready","relative_to":"v1_turn_start"}
{"phase":"v1_exit","kind":"process_exited","relative_to":"v1_turn_done","max_delay_ms":5000}
{"phase":"v2_session_birth","expected_state":"ready","relative_to":"v1_exit","new_session":true,"max_delay_ms":15000}
```

### Step 4 — Dry-run validate

Once the file is written, run the validator against any existing
recording in the folder:

```bash
go run ./tools/onboarding-factory/cmd/expected-validate \
  replaydata/agents/<agent>/scenarios/<scenario>
```

If a recording doesn't exist yet, the validator returns "no
recording present" — fine, you'll re-run after Stage 4.

## Common pitfalls

- **A transient `proc-<PID>` presession row can steal `session_birth`.**
  The daemon's process scanner emits a `proc-<PID>` presession `ready`
  row the moment it detects the process — BEFORE the real session data
  becomes bindable (the transcript file appears, or the store row
  becomes readable). If your first phase pins identity to that birth
  (or later phases do `same_session_as: session_birth`), the matcher
  greedily binds to the short-lived proc-`<PID>` row and every chained
  `same_session_as` fails. **Fix:** anchor the first phase to `"start"`
  and leave it UNPINNED (no `same_session_as`); let the first phase
  that observes a REAL turn (`turn_start` matching `working`) establish
  the session identity that later phases chain off. This is
  transport-neutral — it shows up on FilesUnderRoot adapters
  (claudecode/codex/pi) AND on opencode's `ProcessOwnedStore`, since
  the proc-`<PID>` row comes from the shared scanner, not the parser.
  *Worked example:* pi/multi-turn-conversation went 1/9 → 9/9 once the
  first turn was anchored to `"start"` unpinned.
- **Phase-chaining: don't anchor `_turn_done` directly to
  `_session_birth`.** The validator's matcher picks the FIRST
  matching `ready` after the anchor — for a session that goes ready
  (handoff) → working (turn) → ready (done), the first match is the
  handoff, not the turn end. Insert a `_turn_start` phase (matching
  `working`) between them so `_turn_done` anchors to it and
  naturally matches the post-turn ready.
- **`same_session_as` is the strongest identity proof.** Use it
  whenever a multi-step arc must stay on the same UUID. The default
  matcher accepts any session_id that satisfies state/kind, which
  is wrong when sessions co-exist briefly (e.g. /clear handoff).
- **`new_session: true` is the strongest "content was reset"
  proof.** A fresh UUID means a fresh transcript file. Strongest
  observable for /clear, /new, fork, /resume-with-new-id.

## Verify checklist

- [ ] `expected.jsonl` covers every spec Expected: bullet — count
  bullets in the spec, count phases (or invariants for negative
  assertions), make them match.
- [ ] No phase has both `expected_state` AND `kind`. No phase has
  neither. Mutually exclusive by validator rules.
- [ ] No `relative_to` references a phase declared later in the
  file. No "forward" anchors.
- [ ] No absolute offsets. Run `grep ts_offset_ms expected.jsonl` —
  must return nothing.
- [ ] `text` fields read like the spec's wording, not like the
  recipe's mechanism. (E.g. say "Session appears in ready within
  1s", not "transcript_new event arrives within 1000 ms".)
- [ ] If an existing recording is present, the dry-run validator
  (Step 4) passes. If it fails, either the recording was made
  against a regressed daemon (file an issue and don't "fix"
  expected.jsonl), or the recipe doesn't actually exercise the
  spec's scenario (fix the recipe).

## Anti-patterns

- **Don't write absolute offsets.** See CRITICAL above. Phase-
  relative anchors + `max_delay_ms` tolerances only.
- **Don't update `expected.jsonl` to match a failing recording.**
  The fail signal is "daemon vs spec drift". Update the spec ONLY
  when `.specs/agent-scenarios.md` actually changes wording. If the
  daemon drifted, file an issue and fix the daemon — don't paper
  over the regression by widening the tolerance.
- **Don't list internal flags / classifier rule numbers in `text`.**
  The text field is operator-facing. Say what the user observes,
  not how irrlicht implements detection.
- **Don't omit negative invariants.** "State never entered working
  during this window" is a positive spec assertion — if you leave
  it out, the validator's pass means nothing for that window.

## When to re-run

- The scenario's spec changes (`.specs/agent-scenarios.md` edited):
  re-author so phases still cover every Expected: bullet.
- The validator grows new DSL fields and the cell would benefit
  (e.g. a stronger identity assertion than `same_session_as`).
- A recording's archived run starts failing the spec while latest
  passes — possible spec drift in your favor; re-read and tighten.

## What this mode does NOT do

- It does not write the recipe — that's
  [`../recipe/SKILL.md`](../recipe/SKILL.md).
- It does not run a recording or validate against the latest. Stage
  5's validator does that; see
  [`../validate/SKILL.md`](../validate/SKILL.md).
- It does not modify `.specs/agent-scenarios.md`. The spec markdown
  is the input; expected.jsonl is the output.
