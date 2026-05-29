#!/usr/bin/env bash
# shard-lib.sh — the single home for reading the per-scenario shard catalog
# (replaydata/scenarios/<coverage_id>.json + _meta.json). Introduced in #511,
# when the bash recording/authoring pipeline was ported off the legacy
# scenarios.json / capabilities.json / features.json. Every shard `jq` read in
# the rig goes through here, so the recipe-hash form and the variant-folder
# resolution have ONE owner (the bash twins of Go's recipeHashOf and
# resolveScenarioFolderForAgent).
#
# Sourced as a library; functions echo to stdout and treat absence as empty
# output. MUST NOT call `set` at top level (it would leak options into a
# sourcing shell).
#
# The shard directory is resolved once per call, in order:
#   1. $IR_SHARD_DIR                      — explicit override (lib unit tests)
#   2. $REPO_ROOT/replaydata/scenarios    — callers that already set REPO_ROOT
#   3. <this file>/../../../../../replaydata/scenarios — skill scripts/lib → up 5

# shard_dir → the directory holding the shards + _meta.json.
shard_dir() {
  if [[ -n "${IR_SHARD_DIR:-}" ]]; then echo "$IR_SHARD_DIR"; return; fi
  if [[ -n "${REPO_ROOT:-}" ]]; then echo "$REPO_ROOT/replaydata/scenarios"; return; fi
  ( cd "$(dirname "${BASH_SOURCE[0]}")/../../../../.." && echo "$PWD/replaydata/scenarios" )
}

_shard_file() { echo "$(shard_dir)/$1.json"; }
_shard_meta() { echo "$(shard_dir)/_meta.json"; }

# shard_recipe <coverage_id> <adapter>
#   → the cell's recipe block (.agents[$a].details.recipe) as COMPACT JSON with
#     source key order preserved — byte-identical to the Go recipeHashOf input
#     and to the legacy `jq -c '.by_adapter[$a]'`. Empty when absent. This is
#     the authoritative recipe-hash input (see tools/promote-recording.sh).
shard_recipe() {
  local f; f="$(_shard_file "$1")"
  [[ -f "$f" ]] || return 0
  jq -c --arg a "$2" '.agents[$a].details.recipe // empty' "$f" 2>/dev/null
}

# shard_has_recipe <coverage_id> <adapter> → exit 0 iff a recipe block exists.
shard_has_recipe() { [[ -n "$(shard_recipe "$1" "$2")" ]]; }

# shard_has_assessment <coverage_id> <adapter> → exit 0 iff the cell carries an
# assessment block (.agents[$a].details.assessment). Since #511 the assessment
# lives in the shard, not an on-disk assessment.json, so this is the
# completeness check cell-integrity uses.
shard_has_assessment() {
  local f; f="$(_shard_file "$1")"
  [[ -f "$f" ]] || return 1
  [[ "$(jq -r --arg a "$2" '.agents[$a].details.assessment | if . == null then "n" else "y" end' "$f" 2>/dev/null)" == "y" ]]
}

# shard_cell <coverage_id> <adapter>
#   → the run-cell projection: scenario-level description/requires/verify plus
#     the recipe's per-adapter fields, flattened (the legacy CELL_JSON shape).
#     Empty when the shard or its agent cell is absent.
shard_cell() {
  local f; f="$(_shard_file "$1")"
  [[ -f "$f" ]] || return 0
  jq -c --arg a "$2" '
    select(.agents[$a]) |
    (.agents[$a].details.recipe // {}) as $r |
    {description, requires, verify,
     applicable: $r.applicable, scope_note: $r.scope_note, notes: $r.notes,
     partner_adapter: $r.partner_adapter, prompt: $r.prompt, script: $r.script,
     settings: $r.settings, timeout_seconds: $r.timeout_seconds}
  ' "$f" 2>/dev/null
}

# shard_folder <coverage_id> <adapter>
#   → the on-disk recording folder name for the cell: the basename of the
#     shard's recording_dir, falling back to the coverage_id when none is
#     recorded. The bash twin of Go's resolveScenarioFolderForAgent.
shard_folder() {
  local f; f="$(_shard_file "$1")" b=""
  [[ -f "$f" ]] && b="$(jq -r --arg a "$2" '.agents[$a].recording_dir // "" | split("/") | last // ""' "$f" 2>/dev/null)"
  echo "${b:-$1}"
}

# shard_recipe_dir_names <adapter>
#   → every legitimate on-disk dir name for the adapter's recordings: each
#     shard's name (coverage_id) AND its recording_dir basename. Sorted-unique.
#     A recorded dir NOT in this set is an orphan (no shard to --re-record from).
shard_recipe_dir_names() {
  local sd; sd="$(shard_dir)"
  jq -rs --arg a "$1" '
    .[] | select(.agents[$a]) |
    (.name, (.agents[$a].recording_dir // "" | split("/") | last // empty))
  ' "$sd"/*.json 2>/dev/null | sed '/^$/d' | sort -u
}

# shard_coverage_for_dir <dir-name> <adapter>
#   → the coverage_id (shard name) that owns a recording dir: the shard whose
#     name == <dir> or whose recording_dir basename == <dir>; else <dir> itself
#     (already a coverage_id). Used to find the sibling assessment.json dir.
shard_coverage_for_dir() {
  local sd cov; sd="$(shard_dir)"
  cov="$(jq -rs --arg a "$2" --arg d "$1" '
    [ .[] | select(.name == $d or (.agents[$a].recording_dir // "" | split("/") | last) == $d) | .name ] | first // empty
  ' "$sd"/*.json 2>/dev/null)"
  echo "${cov:-$1}"
}

# meta_transcript_ext <adapter> → transcript file extension (default "jsonl").
meta_transcript_ext() {
  local m; m="$(_shard_meta)"
  [[ -f "$m" ]] || { echo jsonl; return; }
  jq -r --arg a "$1" '.transcript_extensions[$a] // "jsonl"' "$m" 2>/dev/null
}

# meta_min_version <adapter> → pinned minimum CLI version, or empty.
meta_min_version() {
  local m; m="$(_shard_meta)"
  [[ -f "$m" ]] || return 0
  jq -r --arg a "$1" '.min_versions[$a] // empty' "$m" 2>/dev/null
}
