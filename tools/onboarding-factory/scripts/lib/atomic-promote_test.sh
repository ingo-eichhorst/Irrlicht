#!/usr/bin/env bash
# atomic-promote_test.sh — unit tests for atomic-promote.sh. Plain bash (no
# framework), matching the style of classify-failure_test.sh. Run directly or via
# scripts/smoke-test.sh.
#
# The real validator is `go run ./cmd/expected-validate`, which is far too slow
# and too repo-coupled for a unit test, so atomic_promote takes the validator as
# an injected function and these tests pass fakes.
#
# Why this exists (#1333, finding B2). promote-recording.sh used to mkdir the
# FINAL recording dir, copy into it, and only then validate — printing
# "the recording is in place but the validator is unhappy" and exit 3 with the
# bad recording already committed to the tree. Recovering meant a manual
# `rm -rf` of the promoted directory before re-promoting (hit on
# 2-8_autonomous-loop-iteration-limit). `of validate` doesn't catch the leftover
# either: it gates recording COMPLETENESS (events, manifest, transcript, golden),
# never the manifest's pass rate, so a partially-passing recording sits in the
# tree looking complete.

set -uo pipefail   # NOT -e: assertions capture non-zero return codes

DIR="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=atomic-promote.sh
source "$DIR/atomic-promote.sh"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

fails=0
pass() { local label="$1"; echo "  PASS: $label"; return 0; }
fail() { local label="$1" expected="$2" got="$3"; echo "  FAIL: $label — expected [$expected] got [$got]"; fails=$((fails + 1)); return 0; }
assert_eq() {
  local label="$1" expected="$2" got="$3"
  [[ "$got" == "$expected" ]] && pass "$label" || fail "$label" "$expected" "$got"
  return 0
}

# --- injected fakes ------------------------------------------------------
populate_ok()   { printf 'events\n' > "$1/events.jsonl"; printf 'transcript\n' > "$1/transcript.jsonl"; return 0; }
populate_fails() { return 1; }
validate_pass() { echo "4/4 phases"; return 0; }
validate_fail() { echo "3/4 phases"; return 1; }

# Count leftover scratch dirs so a "cleaned up on failure" claim is checked, not
# assumed — a stranded .promote-tmp would be the same class of bug.
scratch_count() { find "$1" -maxdepth 1 -name '.promote-tmp*' | wc -l | tr -d ' '; }

new_cell() {
  local d="$TMP/$1"
  mkdir -p "$d/recordings"
  [[ "${2:-}" == "with-spec" ]] && printf '{"schema_version":1}\n' > "$d/expected.jsonl"
  echo "$d"
}

echo "== happy path: validated, then moved into place =="
cell="$(new_cell happy with-spec)"
out="$(atomic_promote "$cell" "2026-01-01-00-00-00_irrlichd-x" populate_ok validate_pass)"
rc=$?
assert_eq "returns 0" "0" "$rc"
assert_eq "echoes the validator summary" "4/4 phases" "$out"
[[ -f "$cell/recordings/2026-01-01-00-00-00_irrlichd-x/events.jsonl" ]] \
  && pass "recording is in place" || fail "recording is in place" "events.jsonl" "missing"
assert_eq "no scratch dir left" "0" "$(scratch_count "$cell")"

echo "== validation fails: NOTHING is written (this is B2) =="
cell="$(new_cell reject with-spec)"
out="$(atomic_promote "$cell" "2026-01-01-00-00-00_irrlichd-x" populate_ok validate_fail)"
rc=$?
assert_eq "returns 3" "3" "$rc"
assert_eq "still reports the pass rate" "3/4 phases" "$out"
[[ -e "$cell/recordings/2026-01-01-00-00-00_irrlichd-x" ]] \
  && fail "no recording left behind" "absent" "present" || pass "no recording left behind"
assert_eq "no scratch dir left" "0" "$(scratch_count "$cell")"

echo "== a failed promote must not damage the PREVIOUS good recording =="
cell="$(new_cell keepold with-spec)"
mkdir -p "$cell/recordings/2025-12-31-00-00-00_irrlichd-old"
printf 'good\n' > "$cell/recordings/2025-12-31-00-00-00_irrlichd-old/events.jsonl"
atomic_promote "$cell" "2026-01-01-00-00-00_irrlichd-x" populate_ok validate_fail >/dev/null
assert_eq "the old recording survives" "good" "$(cat "$cell/recordings/2025-12-31-00-00-00_irrlichd-old/events.jsonl")"

echo "== no expected.jsonl: promotes without validating =="
cell="$(new_cell nospec)"
atomic_promote "$cell" "2026-01-01-00-00-00_irrlichd-x" populate_ok validate_fail >/dev/null
rc=$?
assert_eq "returns 0 (validator never consulted)" "0" "$rc"
[[ -f "$cell/recordings/2026-01-01-00-00-00_irrlichd-x/events.jsonl" ]] \
  && pass "recording is in place" || fail "recording is in place" "events.jsonl" "missing"

echo "== populate fails: nothing written, nothing stranded =="
cell="$(new_cell popfail with-spec)"
atomic_promote "$cell" "2026-01-01-00-00-00_irrlichd-x" populate_fails validate_pass >/dev/null
rc=$?
assert_eq "returns 1" "1" "$rc"
[[ -e "$cell/recordings/2026-01-01-00-00-00_irrlichd-x" ]] \
  && fail "no recording left behind" "absent" "present" || pass "no recording left behind"
assert_eq "no scratch dir left" "0" "$(scratch_count "$cell")"

echo "== the validator sees the candidate under its own cell dir =="
# expected-validate takes <cell-dir> <recording-name> and resolves
# <cell-dir>/recordings/<name>, so the candidate must be reachable that way
# BEFORE it is moved into the real cell.
cell="$(new_cell reachable with-spec)"
validate_checks_layout() {
  local cd="$1" name="$2"
  [[ -f "$cd/expected.jsonl" ]]              || { echo "no spec"; return 1; }
  [[ -f "$cd/recordings/$name/events.jsonl" ]] || { echo "no candidate"; return 1; }
  echo "4/4 phases"; return 0
}
out="$(atomic_promote "$cell" "2026-01-01-00-00-00_irrlichd-x" populate_ok validate_checks_layout)"
assert_eq "validator resolved spec + candidate" "4/4 phases" "$out"

echo ""
if [[ "$fails" -eq 0 ]]; then
  echo "atomic-promote_test: ALL PASS"
else
  echo "atomic-promote_test: $fails FAILED" >&2
  exit 1
fi
