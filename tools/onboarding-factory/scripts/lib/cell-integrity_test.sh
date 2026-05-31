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

# cell-integrity reads the catalog + per-agent metadata.json via shard-lib.
# Post-restructure, metadata.json is COLOCATED with the recording it describes:
# replaydata/agents/<a>/scenarios/<id>_<name>/{metadata.json, events.jsonl, ...}.
# Legitimate dir names = every folder that holds a metadata.json; an orphan is a
# recording folder WITHOUT one. Point shard-lib at the tree via IR_AGENTS_DIR.
export IR_SCENARIOS_FILE="$TMP/replaydata/scenarios.json"
export IR_AGENTS_DIR="$TMP/replaydata/agents"
mkdir -p "$(dirname "$IR_SCENARIOS_FILE")"
printf '{"meta":{},"scenarios":[]}\n' > "$IR_SCENARIOS_FILE"

S="$IR_AGENTS_DIR/fake/scenarios"
REC="2026-01-01-00-00-00_irrlichd-t"   # the single recording folder per cell
touchf() { mkdir -p "$(dirname "$1")"; printf '%s\n' "${2:-{}}" > "$1"; }
cell_meta() { # cell_meta <folder> <scenario_id>
  touchf "$S/$1/metadata.json" "{\"scenario_id\":\"$2\",\"details\":{\"assessment\":{\"agent_supports\":\"yes\"},\"recipe\":{\"script\":[{\"type\":\"send\"}]}}}"
}
# Every recording artifact lives under recordings/<REC>/; expected.jsonl +
# metadata.json stay at the cell root.
recf() { touchf "$S/$1/recordings/$REC/$2"; }

# 5-1_cov — variant folder (folder != scenario name); complete jsonl recording.
cell_meta 5-1_cov cov
touchf "$S/5-1_cov/expected.jsonl"
recf 5-1_cov events.jsonl
recf 5-1_cov transcript.jsonl
recf 5-1_cov transcript.jsonl.replay.json.golden
# 5-2_md — standard folder, markdown transcript, complete, NO golden expected.
cell_meta 5-2_md md
touchf "$S/5-2_md/expected.jsonl"
recf 5-2_md events.jsonl
recf 5-2_md transcript.md
# 5-3_half — recorded (transcript) but NO events.jsonl (the task-list defect).
cell_meta 5-3_half half
touchf "$S/5-3_half/expected.jsonl"
recf 5-3_half transcript.jsonl
recf 5-3_half transcript.jsonl.replay.json.golden
# 5-4_orphan — complete recording but NO metadata.json (orphan).
touchf "$S/5-4_orphan/expected.jsonl"
recf 5-4_orphan events.jsonl
recf 5-4_orphan transcript.jsonl
recf 5-4_orphan transcript.jsonl.replay.json.golden

fails=0
pass() { echo "  PASS: $1"; }
fail() { echo "  FAIL: $1 — expected [$2] got [$3]"; fails=$((fails + 1)); }
assert_eq() { [[ "$2" == "$3" ]] && pass "$1" || fail "$1" "$2" "$3"; }

echo "== ci_recipe_dir_names: folders that hold a metadata.json =="
assert_eq "fake recipe dirs (orphan excluded — no metadata.json)" \
  "$(printf '5-1_cov\n5-2_md\n5-3_half')" \
  "$(ci_recipe_dir_names fake)"

echo "== ci_coverage_id_for_dir =="
assert_eq "variant folder → its scenario_id" cov "$(ci_coverage_id_for_dir 5-1_cov fake)"
assert_eq "no-metadata dir → itself"          5-4_orphan "$(ci_coverage_id_for_dir 5-4_orphan fake)"

echo "== ci_is_recorded =="
ci_is_recorded "$S/5-1_cov" && r=rec || r=no; assert_eq "recording dir → recorded" rec "$r"
mkdir -p "$S/_empty"
ci_is_recorded "$S/_empty" && r=rec || r=no; assert_eq "empty dir → not recorded" no "$r"
rm -rf "$S/_empty"

echo "== ci_missing_artifacts =="
ci_missing_artifacts fake 5-1_cov "$S/5-1_cov" "$S" >/dev/null
assert_eq "complete variant → rc 0" 0 "$?"
ci_missing_artifacts fake 5-2_md "$S/5-2_md" "$S" >/dev/null
assert_eq "complete md cell (no golden needed) → rc 0" 0 "$?"
probs="$(ci_missing_artifacts fake 5-3_half "$S/5-3_half" "$S")"; rc=$?
assert_eq "half cell → rc 1" 1 "$rc"
assert_eq "half cell → flags the recording's events.jsonl" "recordings/$REC/events.jsonl" "$probs"
probs="$(ci_missing_artifacts fake 5-4_orphan "$S/5-4_orphan" "$S")"; rc=$?
assert_eq "orphan recording → rc 1" 1 "$rc"
[[ "$probs" == recipe-row* ]] && pass "orphan → flags recipe-row" || fail "orphan → flags recipe-row" "recipe-row*" "$probs"

echo "== CLI exit code =="
bash "$DIR/cell-integrity.sh" fake "$TMP/replaydata" >/dev/null 2>&1
assert_eq "half + orphan present → exit 1" 1 "$?"
# Remove the two bad cells; the gate should pass.
rm -rf "$S/5-3_half" "$S/5-4_orphan"
bash "$DIR/cell-integrity.sh" fake "$TMP/replaydata" >/dev/null 2>&1
assert_eq "all recorded cells complete → exit 0" 0 "$?"

echo ""
if [[ "$fails" -eq 0 ]]; then
  echo "cell-integrity_test: ALL PASS"
else
  echo "cell-integrity_test: $fails FAILED" >&2
  exit 1
fi
