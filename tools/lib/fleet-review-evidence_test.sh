#!/usr/bin/env bash
# Drive tools/lib/fleet-review-evidence.sh over committed hand-back fixtures
# (#2022), and pin the distinction it exists to keep: a review that found
# nothing is not a review that never ran.
#
# Three fixtures are verbatim inputs that review of #2022 used to break the
# FIRST version of this checker, which inferred its verdict from prose. Each
# was seen to report PROVEN before the fix that answers it:
#
#   * `conventional-commit.txt` — reported PROVEN because "conventional"
#     contains "convention", one of ir:code-review's five categories, while
#     the sentence says the review subagent returned nothing.
#   * `says-it-did-not-finish.txt` — reported PROVEN on a sentence stating in
#     words that the correctness pass did not complete.
#   * `counts-only.txt` — the reply shape ir:code-review itself calls out.
#     The first version caught it only because the fixture was one bare line;
#     one ordinary sentence naming a path flipped it to PROVEN, so the
#     fixture now carries that sentence.
set -uo pipefail # NOT -e: assertions capture non-zero return codes

REPO_ROOT=$(git rev-parse --show-toplevel)
cd "$REPO_ROOT" || { echo "FAIL: cannot cd to repo root $REPO_ROOT" >&2; exit 1; }

D=tools/lib/testdata/fleet-review-evidence
REVIEW_SKILL=.claude/skills/ir:code-review/SKILL.md
EXEC_SKILL=.claude/skills/ir:exec/SKILL.md
FLEET_SKILL=.claude/skills/ir:fleet/SKILL.md
[ -d "$D" ] || { echo "FAIL: fleet-review-evidence_test — fixture dir $D not found" >&2; exit 1; }

# shellcheck source=tools/lib/fleet-review-evidence.sh
. tools/lib/fleet-review-evidence.sh # defines fleet_review_evidence

# shellcheck source=tools/lib/checker-assert.sh
. tools/lib/checker-assert.sh # defines assert_checker_rc

rc=0
fail() { echo "FAIL: $1" >&2; rc=1; }

# ── 1. PROVEN ───────────────────────────────────────────────────────────────
assert_checker_rc fleet_review_evidence 'a header line plus an explicit no-findings is proven' 0 'PROVEN' "$D/clean.txt"
assert_checker_rc fleet_review_evidence 'a header line plus as many categories as it declares is proven' 0 'PROVEN' "$D/findings.txt"

# An upper-case header must parse. The pattern matched case-insensitively
# while the count was read case-sensitively, so this input matched, failed to
# parse, and fell through to PROVEN with two `integer expression expected`
# errors on stderr — a false green.
assert_checker_rc fleet_review_evidence 'an upper-case header line still parses' 0 'PROVEN' \
  "$D/uppercase-header.txt"
upper_out=$(fleet_review_evidence "$D/uppercase-header.txt" 2>&1)
if grep -qF 'integer expression expected' <<<"$upper_out"; then
  fail "the upper-case header produced a shell error: $upper_out"
else
  echo '  PASS: an upper-case header parses without a shell error'
fi

# ── 2. UNPROVEN — prose can never stand in for the header line ──────────────
assert_checker_rc fleet_review_evidence 'a killed review agent leaves an empty hand-back' 1 'the hand-back is empty' \
  "$D/dead-agent.txt"

assert_checker_rc fleet_review_evidence 'a finding COUNT in prose is not evidence' 1 "no 'review: effort=<tier> findings=<N>' line" \
  "$D/counts-only.txt"

assert_checker_rc fleet_review_evidence 'prose with no header line is unproven' 1 "no 'review: effort=<tier> findings=<N>' line" \
  "$D/no-effort.txt"

# The two inputs that broke the prose-reading version, kept verbatim.
assert_checker_rc fleet_review_evidence '"conventional commit" does not satisfy the category test' 1 "no 'review: effort=<tier> findings=<N>' line" \
  "$D/conventional-commit.txt"

assert_checker_rc fleet_review_evidence 'a hand-back saying the pass did not finish is unproven' 1 "no 'review: effort=<tier> findings=<N>' line" \
  "$D/says-it-did-not-finish.txt"

# The header must agree with the body it claims.
assert_checker_rc fleet_review_evidence 'a header declaring more findings than it carries is unproven' 1 'carries only 1' \
  "$D/header-undercounts.txt"

assert_checker_rc fleet_review_evidence 'findings=0 without the words "no findings" is unproven' 1 "never says 'no findings'" \
  "$D/zero-without-words.txt"

# One PROVEN beside one UNPROVEN must fail: a clean gate for the whole fleet
# cannot be inferred from the ticket that happened to report properly.
assert_checker_rc fleet_review_evidence 'one unproven hand-back fails the batch' 1 'UNPROVEN' \
  "$D/clean.txt" "$D/counts-only.txt"

# ── 3. REFUSAL — could not look ─────────────────────────────────────────────
# No trailing arguments at all — a different case from ONE empty argument.
assert_checker_rc fleet_review_evidence 'no files named is refused' 2 'no files named'

assert_checker_rc fleet_review_evidence 'a missing hand-back is refused, not called unproven' 2 'cannot read' \
  "$D/does-not-exist.txt"

assert_checker_rc fleet_review_evidence 'a directory is refused' 2 'is a directory' "$D"

# ── 4. Vacuity guard — the grammar must track what the callers brief ────────
# The header line is only evidence if the skills actually ask the reviewer for
# it. If a caller stops briefing it, every honest review starts reporting
# UNPROVEN, and this is what turns that into a failure here rather than a
# mystery during a fleet run.
for skill in "$EXEC_SKILL" "$FLEET_SKILL"; do
  [ -r "$skill" ] || { fail "cannot read $skill"; continue; }
  grep -qF 'review: effort=' "$skill" ||
    fail "$skill no longer briefs the reviewer to emit the 'review: effort=' line"
done

# And the values must still be the ones ir:code-review mandates.
if [ ! -r "$REVIEW_SKILL" ]; then
  fail "cannot read $REVIEW_SKILL; the evidence grammar cannot be checked against its source"
else
  for marker in correctness convention test-coverage efficiency simplification; do
    grep -qF -- "$marker" "$REVIEW_SKILL" ||
      fail "category '$marker' is no longer named in $REVIEW_SKILL"
  done
  grep -qiF 'no findings' "$REVIEW_SKILL" ||
    fail "the literal 'no findings' is no longer required by $REVIEW_SKILL"
  for tier in low medium high xhigh max; do
    grep -qF "\`$tier\`" "$REVIEW_SKILL" ||
      fail "effort tier '$tier' is no longer named in $REVIEW_SKILL"
  done

  # The loops catch a tier or category being REMOVED. They cannot catch one
  # being ADDED, and that direction is the one that hurts: the checker would
  # silently report UNPROVEN for an honest review using the new value, with
  # nothing going red. Count them as well.
  tiers=$(sed -n '/^## 2 · Effort ladder$/,/^## 3 /p' "$REVIEW_SKILL" |
    grep -cE '^\| `(low|medium|high|xhigh|max)` \|')
  [ "$tiers" -eq 5 ] ||
    fail "$REVIEW_SKILL's effort ladder has $tiers rows, not the 5 this checker's pattern allows"
  cats=$(grep -oE '`(correctness|convention|test-coverage|efficiency|simplification)`' "$REVIEW_SKILL" |
    sort -u | grep -c .)
  [ "$cats" -eq 5 ] ||
    fail "$REVIEW_SKILL names $cats of the 5 categories this checker's pattern allows"
fi
[ "$rc" -eq 0 ] && echo '  PASS: the grammar matches what ir:code-review mandates and both callers brief'

if [ "$rc" -eq 0 ]; then
  echo 'OK: fleet-review-evidence_test — "found nothing" and "never ran" stay two answers'
fi
exit "$rc"
