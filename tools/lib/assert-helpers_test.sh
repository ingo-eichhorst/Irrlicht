#!/usr/bin/env bash
# assert-helpers_test.sh — grade the two shared assertion helpers,
# tools/lib/mutation-assert.sh and tools/lib/checker-assert.sh, on an expected
# message that begins with `-` (#2045).
#
# WHY THIS EXISTS. Both helpers fixed-string match a caller-supplied expected
# message against captured output. Before #2045 they passed it to grep without
# `--`, so a message such as `--max-age-hours is required` was parsed as a grep
# option: grep exited 2 on an unknown option and the helper reported a correct
# failure as the WRONG failure. Neither helper had a self-test, so nothing
# could show it. The two "matches" rows were run red against the pre-#2045
# helpers before the `--` was added: grep printed "unrecognized option
# `--max-age-hours is required'" and both helpers reported FAIL. The two
# "still reports" rows are controls that pass either way; they keep a helper
# that matches everything from satisfying the first two.
#
# The stubs stand in for the real collaborators so the helpers' own matching is
# what is graded: MUTATE_SH is a script that prints a fixed lock-test output and
# exits 0 (mutate.sh's "mutation applied, tree restored" status), and the
# checker is a shell function.
set -uo pipefail

DIR=$(cd "$(dirname "$0")" && pwd)
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

status=0
bad() { echo "FAIL: $1" >&2; status=1; }
ok() { echo "  PASS: $1"; }

# ── mutation-assert.sh ──────────────────────────────────────────────────────
# shellcheck source=tools/lib/mutation-assert.sh
. "$DIR/mutation-assert.sh"

cat >"$TMP/mutate-stub.sh" <<'EOF'
#!/usr/bin/env bash
echo 'FAIL: --max-age-hours is required'
exit 0
EOF
chmod +x "$TMP/mutate-stub.sh"

# shellcheck disable=SC2034  # read by assert_mutation_is_red
REPO_ROOT=$TMP
# shellcheck disable=SC2034  # read by assert_mutation_is_red
MUTATE_SH=$TMP/mutate-stub.sh
# shellcheck disable=SC2034  # read by assert_mutation_is_red
LOCK_TEST=stub-lock-test.sh

# A message the output does contain, which starts with `--`, must match.
fails=0
assert_mutation_is_red 'dash-led message' f a r '--max-age-hours is required' >"$TMP/out" 2>&1
out=$(cat "$TMP/out")
if [ "$fails" -eq 0 ] && grep -qF 'ok  dash-led message' <<<"$out"; then
  ok 'mutation-assert matches an expected message that starts with --'
else
  bad "mutation-assert rejected a dash-led message the output contains: $out"
fi

# Control: a dash-led message the output does NOT contain must still be
# reported, so the row above cannot pass by the helper matching anything.
fails=0
assert_mutation_is_red 'absent dash-led message' f a r '--no-such-flag' >"$TMP/out" 2>&1
out=$(cat "$TMP/out")
if [ "$fails" -eq 1 ] && grep -qF 'not with the expected message' <<<"$out"; then
  ok 'mutation-assert still reports a dash-led message the output lacks'
else
  bad "mutation-assert did not report an absent dash-led message (fails=$fails): $out"
fi

# ── checker-assert.sh ───────────────────────────────────────────────────────
# shellcheck source=tools/lib/checker-assert.sh
. "$DIR/checker-assert.sh"

stub_checker() { echo '--git-common-dir answered relative'; return 1; }

rc=0
if assert_checker_rc stub_checker 'dash-led needle' 1 '--git-common-dir answered' >/dev/null 2>&1 &&
  [ "$rc" -eq 0 ]; then
  ok 'checker-assert matches a needle that starts with --'
else
  bad 'checker-assert rejected a dash-led needle the output contains'
fi

rc=0
if ! assert_checker_rc stub_checker 'absent dash-led needle' 1 '--no-such-flag' >/dev/null 2>&1 &&
  [ "$rc" -eq 1 ]; then
  ok 'checker-assert still reports a dash-led needle the output lacks'
else
  bad 'checker-assert did not report an absent dash-led needle'
fi

[ "$status" -eq 0 ] && echo 'OK: assert-helpers_test — both helpers match a dash-led expected message'
exit "$status"
