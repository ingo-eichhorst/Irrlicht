#!/usr/bin/env bash
# catalog-orphan-guard.sh — surface the completeness-gate blind-spot found
# 2026-05-29.
#
# THE BLIND-SPOT: a catalog row whose id has NO matching `scenarios[]` variant
# (an "orphan catalog row" — typically a documentation-only / N.A. scenario with
# no recipe, e.g. architect-editor-pair, provider-failover-midturn,
# quota-burndown) carries no `requires`, so the matrix model cannot evaluate its
# applicability the normal way. It then enumerates the cell ONLY for agents that
# happen to have an on-disk `assessment.json` folder, and SILENTLY SKIPS every
# other agent. The completeness gate therefore reports a column COMPLETE while an
# orphan-row cell sits unassessed and invisible — exactly how
# claudecode/quota-burndown hid (gate said COMPLETE; the cell was never
# enumerated because no assessment existed and no scenarios[] row forced it).
#
# This guard makes those hidden cells loud: for every orphan catalog row it
# checks each onboarded agent (from replaydata/scenarios/_meta.json) for an
# assessment, and FAILS listing every (agent, row) that the gate would skip.
#
# Fix a reported GAP by either (a) assessing the cell (write its assessment.json
# — then the matrix enumerates and dispositions it), or (b) giving the catalog
# row a real `scenarios[]` definition (with `requires`) so the cell is enumerated
# for all agents through the normal applicability path.
#
# CLI: catalog-orphan-guard.sh [scenarios.json] [replaydata-root]
#   exit 0 — no orphan row hides an unassessed cell (or there are no orphans)
#   exit 1 — at least one orphan-row cell is invisible to the completeness gate
#   exit 2 — usage / infra (missing inputs, no jq)
#
# Sourced as a library OR runnable as a CLI. MUST NOT call `set` at top level.

_COG_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

catalog_orphan_guard() {
  local scn="$1" root="$2"
  command -v jq >/dev/null 2>&1 || { echo "catalog-orphan-guard: jq is required" >&2; return 2; }
  [[ -f "$scn" ]] || { echo "catalog-orphan-guard: scenarios.json not found at $scn" >&2; return 2; }
  local meta="$root/scenarios/_meta.json"
  [[ -f "$meta" ]] || { echo "catalog-orphan-guard: _meta.json not found at $meta" >&2; return 2; }

  # Orphan catalog rows: catalog id with no scenarios[] variant.
  local orphans
  orphans="$(comm -23 \
    <(jq -r '.catalog[].id' "$scn" | sort -u) \
    <(jq -r '.scenarios[].coverage_id' "$scn" | sort -u))"

  if [[ -z "$orphans" ]]; then
    echo "catalog-orphan-guard: ok — no orphan catalog rows (catalog ⟺ scenarios[] in bijection)"
    return 0
  fi

  local agents rc=0 row a
  agents="$(jq -r '.min_versions | keys[]' "$meta")"

  echo "catalog-orphan-guard: orphan catalog rows (no scenarios[] definition): $(echo "$orphans" | tr '\n' ' ')"
  while IFS= read -r row; do
    [[ -z "$row" ]] && continue
    while IFS= read -r a; do
      [[ -z "$a" ]] && continue
      if [[ ! -f "$root/agents/$a/scenarios/$row/assessment.json" ]]; then
        echo "  GAP  $a/$row — orphan row + no assessment → INVISIBLE to completeness-gate"
        rc=1
      fi
    done <<< "$agents"
  done <<< "$orphans"

  if [[ $rc -eq 0 ]]; then
    echo "catalog-orphan-guard: ok — every orphan-row cell is assessed across all onboarded agents (none hidden)"
  else
    echo "catalog-orphan-guard: FAIL — orphan catalog rows hide unassessed cells from the gate. Assess each, or give the row a scenarios[] definition."
  fi
  return $rc
}

if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
  set -uo pipefail
  SK="$(cd "$_COG_DIR/../.." && pwd)"          # …/ir:onboard-agent
  REPO="$(cd "$SK/../../.." && pwd)"            # repo root
  scn="${1:-$SK/scenarios.json}"
  root="${2:-$REPO/replaydata}"
  catalog_orphan_guard "$scn" "$root"
  exit $?
fi
