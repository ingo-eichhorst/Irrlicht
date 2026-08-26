#!/usr/bin/env bash
# mutation-assert.sh — the shared `assert_mutation_is_red` helper for
# tools/mutate.sh-based mutation-fixture tests whose lock test reports its
# failures as `^FAIL: ` lines containing a specific message (#1823 review
# finding: this exact ~40-line function was copy-pasted byte-for-byte into
# two new fixture files added by the same diff; a third pre-existing fixture,
# tools/lib/error-retention-mutations_test.sh, hand-rolls a DIFFERENT 6-arg
# variant built around `go test -run <name>` rather than a lock-test script,
# so it is left as-is here rather than forced into this shape).
#
# Sourced, not executed:
#
#   . tools/lib/mutation-assert.sh
#   fails=0
#   assert_mutation_is_red "<label>" "<file>" "<anchor>" "<replacement>" "<want_match>"
#   [[ $fails -gt 0 ]] && exit 1
#
# Requires the caller to have already set (both are per-caller, not
# per-mutation, so they stay caller-side rather than becoming two more
# parameters every call site has to repeat):
#   REPO_ROOT   — passed to `cd` before invoking mutate.sh, so <file> and
#                 LOCK_TEST are read relative to the repo root regardless of
#                 the caller's own cwd.
#   MUTATE_SH   — path to tools/mutate.sh.
#   LOCK_TEST   — the lock-test script (repo-relative) that must go red
#                 under the mutation.
#   fails       — a plain integer the caller declared with `fails=0`; this
#                 function increments it in the caller's own shell (no
#                 subshell), so results accumulate across every call the
#                 caller makes before checking `$fails` once at the end.
set -uo pipefail

# assert_mutation_is_red <label> <file> <anchor> <replacement> <want_match>
#
# Applies one mutation via tools/mutate.sh and requires LOCK_TEST to FAIL
# under it, and to name `want_match` in its output.
#
# Both halves matter. A mutation that leaves the lock test green means the
# guard does not reach what it claims to cover. A mutation that goes red for
# the WRONG reason (a shell syntax error, a missing file) would otherwise
# read as success — so the expected failure text is asserted too, not just
# the exit status.
assert_mutation_is_red() {
  local label="$1" file="$2" anchor="$3" replacement="$4" want_match="$5"
  local out rc

  out="$(cd "$REPO_ROOT" && "$MUTATE_SH" "$file" "$anchor" "$replacement" \
    bash "$LOCK_TEST" 2>&1)"
  rc=$?

  # mutate.sh exits 0 when the mutation applied and the tree was restored,
  # WHATEVER the inner command did — the inner result is in the captured
  # text. A non-zero rc is mutate.sh itself refusing (STALE anchor, dirty
  # tree, ...), which is a fixture bug, not a finding about the guard.
  if [[ $rc -ne 0 ]]; then
    echo "FAIL: $label — mutate.sh refused (exit $rc). A STALE anchor means the surrounding text"
    echo "      moved and this fixture needs its anchor updated; it does NOT mean the guard is fine."
    echo "$out" | sed 's/^/      | /'
    fails=$((fails + 1))
    return
  fi

  # The mutation must make the lock test fail...
  if ! grep -qE '^FAIL:' <<<"$out"; then
    echo "FAIL: $label — $LOCK_TEST stayed GREEN under the mutation, so the guard does not reach"
    echo "      what it claims to protect."
    echo "$out" | sed 's/^/      | /'
    fails=$((fails + 1))
    return
  fi

  # ...and fail for the RIGHT reason.
  if ! grep -qF "$want_match" <<<"$out"; then
    echo "FAIL: $label — $LOCK_TEST failed, but not with the expected message."
    echo "      wanted to find: $want_match"
    echo "$out" | sed 's/^/      | /'
    fails=$((fails + 1))
    return
  fi

  echo "ok  $label"
}
