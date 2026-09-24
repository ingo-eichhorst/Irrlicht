#!/usr/bin/env bash
# Drive tools/lib/pr-exists.sh against a stub gh, and prove the substring
# idiom it replaces is genuinely fooled by the same input (#2029).
#
# The load-bearing case is a head that is a PREFIX of another PR's head
# (`fix/x` vs `fix/x-2`). It is asserted from both ends, as ref-exists_test.sh
# does for its object-id row: the naive form must say "found" and pr_exists
# must say "absent". Asserting only the second half would leave open that the
# stub simply has no matching row, which would make the fixture vacuous.
set -uo pipefail # NOT -e: assertions capture non-zero return codes

REPO_ROOT=$(git rev-parse --show-toplevel)
cd "$REPO_ROOT" || { echo "FAIL: cannot cd to repo root $REPO_ROOT" >&2; exit 1; }

STUB=tools/lib/testdata/pr-exists/gh-stub.sh
[ -r "$STUB" ] || { echo "FAIL: pr-exists_test — stub $STUB not found" >&2; exit 1; }

# shellcheck source=tools/lib/pr-exists.sh
. tools/lib/pr-exists.sh # defines pr_exists

rc=0
fail() { echo "FAIL: $1" >&2; rc=1; }

STUB_BIN=$(mktemp -d "${TMPDIR:-/tmp}/pr-exists-stub.XXXXXX")
trap 'rm -rf "$STUB_BIN"' EXIT
cp "$STUB" "$STUB_BIN/gh"
chmod +x "$STUB_BIN/gh"
LOG="$STUB_BIN/queries.log"

# assert_rc <label> <expected-rc> <branch> [ENV=value ...]
assert_rc() {
  local label="$1" want="$2" branch="$3" out got
  shift 3
  : >"$LOG"
  out=$(env "$@" PATH="$STUB_BIN:$PATH" PR_EXISTS_STUB_LOG="$LOG" \
    bash tools/lib/pr-exists.sh "$branch" 2>&1)
  got=$?
  if [ "$got" -ne "$want" ]; then
    fail "$label — wanted exit $want, got $got"
    echo "$out" | sed 's/^/      | /' >&2
    return
  fi
  echo "  PASS: $label (exit $got)"
}

# ── 1. The defect this file exists for ──────────────────────────────────────
# The stub's table holds a PR for `fix/x-2` and none for `fix/x`.
naive=$(PATH="$STUB_BIN:$PATH" gh pr list --state all --limit 100 --json headRefName | grep -c 'fix/x')
if [ "$naive" -lt 1 ]; then
  fail 'the stub no longer carries a head that fix/x is a prefix of; this fixture would prove nothing'
else
  echo "  PASS: the naive \`gh pr list | grep fix/x\` idiom matches $naive row(s) — the false positive is real"
fi

assert_rc 'a head that is only a prefix of another PR head reports 1' 1 'fix/x'

# The stub was actually consulted for that answer — an exit 1 that never
# reached gh would be a refusal wearing the wrong status.
if grep -qxF 'fix/x' "$LOG"; then
  echo '  PASS: pr_exists asked gh about exactly fix/x'
else
  fail 'pr_exists returned without asking gh about fix/x'
fi

# ── 2. Ordinary answers ─────────────────────────────────────────────────────
assert_rc 'a head with a PR reports 0' 0 'fix/x-2'
assert_rc 'a head with no PR reports 1' 1 'feat/4242-never-opened'
# #2029 review: --head matches a fork's PR too. A head whose only PR came
# from a fork has never been made visible from THIS repository.
assert_rc 'a head whose only PR is from a fork reports 1' 1 'fix/forked'

# ── 3. Refusals — could not look must never read as "no PR" ─────────────────
assert_rc 'a glob in the branch name is refused' 2 'feat/*'
assert_rc 'an empty branch name is refused' 2 ''
assert_rc 'gh failing is refused, not reported as absent' 2 'fix/x-2' PR_EXISTS_STUB_FAIL=1
assert_rc 'gh printing non-JSON is refused, not reported as absent' 2 'fix/x-2' PR_EXISTS_STUB_GARBAGE=1

bash tools/lib/pr-exists.sh >/dev/null 2>&1
got=$?
if [ "$got" -eq 2 ]; then
  echo "  PASS: a missing argument is refused (exit 2)"
else
  fail "a missing argument must refuse with 2, got $got"
fi

# Sourcing defines the function and runs nothing.
if ( . tools/lib/pr-exists.sh && declare -F pr_exists >/dev/null ); then
  echo '  PASS: sourcing defines pr_exists without running it'
else
  fail 'sourcing tools/lib/pr-exists.sh did not define pr_exists'
fi

# ── 4. Vacuity guard — the real repository, through the real gh ─────────────
# Without this, every case could pass against a stub whose --head filter is
# exact while the real one is not. Take a real PR head, check it reports 0,
# then drop its last character and check the prefix reports 1.
real_head=$(gh pr list --state all --limit 1 --json headRefName --jq '.[0].headRefName' 2>/dev/null)
if [ -z "$real_head" ]; then
  echo '  SKIP: gh cannot reach this repository (no auth or no network); the stub cases still ran'
else
  pr_exists "$real_head" >/dev/null 2>&1
  got=$?
  [ "$got" -eq 0 ] && echo "  PASS: real gh agrees that $real_head has a PR" ||
    fail "pr_exists said $real_head has no PR (exit $got); real gh just listed it"
  prefix=${real_head%?}
  pr_exists "$prefix" >/dev/null 2>&1
  got=$?
  case "$got" in
    1) echo "  PASS: real gh does not match the prefix '$prefix' to '$real_head'" ;;
    0)
      # A PR with exactly the prefix as its head is possible in principle, so
      # say which one before calling it a defect.
      exact=$(gh pr list --state all --head "$prefix" --json headRefName --jq '.[].headRefName' 2>/dev/null)
      if printf '%s\n' "$exact" | grep -qxF "$prefix"; then
        echo "  SKIP: '$prefix' really has its own PR; the prefix check needs another head"
      else
        fail "real gh matched the prefix '$prefix' to '$real_head' — --head is not exact"
      fi
      ;;
    *) fail "pr_exists refused with $got for '$prefix' while gh answered for '$real_head'" ;;
  esac
fi

if [ "$rc" -eq 0 ]; then
  echo 'OK: pr-exists_test — an exact-head PR check is not fooled by a prefix, and refuses when it cannot look'
fi
exit "$rc"
