---
name: ir:onboard-agent/scenario-create
description: >
  Agent #1 of the ir:onboard-agent pipeline. Adds a brand-new scenario
  ROW to the canonical matrix: a `scenarios[]` + `catalog[]` entry in
  scenarios.json (top-level only — no per-adapter recipe), a
  `scenario-meanings.md` glossary block, a coverage-matrix row with
  every adapter `unknown`, and a new capability in features.json if one
  is needed. One-shot, agent-agnostic, no agent CLI invocation, no live
  recording. Invoked as `/ir:onboard-agent scenario-create <slug>`.
---

# scenario-create (Agent #1)

> **You are running as a focused subagent with no parent context.**
> Everything you need is in this file and the repo. Do not ask the
> dispatcher for clarification — read the files named below, make the
> edits, and return the summary in the "Return contract" section. This
> task spends NO API tokens on agent CLIs and runs NO recording.

## What this does

Adds one new agent-agnostic scenario to the matrix so later stages
(`assess`, then `implement`) have a row to fill. A scenario is defined
ONCE, agent-agnostically, with `requires: [capability]`; per-adapter
recipes and recordings are added later by `implement`. After you run,
the new scenario shows up in the matrix as a row of `unknown` cells —
no recipe, no recording, nothing claimed about any adapter yet.

This is the matrix-ROW counterpart to discovery mode (`--new <slug>`),
which adds matrix COLUMNS (a whole new agent). Don't confuse them.

## Inputs

- `<slug>` — kebab-case scenario id (stable; becomes the fixture dir
  name and the `coverage_id`). E.g. `mid-turn-message-queued`.
- A one-paragraph description of the behavior being captured. If the
  dispatcher didn't pass one, derive it from the slug and state your
  assumption in the summary.

## Files you read

- `.claude/skills/ir:onboard-agent/scenarios.json` — `catalog[]` (public
  scenario list) and `scenarios[]` (declarations + recipes). Read a few
  existing `scenarios[]` entries to copy the shape.
- `.claude/skills/ir:onboard-agent/scenario-meanings.md` — the committed
  prose glossary. Every scenario MUST have a block here (the canonical
  `.specs/agent-scenarios.md` is gitignored/absent in most checkouts, so
  this file is the source-of-record downstream stages read).
- `.claude/skills/ir:onboard-agent/agent-scenarios-coverage.json` — the
  rollup matrix. Read `agents[]` (the adapter list) and one
  `scenarios[]` row for shape.
- `replaydata/agents/features.json` — the canonical capability catalog.
  `requires` IDs must exist here.

## Files you write

1. **`scenarios.json`** — two edits, in this one file:
   - Append a `catalog[]` row: `{id, section, feature}`. Pick the
     `section` from an existing catalog entry that fits (e.g. "Session
     lifecycle", "Tool & permission interaction"); `feature` is the
     human title.
   - Append a `scenarios[]` entry with **top-level fields only**:
     ```jsonc
     {
       "name": "<slug>",
       "coverage_id": "<slug>",
       "description": "<one paragraph: what behavior, why it matters>",
       "requires": ["<capability-id>", ...],
       "verify": { ... }            // agent-agnostic structural invariants
     }
     ```
     **Do NOT add `by_adapter`.** Recipes are `implement`'s job. A
     `scenarios[]` entry without `by_adapter` is valid — it declares the
     scenario and its requirements; `run-cell.sh` simply reports
     "missing-prompt" until a recipe lands.
2. **`scenario-meanings.md`** — append a block in the same format as
   existing blocks (read one first). Required fields:
   - **Essence** — one sentence.
   - **User-observable signal** — what a user SEES (state badge, count,
     link, lifecycle, metric). User-observable only — never internal
     event kinds or classifier rules.
   - **Primitive exercised** — the underlying mechanism + the capability
     it requires (e.g. "Requires `permission_hooks`."). Downstream
     `assess`/`recipe`/`spec` read this field verbatim to pick the
     capability key, so name the `requires` capability here explicitly.
   - **Not to be confused with** — 1–2 sibling scenarios + the
     distinction.
   - **Conceptual flow** — numbered steps.
3. **`agent-scenarios-coverage.json`** — append a `scenarios[]` row:
   ```jsonc
   {
     "id": "<slug>",
     "section": "<same section as the catalog row>",
     "feature": "<same feature title>",
     "coverage": {
       // one entry per slug in the top-level `agents[]` array:
       "<agent>": {"agent_supports": "unknown", "irrlicht_observes": "unknown", "notes": ""}
     }
   }
   ```
   Every adapter starts `unknown`/`unknown` — you are not assessing here.
4. **`replaydata/agents/features.json`** — ONLY if `requires` needs a
   capability that doesn't exist yet. Append a `features[]` entry
   `{id, title, category, description, added_in}` (category = one of the
   existing `categories[]` ids). Set `added_in` to today's date.

## Choosing `requires`

Map the description to capability IDs already in `features.json`. Be
conservative: most scenarios require exactly one capability (often
`headless_mode` for anything driven by a script). Add a NEW capability
only when no existing one names the mechanism — and when you do, the
`scenario-meanings.md` "Primitive exercised" line must mention it so the
assess stage can resolve it.

## Authoring `verify`

Keep it agent-agnostic and structural — the per-adapter `verify` strings
and the machine-checkable `expected.jsonl` come later from `implement`.
A minimal honest block for most scenarios:

```jsonc
"verify": {
  "transitions_topology": ["ready", "working", "ready"],
  "final_state": "ready",
  "tool_calls_max": 0
}
```

Adjust topology/final_state to the description (e.g. a blocking-question
scenario ends `waiting`; a tool scenario allows `tool_calls_max > 0`).
Don't invent matchers that aren't in existing scenarios' `verify` blocks.

## Validation before you return

```bash
SK=.claude/skills/ir:onboard-agent
jq '.' $SK/scenarios.json > /dev/null                      # valid JSON
jq '.' $SK/agent-scenarios-coverage.json > /dev/null       # valid JSON
jq '.' replaydata/agents/features.json > /dev/null         # valid JSON
# every requires id resolves:
comm -23 \
  <(jq -r '.scenarios[] | select(.name=="<slug>") | .requires[]' $SK/scenarios.json | sort -u) \
  <(jq -r '.features[].id' replaydata/agents/features.json | sort -u)
# ^ must print nothing. Any output = a requires id missing from features.json.
```

Confirm the slug now appears in all four artifacts: `catalog[]`,
`scenarios[]`, `scenario-meanings.md`, and the coverage row.

## Return contract

Return ONLY this (≤5 lines), no transcripts:

```
scenario_id: <slug>
capability_ids: [<id>, ...]            # requires; mark any you ADDED to features.json
files_changed: scenarios.json, scenario-meanings.md, agent-scenarios-coverage.json[, features.json]
verify: <one-line summary of the topology you wrote>
next: assess <agent> <slug>  (per adapter, to fill the row)
```

## Anti-patterns

- **Don't write `by_adapter`.** No recipes, no prompts, no scripts. That
  is `implement`'s job and requires per-adapter knowledge you don't have
  here.
- **Don't assess.** Every coverage cell stays `unknown`. You are
  declaring the row exists, not judging any agent against it.
- **Don't skip `scenario-meanings.md`.** Without the block, `assess` and
  `recipe` STOP with "scenario missing from scenario-meanings.md".
- **Don't invent a capability when an existing one fits.** A bloated
  `features.json` makes the matrix harder to reason about. Add one only
  when the mechanism genuinely has no existing key.
- **Don't run a recording or invoke an agent CLI.** This stage is pure
  catalogue editing.
