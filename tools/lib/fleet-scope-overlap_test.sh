#!/usr/bin/env bash
# Drive tools/lib/fleet-scope-overlap.sh over committed issue-body fixtures
# (#2022), and pin the three answers it must keep apart: disjoint, overlapping,
# and undecidable.
#
# WHICH CASE PINS WHAT, each written from the mutation as RUN:
#
#   * `replaydata-tree.md` vs `replaydata-muse.md` pins the tree/file key
#     distinction. Running
#     `tools/mutate.sh tools/lib/fleet-scope-overlap.sh 'key="${token%/}/"'
#     'key=$(dirname "$token")'` reddens exactly this case and no other. An
#     earlier version of this header credited the `adapters-outbound.md` /
#     `pi-adapter.md` pair instead; that pair stays GREEN under that
#     mutation, so the claim was wrong and is corrected here.
#   * `adapters-root-file.md` vs `pi-adapter.md` pins the containment
#     restriction — a directory key derived from a FILE must not swallow its
#     children. Widening the two `case "$k" in */)` arms to `*)` reddens this
#     case. Before this fixture existed the corpus held no file-derived key
#     that was a strict prefix of another, so that widening left every case
#     green.
#
# Every other case below reproduces a defect review of #2022 found on a LIVE
# issue body, and each was seen to fail before the fix that answers it.
set -uo pipefail # NOT -e: assertions capture non-zero return codes

REPO_ROOT=$(git rev-parse --show-toplevel)
cd "$REPO_ROOT" || { echo "FAIL: cannot cd to repo root $REPO_ROOT" >&2; exit 1; }

D=tools/lib/testdata/fleet-scope-overlap
[ -d "$D" ] || { echo "FAIL: fleet-scope-overlap_test — fixture dir $D not found" >&2; exit 1; }

# shellcheck source=tools/lib/fleet-scope-overlap.sh
. tools/lib/fleet-scope-overlap.sh # defines fleet_scope_overlap

# shellcheck source=tools/lib/checker-assert.sh
. tools/lib/checker-assert.sh # defines assert_checker_rc

rc=0
fail() { echo "FAIL: $1" >&2; rc=1; }

# ── 1. Disjoint ─────────────────────────────────────────────────────────────
assert_checker_rc fleet_scope_overlap 'two tickets in unrelated trees are disjoint' 0 'DISJOINT' \
  "$D/pi-adapter.md" "$D/replaydata-muse.md"

assert_checker_rc fleet_scope_overlap 'a sibling package under core/adapters does not collide' 0 'DISJOINT' \
  "$D/pi-adapter.md" "$D/adapters-outbound.md"

assert_checker_rc fleet_scope_overlap 'one ticket alone with a declared scope passes' 0 'DECLARED' \
  "$D/pi-adapter.md"

# The containment restriction. `core/adapters/registry.go` keys to
# `core/adapters`, a strict prefix of `core/adapters/inbound/agents/pi`. A
# file-derived key contains nothing, so these two do not collide.
assert_checker_rc fleet_scope_overlap 'a file-derived parent key does not swallow a child package' 0 'DISJOINT' \
  "$D/adapters-root-file.md" "$D/pi-adapter.md"

# ── 2. Overlapping ──────────────────────────────────────────────────────────
assert_checker_rc fleet_scope_overlap 'two files in the same package overlap' 1 'OVERLAP' \
  "$D/pi-adapter.md" "$D/pi-adapter-sibling.md"

assert_checker_rc fleet_scope_overlap 'a declared tree contains a file beneath it' 1 'OVERLAP' \
  "$D/replaydata-tree.md" "$D/replaydata-muse.md"

# A directory written WITHOUT a trailing slash is a directory, not a file.
# Live #2008 declares "A new `internal/provider` package"; `dirname` turned
# that into `internal`, and two tickets editing one new package reported
# DISJOINT.
assert_checker_rc fleet_scope_overlap 'a bare directory collides with a file inside it' 1 'OVERLAP' \
  "$D/bare-directory.md" "$D/bare-directory-file.md"

# An extensionless root file is a file. `go.work` was dropped entirely, so a
# Go directive bump touching it from two tickets reported DISJOINT.
assert_checker_rc fleet_scope_overlap 'two tickets both touching go.work overlap' 1 'OVERLAP' \
  "$D/gowork-a.md" "$D/gowork-b.md"

# ── 3. Undecidable — never reported as disjoint ─────────────────────────────
assert_checker_rc fleet_scope_overlap 'a ticket with no section 5 is undeclared' 2 'UNDECLARED' \
  "$D/no-section.md" "$D/pi-adapter.md"

assert_checker_rc fleet_scope_overlap 'a section 5 naming only symbols is undeclared' 2 'UNDECLARED' \
  "$D/section-no-paths.md" "$D/pi-adapter.md"

# No trailing arguments at all — a different case from ONE empty argument.
assert_checker_rc fleet_scope_overlap 'no files named is refused' 2 'no files named'

assert_checker_rc fleet_scope_overlap 'an empty filename is refused' 2 "cannot read ''" ''
assert_checker_rc fleet_scope_overlap 'a missing file is refused' 2 'cannot read' "$D/does-not-exist.md"
assert_checker_rc fleet_scope_overlap 'a directory is refused' 2 'is a directory' "$D"

# An undeclared ticket beside an overlapping pair must still refuse, because a
# refusal outranks a finding: acting on "they overlap" while one scope is
# unknown would be acting on an answer the checker did not have.
assert_checker_rc fleet_scope_overlap 'a refusal outranks a finding' 2 'UNDECLARED' \
  "$D/no-section.md" "$D/pi-adapter.md" "$D/pi-adapter-sibling.md"

# ── 4. A token that cannot be parsed is NAMED, never dropped in silence ─────
assert_checker_rc fleet_scope_overlap 'an unparsable token is reported on an IGNORED line' 2 'IGNORED' \
  "$D/section-no-paths.md"

# ── 5. A disclaimed scope is not a declared scope ───────────────────────────
# Live #2010 and #2013 use section 5 to say what they do NOT touch.
# Read the KEY LIST only — everything after the em dash — because the fixture
# FILENAME also contains "core" and matching the whole line passes vacuously.
out=$(fleet_scope_overlap "$D/disclaims-core.md" 2>&1)
disclaim_keys=$(grep '^DECLARED:' <<<"$out" | sed 's/^.*— //')
if [ -z "$disclaim_keys" ]; then
  fail "the disclaimer fixture declared nothing at all; it should still declare replaydata/providers/"
elif grep -qE '(^| )core' <<<"$disclaim_keys"; then
  fail "a 'No change to \`core/domain\`' bullet was parsed as a declared scope; keys were: $disclaim_keys"
else
  echo "  PASS: a bullet disclaiming a scope is not parsed as one (keys: $disclaim_keys)"
fi

# A disclaimer bullet that ALSO names another path is ambiguous. Dropping the
# whole line silently cleared two tickets that both edit tools/lib to share a
# wave — the unsafe direction, and the one the file's own loud-failure rule
# forbids.
assert_checker_rc fleet_scope_overlap 'a bullet that disclaims AND declares refuses' 2 'AMBIGUOUS' \
  "$D/disclaims-and-declares.md" "$D/pi-adapter.md"

# ── 6. A glob token must not consult the filesystem ─────────────────────────
# #2022's own body declares `.claude/skills/**/*.md`. Unquoted expansion
# turned that into eleven keys it never declared, and made the SAME inputs
# give OVERLAP from the repo root and DISJOINT from anywhere else.
glob_here=$(fleet_scope_overlap "$D/glob-token.md" 2>&1)
glob_there=$(cd / && fleet_scope_overlap "$REPO_ROOT/$D/glob-token.md" 2>&1 | sed "s#$REPO_ROOT/##g")
if [ "$glob_here" != "$glob_there" ]; then
  fail "a glob token gives a cwd-dependent answer, so the verdict is not reproducible"
  diff <(printf '%s\n' "$glob_here") <(printf '%s\n' "$glob_there") | sed 's/^/      | /' >&2
else
  echo '  PASS: a glob token gives the same answer from any working directory'
fi
if grep -qF 'ir:triage' <<<"$glob_here"; then
  fail "the glob token expanded against the filesystem: $glob_here"
else
  echo '  PASS: a glob token reduces to its glob-free prefix, not to what is on disk'
fi

# ── 7. Vacuity guard — the real ticket this work came from ──────────────────
real=$(mktemp "${TMPDIR:-/tmp}/fleet-2022-body.XXXXXX")
trap 'rm -f "$real"' EXIT
if gh issue view 2022 --repo ingo-eichhorst/Irrlicht --json body --jq .body >"$real" 2>/dev/null &&
  [ -s "$real" ]; then
  out=$(fleet_scope_overlap "$real" 2>&1)
  got=$?
  if [ "$got" -ne 0 ]; then
    fail "issue 2022's own body should declare a readable scope; got exit $got"
    echo "$out" | sed 's/^/      | /' >&2
  else
    echo '  PASS: issue 2022'"'"'s live body declares a scope this checker can read'
  fi
else
  echo '  SKIP: gh could not fetch issue 2022; the committed fixtures still ran'
fi

if [ "$rc" -eq 0 ]; then
  echo 'OK: fleet-scope-overlap_test — disjoint, overlapping and undecidable stay three answers'
fi
exit "$rc"
