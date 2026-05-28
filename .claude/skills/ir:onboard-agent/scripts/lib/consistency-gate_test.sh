#!/usr/bin/env bash
# consistency-gate_test.sh — unit tests for lib/consistency-gate.sh. Plain bash
# + jq (no framework). Run directly or via scripts/smoke-test.sh. Exits non-zero
# on any failed assertion.
#
# Covers the assessment ⟺ scenarios agreement gate: an un-recorded cell whose
# assessment routes RECORD but whose matrix flag is applicable:false (with no
# documented record_blocked) is a hard error; so is a FROZEN assessment whose
# matrix flag is applicable:true. A documented record_blocked, a committed
# fixture, or a transport-/capability-inapplicable cell all clear the cell.

set -uo pipefail   # NOT -e: assertions capture non-zero return codes

DIR="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=consistency-gate.sh
source "$DIR/consistency-gate.sh"

command -v jq >/dev/null || { echo "consistency-gate_test: jq is required" >&2; exit 2; }

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

echo "== cs_route =="
mkassess "$TMP/a.json" yes full ready
assert_eq "yes/full/ready → record_now"   record_now   "$(cs_route "$TMP/a.json")"
mkassess "$TMP/a.json" no n/a n/a
assert_eq "no/n.a → frozen"               frozen       "$(cs_route "$TMP/a.json")"
mkassess "$TMP/a.json" yes incapable ready
assert_eq "daemon incapable → frozen"     frozen       "$(cs_route "$TMP/a.json")"
mkassess "$TMP/a.json" yes full "gap:keys"
assert_eq "driver gap → driver_gap"       driver_gap   "$(cs_route "$TMP/a.json")"
mkassess "$TMP/a.json" yes unknown ready
assert_eq "daemon unknown → inconclusive" inconclusive "$(cs_route "$TMP/a.json")"

echo "== cs_cell_verdict (pure decision table) =="
assert_eq "record_now + applicable:false + no block → contradiction" CONTRADICTION_RECORD_NOW "$(cs_cell_verdict record_now false 0 "")"
assert_eq "record_now + applicable:false + blocked → ok"             ok                        "$(cs_cell_verdict record_now false 0 infra)"
assert_eq "record_now + applicable:true → ok"                        ok                        "$(cs_cell_verdict record_now true 0 "")"
assert_eq "frozen + applicable:true → contradiction"                 CONTRADICTION_FROZEN      "$(cs_cell_verdict frozen true 0 "")"
assert_eq "frozen + applicable:false → ok"                           ok                        "$(cs_cell_verdict frozen false 0 "")"
assert_eq "recorded short-circuits everything → ok"                  ok                        "$(cs_cell_verdict record_now false 1 "")"
assert_eq "driver_gap is never a contradiction → ok"                 ok                        "$(cs_cell_verdict driver_gap false 0 "")"
assert_eq "inconclusive is never a contradiction → ok"               ok                        "$(cs_cell_verdict inconclusive false 0 "")"

echo "== cs_applicable_state =="
cat > "$TMP/scenarios.json" <<'JSON'
{"catalog":[{"id":"cellA"},{"id":"cellB"},{"id":"cellC"}],
 "scenarios":[
   {"name":"cellA","coverage_id":"cellA","requires":[],"by_adapter":{"ag":{"applicable":false}}},
   {"name":"cellB","coverage_id":"cellB","requires":[],"by_adapter":{"ag":{"applicable":true}}},
   {"name":"cellC","coverage_id":"cellC","requires":[]}
 ]}
JSON
assert_eq "all variants false → false" false  "$(cs_applicable_state "$TMP/scenarios.json" ag cellA)"
assert_eq "a recordable variant → true" true  "$(cs_applicable_state "$TMP/scenarios.json" ag cellB)"
assert_eq "no by_adapter entry → absent" absent "$(cs_applicable_state "$TMP/scenarios.json" ag cellC)"

echo "== cs_errors + CLI: planted contradictions =="
ROOT="$TMP/agents"
mkdir -p "$ROOT/ag/scenarios"
jq -n '{features:{},transport:"line_based"}' > "$ROOT/ag/capabilities.json"
mkassess "$ROOT/ag/scenarios/cellA/assessment.json" yes full ready          # record_now + applicable:false → contradiction
mkassess "$ROOT/ag/scenarios/cellB/assessment.json" no  n/a  n/a            # frozen + applicable:true   → contradiction
errs="$(cs_errors "$TMP/scenarios.json" "$ROOT")"; rc=$?
assert_eq "two contradictions → rc 1" 1 "$rc"
grep -q "ag/cellA: assessment routes RECORD" <<< "$errs" && pass "flags record_now vs applicable:false" || fail "record_now flag" "cellA error" "$errs"
grep -q "ag/cellB: scenarios.json marks by_adapter.ag applicable:true" <<< "$errs" && pass "flags frozen vs applicable:true" || fail "frozen flag" "cellB error" "$errs"

echo "== record_blocked clears the record_now contradiction =="
mkassess "$ROOT/ag/scenarios/cellA/assessment.json" yes full ready infra
errs="$(cs_errors "$TMP/scenarios.json" "$ROOT")"
grep -q "ag/cellA:" <<< "$errs" && fail "record_blocked should clear cellA" "no cellA" "$errs" || pass "record_blocked=infra clears the cellA contradiction"

echo "== a committed fixture clears a contradiction =="
mkassess "$ROOT/ag/scenarios/cellB/assessment.json" no n/a n/a   # still frozen…
echo '{}' > "$ROOT/ag/scenarios/cellB/transcript.jsonl"
echo '{}' > "$ROOT/ag/scenarios/cellB/events.jsonl"              # …but now recorded
cs_errors "$TMP/scenarios.json" "$ROOT" >/dev/null; rc=$?
assert_eq "all contradictions cleared → rc 0" 0 "$rc"

echo "== CLI exit codes =="
rm -f "$ROOT/ag/scenarios/cellB/transcript.jsonl" "$ROOT/ag/scenarios/cellB/events.jsonl"
mkassess "$ROOT/ag/scenarios/cellB/assessment.json" partial full ready   # both cells now consistent-ish: cellA blocked, cellB record_now+applicable:true
bash "$DIR/consistency-gate.sh" "$TMP/scenarios.json" "$ROOT" >/dev/null 2>&1
assert_eq "clean fixture → exit 0" 0 "$?"
mkassess "$ROOT/ag/scenarios/cellA/assessment.json" yes full ready       # drop record_blocked → contradiction again
bash "$DIR/consistency-gate.sh" "$TMP/scenarios.json" "$ROOT" >/dev/null 2>&1
assert_eq "contradiction → exit 1" 1 "$?"

echo ""
if [[ "$fails" -eq 0 ]]; then
  echo "consistency-gate_test: ALL PASS"
else
  echo "consistency-gate_test: $fails FAILED" >&2
  exit 1
fi
