# Cells the Desktop driver cannot reach for reasons outside its control grammar

`recipe-census.txt` plans every recipe against the driver's step grammar and
names the missing **control** for each one it cannot drive. Three cells are
unreachable for reasons that census cannot express, because nothing is missing
from the grammar. They are recorded here so the verdict cites a re-runnable
fact rather than prose.

Verified 2026-09-06.

## 1-7 cloud-background-agent — not-applicable

```sh
jq -r '.details.recipe.applicable, .details.recipe.notes' \
  replaydata/agents/claudecode/scenarios/1-7_cloud-background-agent/metadata.json
```

```
false
agent_supports=no per coverage matrix — claudecode is local-only.
Translator stops here; no recipe.
```

The cell has no recipe at all. It is `not-applicable` for **every** claudecode
profile, `cli-local` included — not a Desktop gap. The only environment the
Desktop control catalog measures is the one titled "Local"; there is no cloud
control to express.

## 2-9 token-quota-exhausted — not-runnable

```sh
jq -c '{driver: .details.recipe.driver, preconditions: .details.recipe.preconditions}' \
  replaydata/agents/claudecode/scenarios/2-9_token-quota-exhausted/metadata.json
```

The cell does not use the shared driver. It names a bespoke recorder that
"builds the mock, launches it, drives claude under `--bare`", and its
preconditions include a mock listening on `localhost:18765`.

Three things it needs, none of which Claude Desktop exposes:

1. a `--bare` CLI launch — Desktop has no equivalent launch shape,
2. an environment path to point the agent at the mock — Desktop authenticates
   through its own OAuth, and the driver has no env path,
3. a bespoke recorder in place of the shared driver.

`desktop_profile_validate_cell` independently refuses `env`, `mock` and
`bare_mode` for this profile, so the cell is refused before the driver starts.

## 4-2 multiple-agents-same-workspace — not-runnable

```sh
jq -r '.details.recipe.partner_adapter' \
  replaydata/agents/claudecode/scenarios/4-2_multiple-agents-same-workspace/metadata.json
grep -c 'desktop-local' tools/onboarding-factory/scripts/run-cell-multi.sh
```

```
codex
0
```

The cell needs two adapters driven concurrently against one shared workspace
under a single daemon. `run-cell-multi.sh` is the harness that does this, and
it has no `desktop-local` awareness whatsoever — zero occurrences. `run-cell.sh`
separately refuses `--attach` for this profile, which is the other way a
Desktop session could have joined an existing daemon.

The claudecode leg is drivable on its own. What is missing is the harness, not
a control — which is why this cell does not appear in the recipe census.

## 2-14 turn-aborted-by-error — not-runnable

```sh
jq -c '.details.recipe | keys' \
  replaydata/agents/claudecode/scenarios/2-14_turn-aborted-by-error/metadata.json
tools/onboarding-factory/scripts/run-cell.sh --execution-profile desktop-local \
  claudecode turn-aborted-by-error
```

```
["applicable","driver","preconditions","settings","setup","timeout_seconds","verify"]
cell has neither prompt nor script: scenario=turn-aborted-by-error adapter=claudecode
```

The same shape as 2-9: the recipe names a bespoke recorder
(`record-turn-aborted-by-error.sh`) instead of a prompt or a script, so there
is nothing for the shared driver to drive. The rig refuses it before any
profile-specific check runs — this is not a Desktop control gap, and the cell
is equally undrivable through the shared `cli-local` path.
