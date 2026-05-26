#!/usr/bin/env bash
# cell-integrity_test.sh — unit tests for lib/cell-integrity.sh. Plain bash + jq
# (no framework). Run directly or via scripts/smoke-test.sh. Exits non-zero on
# any failed assertion.
#
# Covers the #496 RC6 artifact-completeness gate: a recorded cell must carry a
# complete, consistent artifact set — assessment may live in the coverage_id
# sibling dir; a half-recorded cell (no events.jsonl) and an orphan recording
# (no recipe maps to the dir) must both be caught.

set -uo pipefail   # NOT -e: assertions capture non-zero return codes

DIR="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=cell-integrity.sh
source "$DIR/cell-integrity.sh"

command -v jq >/dev/null || { echo "cell-integrity_test: jq is required" >&2; exit 2; }

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

# Catalog: coverage_id `cov` has two recipe names (cov, cov-variant) with a
# `fake` recipe; `md` is a markdown-transcript cell; `orphan-cov` has NO fake
# recipe (a recording under it would be orphaned).
cat > "$TMP/scenarios.json" <<'JSON'
{"scenarios":[
  {"name":"cov","coverage_id":"cov","by_adapter":{"fake":{"script":[{"type":"send"}]}}},
  {"name":"cov-variant","coverage_id":"cov","by_adapter":{"fake":{"script":[{"type":"send"}]}}},
  {"name":"md","coverage_id":"md","by_adapter":{"fake":{"script":[{"type":"send"}]}}},
  {"name":"half","coverage_id":"half","by_adapter":{"fake":{"script":[{"type":"send"}]}}},
  {"name":"orphan-cov","coverage_id":"orphan-cov","by_adapter":{}}
]}
JSON

S="$TMP/scenarios"; mkdir -p "$S"
touchf() { mkdir -p "$(dirname "$1")"; printf '%s\n' "${2:-{}}" > "$1"; }

# cov/ — coverage_id dir, assessment only (NOT recorded → skipped).
touchf "$S/cov/assessment.json"
# cov-variant/ — recipe-name dir, complete recording; assessment in cov/ sibling.
touchf "$S/cov-variant/expected.jsonl"
touchf "$S/cov-variant/events.jsonl"
touchf "$S/cov-variant/transcript.jsonl"
touchf "$S/cov-variant/transcript.jsonl.replay.json.golden"
# md/ — markdown transcript, complete, NO golden expected.
touchf "$S/md/assessment.json"
touchf "$S/md/expected.jsonl"
touchf "$S/md/events.jsonl"
touchf "$S/md/transcript.md"
# half/ — recorded (transcript) but NO events.jsonl (the task-list defect).
touchf "$S/half/assessment.json"
touchf "$S/half/expected.jsonl"
touchf "$S/half/transcript.jsonl"
touchf "$S/half/transcript.jsonl.replay.json.golden"
# orphan-rec/ — complete recording but its dir maps to no fake recipe.
mkdir -p "$S/orphan-rec"
for f in assessment.json expected.jsonl events.jsonl transcript.jsonl transcript.jsonl.replay.json.golden; do
  touchf "$S/orphan-rec/$f"
done

fails=0
pass() { echo "  PASS: $1"; }
fail() { echo "  FAIL: $1 — expected [$2] got [$3]"; fails=$((fails + 1)); }
assert_eq() { [[ "$2" == "$3" ]] && pass "$1" || fail "$1" "$2" "$3"; }

echo "== ci_recipe_dir_names: names ∪ coverage_ids with a recipe =="
assert_eq "fake recipe dirs (orphan-cov excluded — no recipe)" \
  "$(printf 'cov\ncov-variant\nhalf\nmd')" \
  "$(ci_recipe_dir_names "$TMP/scenarios.json" fake)"

echo "== ci_coverage_id_for_dir =="
assert_eq "variant name → its coverage_id" cov "$(ci_coverage_id_for_dir "$TMP/scenarios.json" cov-variant)"
assert_eq "unknown dir → itself"           md  "$(ci_coverage_id_for_dir "$TMP/scenarios.json" md)"

echo "== ci_is_recorded =="
ci_is_recorded "$S/cov" && r=rec || r=no; assert_eq "assessment-only dir → not recorded" no "$r"
ci_is_recorded "$S/cov-variant" && r=rec || r=no; assert_eq "recording dir → recorded" rec "$r"

echo "== ci_missing_artifacts =="
ci_missing_artifacts "$TMP/scenarios.json" fake cov-variant "$S/cov-variant" "$S" >/dev/null
assert_eq "complete variant (assessment in sibling cov/) → rc 0" 0 "$?"
ci_missing_artifacts "$TMP/scenarios.json" fake md "$S/md" "$S" >/dev/null
assert_eq "complete md cell (no golden needed) → rc 0" 0 "$?"
probs="$(ci_missing_artifacts "$TMP/scenarios.json" fake half "$S/half" "$S")"; rc=$?
assert_eq "half cell → rc 1" 1 "$rc"
assert_eq "half cell → flags events.jsonl" events.jsonl "$probs"
probs="$(ci_missing_artifacts "$TMP/scenarios.json" fake orphan-rec "$S/orphan-rec" "$S")"; rc=$?
assert_eq "orphan recording → rc 1" 1 "$rc"
[[ "$probs" == recipe-row* ]] && pass "orphan → flags recipe-row" || fail "orphan → flags recipe-row" "recipe-row*" "$probs"

echo "== CLI exit code =="
ROOT="$TMP/root"; mkdir -p "$ROOT/agents/fake"; cp -R "$S" "$ROOT/agents/fake/scenarios"
bash "$DIR/cell-integrity.sh" fake "$TMP/scenarios.json" "$ROOT" >/dev/null 2>&1
assert_eq "half + orphan present → exit 1" 1 "$?"
# Remove the two bad cells; the gate should pass.
rm -rf "$ROOT/agents/fake/scenarios/half" "$ROOT/agents/fake/scenarios/orphan-rec"
bash "$DIR/cell-integrity.sh" fake "$TMP/scenarios.json" "$ROOT" >/dev/null 2>&1
assert_eq "all recorded cells complete → exit 0" 0 "$?"

echo ""
if [[ "$fails" -eq 0 ]]; then
  echo "cell-integrity_test: ALL PASS"
else
  echo "cell-integrity_test: $fails FAILED" >&2
  exit 1
fi
