#!/usr/bin/env bash
# shard-lib.sh — the single home for reading the onboarding catalog. The
# scenario catalog is one file replaydata/scenarios.json =
# {"meta": {...}, "scenarios": [...]}; each (scenario, adapter) cell is a
# metadata.json at replaydata/agents/<adapter>/scenarios/<id>_<name>/metadata.json
# (folders are prefixed by the scenario's dashed id; every metadata.json carries
# a scenario_id). Every catalog/cell `jq` read in the rig goes through here, so
# the recipe-hash form and the folder resolution have ONE owner (the bash twins
# of Go's recipeHashOf and LoadAdapterCells / FolderForScenario).
#
# Sourced as a library; functions echo to stdout and treat absence as empty
# output. MUST NOT call `set` at top level (it would leak options into a
# sourcing shell).
#
# Path resolution order:
#   1. $IR_SCENARIOS_FILE / $IR_AGENTS_DIR  — explicit overrides (lib unit tests)
#   2. $REPO_ROOT/replaydata/{scenarios.json,agents}
#   3. <this file>/../../../../../replaydata/{scenarios.json,agents} — up 5

# scenarios_file → path to the consolidated catalog (replaydata/scenarios.json).
scenarios_file() {
  if [[ -n "${IR_SCENARIOS_FILE:-}" ]]; then echo "$IR_SCENARIOS_FILE"; return; fi
  if [[ -n "${REPO_ROOT:-}" ]]; then echo "$REPO_ROOT/replaydata/scenarios.json"; return; fi
  ( cd "$(dirname "${BASH_SOURCE[0]}")/../../../../.." && echo "$PWD/replaydata/scenarios.json" )
}

# agents_dir → the replaydata/agents root (sibling of scenarios.json).
agents_dir() {
  if [[ -n "${IR_AGENTS_DIR:-}" ]]; then echo "$IR_AGENTS_DIR"; return; fi
  echo "$(dirname "$(scenarios_file)")/agents"
}

# scenario_global <coverage_id> → the scenario's global object from the catalog
# (compact JSON), or empty when absent.
scenario_global() {
  local f; f="$(scenarios_file)"
  [[ -f "$f" ]] || return 0
  jq -c --arg n "$1" '.scenarios[] | select(.name==$n)' "$f" 2>/dev/null
}

# scenario_id_dashed <coverage_id> → the scenario's dashed id (e.g. 5.4 → 5-4),
# or empty. Used to compute the standard folder name for a not-yet-recorded cell.
scenario_id_dashed() {
  local f; f="$(scenarios_file)"
  [[ -f "$f" ]] || return 0
  jq -r --arg n "$1" '.scenarios[] | select(.name==$n) | .id | gsub("\\.";"-")' "$f" 2>/dev/null
}

# agent_cell_file <coverage_id> <adapter> → path to the cell's metadata.json,
# located by scanning the adapter's scenarios/ for a metadata.json whose
# scenario_id == <coverage_id>. Empty when no cell exists.
agent_cell_file() {
  local ad f sid; ad="$(agents_dir)/$2/scenarios"
  for f in "$ad"/*/metadata.json; do
    [[ -f "$f" ]] || continue
    sid="$(jq -r '.scenario_id // empty' "$f" 2>/dev/null)"
    [[ "$sid" == "$1" ]] && { echo "$f"; return; }
  done
}

# shard_recipe <coverage_id> <adapter>
#   → the cell's recipe block (.details.recipe) as COMPACT JSON with source key
#     order preserved — byte-identical to the Go recipeHashOf input. Empty when
#     absent. This is the authoritative recipe-hash input.
shard_recipe() {
  local f; f="$(agent_cell_file "$1" "$2")"
  [[ -n "$f" && -f "$f" ]] || return 0
  jq -c '.details.recipe // empty' "$f" 2>/dev/null
}

# shard_has_recipe <coverage_id> <adapter> → exit 0 iff a recipe block exists.
shard_has_recipe() { [[ -n "$(shard_recipe "$1" "$2")" ]]; }

# shard_has_assessment <coverage_id> <adapter> → exit 0 iff the cell carries an
# assessment block (.details.assessment in metadata.json).
shard_has_assessment() {
  local f; f="$(agent_cell_file "$1" "$2")"
  [[ -n "$f" && -f "$f" ]] || return 1
  [[ "$(jq -r '.details.assessment | if . == null then "n" else "y" end' "$f" 2>/dev/null)" == "y" ]]
}

# shard_cell <coverage_id> <adapter>
#   → the run-cell projection: scenario-level description/requires/verify (from
#     the catalog) plus the recipe's per-adapter fields (from metadata.json),
#     flattened (the legacy CELL_JSON shape). Empty when the cell is absent.
shard_cell() {
  local g cf; g="$(scenario_global "$1")"; cf="$(agent_cell_file "$1" "$2")"
  [[ -n "$g" && -n "$cf" && -f "$cf" ]] || return 0
  jq -cn --argjson scen "$g" --slurpfile c "$cf" '
    ($c[0].details.recipe // {}) as $r |
    {description: $scen.description, requires: $scen.requires, verify: $scen.verify,
     applicable: $r.applicable, scope_note: $r.scope_note, notes: $r.notes,
     partner_adapter: $r.partner_adapter, prompt: $r.prompt, script: $r.script,
     settings: $r.settings, timeout_seconds: $r.timeout_seconds}
  ' 2>/dev/null
}

# shard_folder <coverage_id> <adapter>
#   → the on-disk recording folder name for the cell: the folder of its existing
#     metadata.json, or — for a not-yet-recorded cell — the computed standard
#     folder <id-dashed>_<coverage_id>. The bash twin of Go's FolderForScenario.
shard_folder() {
  local f; f="$(agent_cell_file "$1" "$2")"
  if [[ -n "$f" && -f "$f" ]]; then
    basename "$(dirname "$f")"; return
  fi
  local idd; idd="$(scenario_id_dashed "$1")"
  if [[ -n "$idd" ]]; then echo "${idd}_$1"; else echo "$1"; fi
}

# shard_recipe_dir_names <adapter>
#   → every legitimate on-disk folder name for the adapter's recordings: each
#     folder that holds a metadata.json. Sorted-unique. A recorded dir NOT in
#     this set is an orphan (no cell to --re-record from).
shard_recipe_dir_names() {
  local ad f; ad="$(agents_dir)/$1/scenarios"
  for f in "$ad"/*/metadata.json; do
    [[ -f "$f" ]] && basename "$(dirname "$f")"
  done | sort -u
}

# shard_coverage_for_dir <dir-name> <adapter>
#   → the coverage_id that owns a recording dir: the scenario_id of the
#     metadata.json at scenarios/<dir-name>/, else <dir-name> itself.
shard_coverage_for_dir() {
  local f; f="$(agents_dir)/$2/scenarios/$1/metadata.json"
  if [[ -f "$f" ]]; then
    local sid; sid="$(jq -r '.scenario_id // empty' "$f" 2>/dev/null)"
    [[ -n "$sid" ]] && { echo "$sid"; return; }
  fi
  echo "$1"
}

# meta_transcript_ext <adapter> → transcript file extension (default "jsonl").
meta_transcript_ext() {
  local f; f="$(scenarios_file)"
  [[ -f "$f" ]] || { echo jsonl; return; }
  jq -r --arg a "$1" '.meta.transcript_extensions[$a] // "jsonl"' "$f" 2>/dev/null
}

# meta_min_version <adapter> → pinned minimum CLI version, or empty.
meta_min_version() {
  local f; f="$(scenarios_file)"
  [[ -f "$f" ]] || return 0
  jq -r --arg a "$1" '.meta.min_versions[$a] // empty' "$f" 2>/dev/null
}
