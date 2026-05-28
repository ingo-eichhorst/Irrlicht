#!/usr/bin/env bash
# consistency-gate_test.sh — CLI integration test for the thin consistency gate.
# Plain bash + jq (no framework). Run directly or via scripts/smoke-test.sh.
#
# Since #508 the gate's decision logic lives in Go (internal/matrix) with its
# own exhaustive parity tests (internal/matrix/matrix_test.go ports the exact
# fixtures this file used to assert against — cs_route, cs_cell_verdict,
# cs_applicable_state, cs_errors). What's left to verify HERE is the
# bash↔binary seam: planted contradictions fail the gate (exit 1), a documented
# record_blocked or a committed fixture clears them (exit 0), and the
# maintainer-facing ERROR lines name the right cell. Skips gracefully when the
# Go toolchain is unavailable.

set -uo pipefail   # NOT -e: assertions capture non-zero return codes

DIR="$(cd "$(dirname "$0")" && pwd)"

if ! command -v go >/dev/null 2>&1; then
  echo "consistency-gate_test: go toolchain not available — skipping (logic covered by internal/matrix tests)"
  exit 0
fi
command -v jq >/dev/null || { echo "consistency-gate_test: jq is required" >&2; exit 2; }

REPO="$(cd "$DIR/../../../../.." && pwd)"
BIN="$REPO/.build/matrix"
mkdir -p "$REPO/.build"
( cd "$REPO/tools/agent-onboarding" && go build -o "$BIN" ./cmd/matrix ) \
  || { echo "consistency-gate_test: failed to build matrix binary" >&2; exit 2; }
export IR_MATRIX_BIN="$BIN"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

fails=0
pass() { echo "  PASS: $1"; }
fail() { echo "  FAIL: $1 — expected [$2] got [$3]"; fails=$((fails + 1)); }
assert_eq() { [[ "$2" == "$3" ]] && pass "$1" || fail "$1" "$2" "$3"; }

mkassess() { # mkassess <path> <supports> <daemon> <driver> [record_blocked]
  mkdir -p "$(dirname "$1")"
  jq -n --arg s "$2" --arg d "$3" --arg v "$4" --arg b "${5:-}" \
    '{schema_version:1, agent_supports:$s, daemon_capability:$d, driver_capability:$v}
     + (if $b=="" then {} else {record_blocked:$b} end)' > "$1"
}

cat > "$TMP/scenarios.json" <<'JSON'
{"catalog":[{"id":"cellA"},{"id":"cellB"},{"id":"cellC"}],
 "scenarios":[
   {"name":"cellA","coverage_id":"cellA","requires":[],"by_adapter":{"ag":{"applicable":false}}},
   {"name":"cellB","coverage_id":"cellB","requires":[],"by_adapter":{"ag":{"applicable":true}}},
   {"name":"cellC","coverage_id":"cellC","requires":[]}
 ]}
JSON

ROOT="$TMP/agents"
mkdir -p "$ROOT/ag/scenarios"
jq -n '{features:{},transport:"line_based"}' > "$ROOT/ag/capabilities.json"

run() { bash "$DIR/consistency-gate.sh" "$TMP/scenarios.json" "$ROOT" 2>&1; }

echo "== planted contradictions fail the gate =="
mkassess "$ROOT/ag/scenarios/cellA/assessment.json" yes full ready   # record_now + applicable:false → contradiction
mkassess "$ROOT/ag/scenarios/cellB/assessment.json" no  n/a  n/a     # frozen + applicable:true   → contradiction
errs="$(run)"; rc=$?
assert_eq "two contradictions → exit 1" 1 "$rc"
grep -q "ag/cellA: assessment routes RECORD" <<<"$errs" && pass "flags record_now vs applicable:false" || fail "record_now flag" "cellA error" "$errs"
grep -q "ag/cellB: scenarios.json marks by_adapter.ag applicable:true" <<<"$errs" && pass "flags frozen vs applicable:true" || fail "frozen flag" "cellB error" "$errs"

echo "== record_blocked clears the record_now contradiction =="
mkassess "$ROOT/ag/scenarios/cellA/assessment.json" yes full ready infra
errs="$(run)"
grep -q "ag/cellA:" <<<"$errs" && fail "record_blocked should clear cellA" "no cellA" "$errs" || pass "record_blocked=infra clears the cellA contradiction"

echo "== a committed fixture clears a contradiction =="
echo '{}' > "$ROOT/ag/scenarios/cellB/transcript.jsonl"
echo '{}' > "$ROOT/ag/scenarios/cellB/events.jsonl"
run >/dev/null; rc=$?
assert_eq "cellA blocked + cellB recorded → exit 0" 0 "$rc"

echo "== drop the exemptions → contradiction again =="
rm -f "$ROOT/ag/scenarios/cellB/transcript.jsonl" "$ROOT/ag/scenarios/cellB/events.jsonl"
mkassess "$ROOT/ag/scenarios/cellB/assessment.json" partial full ready   # cellB now consistent (record_now + applicable:true)
mkassess "$ROOT/ag/scenarios/cellA/assessment.json" yes full ready       # cellA contradiction again (dropped record_blocked)
run >/dev/null; rc=$?
assert_eq "contradiction → exit 1" 1 "$rc"

echo ""
if [[ "$fails" -eq 0 ]]; then
  echo "consistency-gate_test: ALL PASS"
else
  echo "consistency-gate_test: $fails FAILED" >&2
  exit 1
fi
