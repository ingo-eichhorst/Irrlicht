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

# #510/#511: the matrix reads per-scenario shards (replaydata/scenarios/), so
# the gate's input is shards, not the retired scenarios.json + capabilities.json.
# The binary derives the shard dir (repo root) from --agents-root, so we build
# <root>/replaydata/scenarios/*.json + _meta.json under TMP.
ROOT="$TMP/replaydata"
SCEN_ACC="$TMP/_scen"
mkdir -p "$SCEN_ACC" "$ROOT/agents/fake"

# rebuild_catalog → (re)write the consolidated replaydata/scenarios.json from
# the per-name global accumulator, with the meta block the matrix needs.
rebuild_catalog() {
  jq -s '{meta:{min_versions:{fake:"0.0.0"},transcript_extensions:{fake:"jsonl"}}, scenarios: .}' \
    "$SCEN_ACC"/*.json > "$ROOT/scenarios.json"
}

# shard <name> <agents-json> — register scenario <name> (global fields) and write
# a metadata.json per agent in <agents-json> (keyed by scenario_id == <name>), the
# data the matrix now reads from replaydata/agents/<a>/scenarios/<folder>/metadata.json.
shard() {
  local name="$1" agents="$2" a
  printf '{"id":"1.%s","name":"%s","section":"S","feature":"F"}\n' "$RANDOM" "$name" > "$SCEN_ACC/$name.json"
  for a in $(jq -r 'keys[]' <<<"$agents"); do
    mkdir -p "$ROOT/agents/$a/scenarios/$name"
    jq -c --arg a "$a" --arg n "$name" '.[$a] + {scenario_id:$n}' <<<"$agents" \
      > "$ROOT/agents/$a/scenarios/$name/metadata.json"
  done
  rebuild_catalog
}
# A recorded cell carries non-empty artifact refs (cellRecorded checks the refs,
# not the files on disk); an assessed-not-recorded cell has an assessment but
# no refs; an unassessed cell has neither.
ASSESS_OK='{"agent_supports":"yes","daemon_capability":"full","driver_capability":"ready"}'
REC='{"events":"fake/scenarios/rec/events.jsonl","transcript":"fake/scenarios/rec/transcript.jsonl"}'

shard rec            "{\"fake\":{\"artifacts\":$REC,\"details\":{\"assessment\":$ASSESS_OK,\"recipe\":{\"script\":[{\"type\":\"send\"}]}}}}"
shard unrec-assessed "{\"fake\":{\"details\":{\"assessment\":$ASSESS_OK,\"recipe\":{\"script\":[{\"type\":\"send\"}]}}}}"
shard unassessed     '{"fake":{"details":{"recipe":{"script":[{"type":"send"}]}}}}'

fails=0
pass() { echo "  PASS: $1"; }
fail() { echo "  FAIL: $1 — expected [$2] got [$3]"; fails=$((fails + 1)); }
assert_eq() { [[ "$2" == "$3" ]] && pass "$1" || fail "$1" "$2" "$3"; }

echo "== CLI exit codes (bash↔matrix seam) =="
bash "$DIR/completeness-gate.sh" ghost "$ROOT" >/dev/null 2>&1
assert_eq "unknown adapter (not in _meta) → exit 2 (infra)" "2" "$?"

bash "$DIR/completeness-gate.sh" fake "$ROOT" >/dev/null 2>&1
assert_eq "non-terminal cells present → exit 1" "1" "$?"

echo "== output: GAP line names the next action =="
out="$(bash "$DIR/completeness-gate.sh" fake "$ROOT" 2>&1)"
grep -q "unrec-assessed.*assessed_not_recorded → implement fake unrec-assessed" <<<"$out" \
  && pass "implement hint for assessed_not_recorded" \
  || fail "implement hint" "implement fake unrec-assessed line" "$out"
grep -q "unassessed.*unassessed → assess fake unassessed" <<<"$out" \
  && pass "assess hint for unassessed" \
  || fail "assess hint" "assess fake unassessed line" "$out"

# Make every non-terminal cell terminal; the gate should now pass.
shard unrec-assessed "{\"fake\":{\"artifacts\":{\"events\":\"e\",\"transcript\":\"t\"},\"details\":{\"assessment\":$ASSESS_OK}}}"   # now recorded
shard unassessed     '{"fake":{"details":{"assessment":{"agent_supports":"no","daemon_capability":"n/a","driver_capability":"ready"}}}}'  # supports=no → applicable_false
bash "$DIR/completeness-gate.sh" fake "$ROOT" >/dev/null 2>&1
assert_eq "all terminal → exit 0" "0" "$?"

echo ""
if [[ "$fails" -eq 0 ]]; then
  echo "completeness-gate_test: ALL PASS"
else
  echo "completeness-gate_test: $fails FAILED" >&2
  exit 1
fi
