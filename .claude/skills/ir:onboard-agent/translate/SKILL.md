---
name: ir:onboard-agent/translate
description: >
  Mode D — per-cell scenario translation. Given one (agent, scenario)
  cell from the coverage matrix, produce a deterministic recording
  recipe (preconditions, exact driver steps, irrlicht-side verify
  assertions) and append it to .claude/skills/ir:onboard-agent/scenarios.json.
  Invoked as `/ir:onboard-agent translate <agent> <scenario-id>`.
---

# Mode D: per-cell translation

Reads the prose spec for one scenario, looks up the agent's
applicability verdict, consults the adapter's transport knowledge,
and emits a recipe precise enough to run the recording repeatedly and
get mostly the same output every time.

The recipe goes into `.claude/skills/ir:onboard-agent/scenarios.json` —
the same file `run-cell.sh` reads when you later record the cell. The
viewer's scenario detail page renders the new fields automatically.

## Invocation

```
/ir:onboard-agent translate <agent> <scenario-id>
```

- `<agent>` — the adapter slug (`claudecode`, `codex`, `pi`, `aider`,
  `opencode`).
- `<scenario-id>` — the kebab id used in `.specs/agent-scenarios-coverage.json`
  (e.g. `session-start`, `user-esc-interrupt`, `auto-executed-tool-call`).

Worked example committed: `claudecode × session-start` —
`.claude/skills/ir:onboard-agent/scenarios.json` → `scenarios[]` entry
where `coverage_id == "session-start"`. Read it to see the schema
applied end-to-end.

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

A heading can have multiple Scenario/Expected sub-blocks (e.g.
`session-end` has three variants). Translate the **primary** block
(usually the first) and reference the others in `notes`.

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

### Step 3 — Read the adapter's transport knowledge

For the chosen agent, read in order:

1. `core/adapters/inbound/agents/<agent>/config.go` — `ProcessName`,
   `TranscriptFilename`, `Capabilities`, and any `DiscoverPID`
   wiring. Defines what irrlicht sees.
2. `.claude/skills/ir:onboard-agent/scripts/drive-<agent>-interactive.sh`
   — the supported step grammar (`send` / `wait_turn` / `sleep` /
   `interrupt` / `slash`) and CLI flags the driver passes. Your
   `script` must stay inside this grammar.
3. `.claude/skills/ir:onboard-agent/install-instructions.md` — any
   per-agent gates (LM Studio for aider, API auth for codex/claudecode,
   etc.). These become `preconditions` entries.

For the headless variant (`drive-<agent>.sh`, single-shot
`--print`-style invocation), the recipe uses `prompt: "..."` instead
of `script`. Pick interactive when:

- The scenario involves more than one user turn.
- The scenario sends an interrupt or slash command mid-turn.
- The scenario is idle-observation (no prompts at all — interactive
  with a sleep-only script is the canonical pattern).

Pick headless for single-turn deterministic prompts where the model's
output doesn't matter.

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

### Step 5 — Write the recipe

Insert the JSON entry into `scenarios.json -> scenarios[]`. Append
near related scenarios (session-start near session-end, etc.) so the
file stays readable. Run `jq '.' scenarios.json > /dev/null` to
confirm the JSON is valid.

If the agent's row already exists (e.g. you're translating a second
cell for the same scenario), add to `by_adapter` instead of creating a
duplicate entry.

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

- **Don't add `send` steps to an idle-observation scenario.** The
  model's reply is variance you don't need; the spec didn't ask for
  it.
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
