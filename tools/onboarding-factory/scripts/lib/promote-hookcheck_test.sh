#!/usr/bin/env bash
# promote-hookcheck_test.sh — unit tests for promote-hookcheck.sh. Plain bash
# (no framework), matching atomic-promote_test.sh's style. Run directly or via
# scripts/smoke-test.sh.
#
# check_fn and confirm_fn are injected fakes (see the lib header) so these
# tests pin the decision table without invoking `go run ./cmd/of hookcheck`
# or a real terminal — the same shape atomic-promote_test.sh already uses for
# its populate/validate functions.

set -uo pipefail   # NOT -e: assertions capture non-zero return codes

DIR="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=promote-hookcheck.sh
source "$DIR/promote-hookcheck.sh"

fails=0
pass() { local label="$1"; echo "  PASS: $label"; return 0; }
fail() { local label="$1" expected="$2" got="$3"; echo "  FAIL: $label — expected [$expected] got [$got]"; fails=$((fails + 1)); return 0; }
assert_eq() {
  local label="$1" expected="$2" got="$3"
  [[ "$got" == "$expected" ]] && pass "$label" || fail "$label" "$expected" "$got"
  return 0
}

# --- injected fakes -------------------------------------------------------
# Each check_* prints "<declares> <has_hook>" and exits 0, or fails per its name.
check_declares_has_hook()    { echo "true true"; return 0; }
check_declares_no_hook()     { echo "true false"; return 0; }
check_no_declares_no_hook()  { echo "false false"; return 0; }
check_broken()               { echo "of hookcheck: connection refused" >&2; return 1; }

# confirm_* — CONFIRM_CALLS counts invocations so a case can assert the
# operator was never even asked (the healthy-path cases must short-circuit
# before confirm_fn runs at all).
CONFIRM_CALLS=0
confirm_yes() { CONFIRM_CALLS=$((CONFIRM_CALLS + 1)); return 0; }
confirm_no()  { CONFIRM_CALLS=$((CONFIRM_CALLS + 1)); return 1; }
confirm_must_not_be_called() {
  CONFIRM_CALLS=$((CONFIRM_CALLS + 1))
  echo "confirm_fn was called when it should not have been" >&2
  return 1
}

echo "== declares hooks, has one — the healthy case: promote, never ask =="
CONFIRM_CALLS=0
promote_hookcheck codex /tmp/x.jsonl check_declares_has_hook confirm_must_not_be_called >/dev/null 2>&1
rc=$?
assert_eq "returns 0" "0" "$rc"
assert_eq "confirm_fn not called" "0" "$CONFIRM_CALLS"

echo "== declares no hooks — nothing to ask regardless of has_hook =="
CONFIRM_CALLS=0
promote_hookcheck aider /tmp/x.jsonl check_no_declares_no_hook confirm_must_not_be_called >/dev/null 2>&1
rc=$?
assert_eq "returns 0" "0" "$rc"
assert_eq "confirm_fn not called" "0" "$CONFIRM_CALLS"

echo "== the check fires: hook-free recording of a hooks-declaring adapter, operator says no =="
CONFIRM_CALLS=0
promote_hookcheck mistral-vibe /tmp/x.jsonl check_declares_no_hook confirm_no >/dev/null 2>&1
rc=$?
assert_eq "refuses" "1" "$rc"
assert_eq "confirm_fn was asked" "1" "$CONFIRM_CALLS"

echo "== the override path: same hook-free case, operator says yes =="
CONFIRM_CALLS=0
promote_hookcheck mistral-vibe /tmp/x.jsonl check_declares_no_hook confirm_yes >/dev/null 2>&1
rc=$?
assert_eq "promotes" "0" "$rc"
assert_eq "confirm_fn was asked" "1" "$CONFIRM_CALLS"

echo "== the check itself cannot run: refuse rather than promote blind =="
CONFIRM_CALLS=0
promote_hookcheck codex /tmp/x.jsonl check_broken confirm_must_not_be_called >/dev/null 2>&1
rc=$?
assert_eq "returns 2 (check failure, distinct from a real gap)" "2" "$rc"
assert_eq "confirm_fn not called" "0" "$CONFIRM_CALLS"

# --- default_hookfree_check: hardened jq parsing --------------------------
# `go` is shadowed (same idiom as curl elsewhere) so these exercise the REAL
# check_fn's parsing without a live `of hookcheck` build. REPO_ROOT must
# resolve so `cd "$REPO_ROOT"` doesn't fail before the fake `go` ever runs.
# shellcheck disable=SC2034  # read by the SOURCED default_hookfree_check,
# not by this file — same shape as the other sourced-knob disables above.
REPO_ROOT="$PWD"

echo "== default_hookfree_check: well-formed output still parses (lock) =="
go() { printf '{"agent":"codex","declares_hooks":true,"has_hook_event":false}'; return 0; }
out="$(default_hookfree_check codex /tmp/x.jsonl 2>/dev/null)"
rc=$?
assert_eq "returns 0" "0" "$rc"
assert_eq "echoes 'true false'" "true false" "$out"

echo "== default_hookfree_check: declares_hooks MISSING entirely — refused, not read as false =="
go() { printf '{"agent":"codex","has_hook_event":false}'; return 0; }
default_hookfree_check codex /tmp/x.jsonl >/dev/null 2>&1
assert_eq "returns 1 (a missing field is a parse failure, not a false)" "1" "$?"

echo "== default_hookfree_check: declares_hooks is JSON null — jq prints the string 'null', still refused =="
go() { printf '{"agent":"codex","declares_hooks":null,"has_hook_event":false}'; return 0; }
default_hookfree_check codex /tmp/x.jsonl >/dev/null 2>&1
assert_eq "returns 1 (nonempty 'null' must not slip past an emptiness-only guard)" "1" "$?"

echo "== default_hookfree_check: has_hook_event missing while declares_hooks is fine — still refused =="
go() { printf '{"agent":"codex","declares_hooks":true}'; return 0; }
default_hookfree_check codex /tmp/x.jsonl >/dev/null 2>&1
assert_eq "returns 1" "1" "$?"

echo
if [[ "$fails" -eq 0 ]]; then
  echo "all promote-hookcheck_test.sh cases passed"
  exit 0
else
  echo "$fails promote-hookcheck_test.sh case(s) failed"
  exit 1
fi
