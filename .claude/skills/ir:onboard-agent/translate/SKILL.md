---
name: ir:onboard-agent/translate
description: >
  Mode D — per-cell scenario translation. Given one (agent, scenario)
  cell from the coverage matrix, produce a deterministic recording
  recipe (preconditions, exact driver steps, irrlicht-side verify
  assertions) and append it to .claude/skills/ir:onboard-agent/scenarios.json.
  Invoked as `/ir:onboard-agent translate <agent> <scenario-id>`.
  Designed for careful, gated execution — each step has a verification
  checkpoint, and the skill is expected to take a long time (often
  30–60 minutes per cell) in exchange for recipes that re-record
  deterministically for years.
---

# Mode D: per-cell translation

Reads the prose spec for one scenario, looks up the agent's
applicability verdict, consults the adapter's transport knowledge,
and emits a recipe precise enough to run the recording repeatedly and
get mostly the same output every time.

The recipe goes into `.claude/skills/ir:onboard-agent/scenarios.json` —
the same file `run-cell.sh` reads when you later record the cell. The
viewer's scenario detail page renders the new fields automatically.

> **Stages 2 + 3 of the cell lifecycle.** This skill covers recipe
> authoring (Stage 2) AND spec authoring (Stage 3, Step 3.5 below) for
> one cell. Stage 1 (assessment) is handled by
> [`../assess/SKILL.md`](../assess/SKILL.md) for one cell, or
> [`../survey/SKILL.md`](../survey/SKILL.md) for whole-agent batches.
> Stages 4–5 (recording + validation) are
> [`run-cell.sh`](../scripts/run-cell.sh) + `promote-recording.sh` +
> `expected-validate`. The end-to-end walkthrough across all five
> stages is [`../cell-lifecycle.md`](../cell-lifecycle.md).

## Working approach

**This skill is allowed to take a long time. Correctness matters more
than speed.** Each recipe gets re-recorded for years, and every
deviation between runs is a regression report waiting to happen. A
recipe that takes 30 minutes to translate carefully and then runs
deterministically beats a 5-minute recipe that flakes one in three
re-records. Budget accordingly.

Three rules govern every step below:

1. **Clean inputs.** The spec block, the coverage verdict, and the
   adapter's transport knowledge are the *only* sources of truth.
   Don't guess from the agent's general reputation or from
   third-party tutorials; if a primary source doesn't speak to a
   behavior, mark it `unknown` and stop. Re-run Mode C
   (`/ir:onboard-agent survey <agent>`) to lift the verdict before
   continuing — fabricating a recipe against an unknown verdict
   produces a recording that proves the wrong thing.

2. **Verification gates between steps.** Each step below ends with
   a `► Verify before moving on:` checklist. Don't proceed to the
   next step until every item is satisfied. The cost of stopping
   to check is low; the cost of finding a wrong assumption embedded
   in the final recipe is high.

3. **Very clear descriptions.** The recipe's `description` and the
   per-agent `notes` are read by future operators (and future you)
   far more often than they're written. A new reader should be able
   to answer four questions just from the text:
   - *What scenario am I capturing?* — the spec text in plain English
     (1–2 sentences, NO references to "the scenario above" or other
     undefined antecedents).
   - *Why this exact recipe shape?* — every non-obvious choice
     (lazy-transcript nudge, fresh-cwd-per-restart, trailing sleep,
     etc.) gets a sentence of "why" so the next translator doesn't
     remove it thinking it's vestigial.
   - *How is it going to differ between runs?* — anything model-
     dependent (token counts, exact assistant wording, timing
     within a few hundred ms) is called out so a re-record diff
     against the structural baseline doesn't read as a regression.
   - *Where would I look if it broke?* — adapter-specific gotchas
     listed in `preconditions` or `notes` so the operator isn't
     guessing.
   - *Why is `expected.jsonl` the source of truth?* — it's the spec-
     grounded benchmark, updated only when the spec itself changes.
     Re-recording against a regressed daemon can't silently rebase it,
     so regressions surface as validation failures rather than
     disappearing into a refreshed truth file.

If a step's verification can't be satisfied with the inputs at hand,
**stop and ask the maintainer** rather than guess. A missing piece
of evidence is a real signal — translate that into either a
`prerequisites_hint` on the survey or a `partial` verdict, then
re-translate after the maintainer fills the gap.

## Invocation

```
/ir:onboard-agent translate <agent> <scenario-id>
```

- `<agent>` — the adapter slug (`claudecode`, `codex`, `pi`, `aider`,
  `opencode`).
- `<scenario-id>` — the kebab id used in `.specs/agent-scenarios-coverage.json`
  (e.g. `session-start`, `user-esc-interrupt`, `auto-executed-tool-call`).

Worked examples committed (both under `scenarios.json -> scenarios[]`):

- `claudecode × session-start` (`coverage_id == "session-start"`) —
  the basic single-session shape: 1 prompt + wait_turn + trailing
  sleep, lazy-transcript nudge documented.
- `claudecode × session-end` (`coverage_id == "session-end"`) — the
  multi-variant chain: 3 lifetimes back-to-back, one per spec variant,
  using `restart` / `sigkill` / `exit_clean` step types. Read this
  one when translating any scenario whose spec has more than one
  `Scenario:` / `Expected:` block under the same Feature heading.

## What this mode produces

A single entry in `scenarios.json -> scenarios[]` with this shape:

```jsonc
{
  "name": "<scenario-id>",                  // same as coverage_id by default
  "description": "<one-paragraph what+why>",// drawn from the spec's Scenario: text
  "coverage_id": "<scenario-id>",           // joins to .specs/agent-scenarios-coverage.json
  "idle_only": true|false,                  // true when there's no `send` step
  "requires": [...],                        // capability requirements
  "verify": {...},                          // top-level invariants
  "by_adapter": {
    "<agent>": {
      "applicable": true,                   // false stops the cell with a notes field
      "preconditions": ["...", "..."],      // what the operator must have ready
      "setup": ["...", "..."],              // what run-cell.sh / driver auto-handle
      "script": [ ... step objects ... ],   // exact driver loop
      "verify": ["...", "..."],             // irrlicht-side observable assertions
      "settings": {...},                    // claude/agent settings.json blob
      "timeout_seconds": N
    }
  }
}
```

`preconditions`, `setup`, and `verify` are plain-English bullets — the
viewer renders them as checklists on the scenario detail page. The
exact driver mechanics live in `script`; everything else is operator-
facing documentation.

## Process

### Step 1 — Read the prose spec

```
.specs/agent-scenarios.md
```

Find the `### Feature:` heading whose kebab slug matches
`<scenario-id>`. Capture every `Scenario:` paragraph and every
`Expected:` bullet under it.

A heading can have **multiple** Scenario/Expected sub-blocks (e.g.
`session-end` has three: clean exit, SIGKILL mid-idle, crash
mid-turn). **Default: capture all of them in one recording** by
chaining sessions in the script — see "Multi-variant scenarios"
below. Only fall back to translating just the primary block when
the variants demand fundamentally different setups (different
prerequisites, different agent CLIs) that can't share one
recording.

► **Verify before moving on:**
- [ ] Captured every word of the Scenario: paragraph(s) and every
  Expected: bullet — paraphrasing loses precision.
- [ ] Counted the number of variants. If >1, decide chain-in-one vs
  split-into-N now (chain is the default).
- [ ] Identified each Expected bullet as either user-observable
  (state badge, count, link, lifecycle, metric) or internal (event
  kind, classifier rule, internal flag). Internal-only bullets
  should not exist — if you see one, it's a spec bug; flag it to
  the maintainer before proceeding.

### Step 2 — Read the verdict

```
.specs/agent-scenarios-coverage.json   →   .scenarios[].coverage[<agent>]
```

- `agent_supports == "yes"` → produce the recipe normally.
- `agent_supports == "partial"` → produce the recipe; mirror the
  coverage `notes` field into the recipe's `preconditions` so the
  operator knows the caveat up front.
- `agent_supports == "no"` → DO NOT fabricate. Add a
  `by_adapter.<agent>` entry with only `{"applicable": false, "notes": "..."}`
  and stop.
- `agent_supports == "unknown"` → flip to Mode C
  (`/ir:onboard-agent survey <agent>`) first to lift the verdict;
  re-run translate after the maintainer merges the survey.

► **Verify before moving on:**
- [ ] The verdict cell exists for `<agent>` in
  `.specs/agent-scenarios-coverage.json` — no fabricating a column.
- [ ] If `agent_supports == "partial"`, the coverage `notes` field
  is non-empty AND you understand the caveat well enough to mirror
  it into `preconditions`. If the notes are vague (e.g. "needs
  more investigation"), stop and surface the gap to the maintainer.
- [ ] If `agent_supports != "yes"`, you have a documented reason
  to proceed (or not). Don't guess.

### Step 3 — Read the adapter's transport knowledge

For the chosen agent, read in order:

1. `core/adapters/inbound/agents/<agent>/config.go` — `ProcessName`,
   `TranscriptFilename`, `Capabilities`, and any `DiscoverPID`
   wiring. Defines what irrlicht sees.
2. `.claude/skills/ir:onboard-agent/scripts/drive-<agent>-interactive.sh`
   — the supported step grammar and CLI flags the driver passes.
   Your `script` must stay inside this grammar. Currently supported
   step types (claudecode is the reference; other drivers implement
   a subset):

   | type           | semantics                                                      | drivers           |
   |---             |---                                                              |---                 |
   | `send`         | type text + Enter; bumps expected-turn count                    | all interactive    |
   | `slash`        | same as `send`, used for `/cmd`-style slash commands            | all interactive    |
   | `wait_turn`    | block until the agent finishes the current LLM round            | all interactive    |
   | `sleep`        | pause N seconds (field: `seconds`)                              | all interactive    |
   | `interrupt`    | send Escape (claudecode/codex/pi) or Ctrl-C (aider) mid-turn    | all interactive    |
   | `restart`      | kill current tmux, mint new UUID + fresh cwd, re-init session   | claudecode         |
   | `sigkill`      | `kill -9` the current agent process (forced termination)        | claudecode         |
   | `exit_clean`   | Ctrl-D to the TUI for a graceful shutdown                       | claudecode         |
3. `.claude/skills/ir:onboard-agent/install-instructions.md` — any
   per-agent gates (LM Studio for aider, API auth for codex/claudecode,
   etc.). These become `preconditions` entries.
4. Existing recipes under `scenarios.json -> scenarios[].by_adapter`
   for the same agent — they encode hard-won quirks (CLI flags,
   trust dialogs, timing) you should reuse rather than re-discover.

For the headless variant (`drive-<agent>.sh`, single-shot
`--print`-style invocation), the recipe uses `prompt: "..."` instead
of `script`. Pick interactive when:

- The scenario involves more than one user turn.
- The scenario sends an interrupt or slash command mid-turn.
- The scenario is idle-observation (no prompts at all — interactive
  with a sleep-only script is the canonical pattern).

Pick headless for single-turn deterministic prompts where the model's
output doesn't matter.

#### Adapter quirks that change the recipe shape

Some agents have transport behaviors that turn a pure-idle scenario
into something that needs a minimal interaction. Check these before
finalizing the script:

- **Lazy transcript materialization** — `claudecode` and `aider` only
  create the per-session transcript file once the first user input
  lands. A pure-idle launch (no `send`) leaves the UUID-keyed
  transcript missing, and `run-cell.sh`'s curator can't find events
  for the session. **Fix:** add a 1-token nudge (e.g. `send "Reply
  ok"` + `wait_turn`) so the UUID transcript materializes. The
  pre-session (`proc-<PID>`) is still observable from launch
  independent of the nudge — that's where the spec's "Session
  appears in ready within 1s" assertion is grounded. Document the
  nudge in the recipe's top-level `description` so the deviation
  from the spec's "sits idle" wording is explicit.

- **No PID binding** — `opencode` uses a SQLite watcher; there's no
  process to bind, so PID-related Expected bullets don't translate
  literally. Recipe should drop PID-specific `verify` items and
  note in `preconditions` that the daemon must have SQLite
  watching enabled.

- **Idle-flush turn-end** — `aider` settles `working → ready` only
  after a ~5 s idle window. Recipes for any aider scenario need a
  trailing sleep ≥6 s (one full idle-flush cycle plus slack) before
  daemon shutdown, otherwise the final ready transition is missed.

If you discover a new quirk while translating a cell, add it here so
the next translator doesn't have to re-discover it.

► **Verify before moving on:**
- [ ] Read the adapter's `config.go` and confirmed the transcript
  filename + process name + PID-discovery wiring. Recipes can't
  assume; they must match what irrlicht looks for.
- [ ] Read the interactive driver and confirmed which step types it
  implements. Any step the recipe needs but the driver doesn't
  support → extend the driver first, then return here.
- [ ] Cross-checked any matching quirk in the "Adapter quirks"
  list. Lazy transcripts, no-PID-binding agents, idle-flush
  turn-end — every one of these changes the recipe shape.
- [ ] If this is the agent's first scenario, also confirmed any
  install-instructions.md gates (auth, local servers, model
  availability) — they become `preconditions` entries.

#### Multi-variant scenarios (chained sessions in one recording)

When the spec has multiple `Scenario:` / `Expected:` sub-blocks under
one Feature heading (e.g. `session-end` has clean exit + SIGKILL +
crash), the default is to capture all variants in **one recording**
by chaining sessions via `restart`. The viewer's state band renders
each variant as a distinct colored arc with grey gaps where no
session is alive between them — that visual difference is the whole
point of recording them together.

Recipe shape — three claudecode variants in one recording (the
committed `session-end` recipe):

```jsonc
"script": [
  // Variant 1: clean exit
  {"type": "send", "text": "Reply with exactly the word: ok"},
  {"type": "wait_turn"},
  {"type": "sleep", "seconds": 2},
  {"type": "exit_clean"},     // Ctrl-D for a graceful shutdown
  {"type": "sleep", "seconds": 5},

  // Variant 2: SIGKILL mid-idle
  {"type": "restart"},        // new UUID + fresh cwd + new tmux
  {"type": "send", "text": "Reply with exactly the word: ok"},
  {"type": "wait_turn"},
  {"type": "sleep", "seconds": 3},
  {"type": "sigkill"},        // kill -9 claude
  {"type": "sleep", "seconds": 5},

  // Variant 3: SIGKILL mid-turn
  {"type": "restart"},
  {"type": "send", "text": "Write a 200 word essay …"},
  {"type": "sleep", "seconds": 2},
  {"type": "sigkill"},        // kill while still in working
  {"type": "sleep", "seconds": 8}
]
```

Hard-won lessons from the worked example:

- **Fresh cwd per `restart` is non-negotiable.** Claudecode caches
  "trust this folder" per directory. Re-using the same cwd skips
  the trust dialog and the driver's wait-for-trust loop hangs
  forever. The driver auto-creates `cwd-2`, `cwd-3`, etc. — your
  recipe doesn't have to do anything, but knowing this means you
  won't be tempted to "optimize" by sharing cwds across variants.
- **Sleep before SIGKILL on idle.** A 3 s sleep after `wait_turn`
  lets the classifier settle `working → ready` before the kill,
  so the recording's last live state is `ready` (matches variant 2
  Expected "Session disappears within 1s; state never entered
  working post-exit").
- **Variant-3 race is real.** Killing mid-turn means the daemon
  may not have time to emit a `working → ready` sweep-recovery
  transition before the 8 s tail expires. Document this in the
  recipe's `verify` ("accept either shape") rather than asserting
  the recovery — the live dashboard's sweep handles it correctly
  even when the recording window misses it.
- **State band needs `process_exited` as an end signal.** SIGKILL'd
  UUID sessions never get a `transcript_removed`; only
  `process_exited` marks them dead. The state band's `aliveUntil`
  map recognizes all three (`process_exited`, `transcript_removed`,
  `presession_removed`) and picks the earliest — without this, the
  band rendered the whole multi-variant run as one continuous arc.

Multi-session wiring (provided by the existing pipeline — no recipe
boilerplate needed):

- The driver tracks `SESSION_UUIDS[]` / `SESSION_TRANSCRIPTS[]`
  across restarts and writes them to `staging/session.uuids` /
  `staging/transcript.paths`.
- `run-cell.sh` detects the multi-session case and exports
  `IRRLICHT_EXTRA_SESSION_IDS` + `IRRLICHT_EXTRA_TRANSCRIPTS` to
  the curator.
- `curate-lifecycle-fixture.sh` unions the secondary UUIDs into
  the event filter and concatenates the transcripts in
  driver-recorded order.

Verify-list patterns for multi-variant recipes:

- Assert the **session count**: "events.jsonl contains exactly N
  distinct UUID session_ids".
- Assert one `process_exited` event **per UUID**.
- Per-variant: assert the live state immediately before
  `process_exited` matches the spec's variant-specific Expected
  ("State is ready at exit" for clean / SIGKILL-idle; accept
  either ready or working for SIGKILL-mid-turn).
- Assert the **timeline visual**: "N colored arcs separated by
  grey gaps in the state band" — verified by opening the
  playback page in the viewer.

### Step 3.5 — Author `expected.jsonl` FIRST

**Before writing the recipe, write `expected.jsonl`.** This is the
spec-grounded benchmark every future re-recording will be checked
against. The file lives at
`replaydata/agents/<agent>/scenarios/<scenario>/expected.jsonl`.
It is the single source of behavioral truth — re-recording cannot
silently rebase it, so regressions surface as validation failures
rather than disappearing into a refreshed offset file.

Schema (one meta line + N phase lines):

```jsonl
{"schema_version":1,"scenario_id":"<id>","source":".specs/agent-scenarios.md → Feature: <name>","notes":"..."}
{"phase":"session_birth","expected_state":"ready","relative_to":"start","max_delay_ms":1000,"text":"Session appears in ready within 1s"}
{"phase":"pid_bind","kind":"pid_discovered","relative_to":"start","max_delay_ms":1000,"text":"PID bound within 1s"}
{"phase":"first_turn_start","expected_state":"working","relative_to":"session_birth","text":"User prompt → working"}
{"phase":"idle_window","expected_state":"ready","relative_to":"first_turn_start","duration_at_least_ms":15000,"invariants":["no transcript_removed for primary session","no state_transition to working"],"text":"..."}
```

Field semantics:

- `phase` — spec-grounded label (kebab or snake case). Same names work
  across agents. For multi-variant recordings prefix with `v1_`, `v2_`,
  etc. so phases are unambiguous.
- `expected_state` — one of `ready` / `working` / `waiting`. Asserts a
  state-band transition.
- `kind` — daemon event kind (e.g. `pid_discovered`, `process_exited`).
  Asserts a lifecycle event rather than a state. Phases have EXACTLY
  ONE of `expected_state` or `kind`.
- `relative_to` — anchor phase name. `"start"` means recording start.
  All other values must reference a phase declared EARLIER in the file.
  When chaining variants, use the previous variant's `*_exit` phase as
  the anchor.
- `max_delay_ms` — phase event must arrive within this delay of the
  anchor. Omit for "any time after anchor".
- `duration_at_least_ms` — for idle/dwell phases. Asserts the
  `expected_state` persists at least this long without flipping away.
- `same_session_as` — optional. Pins the matched event's `session_id`
  to a specific earlier phase's match. Use this when an arc spans
  multiple state transitions on the SAME session and you want to
  reject candidates from a co-occurring different session (e.g.
  during a /clear handoff, pin v1's turn phases to the original UUID
  so a transition on the post-/clear UUID can't satisfy them).
- `new_session` — optional bool. When true, the matched event's
  `session_id` must NOT equal any previously-matched phase's
  session_id. Use this to assert "a brand-new session appears here"
  — the strongest observable proof that a /clear or fork created a
  fresh transcript. Mutually exclusive with `same_session_as`.
- `invariants` — plain-English negative assertions over the phase's
  time window. Two DSL forms supported:
  - `"no <kind> for <session-noun>"` — e.g. `"no transcript_removed for primary session"`
  - `"no state_transition to <state>"` — e.g. `"no state_transition to working"`
  Unknown forms are silently skipped (graceful degradation).
- `trigger` — optional documentation hint (`user_prompt`, `tool_call`,
  `interrupt`, `process_exit`). No validator semantics this iteration.
- `text` — the spec's wording for the assertion. Operator-facing.

**CRITICAL — what NEVER appears in `expected.jsonl`:**

- Absolute `ts_offset_ms` values. The whole point of expected.jsonl is
  that the same file validates every re-record regardless of when it
  ran. Express timing as `max_delay_ms` relative to a previously-
  declared phase, not as an absolute offset.
- Numbers copied from a specific recording. If you find yourself
  measuring events and writing the offsets down verbatim, step back —
  the spec describes a *bound* (within N ms of phase X), not the
  particular offset this recording happened to produce.

**Common phase-chaining pitfall:** when matching the post-turn
`ready`, anchor it to a `working` phase (not to the session's first
`ready`) so the validator doesn't match the UUID-handoff ready
instead. The committed `session-end` recipe is the worked example —
each variant has `_session_birth` → `_turn_start` → `_turn_done` →
`_exit` in that order.

When you finish writing the file, run the validator dry against the
existing recording (if one exists):

```bash
go run ./tools/agent-onboarding/cmd/expected-validate \
  replaydata/agents/<agent>/scenarios/<scenario>
```

If a recording doesn't exist yet, the validator returns "no
expected.jsonl present" — that's fine; you'll re-run after Step 6.

► **Verify before moving on:**
- [ ] `expected.jsonl` covers every spec Expected: bullet — count
  bullets in the spec, count phases in the file (or invariants for
  negative assertions), make them match.
- [ ] No phase has both `expected_state` AND `kind`. No phase has
  neither. Mutually exclusive by validator rules.
- [ ] No `relative_to` references a phase declared later in the file.
  No "forward" anchors.
- [ ] No absolute offsets. Run `grep ts_offset_ms expected.jsonl`;
  it must return nothing.
- [ ] `text` fields read like the spec's wording, not like the
  recipe's mechanism. (E.g. say "Session appears in ready within
  1s", not "transcript_new event arrives within 1000 ms".)
- [ ] If an existing recording is present, the dry-run validator
  passes against it. If it fails, either the recording was made
  against a regressed daemon (file an issue and don't "fix"
  expected.jsonl), or the recipe doesn't actually exercise the
  spec's scenario (fix the recipe in Step 5).

### Step 4 — Translate Expected bullets into verify strings

Each spec `Expected:` bullet maps to one or more `verify` strings.
Keep the wording user-observable — anchored to `events.jsonl` events,
session state, or `transcript.jsonl` content. **Do not** reference
internal flags, classifier rule numbers, or reason strings.

Example mapping (from `session-start`):

| Spec Expected | Translated verify |
|---|---|
| "Session appears in `ready` within 1s of agent launch." | "events.jsonl contains a transcript_new event for the session UUID within 1000 ms of the tmux new-session timestamp" |
| | "events.jsonl contains a state_transition to ready for the same session_id within 1000 ms of transcript_new" |
| "PID bound within 1s." | "events.jsonl contains a pid_discovered event for the same session_id within 1000 ms of transcript_new" |

When the scenario implies a **negative** invariant ("state never
entered working"), add an explicit "no … appears in the recording"
verify entry so the maintainer remembers to check the absence.

► **Verify before moving on:**
- [ ] Every Expected bullet from Step 1 has at least one `verify`
  string. No bullet is dropped silently.
- [ ] Every `verify` string is anchored to `events.jsonl` / state
  / `transcript.jsonl` content — not to internal flags, rule
  numbers, or reason strings.
- [ ] Negative invariants ("state never entered X") have explicit
  "no … appears" verify entries.
- [ ] No `verify` string asserts a behavior the recipe's script
  can't actually exercise. (E.g. don't assert "subagent count = 3"
  when the script sends no Agent tool calls.)

### Step 5 — Write the recipe

Insert the JSON entry into `scenarios.json -> scenarios[]`. Append
near related scenarios (session-start near session-end, etc.) so the
file stays readable. Run `jq '.' scenarios.json > /dev/null` to
confirm the JSON is valid.

If the agent's row already exists (e.g. you're translating a second
cell for the same scenario), add to `by_adapter` instead of creating a
duplicate entry.

► **Verify before moving on:**
- [ ] `jq '.' .claude/skills/ir:onboard-agent/scenarios.json > /dev/null`
  succeeds (no JSON syntax errors).
- [ ] The `description` answers all four questions from the
  "Very clear descriptions" rule above (what / why / how-it-differs
  / where-to-look-if-broken). Read it cold as if you've never seen
  the scenario before; if any answer is unclear, rewrite the
  description before recording.
- [ ] `preconditions` enumerate every external dependency the
  operator needs in place before pressing record. A new operator
  reading the recipe should know whether the cell is runnable
  for them right now.
- [ ] `setup` enumerates everything `run-cell.sh` / the driver
  handles automatically, so the operator understands which parts
  are their job (preconditions) vs the pipeline's (setup).
- [ ] The script's steps are in the order the driver will execute
  them; no implicit assumption that the operator will reorder
  anything.

### Step 6 — Record and validate against the spec

Run the recording once to validate the recipe end-to-end:

```bash
.claude/skills/ir:onboard-agent/scripts/run-cell.sh --attach <agent> <scenario-id>
```

If it succeeds and the recording's structural events match the
recipe's `verify` list, promote it via the helper script:

```bash
STAGE=.build/refresh/<agent>/<scenario-id>-<timestamp>
./tools/promote-recording.sh "$STAGE" <agent> <scenario-id>
```

The helper:

1. Archives the previous top-level recording into
   `replaydata/agents/<agent>/scenarios/<scenario-id>/recordings/<ts>_<daemon-ver>/`
   along with a `manifest.json` (daemon version, agent CLI version,
   recipe hash, frozen expected pass rate, recording start ts). This
   builds the history the viewer's recording-history dropdown reads.
2. Copies the staged recording into the top-level slot and writes a
   top-level `manifest.json` describing the new latest.
3. Re-runs the expected-validator against the new recording. **Exits
   non-zero if validation fails** — leaving the new files in place
   but flagging the drift so the maintainer reviews before the
   archive becomes the de-facto latest. To roll back, move the
   most-recent archive's files back to the top level:
   `mv recordings/<latest>/{events,transcript}.jsonl ./`.

The recipe's plain-English `verify` items and `expected.jsonl` should
agree on the same set of assertions.

► **Verify before declaring done:**
- [ ] Recording was committed via `--attach` mode against the user's
  real daemon (not isolated mode), and the events.jsonl matches every
  bullet in the recipe's `verify` list. Mismatches are fixable: tighten
  the recipe (more sleep, different step ordering) and re-record until
  it's stable across two consecutive runs.
- [ ] **The spec-grounded expected validator passes against the new
  recording**: `go run ./tools/agent-onboarding/cmd/expected-validate
  replaydata/agents/<agent>/scenarios/<scenario>` exits 0. This is
  the load-bearing assertion — a fail here means either the recipe
  doesn't exercise the spec (fix recipe + re-record) OR the daemon
  drifted from the spec (file an issue and STOP — do NOT update
  expected.jsonl to match a regression).
- [ ] `tools/replay-fixtures.sh` runs green against the new fixture
  (replays the recording AND runs expected-validate; both must pass).
- [ ] `go test ./tools/agent-onboarding/... -race -count=1` runs
  green (catches schema mismatches and viewer-side breaks).
- [ ] Open the viewer playback page for the new recording. Visual
  spot-checks:
  - State band reflects what the verify list asserts.
  - Turn lane shows the right number of user/assistant ticks.
  - "Spec expectations" panel shows the right pass/fail per phase.
  - "Emitted state transitions" panel lists each session ending in
    `→ ∅` (or whatever the recipe documented).
- [ ] Re-record once more from scratch (`run-cell.sh --attach …`).
  Promote into a sibling dir under `.build/refresh/` and diff
  structural fields (state-transition order, session count,
  process_exited count) against the committed fixture. **They must
  match.** Drift here means the recipe has variance that will bite
  someone in six months. Both recordings should pass the same
  unchanged `expected.jsonl`.

## Determinism budget

A re-execution should produce a recording whose **structural** events
match the original. Things that may legitimately differ between runs:

- Wall-clock timestamps (every event).
- Session UUID (fresh per run).
- PID (fresh per run).
- Token counts and cost (model-output variance).
- Cache-read counts (cold-start cache vs warm).

Things that must NOT differ between runs:

- The set of `state_transition` events and their order.
- The list of session IDs that appear (modulo the UUID itself).
- The transcript turn count (`user`/`assistant` line counts).
- The final session state.

If your recipe lets a re-record produce a different structural event
sequence, tighten it: pin model versions, add `sleep` steps to absorb
classifier debounce windows, or move from headless to interactive so
the driver controls completion timing instead of inferring it from
`--print`'s exit.

## Anti-patterns

- **Don't add `send` steps to an idle-observation scenario *unless*
  the adapter has lazy-transcript behavior** (see "Adapter quirks"
  above). For agents that write the transcript on launch (e.g.
  `codex`, `pi`), a sleep-only script is the right shape. For
  `claudecode` / `aider`, a 1-token nudge is needed to make the
  UUID-keyed session observable.
- **Don't list internal event kinds in `verify` that the spec didn't
  imply.** "state_transition → working" is fair when the spec says
  "ready → working"; "rule_15a fired" is not.
- **Don't omit the trailing sleep.** Even idle-only recipes need ≥4 s
  after the last interaction so `run-cell.sh`'s daemon kill doesn't
  race the classifier's final-state emission. The trailing-sleep rule
  from iteration 4 (multi-turn-conversation) still applies here.
- **Don't translate a scenario whose verdict is `agent_supports:
  "no"`.** Mark `applicable: false` and move on — fabricating a
  recipe just produces a recording that proves the wrong thing.
- **Don't write `verify` items as machine assertions.** The recipe's
  `verify` field is plain-English for the operator to spot-check
  (e.g. "events.jsonl contains a transcript_new event within 1000 ms");
  the structured machine-checkable form belongs in `expected.jsonl`
  as phase declarations. Both exist — the recipe `verify` is the
  maintainer-facing docs; `expected.jsonl` is what the validator
  runs against.
- **Don't translate only the primary variant when the spec has
  multiple `Scenario:` blocks.** Default to chaining all variants
  in one recording (see "Multi-variant scenarios"). Splitting them
  forfeits the timeline-visual story (state band arcs separated by
  grey gaps) and produces N partial recordings instead of one
  complete one. Only split when the variants require fundamentally
  different setup that can't share a recording.
- **Don't reuse a cwd across `restart` steps.** Claudecode caches
  trust per-directory; reusing the cwd skips the trust dialog and
  the wait-for-trust loop hangs. The driver mints `cwd-2`, `cwd-3`
  automatically; don't second-guess it.
- **Don't assert the variant-3 sweep-to-ready transition in
  `verify`.** When a session is SIGKILL'd mid-turn (variant 3 of
  session-end and similar crash-mid-action scenarios), the daemon's
  sweep may not have time to emit a `working → ready` recovery
  transition before the recording's tail expires. Write the verify
  as "accept either shape" — the live dashboard handles recovery
  correctly even when the recording window misses it.
- **Don't write absolute offsets into `expected.jsonl`.** It uses
  phase-relative anchors + tolerance windows (`max_delay_ms` measured
  from a previously-declared phase), never absolute `ts_offset_ms`
  values copied from a recording. If you find yourself measuring
  events and writing offsets verbatim, step back — the spec describes
  a *bound*, not the particular offset this recording produced.
- **Don't update `expected.jsonl` when a re-record fails validation.**
  The fail signal is "daemon vs spec drift". Update `expected.jsonl`
  ONLY when the spec at `.specs/agent-scenarios.md` actually changes
  wording. If the daemon drifted, file an issue and fix the daemon —
  don't paper over the regression by widening the tolerance.
- **Don't anchor a `_turn_done` phase directly to `_session_birth`.**
  The validator's matcher picks the FIRST matching `ready` after the
  anchor — for a session that goes ready (handoff) → working (turn) →
  ready (done), the first match is the handoff, not the turn end. Insert
  a `_turn_start` phase (matching `working`) between them so
  `_turn_done` anchors to it and naturally matches the post-turn ready.

## When to re-run

- The scenario's spec changes (`.specs/agent-scenarios.md` edited):
  re-translate so the recipe's `verify` items still match the spec's
  `Expected:` bullets.
- The agent ships a major version that changes the relevant transport
  (new `--session-id` semantics, new hook URL, etc.).
- The coverage verdict flips (`partial → yes`, or `unknown → yes`).

## What this mode does NOT do

- It does not run the recording. After the recipe is in
  `scenarios.json`, the maintainer runs `/ir:onboard-agent <agent>
  <scenario>` (or `--attach <agent> <scenario>`) to actually record.
- It does not modify `.specs/agent-scenarios-coverage.json`. The
  coverage file is the maintainer's editorial truth; the recipe in
  `scenarios.json` is the operational instance.
- It does not edit `.specs/agent-scenarios.md`. Mode D consumes the
  spec; it doesn't write it back.
