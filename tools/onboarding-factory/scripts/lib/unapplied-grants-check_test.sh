#!/usr/bin/env bash
# unapplied-grants-check_test.sh — unit tests for unapplied-grants-check.sh.
# Plain bash (no framework). Run directly or via scripts/smoke-test.sh.
#
# curl is shadowed with a fake function that returns canned JSON keyed off
# FAKE_PERM_JSON, the same "shadow the external command" idiom
# spawn-record-daemon_test.sh uses for kill: what is under test is the
# DECISION (does an unapplied grant for THIS adapter refuse?), never the
# network round-trip.

set -uo pipefail   # NOT -e: assertions capture non-zero return codes

DIR="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=unapplied-grants-check.sh
source "$DIR/unapplied-grants-check.sh"

fails=0
pass() { local label="$1"; echo "  PASS: $label"; return 0; }
fail() { local label="$1" expected="$2" got="$3"; echo "  FAIL: $label — expected [$expected] got [$got]"; fails=$((fails + 1)); return 0; }
assert_eq() {
  local label="$1" expected="$2" got="$3"
  [[ "$got" == "$expected" ]] && pass "$label" || fail "$label" "$expected" "$got"
  return 0
}

FAKE_PERM_JSON=""
curl() {
  # Mirror the real invocation shape closely enough that a future flag-order
  # change here would be visibly wrong, without hard-asserting every flag.
  printf '%s' "$FAKE_PERM_JSON"
  return 0
}

echo "== no unapplied grants at all: passes =="
FAKE_PERM_JSON='{"mode":"grant-all","unapplied_grants":[]}'
check_unapplied_grants "127.0.0.1:7838" "mistral-vibe" >/dev/null 2>&1
assert_eq "returns 0" "0" "$?"

echo "== unapplied grant for THIS adapter (#1362's hooktoml-refusal shape): refuses and names it =="
FAKE_PERM_JSON='{"mode":"grant-all","unapplied_grants":[{"agent":"mistral-vibe","key":"hooks","reason":"hooktoml write refused"}]}'
out="$(check_unapplied_grants "127.0.0.1:7838" "mistral-vibe" 2>&1 >/dev/null)"
rc=$?
assert_eq "returns 1" "1" "$rc"
case "$out" in
  *"mistral-vibe/hooks: hooktoml write refused"*) pass "names the refusal" ;;
  *) fail "names the refusal" "mistral-vibe/hooks: hooktoml write refused" "$out" ;;
esac

echo "== unapplied grant for a DIFFERENT adapter: does not block this cell =="
FAKE_PERM_JSON='{"mode":"grant-all","unapplied_grants":[{"agent":"codex","key":"hooks","reason":"version floor"}]}'
check_unapplied_grants "127.0.0.1:7838" "mistral-vibe" >/dev/null 2>&1
assert_eq "returns 0 (scoped to the adapter this cell records)" "0" "$?"

echo "== pre-#570 daemon / unreachable endpoint (empty response): nothing to check, passes =="
FAKE_PERM_JSON=''
check_unapplied_grants "127.0.0.1:7838" "mistral-vibe" >/dev/null 2>&1
assert_eq "returns 0" "0" "$?"

echo "== daemon too old to expose unapplied_grants at all: passes =="
FAKE_PERM_JSON='{"mode":"grant-all"}'
check_unapplied_grants "127.0.0.1:7838" "mistral-vibe" >/dev/null 2>&1
assert_eq "returns 0" "0" "$?"

echo
if [[ "$fails" -eq 0 ]]; then
  echo "all unapplied-grants-check_test.sh cases passed"
  exit 0
else
  echo "$fails unapplied-grants-check_test.sh case(s) failed"
  exit 1
fi
