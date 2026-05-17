# Cell Lifecycle — End-to-End Workflow for One (Agent × Scenario)

Every (agent × scenario) cell in the irrlicht coverage matrix moves
through a five-stage pipeline. This doc is the canonical walkthrough:
each stage names its file, its tool, its success criterion, and what
it hands to the next stage. The viewer's overview renders these stages
as a 5-segment strip per cell (`●● ✎ § N ✓`); the doc tells you how
to fill each segment.

```
  1. Assessment   2. Recipe       3. Spec         4. Recording      5. Validation
  ──────────────  ──────────────  ──────────────  ────────────────  ──────────────
  matrix verdict  scenarios.json  expected.jsonl  events.jsonl +    pass/fail per
  for this agent  by_adapter[a]   phase DSL       transcript.jsonl  phase + manifest
       │              │                │                │                  │
       ▼              ▼                ▼                ▼                  ▼
   /assess          /recipe         /spec               /record            /validate
  (or /survey)
```

If `agent_supports == "no"` at Stage 1, the pipeline is frozen — no
recipe, no spec, no recording. Otherwise each stage's output unlocks
the next.

---

## Stage 1 — Assessment

**Question:** Does this agent support the scenario? Can irrlicht
observe it?

**Files:**

- `.specs/agent-scenarios-coverage.json` — current-state rollup
  (gitignored, maintainer-owned in the main checkout). The matrix
  the overview reads.
- `replaydata/agents/<agent>/scenarios/<scenario>/assessment.json` —
  point-in-time record of one assessment moment (committed). Carries
  the timestamp, verdict, prose reasoning, and sources behind the
  matrix entry. Surfaced on the scenario-agent detail page in the
  viewer. The first artifact a cell produces — exists even before
  any recording.

**Tool:** `/ir:onboard-agent assess <agent> <scenario>` (see
[`assess/SKILL.md`](assess/SKILL.md)) for one cell. For batched
matrix-wide work across all scenarios of one agent, use
`/ir:onboard-agent survey <agent>` (see `survey/SKILL.md`). Don't
default to `unknown` — only record it when an honest search came up
empty.

**Matrix entry shape (rollup):**

```json
{
  "agent_supports":   "yes" | "no" | "partial" | "unknown",
  "irrlicht_observes": "yes" | "no" | "partial" | "unknown" | "n/a",
  "notes": "one or two sentences citing the source"
}
```

**Assessment artifact shape (record):**

```json
{
  "schema_version": 1,
  "scenario_id": "<id>",
  "agent": "<agent-slug>",
  "assessed_at": "2026-05-17T00:00:00Z",
  "agent_supports": "yes" | "no" | "partial" | "unknown",
  "irrlicht_observes": "yes" | "no" | "partial" | "unknown" | "n/a",
  "confidence": 0.0-1.0,
  "body": "markdown prose — Verdict, Reasoning, etc.",
  "caveats": [
    "Known limitation or metric drift the verdict doesn't capture.",
    "One short sentence per caveat. The viewer renders these as a yellow callout above Sources."
  ],
  "sources": [
    {"kind": "url",  "ref": "https://...",     "note": "..."},
    {"kind": "file", "ref": "path/to/file.go", "note": "..."}
  ]
}
```

One file per (agent, scenario); re-assessment overwrites it (git
preserves history). The viewer renders the body as preformatted
wrapping text — markdown headings (`## Verdict`) read fine as-is.

**When to use `caveats`:** the spec for many cells is narrower than
"irrlicht has zero blind spots about the feature." A cell can be
`irrlicht_observes: yes` for spec purposes while still having known
limitations that a maintainer should be aware of. Use the caveats
array to capture them. Two common patterns:

1. **Feature invisible to file-watching but spec-compliant anyway.**
   E.g. `claudecode/checkpoint-rewind`: the rewind itself produces no
   transcript event, but the spec only requires correct state
   after — which irrlicht delivers. Caveat: "Rewind events are
   invisible … spec compliance is unaffected."

2. **Metric drift downstream of an unobserved event.** Same example:
   after a rewind, irrlicht's context-utilization % overstates
   what claude actually has in context (claude reads a truncated
   subset of the transcript; irrlicht sums the whole file).
   Caveat: "Context utilization % may overstate after …".

Authoring rule: if you're tempted to downgrade the verdict to
`partial` because of a known-but-narrow gap, ask first whether the
canonical spec actually requires the missing observation. If not,
keep the verdict honest (`yes`) and surface the gap as a caveat.

**Authoring flow:** `/ir:onboard-agent assess <agent> <scenario>`
dispatches a research subagent, synthesizes the verdict + caveats +
sources, and writes the JSON. Re-runs overwrite silently. See
[`assess/SKILL.md`](assess/SKILL.md) for the per-step recipe and
worked examples.

**Transcription:** the maintainer copies the verdict + a one-line
note from `assessment.json` into `.specs/agent-scenarios-coverage.json`.
The artifact is the source-of-record; the matrix is the rollup the
overview reads.

**Success criterion:** the verdict is honest about what's true *now*
(agent docs may have changed since last assessment), the notes line
names the specific feature (e.g. `/rewind` for claudecode checkpoint,
`<turn_aborted>` marker for codex interrupts), and `assessment.json`
cites at least the primary source(s) consulted.

**When `agent_supports == "no"`:** Stop. Don't author a recipe; the
cell stays frozen on the overview as `✗`. The `assessment.json`
still goes in — it documents *why* the cell is blocked and what
would unblock it. Periodic re-assessment (when a new agent version
ships) may unlock it.

**Common stale signals to re-check:**

- "Issue #N is fixed" — verify by checking the daemon's recent
  commits or running the recipe.
- "Feature was unknown last quarter" — re-search the agent's release
  notes.
- An archived recording exists with `partial` verdict but its
  measurement now passes clean — drift signal; matrix is stale.
- `assessed_at` is older than the agent's last major version bump.

---

## Stage 2 — Recipe (per-adapter driver script)

**Question:** What sequence of driver actions exercises this scenario
for this specific agent CLI?

**File:** `.claude/skills/ir:onboard-agent/scenarios.json` →
`scenarios[].by_adapter[<agent>]`.

**Tool:** `/ir:onboard-agent recipe <agent> <scenario-id>` (see
[`recipe/SKILL.md`](recipe/SKILL.md) for the per-step guide).

**Output shape:**

```jsonc
"by_adapter": {
  "claudecode": {
    "applicable": true,
    "preconditions": [...],
    "setup": [...],
    "script": [
      {"type": "send", "text": "Reply with: ok"},
      {"type": "wait_turn"},
      {"type": "sleep", "seconds": 4}
    ],
    "settings": {},
    "timeout_seconds": 90,
    "verify": ["plain-English bullet for the operator..."]
  }
}
```

**Step types** (from `scripts/drive-claudecode-interactive.sh` and
peers):

- `send` — types text + Enter, increments turn counter
- `wait_turn` — blocks until expected turn count reached
- `sleep` — wall-clock pause (use ≥4s after last interaction for
  classifier debounce)
- `interrupt` — sends Escape (claudecode binds Esc to cancel turn)
- `keys` — raw tmux key sequence ("Escape Escape", "Up Up Enter")
  for driving picker UIs
- `restart` — tears down + relaunches the agent (used in session-end /
  session-resume chains)
- `resume` — relaunch with `--resume <uuid>` (claudecode-specific)
- `sigkill` — SIGKILL the agent (crash-mid-turn scenarios)
- `exit_clean` — C-d to exit cleanly
- `reset_session` — `/clear` (claudecode-specific)

**Why this is per-adapter:** same scenario shape (e.g. "session-start")
needs different driver scripts per agent. claudecode needs a 1-token
nudge to materialize its lazy transcript; codex writes the transcript
on launch; aider uses headless `--print`. The spec (Stage 3) is
agent-agnostic.

**Success criterion:** the script terminates cleanly under
`timeout_seconds` and the `verify` items are operator-readable
sanity checks (machine-checkable assertions belong in the spec).

---

## Stage 3 — Spec (expected.jsonl)

**Question:** What lifecycle events must the daemon emit when the
recipe runs?

**File:**
`replaydata/agents/<agent>/scenarios/<scenario>/expected.jsonl`.

**Tool:** `/ir:onboard-agent spec <agent> <scenario-id>` (see
[`spec/SKILL.md`](spec/SKILL.md) for the phase DSL reference + the
authoring walkthrough).

**Shape:** one meta line + N phase lines:

```jsonl
{"schema_version":1,"scenario_id":"...","source":".specs/agent-scenarios.md → Feature: ...","notes":"..."}
{"phase":"session_birth","expected_state":"ready","relative_to":"start","max_delay_ms":1000,"text":"..."}
{"phase":"v1_turn_start","expected_state":"working","relative_to":"session_birth","same_session_as":"session_birth","text":"..."}
{"phase":"v2_session_birth","expected_state":"ready","relative_to":"v1_turn_end","new_session":true,"max_delay_ms":15000,"text":"..."}
```

**Phase DSL field reference:**

| Field                | Purpose                                                      |
|---                   |---                                                            |
| `phase`              | Unique name; used as anchor target for later phases            |
| `expected_state`     | `ready`/`working`/`waiting` — matches a state_transition       |
| `kind`               | Event kind like `transcript_removed`, `pid_discovered`         |
| `relative_to`        | Anchor phase name (or `start`); future phases reference this   |
| `max_delay_ms`       | Phase event must arrive within this delay of the anchor        |
| `duration_at_least_ms` | State must persist this long without flipping away           |
| `same_session_as`    | Pin to a previous phase's matched session_id                   |
| `new_session`        | Match must be on a session_id NOT seen by any earlier phase    |
| `invariants`         | Negative DSL assertions over the phase's window                |
| `text`               | Spec wording for the operator panel                            |

`expected_state` and `kind` are mutually exclusive. `same_session_as`
and `new_session` are mutually exclusive. `same_session_as` is the
strongest tool for asserting identity across multi-step arcs;
`new_session` is the strongest proxy for "content was reset" (new
UUID = fresh transcript file).

**Critical rule:** never use absolute `ts_offset_ms`. Express timing
as `max_delay_ms` relative to a previously-declared phase. The same
spec must validate every re-recording regardless of when it ran.

**`known_failing: true` in meta** — set this when the spec describes
expected behavior the daemon doesn't yet deliver. The validator still
runs the spec; `replay-fixtures.sh` logs the failure rather than
erroring out. Required signal for documented daemon gaps. Drop the
flag the moment the gap closes (the test will then error to remind
you).

**Success criterion:**

- `expected.jsonl` parses cleanly
- Validator can express what the spec actually says (if you find
  yourself adding workarounds, the DSL probably needs an extension,
  not the spec)
- The first recording will produce a measurable pass/fail per phase

---

## Stage 4 — Recording

**Question:** What does the daemon actually emit when the recipe runs
against a live agent?

**Files:**

- `replaydata/agents/<agent>/scenarios/<scenario>/events.jsonl`
- `replaydata/agents/<agent>/scenarios/<scenario>/transcript.jsonl`
- `replaydata/agents/<agent>/scenarios/<scenario>/manifest.json`
- `replaydata/agents/<agent>/scenarios/<scenario>/recordings/<TS>_irrlichd-<ver>/` (previous recordings, archived)

**Tool:** `/ir:onboard-agent record <agent> <scenario>` (see
[`record/SKILL.md`](record/SKILL.md) for `--attach` mode, the
preconditions, and the determinism re-record check). Wraps
`scripts/run-cell.sh` + `tools/promote-recording.sh`.

**Prerequisites:**

- `irrlichd --record` is running. For dev, build with
  `tools/build-dev.sh` (injects git sha into the version string so
  the manifest captures which build produced the recording) and
  start with `core/bin/irrlichd --record`.
- Working tree is clean (precheck refuses uncommitted replaydata
  changes).
- Agent CLI is on PATH and authenticated.

**What promote-recording.sh does:**

1. Archives the previous top-level recording into
   `recordings/<previous-start-ts>_irrlichd-<daemon-ver>/` along
   with a `manifest.json` (daemon version, agent CLI version, recipe
   hash, frozen expected pass rate).
2. Copies the staged recording into the scenario root.
3. Writes a new top-level `manifest.json` describing the latest.
4. Re-runs the validator against the new recording. **Exits non-zero
   if validation fails** unless the spec is marked `known_failing`.

**Rollback:** `mv recordings/<latest>/{events,transcript}.jsonl ./`.

**Success criterion:** events.jsonl + transcript.jsonl are present
under the scenario root and structural events (state_transitions,
transcript_new/_removed, process_exited) match the recipe's `verify`
items. A timeout or driver error is a recipe bug; tighten the recipe
(more sleep, different step ordering) and re-record.

---

## Stage 5 — Validation

**Question:** Does the recording satisfy the spec?

**Tool:** `/ir:onboard-agent validate <agent> <scenario>` (see
[`validate/SKILL.md`](validate/SKILL.md) for the decision tree and
drift-detection loop). Wraps the `expected-validate` binary. To
run across the full tree at once: `tools/replay-fixtures.sh`.

**Output:** `N/M phases passed`, per-phase reasons, structured JSON
suitable for the viewer's Spec expectations panel.

**Decision tree:**

| Result                              | Action                                                |
|---                                   |---                                                    |
| All phases pass                      | Done. Update matrix to `irrlicht_observes: "yes"`.    |
| Some fail, `known_failing` set       | Documented daemon gap. Stays in the spec. File issue. |
| Some fail, no `known_failing`        | Choose: tighten recipe / fix daemon / fix spec        |
| All pass, but `known_failing` set    | Gap closed. Drop the flag immediately.                |
| Validator error (`validator_error`)  | Spec is malformed; fix `expected.jsonl`.              |

**Three honest reasons a phase fails:**

1. **Recipe doesn't exercise the spec** — the driver's actions don't
   produce the asserted events. Tighten timing, add a `keys` step
   for picker navigation, etc. Re-record.
2. **Daemon drifted from spec** — the agent emits the events but the
   daemon parses/classifies them wrong. File an issue, possibly fix
   the daemon (see Iteration Loops below). DO NOT silently rebase
   `expected.jsonl` to match the regression.
3. **Spec is overspecified** — asserts something not in the spec's
   wording (e.g. exact step counts, internal flag names). Loosen
   the assertion to match what `.specs/agent-scenarios.md` actually
   says.

---

## Iteration loops

### Recording loop (Stages 4-5)

```
record → validate → fail? tighten recipe → record → validate ...
```

Tighten recipe means: more sleep between sends, different driver step
ordering, fix counter drift after non-turn slash commands.

### Daemon-fix loop (when validation surfaces a real gap)

```
1. expected.jsonl spec asserts the right behavior  (Stage 3)
2. validator fails with precise message (e.g. "no transcript_removed
   for session <UUID>")                            (Stage 5)
3. fix the daemon (core/...)
4. tools/build-dev.sh                              (rebuild)
5. swap running daemon (kill old, start core/bin/irrlichd --record)
6. re-record (Stage 4)
7. validator passes; drop known_failing flag      (Stage 3)
```

The spec is the regression contract. Don't relax it because the
daemon doesn't pass; fix the daemon.

### Drift-detection loop (over time, across daemon versions)

The viewer's recording-history dropdown shows the current latest plus
every archived recording with its `manifest.json` (daemon_version +
expected_pass_rate at archive time). Selecting an archive re-runs
the CURRENT spec against the archived events. Two outcomes:

- Frozen pass rate matches fresh evaluation → no drift.
- Frozen passed but fresh fails → the daemon went backward (or the
  spec moved forward without re-recording the archives). Investigate.

---

## Walking the pipeline for ONE cell — concrete example

Taking `claudecode/session-reset` as a worked example:

| Stage | Artifact                                                                                                           | Tool                         |
|---    |---                                                                                                                 |---                            |
| 1     | matrix entry `irrlicht_observes: "yes"` after issue #169 daemon fix                                               | `/assess` (or `/survey`)      |
| 2     | `scenarios.json` `by_adapter.claudecode.script` — send + wait_turn + sleep + reset_session + send + wait_turn      | `/recipe`                     |
| 3     | `replaydata/.../session-reset/expected.jsonl` — 9 phases including `same_session_as: v1_session_handoff` and `new_session: true` on v2 | `/spec`                       |
| 4     | `replaydata/.../session-reset/{events,transcript}.jsonl` + `manifest.json` + N archived `recordings/<ts>_irrlichd-<ver>/` | `/record`                     |
| 5     | `9/9 phases passed`                                                                                                | `/validate`                   |

On the overview, this row's claudecode cell now renders `●● ✎ § N ✓`
— full pipeline lit, no drift outline.

---

## Out of scope

- **Content-level assertions** (e.g. "the new transcript has no
  carryover from the old"). irrlicht observes lifecycle events from
  events.jsonl, not transcript content. If a future check is needed,
  a separate tool consuming transcript.jsonl is the right shape — not
  an extension of the events validator.
- **Multi-cell batching.** Recipes are per-cell; recording is per-cell;
  validation is per-cell. Surveying is per-agent (covers all scenarios
  for one agent at once) but that's the only batched step.
- **Daemon fixes outside the spec's regression contract.** Internal
  refactors that don't affect the events.jsonl emission shape don't
  need to touch the cell pipeline.
