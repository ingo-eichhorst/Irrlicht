#!/usr/bin/env bash
# catalog-drift.sh — the bijection gate across the scenario catalogs (#496 RC5).
#
# Four catalogs describe the matrix and they used to drift silently:
#   - catalog[]      in scenarios.json — the matrix axis (rows the overview renders)
#   - scenarios[]    in scenarios.json — the recipe registry (coverage_id per recipe)
#   - rollup         in agent-scenarios-coverage.json — editorial coverage
#   - .specs/agent-scenarios.md — the maintainer's SOURCE catalog (gitignored;
#     absent in CI/worktree — checked only when present)
#
# This gate makes the named drift a HARD failure instead of a printed note:
#   ERROR — a scenarios[] coverage_id that names no catalog[] row (recipe → phantom cell)
#   ERROR — a rollup id that names no catalog[] row (editorial row → phantom cell)
#   ERROR — a catalog[] row absent from the rollup (cell with no editorial coverage)
#   WARN  — a catalog[] row with no scenarios[] recipe (a cell awaiting authoring;
#           `--stub` emits a paste-ready scenarios[] row for it)
#   WARN  — (when .specs is present) a source id with no catalog[] row
#
# Sourced as a library (functions only; see catalog-drift_test.sh) AND runnable
# as a CLI. MUST NOT call `set` (it would leak options into a sourcing shell).

# cd_catalog_ids / cd_scenario_coverage_ids / cd_rollup_ids <file> → sorted-unique ids
cd_catalog_ids()           { jq -r '.catalog[].id'          "$1" 2>/dev/null | sort -u; }
cd_scenario_coverage_ids() { jq -r '.scenarios[].coverage_id' "$1" 2>/dev/null | sort -u; }
cd_rollup_ids()            { jq -r '.scenarios[].id'         "$1" 2>/dev/null | sort -u; }

# cd_errors <scenarios.json> <rollup.json>
#   → prints each hard drift, one per line. Returns 0 when clean, 1 otherwise.
cd_errors() {
  local sj="$1" rj="$2" rc=0 line
  local cat sc ro
  cat="$(cd_catalog_ids "$sj")"
  sc="$(cd_scenario_coverage_ids "$sj")"
  ro="$(cd_rollup_ids "$rj")"
  while IFS= read -r line; do [[ -n "$line" ]] && { echo "scenarios[] coverage_id '$line' has no catalog[] row"; rc=1; }; done \
    < <(comm -23 <(printf '%s\n' "$sc" | sed '/^$/d') <(printf '%s\n' "$cat" | sed '/^$/d'))
  while IFS= read -r line; do [[ -n "$line" ]] && { echo "rollup id '$line' has no catalog[] row"; rc=1; }; done \
    < <(comm -23 <(printf '%s\n' "$ro" | sed '/^$/d') <(printf '%s\n' "$cat" | sed '/^$/d'))
  while IFS= read -r line; do [[ -n "$line" ]] && { echo "catalog[] row '$line' is missing from the rollup (no editorial coverage)"; rc=1; }; done \
    < <(comm -23 <(printf '%s\n' "$cat" | sed '/^$/d') <(printf '%s\n' "$ro" | sed '/^$/d'))
  return "$rc"
}

# cd_recipeless_catalog_ids <scenarios.json>
#   → catalog ids with no scenarios[] recipe (a cell awaiting authoring).
cd_recipeless_catalog_ids() {
  comm -23 <(cd_catalog_ids "$1") <(cd_scenario_coverage_ids "$1")
}

# cd_stub <scenarios.json> <catalog-id>
#   → a paste-ready scenarios[] stub object for a recipe-less catalog cell.
cd_stub() {
  local sj="$1" id="$2"
  jq -n --arg id "$id" --argjson row "$(jq -c --arg id "$id" '.catalog[] | select(.id==$id)' "$sj")" '
    {name: $id, coverage_id: $id,
     description: ("STUB — catalog cell \"\($row.feature // $id)\" (\($row.section // "?")) awaiting a recipe (#496 RC5). Author requires + by_adapter via scenario-create/implement."),
     requires: []}'
}

# CLI: catalog-drift.sh [--stub] [scenarios.json] [rollup.json] [specs-md]
#   exit 0 — no hard drift; exit 1 — at least one ERROR (listed on stderr).
if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
  set -uo pipefail
  STUB=0; [[ "${1:-}" == "--stub" ]] && { STUB=1; shift; }
  SK="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"          # …/ir:onboard-agent (lib → scripts → skill root)
  REPO="$(cd "$SK/../../.." && pwd)"
  sj="${1:-$SK/scenarios.json}"
  rj="${2:-$SK/agent-scenarios-coverage.json}"
  specs="${3:-$REPO/.specs/agent-scenarios.md}"

  command -v jq >/dev/null || { echo "catalog-drift: jq is required" >&2; exit 2; }

  echo "== catalog-drift gate ==" >&2
  rc=0
  if errs="$(cd_errors "$sj" "$rj")"; then
    echo "  catalog ⟺ rollup ⟺ scenarios: no phantom/invisible rows" >&2
  else
    while IFS= read -r e; do [[ -n "$e" ]] && echo "  ERROR: $e" >&2; done <<< "$errs"
    rc=1
  fi

  # WARN: catalog cells with no recipe yet (tracked TODOs, not drift).
  recipeless="$(cd_recipeless_catalog_ids "$sj")"
  if [[ -n "$recipeless" ]]; then
    echo "  note: catalog cells awaiting a scenarios[] recipe (author via scenario-create):" >&2
    while IFS= read -r id; do [[ -n "$id" ]] && echo "    - $id" >&2; done <<< "$recipeless"
    if [[ "$STUB" -eq 1 ]]; then
      echo "  --stub: paste these into scenarios.json scenarios[]:" >&2
      while IFS= read -r id; do [[ -n "$id" ]] && cd_stub "$sj" "$id"; done <<< "$recipeless"
    fi
  fi

  # SOURCE catalog leg (#496 RC5): checked only when .specs is present (it is
  # gitignored, so absent in CI). Best-effort — extract backtick-quoted
  # kebab ids and warn on any with no catalog[] row.
  if [[ -f "$specs" ]]; then
    cat_ids="$(cd_catalog_ids "$sj")"
    src_ids="$(grep -oE '`[a-z0-9]+(-[a-z0-9]+)+`' "$specs" 2>/dev/null | tr -d '`' | sort -u)"
    missing="$(comm -23 <(printf '%s\n' "$src_ids" | sed '/^$/d') <(printf '%s\n' "$cat_ids" | sed '/^$/d'))"
    if [[ -n "$missing" ]]; then
      echo "  note: source catalog .specs/agent-scenarios.md mentions ids with no catalog[] row (verify — best-effort parse):" >&2
      while IFS= read -r id; do [[ -n "$id" ]] && echo "    - $id" >&2; done <<< "$missing"
    fi
  fi

  echo "" >&2
  if [[ "$rc" -eq 0 ]]; then
    echo "catalog-drift: catalogs are consistent" >&2
    exit 0
  fi
  echo "catalog-drift: catalog drift detected (see ERRORs above) — a row names a phantom cell or a catalog cell is missing editorial coverage" >&2
  exit 1
fi
