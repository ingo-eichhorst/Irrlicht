#!/usr/bin/env bash
# rebase-conflict-check_test.sh — proof, both directions, for
# tools/lib/rebase-conflict-check.sh (#1824).
#
# WHY THIS FILE EXISTS. rebase-conflict-check.sh is a check #1824 ADDS: it has
# no "before the fix" to run red against, so per AGENTS.md and
# docs/testing-philosophy.md it earns its place only by being seen to fail
# when the thing it protects is broken, and to pass when it isn't. Both
# fixtures below are committed under tools/lib/testdata/rebase-conflict-check/
# rather than generated on the fly, so this evidence outlives the PR that
# wrote it and re-runs on every future change to the checker.
set -uo pipefail

REPO_ROOT=$(git rev-parse --show-toplevel)
cd "$REPO_ROOT" || { echo "FAIL: cannot cd to repo root $REPO_ROOT" >&2; exit 1; }

# shellcheck source=./rebase-conflict-check.sh
. tools/lib/rebase-conflict-check.sh   # defines rebase_conflict_check

FIXTURE_DIR=tools/lib/testdata/rebase-conflict-check
rc=0
fail() { echo "FAIL: $1" >&2; rc=1; }

# assert_rc <label> <expected-rc> <arg...> -- <must-contain-in-output>
# The literal `--` separates the checker's own arguments from the single
# "must contain" string, so a case can pass more than one file.
assert_rc() {
  local label="$1" want="$2"
  shift 2
  local args=()
  while [ "$#" -gt 0 ] && [ "$1" != "--" ]; do
    args+=("$1")
    shift
  done
  shift   # drop the --
  local want_text="${1:-}"
  local out got
  # "${args[@]+"${args[@]}"}" rather than a bare "${args[@]}": under bash 3.2
  # (this repo's macOS baseline — AGENTS.md's gate-budget.sh notes the same
  # constraint), `set -u` treats a truly EMPTY array's `[@]` expansion as an
  # unbound variable, which the no-arguments refusal case below exercises on
  # purpose.
  out=$(rebase_conflict_check "${args[@]+"${args[@]}"}" 2>&1)
  got=$?
  if [ "$got" -ne "$want" ]; then
    fail "$label: expected exit $want, got $got (output: $out)"
    return
  fi
  if [ -n "$want_text" ] && [[ "$out" != *"$want_text"* ]]; then
    fail "$label: exit $got was right but output missing '$want_text' — got: $out"
    return
  fi
  echo "  PASS: $label (exit $got)"
}

# --- RED: a file that still carries the marker ------------------------------
assert_rc "a file with an unresolved <<<<<<< marker is a FINDING (exit 1)" \
  1 "$FIXTURE_DIR/with-marker.txt" -- \
  "CONFLICT: $FIXTURE_DIR/with-marker.txt:4:<<<<<<< HEAD"

# --- GREEN: a clean file --------------------------------------------------
assert_rc "a clean file passes (exit 0)" \
  0 "$FIXTURE_DIR/clean.txt" -- \
  "OK: rebase-conflict-check"

# --- mixed set: one clean, one marked — the marker still surfaces ----------
assert_rc "one clean plus one marked file still FAILS, and names the marked one" \
  1 "$FIXTURE_DIR/clean.txt" "$FIXTURE_DIR/with-marker.txt" -- \
  "CONFLICT: $FIXTURE_DIR/with-marker.txt:4:<<<<<<< HEAD"

# --- a path git diff --name-only would list for a DELETE is a silent skip --
# Not a refusal: a deleted file has nothing left to scan, and `git diff
# --name-only` lists deletions alongside adds/modifies.
assert_rc "a path that does not exist (a deletion) is skipped, not refused" \
  0 "$FIXTURE_DIR/clean.txt" "$FIXTURE_DIR/does-not-exist.txt" -- \
  "OK: rebase-conflict-check"

# --- refusal: a real I/O problem, not a deletion ----------------------------
assert_rc "a directory argument is a REFUSAL (exit 2), not a silent pass" \
  2 "$FIXTURE_DIR" -- \
  "is a directory, not a file"

assert_rc "no arguments at all is a REFUSAL (exit 2)" \
  2 -- \
  "no files named"

# --- vacuity guard: real, current, non-fixture files must pass -------------
# Without this, every case above could pass while the checker was wired to
# nothing but its own fixtures. This checker's own source and AGENTS.md are
# both real tracked files with no conflict markers in the current tree — this
# must currently PASS.
assert_rc "the checker's own source file passes its own check" \
  0 "tools/lib/rebase-conflict-check.sh" -- \
  "OK: rebase-conflict-check"
assert_rc "the repo's real, current AGENTS.md passes" \
  0 "AGENTS.md" -- \
  "OK: rebase-conflict-check"

[ "$rc" -eq 0 ] && echo "OK: rebase-conflict-check_test — catches an unresolved marker and passes a clean tree, both directions proven"
exit "$rc"
