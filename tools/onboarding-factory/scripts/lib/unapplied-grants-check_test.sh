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

echo "== a pre-fetched perm_json (3rd arg) is used as-is — no curl call =="
# curl is rigged to return a DIRTY snapshot; if the 3rd arg were ignored and
# curl called anyway, this would wrongly refuse (return 1) instead of 0.
curl() { printf '%s' '{"mode":"grant-all","unapplied_grants":[{"agent":"mistral-vibe","key":"hooks","reason":"curl should not have been called"}]}'; return 0; }
check_unapplied_grants "127.0.0.1:7838" "mistral-vibe" '{"mode":"grant-all","unapplied_grants":[]}' >/dev/null 2>&1
assert_eq "returns 0 (reused the pre-fetched clean JSON, never fetched a second time)" "0" "$?"
curl() { printf '%s' "$FAKE_PERM_JSON"; return 0; }   # restore the plain fake for anything below

# --- wait_for_unapplied_grants_clear: the poll wrapper ---------------------
# Ticks fast (no real sleep) and few, so the "never clears" case below doesn't
# spend wall time. curl runs inside `$(...)` command substitution, i.e. its
# own subshell — a plain variable it incremented would be discarded the
# moment that subshell exits, so CALLS is a FILE (surviving across subshells)
# rather than a variable, and calls() reads it back for the assertions.
UNAPPLIED_GRANTS_WAIT_TICKS=4
# shellcheck disable=SC2034  # read by the SOURCED library
# (wait_for_unapplied_grants_clear's `sleep "$UNAPPLIED_GRANTS_WAIT_TICK_S"`),
# not by this file — the linter does not follow a source through a variable
# path, the same shape spawn-record-daemon_test.sh notes for its own knobs.
UNAPPLIED_GRANTS_WAIT_TICK_S=0
CALLS_FILE="$(mktemp)"
trap 'rm -f "$CALLS_FILE"' EXIT
FAKE_SEQUENCE=()   # one JSON string per call; the last entry repeats past the end

calls() { cat "$CALLS_FILE"; return 0; }

curl() {
  local n
  n=$(($(cat "$CALLS_FILE") + 1))
  echo "$n" > "$CALLS_FILE"
  local idx=$((n - 1))
  if (( idx < ${#FAKE_SEQUENCE[@]} )); then
    printf '%s' "${FAKE_SEQUENCE[$idx]}"
  else
    # bash 3.2 (stock macOS) has no negative array indices — clamp instead.
    printf '%s' "${FAKE_SEQUENCE[${#FAKE_SEQUENCE[@]}-1]}"
  fi
  return 0
}

CLEAN='{"mode":"grant-all","unapplied_grants":[]}'
DIRTY='{"mode":"grant-all","unapplied_grants":[{"agent":"gemini-cli","key":"hooks","reason":"hooktoml write refused"}]}'

echo "== poll wrapper: unapplied grant present on the FIRST poll — refuses without waiting out the ceiling =="
echo 0 > "$CALLS_FILE"
FAKE_SEQUENCE=("$DIRTY")
wait_for_unapplied_grants_clear "127.0.0.1:7838" "gemini-cli" >/dev/null 2>&1
assert_eq "returns 1" "1" "$?"
assert_eq "stopped at the first poll" "1" "$(calls)"

echo "== poll wrapper: THE RACE — clean on polls 1-2 (Apply still mid-flight), unapplied on poll 3 =="
echo 0 > "$CALLS_FILE"
FAKE_SEQUENCE=("$CLEAN" "$CLEAN" "$DIRTY")
wait_for_unapplied_grants_clear "127.0.0.1:7838" "gemini-cli" >/dev/null 2>&1
assert_eq "still refuses (a later poll catches what the first sample missed)" "1" "$?"
assert_eq "took 3 polls to see it" "3" "$(calls)"

echo "== poll wrapper: never becomes unapplied within the ceiling — clean, and actually polled the full ceiling =="
echo 0 > "$CALLS_FILE"
FAKE_SEQUENCE=("$CLEAN")
wait_for_unapplied_grants_clear "127.0.0.1:7838" "gemini-cli" >/dev/null 2>&1
assert_eq "returns 0" "0" "$?"
assert_eq "polled the full ceiling rather than returning after one sample" "$UNAPPLIED_GRANTS_WAIT_TICKS" "$(calls)"

echo
if [[ "$fails" -eq 0 ]]; then
  echo "all unapplied-grants-check_test.sh cases passed"
  exit 0
else
  echo "$fails unapplied-grants-check_test.sh case(s) failed"
  exit 1
fi
