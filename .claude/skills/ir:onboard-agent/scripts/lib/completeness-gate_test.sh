#!/usr/bin/env bash
# completeness-gate_test.sh — unit tests for lib/completeness-gate.sh. Plain
# bash + jq (no framework). Run directly or via scripts/smoke-test.sh. Exits
# non-zero on any failed assertion.
#
# Covers the #496 RC4 forcing function: every coverage_id applicable to an
# agent must reach a terminal verdict; a cell with no assessment or an
# assessed-but-unrecorded cell is non-terminal and must be caught.

set -uo pipefail   # NOT -e: assertions capture non-zero return codes

DIR="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=completeness-gate.sh
source "$DIR/completeness-gate.sh"

command -v jq >/dev/null || { echo "completeness-gate_test: jq is required" >&2; exit 2; }

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

# Capabilities: feat_a/feat_b true, feat_x false; transport=structured_store.
# A scenario requiring feat_x is N/A; one requiring transport line_based is N/A
# too (#496 RC7); requiring feat_a (no transport constraint) is applicable.
cat > "$TMP/capabilities.json" <<'JSON'
{"agent":"fake","transport":"structured_store","features":{"feat_a":true,"feat_b":true,"feat_x":false}}
JSON

# Catalog: six cells exercising every disposition + one N/A (requires feat_x).
# `dual` shares its coverage_id across two recipe names (recorded under the
# coverage_id dir, proving the candidate-dir fallback).
cat > "$TMP/scenarios.json" <<'JSON'
{"scenarios":[
  {"name":"rec","coverage_id":"rec","requires":["feat_a"],"by_adapter":{"fake":{"script":[{"type":"send"}]}}},
  {"name":"gap","coverage_id":"gap","requires":["feat_a"],"by_adapter":{"fake":{"script":[{"type":"keys"}]}}},
  {"name":"frozen","coverage_id":"frozen","requires":["feat_a"],"by_adapter":{"fake":{"script":[{"type":"send"}]}}},
  {"name":"degraded","coverage_id":"degraded","requires":["feat_a"],"by_adapter":{"fake":{"applicable":false}}},
  {"name":"missed","coverage_id":"missed","requires":["feat_a"]},
  {"name":"ready-unrec","coverage_id":"ready-unrec","requires":["feat_a"],"by_adapter":{"fake":{"script":[{"type":"send"}]}}},
  {"name":"dual","coverage_id":"dual","requires":["feat_a"],"by_adapter":{"fake":{"script":[{"type":"send"}]}}},
  {"name":"dual-variant","coverage_id":"dual","requires":["feat_a"],"by_adapter":{"fake":{"script":[{"type":"send"}]}}},
  {"name":"na","coverage_id":"na","requires":["feat_x"],"by_adapter":{"fake":{"script":[{"type":"send"}]}}},
  {"name":"line-only","coverage_id":"line-only","requires":["feat_a"],"requires_transport":["line_based"],"by_adapter":{"fake":{"script":[{"type":"send"}]}}}
]}
JSON

SDIR="$TMP/scenarios"
mk_assess() { # <dir> <supports> <daemon> <driver>
  mkdir -p "$SDIR/$1"
  printf '{"agent_supports":"%s","daemon_capability":"%s","driver_capability":"%s"}\n' \
    "$2" "$3" "$4" > "$SDIR/$1/assessment.json"
}

mk_assess rec yes full ready
printf '{}\n' > "$SDIR/rec/transcript.jsonl"; printf '{}\n' > "$SDIR/rec/events.jsonl"
mk_assess gap partial full gap:keys
mk_assess frozen no n/a ready
mk_assess degraded yes full ready             # recipe applicable:false ⇒ applicable_false
mk_assess ready-unrec yes full ready          # assessed recordable, NO recording
mk_assess dual yes full ready
printf '{}\n' > "$SDIR/dual/transcript.jsonl"; printf '{}\n' > "$SDIR/dual/events.jsonl"
# `missed` and `na` get no dir at all.

fails=0
pass() { echo "  PASS: $1"; }
fail() { echo "  FAIL: $1 — expected [$2] got [$3]"; fails=$((fails + 1)); }
assert_eq() { [[ "$2" == "$3" ]] && pass "$1" || fail "$1" "$2" "$3"; }

echo "== cg_applicable_coverage_ids: requires vs capabilities =="
applicable="$(cg_applicable_coverage_ids "$TMP/scenarios.json" "$TMP/capabilities.json")"
assert_eq "applicable set = feat_a coverage_ids (dual collapses; na + line-only excluded)" \
  "$(printf 'degraded\ndual\nfrozen\ngap\nmissed\nready-unrec\nrec')" \
  "$applicable"
# line-only requires transport line_based; the fake agent is structured_store.
if printf '%s\n' "$applicable" | grep -qx line-only; then
  fail "requires_transport excludes line-only for structured_store agent" "absent" "present"
else
  pass "requires_transport excludes line-only for structured_store agent"
fi

echo "== cg_disposition: one verdict per cell =="
assert_eq "recorded"               recorded               "$(cg_disposition "$TMP/scenarios.json" fake rec "$SDIR")"
assert_eq "driver_gap"             driver_gap             "$(cg_disposition "$TMP/scenarios.json" fake gap "$SDIR")"
assert_eq "frozen → applicable_false" applicable_false    "$(cg_disposition "$TMP/scenarios.json" fake frozen "$SDIR")"
assert_eq "recipe applicable:false → applicable_false" applicable_false "$(cg_disposition "$TMP/scenarios.json" fake degraded "$SDIR")"
assert_eq "no assessment → unassessed" unassessed         "$(cg_disposition "$TMP/scenarios.json" fake missed "$SDIR")"
assert_eq "assessed recordable, no recording → assessed_not_recorded" assessed_not_recorded "$(cg_disposition "$TMP/scenarios.json" fake ready-unrec "$SDIR")"
assert_eq "dual recorded under coverage_id dir" recorded  "$(cg_disposition "$TMP/scenarios.json" fake dual "$SDIR")"

echo "== CLI exit code: non-terminal cells fail the gate =="
bash "$DIR/completeness-gate.sh" fake "$TMP/scenarios.json" "$TMP/root-missing" >/dev/null 2>&1
assert_eq "missing capabilities → exit 2 (infra)" "2" "$?"

# Build a complete root (cp the fixtures so the CLI's path layout resolves).
ROOT="$TMP/root"; mkdir -p "$ROOT/agents/fake/scenarios"
cp "$TMP/capabilities.json" "$ROOT/agents/fake/capabilities.json"
cp -R "$SDIR/." "$ROOT/agents/fake/scenarios/"
bash "$DIR/completeness-gate.sh" fake "$TMP/scenarios.json" "$ROOT" >/dev/null 2>&1
assert_eq "two non-terminal cells (missed + ready-unrec) → exit 1" "1" "$?"

# Make the two non-terminal cells terminal; the gate should now pass.
mk_assess_root() { mkdir -p "$ROOT/agents/fake/scenarios/$1"; printf '{"agent_supports":"%s","daemon_capability":"%s","driver_capability":"%s"}\n' "$2" "$3" "$4" > "$ROOT/agents/fake/scenarios/$1/assessment.json"; }
mk_assess_root missed no n/a ready                                  # → applicable_false
printf '{}\n' > "$ROOT/agents/fake/scenarios/ready-unrec/transcript.jsonl"
printf '{}\n' > "$ROOT/agents/fake/scenarios/ready-unrec/events.jsonl"  # → recorded
bash "$DIR/completeness-gate.sh" fake "$TMP/scenarios.json" "$ROOT" >/dev/null 2>&1
assert_eq "all terminal → exit 0" "0" "$?"

echo ""
if [[ "$fails" -eq 0 ]]; then
  echo "completeness-gate_test: ALL PASS"
else
  echo "completeness-gate_test: $fails FAILED" >&2
  exit 1
fi
