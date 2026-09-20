#!/usr/bin/env bash
# Drive tools/lib/ref-exists.sh against a stub git, and prove the substring
# idiom it replaces is genuinely fooled by the same input (#2022).
#
# The load-bearing case is `a 40-hex object id ending in the ticket digits`.
# It is asserted from BOTH ends: the naive form must say "found" and
# ref_exists must say "absent". Asserting only the second half would leave
# open the possibility that the stub simply has no matching row, which would
# make the whole fixture vacuous.
set -uo pipefail # NOT -e: assertions capture non-zero return codes

REPO_ROOT=$(git rev-parse --show-toplevel)
cd "$REPO_ROOT" || { echo "FAIL: cannot cd to repo root $REPO_ROOT" >&2; exit 1; }

STUB=tools/lib/testdata/ref-exists/git-stub.sh
[ -r "$STUB" ] || { echo "FAIL: ref-exists_test — stub $STUB not found" >&2; exit 1; }

# shellcheck source=tools/lib/ref-exists.sh
. tools/lib/ref-exists.sh # defines ref_exists

rc=0
fail() { echo "FAIL: $1" >&2; rc=1; }

# A PATH whose first entry answers `git` with the stub. The real git is still
# reachable behind it, which is why the stub refuses every subcommand but
# ls-remote rather than silently succeeding.
STUB_BIN=$(mktemp -d "${TMPDIR:-/tmp}/ref-exists-stub.XXXXXX")
trap 'rm -rf "$STUB_BIN"' EXIT
cp "$STUB" "$STUB_BIN/git"
chmod +x "$STUB_BIN/git"

# assert_rc <label> <expected-rc> <remote> <branch>
assert_rc() {
  local label="$1" want="$2" remote="$3" branch="$4" out got
  out=$(PATH="$STUB_BIN:$PATH" ref_exists "$remote" "$branch" 2>&1)
  got=$?
  if [ "$got" -ne "$want" ]; then
    fail "$label — wanted exit $want, got $got"
    echo "$out" | sed 's/^/      | /' >&2
    return
  fi
  echo "  PASS: $label (exit $got)"
}

# ── 1. The defect this file exists for ──────────────────────────────────────
# The stub's table holds 4f1c9e2a7b3d5f8e0a1c2d3e4f5a6b7c8d9e2005, an object
# id ending in "2005", on the row for feat/1999-other-work. No branch for
# ticket 2005 exists anywhere in it.

naive=$(PATH="$STUB_BIN:$PATH" git ls-remote --heads origin | grep -c 2005)
if [ "$naive" -lt 1 ]; then
  fail 'the stub no longer carries an object id containing 2005; this fixture would prove nothing'
else
  echo "  PASS: the naive \`ls-remote | grep 2005\` idiom matches $naive row(s) — the false positive is real"
fi

# `grep -w` behaves differently on the two rows, and both halves are asserted
# because the difference is the whole reason ir:exec section 2 keeps its
# pattern search while the push-landed check does not.
naive_w=$(PATH="$STUB_BIN:$PATH" git ls-remote --heads origin | grep -w 2005)
if printf '%s\n' "$naive_w" | grep -q '8d9e2005'; then
  fail 'grep -w matched the object-id row; the claim that a hex neighbour blocks it is wrong'
else
  echo '  PASS: `grep -w 2005` does NOT match the object-id row — a hex digit is a word character'
fi
if printf '%s\n' "$naive_w" | grep -q '1999-backport-2005-fix'; then
  echo '  PASS: `grep -w 2005` DOES match a foreign branch whose name carries the digits between hyphens'
else
  fail 'grep -w no longer matches the hyphenated branch row; the "-w is not enough" claim would go unchecked'
fi

assert_rc 'ref_exists is not fooled by the forged object id' 1 origin feat/2005-pi-provider-model-change

# ── 2. Ordinary answers ─────────────────────────────────────────────────────
assert_rc 'a branch that exists reports 0' 0 origin feat/1999-other-work
assert_rc 'a branch that does not exist reports 1' 1 origin feat/4242-never-pushed

# ── 3. Refusals — could not look must never read as absent ──────────────────
assert_rc 'a glob in the branch name is refused' 2 origin 'feat/*'
assert_rc 'an empty branch name is refused' 2 origin ''

ref_exists origin >/dev/null 2>&1
got=$?
[ "$got" -eq 2 ] || fail "a missing argument must refuse with 2, got $got"
[ "$got" -eq 2 ] && echo "  PASS: a missing argument is refused (exit 2)"

PATH="$STUB_BIN:$PATH" REF_EXISTS_STUB_UNREACHABLE=1 \
  ref_exists origin feat/1999-other-work >/dev/null 2>&1
got=$?
if [ "$got" -ne 2 ]; then
  fail "an unreachable remote must refuse with 2, not report absence; got $got"
else
  echo "  PASS: an unreachable remote is refused (exit 2), not reported as absent"
fi

# ── 4. Vacuity guard — the real remote, through the real git ────────────────
# Without this, every case could pass against a stub that answers nothing the
# way real git does.
# One round trip on the happy path. The raw `git ls-remote` runs only when
# ref_exists reports 2, which is the case that discriminates "the remote is
# unreachable" (SKIP) from "ref_exists is broken" (FAIL). Calling both every
# time made the identical request twice, measured at ~0.62s each.
ref_exists origin main >/dev/null 2>&1
got=$?
case "$got" in
  0) echo '  PASS: real git agrees that origin/main exists' ;;
  1) fail 'ref_exists said origin/main is absent; real git lists it' ;;
  *)
    if git ls-remote --exit-code --heads origin refs/heads/main >/dev/null 2>&1; then
      fail "ref_exists refused with $got for origin/main, which real git reports as present"
    else
      echo '  SKIP: no reachable origin with a main branch; the stub cases still ran'
    fi
    ;;
esac

if [ "$rc" -eq 0 ]; then
  echo 'OK: ref-exists_test — exact ref checks survive an object id carrying the ticket digits'
fi
exit "$rc"
