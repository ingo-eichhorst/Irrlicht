---
name: ir:onboard-agent/recipe
description: >
  Per-cell recipe authoring (Stage 2). Given one (agent, scenario)
  cell, produces a deterministic recording recipe (preconditions,
  exact driver steps, irrlicht-side verify assertions) and writes it
  into .claude/skills/ir:onboard-agent/scenarios.json -> scenarios[].by_adapter[<agent>].
  Invoked as `/ir:onboard-agent recipe <agent> <scenario-id>`.
  Designed for careful, gated execution — each step has a verification
  checkpoint, and the skill is expected to take a long time (often
  30–60 minutes per cell) in exchange for recipes that re-record
  deterministically for years.
---

> **Private stage of `implement` (#508 #5).** recipe → spec → record → validate
> are internal building blocks `implement` runs in order — NOT top-level verbs.
> The dispatcher never routes here directly; `implement` reads this doc for
> mechanics. See `skill.md` → "Verb hierarchy".

# Recipe authoring (Stage 2)

Reads the prose spec for one scenario, looks up the agent's
applicability verdict, consults the adapter's transport knowledge,
and emits a recipe precise enough to run the recording repeatedly and
get mostly the same output every time.

The recipe goes into `.claude/skills/ir:onboard-agent/scenarios.json` —
the same file `run-cell.sh` reads when you later record the cell. The
viewer's scenario detail page renders the new fields automatically.

> **Stage 2 of the cell lifecycle.** This skill covers ONLY recipe
> authoring. Other stages have their own skills:
> - Stage 1 (assessment) → [`../assess/SKILL.md`](../assess/SKILL.md)
>   — single cell, or `--column <agent>` / `--row <scenario>` for
>   matrix scans.
> - Stage 3 (spec) → [`../spec/SKILL.md`](../spec/SKILL.md). The
>   spec (`expected.jsonl`) is the benchmark every recording is
>   validated against. **Author the spec BEFORE the recipe** so the
>   recipe's `verify` items line up with spec phases.
> - Stage 4 (recording) → [`../record/SKILL.md`](../record/SKILL.md).
> - Stage 5 (validation) → [`../validate/SKILL.md`](../validate/SKILL.md).
> - End-to-end walkthrough → [`../cell-lifecycle.md`](../cell-lifecycle.md).

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
   behavior, mark it `unknown` and stop. Re-run
   `/ir:onboard-agent assess <agent> <scenario>` (or
   `assess --column <agent>` for a batch scan) to lift the verdict
   before continuing — fabricating a recipe against an unknown
   verdict produces a recording that proves the wrong thing.

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
     etc.) gets a sentence of "why" so the next recipe author
     doesn't remove it thinking it's vestigial.
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
of evidence is a real signal — encode it as either a
`prerequisites_hint` on the column scan or a `partial` verdict on
the cell's assessment, then re-author the recipe after the
maintainer fills the gap.

## Invocation

```
/ir:onboard-agent recipe <agent> <scenario-id>
```

- `<agent>` — the adapter slug (`claudecode`, `codex`, `pi`, `aider`,
  `opencode`).
- `<scenario-id>` — the kebab id used in `.claude/skills/ir:onboard-agent/agent-scenarios-coverage.json`
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
  "coverage_id": "<scenario-id>",           // joins to .claude/skills/ir:onboard-agent/agent-scenarios-coverage.json
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

### Step 1 — Slice the cell

```
.claude/skills/ir:onboard-agent/scripts/slice-cell.sh <scenario-id> <agent>
```

One call prints exactly three things and nothing else — the
`scenarios.json` entry + this agent's existing recipe (if any), the
`### <scenario-id>` block from `scenario-meanings.md`, and the
`agent-scenarios-coverage.json` cell. Use it instead of reading the
whole catalogs; Step 2 reads the coverage cell from this same output.

From the scenario-meanings block capture all five fields (Essence,
User-observable signal, Primitive exercised, Not to be confused with,
Conceptual flow) — the **User-observable signal** lines are what your
recipe's `verify` bullets must make observable. (A gitignored
`.specs/agent-scenarios.md`, if present, adds `Scenario:`/`Expected:`
precision — usually absent, and the slice is sufficient.)

A heading can have **multiple** Scenario/Expected sub-blocks (e.g.
`session-end` has three: clean exit, SIGKILL mid-idle, crash
mid-turn). **Default: capture all of them in one recording** by
chaining sessions in the script — see "Multi-variant scenarios"
below. Only fall back to translating just the primary block when
the variants demand fundamentally different setups (different
prerequisites, different agent CLIs) that can't share one
recording.

► **Verify before moving on:**
- [ ] Ran `slice-cell.sh` and read all five scenario-meanings fields from its output.
- [ ] `slice-cell.sh` exited non-zero (scenario missing from `scenarios.json` or `scenario-meanings.md`) — STOP and ask the maintainer to run `scenario-create` first.
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

Use the coverage cell already printed by `slice-cell.sh` in Step 1
(`agent-scenarios-coverage.json → .scenarios[].coverage[<agent>]`) — no
second read.

- `agent_supports == "yes"` → produce the recipe normally.
- `agent_supports == "partial"` → produce the recipe; mirror the
  coverage `notes` field into the recipe's `preconditions` so the
  operator knows the caveat up front.
- `agent_supports == "no"` → DO NOT fabricate. Add a
  `by_adapter.<agent>` entry with only `{"applicable": false, "notes": "..."}`
  and stop.
- `agent_supports == "unknown"` → run
  `/ir:onboard-agent assess <agent> <scenario>` first to lift the
  verdict; re-run `/ir:onboard-agent recipe` after the maintainer
  merges the resulting assessment into the matrix.

► **Verify before moving on:**
- [ ] The verdict cell exists for `<agent>` in the slice output — no
  fabricating a column.
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
2. `.claude/skills/ir:onboard-agent/step-grammar.md` — the shared step
   vocabulary (`send`, `slash`, `wait_turn`, `sleep`, `interrupt`,
   `keys`, `restart`, `resume`, `reset_session`, `fork`, `sigkill`,
   `exit_clean`, `start_session`, `session`) with each step's fields and
   which drivers support it. Your `script` must stay inside this grammar.
   Read this one-page reference instead of the ~600-line driver; only
   open `drive-<agent>-interactive.sh` for a per-agent quirk the grammar
   doesn't cover.
3. `.claude/skills/ir:onboard-agent/install-instructions.md` — any
   per-agent gates (LM Studio for aider, API auth for codex/claudecode,
   etc.). These become `preconditions` entries.
4. This cell's prior recipe (if any) is already in the Step 1 slice. To
   reuse hard-won quirks (CLI flags, trust dialogs, timing) from one of
   the agent's OTHER scenarios, run `slice-cell.sh <other-scenario>
   <agent>` for that specific scenario rather than reading all of
   `scenarios.json`.

For the headless variant (`drive-<agent>.sh`, single-shot
`--print`-style invocation), the recipe uses `prompt: "..."` instead
of `script`. Pick interactive when:

- The scenario involves more than one user turn.
- The scenario sends an interrupt or slash command mid-turn.
- The scenario is idle-observation (no prompts at all — interactive
  with a sleep-only script is the canonical pattern).
- **The scenario asserts the full lifecycle arc** (`turn_end` /
  `pid_bind` / `teardown`) **and the agent's headless mode exits at
  turn completion.** The agent PROCESS must outlive the daemon's
  observation/scan window: a one-shot `--print` run exits before the
  daemon classifies the working→ready settle (and, on a very short
  run, before the PID scanner ticks — acute for PID-keyed transports
  like opencode's `ProcessOwnedStore`, where a dead process is
  unreadable). Interactive keeps the process alive across the turn,
  then a clean teardown supplies `process_exited`. *Symptom of getting
  this wrong:* a headless cell that records but validates ~1–3/5, the
  tail phases failing while their events are visibly present — a
  fast-exit truncation, not a daemon bug.

Pick headless for single-turn deterministic prompts where the model's
output doesn't matter and the spec doesn't depend on observing the
post-turn settle or teardown (e.g. session-start: birth + pid_bind in
the first scanner tick).

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

- **Slash command vs picker navigation** — before declaring a
  slash-driven cell a `driver_gap` for want of a `keys` step, check
  whether the command takes an INLINE ARGUMENT. A command like
  `/model <provider>/<id>` or `/compact` is sendable with a single
  `slash` step and needs no `keys`; only a command that opens an
  interactive picker navigated by arrow keys actually requires `keys`
  (which most drivers lack). Verify via the agent's `--help` /
  `strings` / slash catalog. *Worked example:* pi's `/model <id>` is
  inline → recordable via `slash`; pi's `/tree` checkpoint picker
  needs arrow-key nav → genuine `driver_gap`. Applies only to agents
  with a slash/REPL surface.

If you discover a new quirk while authoring a recipe, add it here
so the next recipe author doesn't have to re-discover it.

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

### Step 3.5 — Author the spec FIRST (separate skill)

**Before writing the recipe, the spec must exist.** Run
`/ir:onboard-agent spec <agent> <scenario>` to produce
`replaydata/agents/<agent>/scenarios/<scenario>/expected.jsonl` —
the phase DSL the validator runs against every recording. See
[`../spec/SKILL.md`](../spec/SKILL.md) for the full authoring guide
(schema, field semantics, anti-patterns, validation).

Why first: this recipe's Step 4 maps each spec phase to one or more
plain-English `verify` strings. Without the spec, you'd be writing
recipe verify items against your own imagination instead of against
the authoritative phase list.

► **Verify before moving on:**
- [ ] `replaydata/agents/<agent>/scenarios/<scenario>/expected.jsonl`
  exists and covers every Expected: bullet in the prose spec.
- [ ] Its dry-run against any existing recording passes (or returns
  "no recording present").

### Step 4 — Translate spec phases into recipe verify strings

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

### Step 6 — Hand off to record + validate

The recipe is in `scenarios.json`. The next two stages happen via
their own skills:

- **Stage 4 (recording).** Run
  `/ir:onboard-agent record <agent> <scenario>` (alias for the
  unkeyed `/ir:onboard-agent <agent> <scenario>`). See
  [`../record/SKILL.md`](../record/SKILL.md) for `--attach` mode,
  the run-cell.sh + promote-recording.sh pipeline, and the
  determinism re-record check.
- **Stage 5 (validation).** `promote-recording.sh` auto-invokes
  the expected-validator at the end of recording. To re-run by
  hand: `/ir:onboard-agent validate <agent> <scenario>`. See
  [`../validate/SKILL.md`](../validate/SKILL.md) for the decision
  tree and drift-detection loop.

After both stages pass, the recipe is done. The recipe's plain-
English `verify` items and the spec's `expected.jsonl` should agree
on the same set of assertions — Stage 4 + 5 are the load-bearing
check that they do.

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
- **Don't author a recipe for a scenario whose verdict is
  `agent_supports: "no"`.** Mark `applicable: false` and move on —
  fabricating a recipe just produces a recording that proves the
  wrong thing.
- **Don't write `verify` items as machine assertions.** The recipe's
  `verify` field is plain-English for the operator to spot-check
  (e.g. "events.jsonl contains a transcript_new event within 1000 ms");
  the structured machine-checkable form belongs in `expected.jsonl`
  as phase declarations. Both exist — the recipe `verify` is the
  maintainer-facing docs; `expected.jsonl` is what the validator
  runs against.
- **Don't restrict the recipe to only the primary variant when the
  spec has multiple `Scenario:` blocks.** Default to chaining all
  variants in one recording (see "Multi-variant scenarios").
  Splitting them forfeits the timeline-visual story (state band
  arcs separated by grey gaps) and produces N partial recordings
  instead of one complete one. Only split when the variants require
  fundamentally different setup that can't share a recording.
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
- **Don't author multi-entity recipes whose entities complete
  simultaneously.** When the recording exercises multiple subagents
  / sessions / iterations whose VISIBLE-OVER-TIME state is what the
  dashboard surfaces (subagent count chip, in-progress iteration
  counter, etc.), give each entity a deterministic stagger so the
  decrement is observable as a sequence of events, not a single
  jump. **Worked example — `claudecode/foreground-subagent` iteration 1:** the
  v1 recipe asked the parent to launch TWO subagents that each
  *read a file*. Both subagents finished within ~1s of each other,
  so the dashboard's subagent count went `2 → 0` in one tick — the
  spec passed 8/8 phases on structural assertions (`parent_linked`
  count, parent working span, terminal ready) but the recording
  failed the unspoken intent: the count-chip animation never
  played, because there was no observable middle state. **v2 fix:**
  three subagents each running `bash sleep N` for `N = 5/10/15`
  seconds. Completion times are now deterministic and ordered, and
  the spec chains `child1_done → child2_done → child3_done` via
  `relative_to` + `same_session_as` so the validator confirms the
  staggered order, not just the eventual count. The shape that
  matters for the dashboard now matches the shape the validator
  enforces. Generalize: **the recipe must exercise the user-visible
  surface, not just the daemon's structural events**. If you can't
  describe what an operator would SEE animate in the dashboard,
  the recipe is probably too thin.

## When to re-run

- The scenario's spec changes (`.specs/agent-scenarios.md` edited):
  re-author so the recipe's `verify` items still match the spec's
  `Expected:` bullets.
- The agent ships a major version that changes the relevant transport
  (new `--session-id` semantics, new hook URL, etc.).
- The coverage verdict flips (`partial → yes`, or `unknown → yes`).

## What this mode does NOT do

- It does not run the recording. After the recipe is in
  `scenarios.json`, the maintainer runs `/ir:onboard-agent <agent>
  <scenario>` (or `--attach <agent> <scenario>`) to actually record.
- It does not modify `.claude/skills/ir:onboard-agent/agent-scenarios-coverage.json`. The
  coverage file is the maintainer's editorial truth; the recipe in
  `scenarios.json` is the operational instance.
- It does not edit `.specs/agent-scenarios.md`. The recipe skill
  consumes the prose spec; it doesn't write it back.
