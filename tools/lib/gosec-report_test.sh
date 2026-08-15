#!/usr/bin/env bash
# gosec-report_test.sh — unit tests for lib/gosec-report.sh, the classifier
# tools/security-scan.sh reads a gosec JSON report through. Plain bash, no
# framework, matching tools/lib/changed-files_test.sh. Run directly, or via
# tools/preflight.sh's `tools` gate.
#
# Covers #1570: security-scan.sh used to run gosec TWICE per module — an
# unfiltered informational pass and a `-severity high -confidence high` gate
# pass — for 172s + 172s on the core module, 344s of the security gate's
# measured 355s. Those flags filter the REPORT, not the analysis, so the second
# run re-derived a strict subset of the first at full price. One JSON run now
# answers both, and these tests are what stands in for the second run's exit
# code.
#
# The fixtures under testdata/gosec-report/ are committed rather than generated
# here, the same choice testdata/posix-lint/ makes: the mutation evidence has
# to outlive the PR that added it, and a real 174s gosec run is not something a
# unit test can perform. They are trimmed from an actual report of this repo's
# core module (Stats: 263 files / 66,534 lines) so the shapes are the ones
# gosec really emits, `"Issues": null` included.
#
# The load-bearing case is near-miss.json. It carries HIGH/MEDIUM and
# MEDIUM/HIGH and nothing else, so it is the ONE fixture that tells
# `severity == HIGH and confidence == HIGH` apart from `... or ...`: every
# other fixture passes both spellings. clean.json is the vacuity guard — a
# classifier that blocked unconditionally would satisfy every negative case
# here and read as excellent coverage.

set -uo pipefail   # NOT -e: assertions capture non-zero return codes

DIR="$(cd "$(dirname "$0")" && pwd)"
DATA="$DIR/testdata/gosec-report"
# shellcheck source=gosec-report.sh
source "$DIR/gosec-report.sh"

fails=0
pass() { echo "  PASS: $1"; return 0; }
fail() { echo "  FAIL: $1 — expected [$2] got [$3]"; fails=$((fails + 1)); return 0; }
assert_eq() {
  [[ "$2" == "$3" ]] && pass "$1" || fail "$1" "$2" "$3"
  return 0
}
assert_contains() {
  case "$3" in
    *"$2"*) pass "$1" ;;
    *) fail "$1" "output containing: $2" "$3" ;;
  esac
  return 0
}
assert_not_contains() {
  case "$3" in
    *"$2"*) fail "$1" "output NOT containing: $2" "$3" ;;
    *) pass "$1" ;;
  esac
  return 0
}

# run_check <fixture-basename> — captures stdout+stderr and the status.
OUT=""
RC=0
run_check() {
  OUT=$(gosec_report_check "$DATA/$1" "testmod" 2>&1)
  RC=$?
  return 0
}

echo "== the fixtures are what the tests think they are =="
# Without this, a fixture edited into a different shape would silently retarget
# every assertion below it — the corpus quietly stopping to contain its own
# test cases, which reads as a pass.
assert_eq "near-miss.json really carries no HIGH/HIGH issue" \
  "0" "$(jq '[.Issues[] | select(.severity=="HIGH" and .confidence=="HIGH")] | length' "$DATA/near-miss.json")"
assert_eq "near-miss.json really carries a HIGH severity issue" \
  "2" "$(jq '[.Issues[] | select(.severity=="HIGH")] | length' "$DATA/near-miss.json")"
assert_eq "near-miss.json really carries a HIGH confidence issue" \
  "1" "$(jq '[.Issues[] | select(.confidence=="HIGH")] | length' "$DATA/near-miss.json")"
assert_eq "high-high.json really carries exactly one HIGH/HIGH issue" \
  "1" "$(jq '[.Issues[] | select(.severity=="HIGH" and .confidence=="HIGH")] | length' "$DATA/high-high.json")"
assert_eq "empty-scan.json really reports zero files" \
  "0" "$(jq '.Stats.files' "$DATA/empty-scan.json")"
# jq answers a parse error with 5, not 1, so this asserts "non-zero" rather
# than pinning a number that is not the one jq actually returns.
assert_eq "truncated.json really is unparseable" \
  "unparseable" "$(jq -e . "$DATA/truncated.json" >/dev/null 2>&1 && echo parseable || echo unparseable)"

echo ""
echo "== a readable report with no High/High passes (the vacuity guard) =="
run_check clean.json
assert_eq "clean.json → 0" "0" "$RC"
assert_contains "clean.json names the coverage it had" "scanned 263 file(s)" "$OUT"
assert_contains "clean.json still lists its non-blocking findings" "G304" "$OUT"

run_check null-issues.json
assert_eq "null-issues.json (gosec's empty-result shape) → 0" "0" "$RC"
assert_contains "null-issues.json still names its coverage" "scanned 263 file(s)" "$OUT"

echo ""
echo "== High severity OR High confidence alone does not block =="
# The whole point of this fixture: swap the `and` in gosec_report_check for an
# `or` and this is the only case in the file that goes red.
run_check near-miss.json
assert_eq "near-miss.json → 0 (HIGH/MEDIUM and MEDIUM/HIGH are not blocking)" "0" "$RC"
assert_contains "near-miss.json still lists the HIGH/MEDIUM finding" "G115" "$OUT"

echo ""
echo "== one High/High finding blocks, and is named =="
run_check high-high.json
assert_eq "high-high.json → 1" "1" "$RC"
assert_contains "high-high.json names the blocking rule" "G402" "$OUT"
assert_contains "high-high.json names the blocking file:line" "client.go:27" "$OUT"

echo ""
echo "== a scan that could not run is NOT a clean scan =="
# security-scan.sh's own header: "A silently-skipped scan is indistinguishable
# from a clean one." Each of these produces "no High/High findings" under a
# classifier that only counts matching issues.
run_check empty-scan.json
assert_eq "empty-scan.json (0 files) → 2, not 0" "2" "$RC"
assert_contains "empty-scan.json says why" "covered 0 files" "$OUT"
assert_not_contains "empty-scan.json does not read as a finding count" "scanned 0 file(s)" "$OUT"

run_check truncated.json
assert_eq "truncated.json (unparseable) → 2" "2" "$RC"
assert_contains "truncated.json says it is not valid JSON" "not valid JSON" "$OUT"

run_check no-stats.json
assert_eq "no-stats.json (parses, no .Stats) → 2" "2" "$RC"
assert_contains "no-stats.json refuses to read it as clean" "refusing to read it as a clean scan" "$OUT"

run_check empty-file.json
assert_eq "empty-file.json (gosec wrote nothing) → 2" "2" "$RC"
assert_contains "empty-file.json says gosec produced nothing" "produced nothing to read" "$OUT"

OUT=$(gosec_report_check "$DATA/does-not-exist.json" "testmod" 2>&1); RC=$?
assert_eq "a missing report → 2" "2" "$RC"

OUT=$(gosec_report_check "" "testmod" 2>&1); RC=$?
assert_eq "no report path at all → 2" "2" "$RC"
assert_contains "an empty path is named as such" "<none>" "$OUT"

echo ""
echo "== every failure names the module, so a six-module sweep is readable =="
for f in empty-scan.json truncated.json no-stats.json empty-file.json; do
  run_check "$f"
  assert_contains "$f names the module in its refusal" "testmod" "$OUT"
done

echo ""
if [[ "$fails" -eq 0 ]]; then
  echo "gosec-report_test: ALL PASS"
else
  echo "gosec-report_test: $fails FAILED" >&2
  exit 1
fi
