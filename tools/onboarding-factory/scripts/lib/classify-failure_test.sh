#!/usr/bin/env bash
# classify-failure_test.sh — unit tests for classify-failure.sh. Plain bash +
# jq (no framework). Run directly or via scripts/smoke-test.sh.
#
# classify-failure.sh is invoked as a subprocess (not sourced — it calls
# `exit 0` from inside emit()), so each case builds a fake staging dir and
# checks the emitted JSON's .code field. Added alongside #1018's daemon_crashed
# branch, which was the first classification to read $STAGING/daemon.log —
# previously staged but never inspected.

set -uo pipefail   # NOT -e: assertions capture non-zero return codes

DIR="$(cd "$(dirname "$0")" && pwd)"
SCRIPT="$DIR/classify-failure.sh"

command -v jq >/dev/null || { echo "classify-failure_test: jq is required" >&2; exit 2; }

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

fails=0
pass() { local label="$1"; echo "  PASS: $label"; return 0; }
fail() { local label="$1" expected="$2" got="$3"; echo "  FAIL: $label — expected [$expected] got [$got]"; fails=$((fails + 1)); return 0; }
assert_code() {
  local label="$1" expected="$2" staging="$3"
  local got
  got="$(bash "$SCRIPT" "$staging" | jq -r '.code')"
  [[ "$got" == "$expected" ]] && pass "$label" || fail "$label" "$expected" "$got"
}

echo "== daemon_crashed: panic in daemon.log =="
d="$TMP/panic"; mkdir -p "$d"
printf 'starting up\npanic: runtime error: invalid memory address\n' > "$d/daemon.log"
assert_code "panic: -> daemon_crashed" "daemon_crashed" "$d"

echo "== daemon_crashed: fatal error in daemon.log =="
d="$TMP/fatal"; mkdir -p "$d"
printf 'fatal error: all goroutines are asleep - deadlock!\n' > "$d/daemon.log"
assert_code "fatal error: -> daemon_crashed" "daemon_crashed" "$d"

echo "== daemon.log present but clean: falls through, not daemon_crashed =="
d="$TMP/clean"; mkdir -p "$d"
printf 'starting up\nlistening on :7837\nshutting down cleanly\n' > "$d/daemon.log"
assert_code "clean daemon.log -> unknown, not daemon_crashed" "unknown" "$d"

echo "== pre-existing branches still work (regression guard) =="
d="$TMP/auth"; mkdir -p "$d"
printf 'please log in to continue\n' > "$d/driver.log"
assert_code "auth failure -> auth_failed" "auth_failed" "$d"

d="$TMP/dirty"; mkdir -p "$d"
printf 'another irrlichd is running on port 7837\n' > "$d/precheck.log"
assert_code "daemon already running -> daemon_dirty" "daemon_dirty" "$d"

echo "== no signal at all -> unknown =="
d="$TMP/empty"; mkdir -p "$d"
assert_code "nothing staged -> unknown" "unknown" "$d"

echo ""
if [[ "$fails" -eq 0 ]]; then
  echo "classify-failure_test: ALL PASS"
else
  echo "classify-failure_test: $fails FAILED" >&2
  exit 1
fi
