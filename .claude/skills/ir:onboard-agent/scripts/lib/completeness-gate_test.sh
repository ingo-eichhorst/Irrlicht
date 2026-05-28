#!/usr/bin/env bash
# completeness-gate_test.sh — CLI integration test for the thin completeness
# gate. Plain bash + jq (no framework). Run directly or via scripts/smoke-test.sh.
#
# Since #508 the gate's decision logic lives in Go (internal/matrix) with its
# own exhaustive parity tests (internal/matrix/matrix_test.go ports the exact
# fixtures this file used to assert against). What's left to verify HERE is the
# bash↔binary seam: that completeness-gate.sh translates its CLI arguments
# correctly and propagates the binary's exit codes (0 complete / 1 incomplete /
# 2 infra). Skips gracefully when the Go toolchain is unavailable.

set -uo pipefail   # NOT -e: assertions capture non-zero return codes

DIR="$(cd "$(dirname "$0")" && pwd)"

if ! command -v go >/dev/null 2>&1; then
  echo "completeness-gate_test: go toolchain not available — skipping (logic covered by internal/matrix tests)"
  exit 0
fi
command -v jq >/dev/null || { echo "completeness-gate_test: jq is required" >&2; exit 2; }

# Pre-build the matrix binary once so each gate call is a fast exec, not a build.
REPO="$(cd "$DIR/../../../../.." && pwd)"
BIN="$REPO/.build/matrix"
mkdir -p "$REPO/.build"
( cd "$REPO/tools/agent-onboarding" && go build -o "$BIN" ./cmd/matrix ) \
  || { echo "completeness-gate_test: failed to build matrix binary" >&2; exit 2; }
export IR_MATRIX_BIN="$BIN"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

# Catalog: cells exercising every disposition + one N/A (requires feat_x) and a
# transport-N/A cell (requires_transport line_based vs the structured_store agent).
cat > "$TMP/scenarios.json" <<'JSON'
{"scenarios":[
  {"name":"rec","coverage_id":"rec","requires":["feat_a"],"by_adapter":{"fake":{"script":[{"type":"send"}]}}},
  {"name":"gap","coverage_id":"gap","requires":["feat_a"],"by_adapter":{"fake":{"script":[{"type":"keys"}]}}},
  {"name":"frozen","coverage_id":"frozen","requires":["feat_a"],"by_adapter":{"fake":{"script":[{"type":"send"}]}}},
  {"name":"degraded","coverage_id":"degraded","requires":["feat_a"],"by_adapter":{"fake":{"applicable":false}}},
  {"name":"missed","coverage_id":"missed","requires":["feat_a"]},
  {"name":"ready-unrec","coverage_id":"ready-unrec","requires":["feat_a"],"by_adapter":{"fake":{"script":[{"type":"send"}]}}},
  {"name":"na","coverage_id":"na","requires":["feat_x"],"by_adapter":{"fake":{"script":[{"type":"send"}]}}},
  {"name":"line-only","coverage_id":"line-only","requires":["feat_a"],"requires_transport":["line_based"],"by_adapter":{"fake":{"script":[{"type":"send"}]}}}
]}
JSON

ROOT="$TMP/replaydata"
SDIR="$ROOT/agents/fake/scenarios"
mkdir -p "$ROOT/agents/fake"
cat > "$ROOT/agents/fake/capabilities.json" <<'JSON'
{"agent":"fake","transport":"structured_store","features":{"feat_a":true,"feat_b":true,"feat_x":false}}
JSON

mk_assess() { mkdir -p "$SDIR/$1"; printf '{"agent_supports":"%s","daemon_capability":"%s","driver_capability":"%s"}\n' "$2" "$3" "$4" > "$SDIR/$1/assessment.json"; }
record()    { mkdir -p "$SDIR/$1"; printf '{}\n' > "$SDIR/$1/transcript.jsonl"; printf '{}\n' > "$SDIR/$1/events.jsonl"; }

mk_assess rec yes full ready; record rec
mk_assess gap partial full gap:keys
mk_assess frozen no n/a ready
mk_assess degraded yes full ready          # recipe applicable:false ⇒ applicable_false
mk_assess ready-unrec yes full ready       # assessed recordable, NO recording → non-terminal
# `missed` + `na` get no dir at all.

fails=0
pass() { echo "  PASS: $1"; }
fail() { echo "  FAIL: $1 — expected [$2] got [$3]"; fails=$((fails + 1)); }
assert_eq() { [[ "$2" == "$3" ]] && pass "$1" || fail "$1" "$2" "$3"; }

echo "== CLI exit codes (bash↔matrix seam) =="
bash "$DIR/completeness-gate.sh" fake "$TMP/scenarios.json" "$TMP/root-missing" >/dev/null 2>&1
assert_eq "missing capabilities → exit 2 (infra)" "2" "$?"

bash "$DIR/completeness-gate.sh" fake "$TMP/scenarios.json" "$ROOT" >/dev/null 2>&1
assert_eq "non-terminal cells present → exit 1" "1" "$?"

# Make every non-terminal cell terminal; the gate should now pass.
mk_assess missed no n/a ready              # → applicable_false
record ready-unrec                         # → recorded
bash "$DIR/completeness-gate.sh" fake "$TMP/scenarios.json" "$ROOT" >/dev/null 2>&1
assert_eq "all terminal → exit 0" "0" "$?"

echo "== output: GAP line names the next action =="
out="$(bash "$DIR/completeness-gate.sh" fake "$TMP/scenarios.json" "$TMP/root-missing" 2>&1)"
mk_assess ready-unrec yes full ready       # restore one non-terminal cell
rm -f "$SDIR/ready-unrec/transcript.jsonl" "$SDIR/ready-unrec/events.jsonl"
out="$(bash "$DIR/completeness-gate.sh" fake "$TMP/scenarios.json" "$ROOT" 2>&1)"
grep -q "ready-unrec.*assessed_not_recorded → implement fake ready-unrec" <<<"$out" \
  && pass "implement hint for assessed_not_recorded" \
  || fail "implement hint" "implement fake ready-unrec line" "$out"

echo ""
if [[ "$fails" -eq 0 ]]; then
  echo "completeness-gate_test: ALL PASS"
else
  echo "completeness-gate_test: $fails FAILED" >&2
  exit 1
fi
