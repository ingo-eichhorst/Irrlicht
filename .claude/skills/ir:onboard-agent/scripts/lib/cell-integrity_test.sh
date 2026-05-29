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

# #511: cell-integrity reads per-scenario shards (replaydata/scenarios/) via
# shard-lib. Legitimate dir names = each shard's name PLUS its recording_dir
# basename, so the variant-folder split (coverage `cov` recorded under the
# `cov-variant` folder) falls straight out of the shard's recording_dir.
# Build shard fixtures and point shard-lib at them with IR_SHARD_DIR.
export IR_SHARD_DIR="$TMP/replaydata/scenarios"
mkdir -p "$IR_SHARD_DIR"
# Coverage `cov` is recorded under the `cov-variant` folder; `md`/`half` record
# under their own name; `orphan-cov` has no `fake` cell at all (a recording
# under it would be orphaned).
# details carries both the recipe and the assessment (the assessment moved into
# the shard in #511; cell-integrity checks it there, not an on-disk file).
cat > "$IR_SHARD_DIR/cov.json" <<'JSON'
{"name":"cov","agents":{"fake":{"recording_dir":"fake/scenarios/cov-variant","details":{"assessment":{"agent_supports":"yes"},"recipe":{"script":[{"type":"send"}]}}}}}
JSON
cat > "$IR_SHARD_DIR/md.json" <<'JSON'
{"name":"md","agents":{"fake":{"recording_dir":"fake/scenarios/md","details":{"assessment":{"agent_supports":"yes"},"recipe":{"script":[{"type":"send"}]}}}}}
JSON
cat > "$IR_SHARD_DIR/half.json" <<'JSON'
{"name":"half","agents":{"fake":{"recording_dir":"fake/scenarios/half","details":{"assessment":{"agent_supports":"yes"},"recipe":{"script":[{"type":"send"}]}}}}}
JSON
cat > "$IR_SHARD_DIR/orphan-cov.json" <<'JSON'
{"name":"orphan-cov","agents":{}}
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

echo "== ci_recipe_dir_names: shard names ∪ recording_dir basenames =="
assert_eq "fake recipe dirs (orphan-cov excluded — no fake cell)" \
  "$(printf 'cov\ncov-variant\nhalf\nmd')" \
  "$(ci_recipe_dir_names fake)"

echo "== ci_coverage_id_for_dir =="
assert_eq "variant folder → its coverage_id" cov "$(ci_coverage_id_for_dir cov-variant fake)"
assert_eq "unknown dir → itself"             md  "$(ci_coverage_id_for_dir md fake)"

echo "== ci_is_recorded =="
ci_is_recorded "$S/cov" && r=rec || r=no; assert_eq "assessment-only dir → not recorded" no "$r"
ci_is_recorded "$S/cov-variant" && r=rec || r=no; assert_eq "recording dir → recorded" rec "$r"

echo "== ci_missing_artifacts =="
ci_missing_artifacts fake cov-variant "$S/cov-variant" "$S" >/dev/null
assert_eq "complete variant (assessment in sibling cov/) → rc 0" 0 "$?"
ci_missing_artifacts fake md "$S/md" "$S" >/dev/null
assert_eq "complete md cell (no golden needed) → rc 0" 0 "$?"
probs="$(ci_missing_artifacts fake half "$S/half" "$S")"; rc=$?
assert_eq "half cell → rc 1" 1 "$rc"
assert_eq "half cell → flags events.jsonl" events.jsonl "$probs"
probs="$(ci_missing_artifacts fake orphan-rec "$S/orphan-rec" "$S")"; rc=$?
assert_eq "orphan recording → rc 1" 1 "$rc"
[[ "$probs" == recipe-row* ]] && pass "orphan → flags recipe-row" || fail "orphan → flags recipe-row" "recipe-row*" "$probs"

echo "== CLI exit code =="
ROOT="$TMP/root"; mkdir -p "$ROOT/agents/fake"; cp -R "$S" "$ROOT/agents/fake/scenarios"
bash "$DIR/cell-integrity.sh" fake "$ROOT" >/dev/null 2>&1
assert_eq "half + orphan present → exit 1" 1 "$?"
# Remove the two bad cells; the gate should pass.
rm -rf "$ROOT/agents/fake/scenarios/half" "$ROOT/agents/fake/scenarios/orphan-rec"
bash "$DIR/cell-integrity.sh" fake "$ROOT" >/dev/null 2>&1
assert_eq "all recorded cells complete → exit 0" 0 "$?"

echo ""
if [[ "$fails" -eq 0 ]]; then
  echo "cell-integrity_test: ALL PASS"
else
  echo "cell-integrity_test: $fails FAILED" >&2
  exit 1
fi
