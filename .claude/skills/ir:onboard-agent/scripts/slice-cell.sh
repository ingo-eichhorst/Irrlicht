#!/usr/bin/env bash
# slice-cell.sh — print just the catalog slices one (scenario, adapter)
# cell needs, instead of making a subagent read three whole catalogs.
#
# A single recipe/assess/spec call only touches ~1/44th of each catalog
# (scenarios.json ~60k tok, scenario-meanings.md ~11k, coverage ~10k).
# This emits three labeled sections — the scenario entry + that adapter's
# recipe, the scenario-meanings block, and the coverage cell — and nothing
# else, so the caller spends ~3k tokens where a full read spent ~80k.
#
# Usage:
#   slice-cell.sh <scenario-id> <adapter>
#
#   scenario-id: the kebab id (e.g. session-start, auto-executed-tool-call).
#                Matched against scenarios.json `name`, mirroring run-cell.sh
#                (`select(.name == $s)`); falls back to `coverage_id` so the
#                7 scenarios whose name != coverage_id still resolve.
#   adapter:     claudecode | codex | pi | aider | opencode
#
# The scenario entry's own `coverage_id` (which == name for recorded cells)
# is then used to key the scenario-meanings block and the coverage cell, so
# all three slices describe the same cell even when name != coverage_id.
#
# Exit 1 when the scenario isn't in scenarios.json or has no
# scenario-meanings block — the same STOP signal the SKILL.md steps expect.

set -euo pipefail

if [[ $# -ne 2 ]]; then
  echo "usage: slice-cell.sh <scenario-id> <adapter>" >&2
  exit 2
fi

SCENARIO="$1"
ADAPTER="$2"

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
SKILL_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
SCENARIOS_JSON="$SKILL_DIR/scenarios.json"
MEANINGS_MD="$SKILL_DIR/scenario-meanings.md"
COVERAGE_JSON="$SKILL_DIR/agent-scenarios-coverage.json"

fail() {
  echo "slice-cell: $*" >&2
  exit 1
}

# Match by name first (what run-cell.sh records against), then by
# coverage_id. Reused for both the projection and the coverage_id lookup.
JQ_MATCH='([.scenarios[] | select(.name == $s)]) as $byname
  | (if ($byname | length) > 0 then $byname
     else [.scenarios[] | select(.coverage_id == $s)] end)'

# --- 1. scenarios.json entry + this adapter's recipe -------------------
SCENARIO_SLICE="$(jq --arg s "$SCENARIO" --arg a "$ADAPTER" "
  $JQ_MATCH
  | .[]
  | {name, description, coverage_id, idle_only, requires, verify, recipe: .by_adapter[\$a]}
" "$SCENARIOS_JSON")"

if [[ -z "$SCENARIO_SLICE" ]]; then
  # The arg order here is <scenario-id> <adapter>, scenario first — the
  # reverse of run-cell.sh / the dispatcher (`<agent> <scenario>`). If the
  # first arg is an adapter slug, the caller most likely swapped them.
  case "$SCENARIO" in
    claudecode|codex|pi|aider|opencode)
      fail "'$SCENARIO' is an adapter slug, not a scenario. Args are <scenario-id> <adapter> (scenario first) — did you swap them?" ;;
    *)
      fail "scenario not found in scenarios.json: $SCENARIO (run scenario-create first?)" ;;
  esac
fi

# Canonical coverage id for the meanings + coverage lookups. Defaults to
# name when no coverage_id is set; equals name for every recorded cell.
COVERAGE_ID="$(jq -r --arg s "$SCENARIO" "
  $JQ_MATCH
  | .[0]
  | if has(\"coverage_id\") and .coverage_id != null then .coverage_id else .name end
" "$SCENARIOS_JSON")"

# When the input is a scenario NAME whose coverage_id differs (the 7
# name != coverage_id scenarios), the recipe below is this scenario's but
# the meanings + coverage sections describe the shared coverage cell —
# which may belong to a different same-coverage_id scenario. Warn so the
# caller doesn't author a spec against a contradictory meaning. Pass the
# coverage id (what the matrix + run-cell.sh use) for a consistent slice.
if [[ "$COVERAGE_ID" != "$SCENARIO" ]]; then
  echo "slice-cell: note — '$SCENARIO' resolves to coverage_id '$COVERAGE_ID'; the scenario-meanings and coverage sections below describe cell '$COVERAGE_ID', not '$SCENARIO' specifically. Pass the coverage id for a self-consistent slice." >&2
fi

echo "=== scenarios.json — $SCENARIO / $ADAPTER ==="
echo "$SCENARIO_SLICE"
echo

# --- 2. scenario-meanings.md `### <coverage_id>` block -----------------
# awk: print from the matching header up to (not including) the next `### `.
MEANINGS_BLOCK="$(awk -v id="$COVERAGE_ID" '
  $0 == "### " id { grab = 1; print; next }
  grab && /^### / { exit }
  grab { print }
' "$MEANINGS_MD")"

if [[ -z "$MEANINGS_BLOCK" ]]; then
  fail "no '### $COVERAGE_ID' block in scenario-meanings.md (run scenario-create first?)"
fi

echo "=== scenario-meanings.md — ### $COVERAGE_ID ==="
echo "$MEANINGS_BLOCK"
echo

# --- 3. agent-scenarios-coverage.json cell -----------------------------
COVERAGE_CELL="$(jq --arg s "$COVERAGE_ID" --arg a "$ADAPTER" '
  .scenarios[] | select(.id == $s) | .coverage[$a]
' "$COVERAGE_JSON")"

echo "=== agent-scenarios-coverage.json — $COVERAGE_ID / $ADAPTER ==="
if [[ -z "$COVERAGE_CELL" || "$COVERAGE_CELL" == "null" ]]; then
  echo '"no coverage entry for this (scenario, adapter) — flag to maintainer"'
else
  echo "$COVERAGE_CELL"
fi
