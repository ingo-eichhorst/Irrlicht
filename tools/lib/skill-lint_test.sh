#!/usr/bin/env bash
# skill-lint_test.sh — unit tests for tools/skill-lint.sh. Plain bash (no
# framework), matching the style of lib/changed-files_test.sh. Run directly, or
# via tools/preflight.sh's `tools` gate and test.yml's "Test the shared shell
# libs" step. Exits non-zero on any failed assertion.
#
# Covers issue #1209: nothing mechanical read .claude/skills/**/*.md, so a PR
# touching only skill files went green having checked nothing. The linter is
# exercised against a fixture corpus under testdata/skill-lint/skills/ — one
# deliberately broken file per check, plus files that must stay clean — rather
# than against real skill files, so the assertions do not move when a real
# skill is edited.
#
# The clean fixtures carry as much weight as the broken ones. `good/SKILL.md`
# is built out of exactly the constructs that would make a naive linter
# unusable here: prose that *documents* {{TOKEN}} and REPEAT:/OPTIONAL: markers
# inside backticks (which is how .claude/skills/ir:exec/SKILL.md really reads),
# a conflict marker shown inside a fence, a correct above/below reference, and
# a lead-in count that matches its list once nested items are excluded.

set -uo pipefail   # NOT -e: assertions capture non-zero return codes

DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$DIR/../.." && pwd)"
LINT="$ROOT/tools/skill-lint.sh"
FIXTURES="$DIR/testdata/skill-lint/skills"

fails=0
pass() { echo "  PASS: $1"; return 0; }
fail() {
  echo "  FAIL: $1 — expected [$2] got [$3]"
  fails=$((fails + 1))
  return 0
}
assert_eq() {
  local label="$1" expected="$2" actual="$3"
  [[ "$expected" == "$actual" ]] && pass "$label" || fail "$label" "$expected" "$actual"
  return 0
}
assert_contains() {
  local label="$1" needle="$2" haystack="$3"
  case "$haystack" in
    *"$needle"*) pass "$label" ;;
    *) fail "$label" "contains: $needle" "$haystack" ;;
  esac
  return 0
}
assert_not_contains() {
  local label="$1" needle="$2" haystack="$3"
  case "$haystack" in
    *"$needle"*) fail "$label" "does NOT contain: $needle" "$haystack" ;;
    *) pass "$label" ;;
  esac
  return 0
}

OUT=""
RC=0
# lint <args...> — run the linter, capturing combined output in OUT and the
# exit status in RC. Run from the repo root so relative paths in the output
# match what a developer would see.
lint() {
  OUT=$(cd "$ROOT" && bash "$LINT" "$@" 2>&1)
  RC=$?
  return 0
}

# fixture <name> — the single .md file of a fixture skill directory.
fixture() {
  local d="$FIXTURES/$1"
  if [[ -f "$d/SKILL.md" ]]; then echo "$d/SKILL.md"; else echo "$d/reference.md"; fi
}

echo "== check 1: unresolved conflict markers are ERRORS =="
lint "$(fixture conflict)"
assert_contains "flags <<<<<<<"          "unresolved merge conflict marker <<<<<<<" "$OUT"
assert_contains "flags ======="          "unresolved merge conflict marker =======" "$OUT"
assert_contains "flags >>>>>>>"          "unresolved merge conflict marker >>>>>>>" "$OUT"
assert_eq       "a conflict marker fails the gate" "1" "$RC"

echo "== check 2: unfilled tokens and leftover scaffolding are ERRORS =="
lint "$(fixture template)"
assert_contains "flags an unfilled {{TOKEN}}"   "unfilled template token {{TLDR}}" "$OUT"
assert_contains "flags a REPEAT:/OPTIONAL: comment" "[template-scaffold]" "$OUT"
assert_eq       "leftover scaffolding fails the gate" "1" "$RC"

echo "== check 3: internal references are WARNINGS =="
lint "$(fixture refs)"
assert_contains "flags a backwards direction"  "[ref-direction]" "$OUT"
assert_contains "names the heading and its real side" 'that heading is above' "$OUT"
assert_contains "flags a reference naming no heading" "[ref-dangling]" "$OUT"
assert_eq       "warnings alone do not fail the gate" "0" "$RC"

echo "== check 4: an announced count that disagrees with the list is a WARNING =="
lint "$(fixture listcount)"
assert_contains "flags three-announced/four-listed" "[list-count]" "$OUT"
assert_contains "reports both numbers"              'announces "three" (3) but 4 list item' "$OUT"
assert_eq       "warnings alone do not fail the gate" "0" "$RC"

echo "== check 5: frontmatter sanity is a WARNING, and SKILL.md-only =="
lint "$(fixture wrongname)"
assert_contains "flags name: disagreeing with the directory" \
  'name: is "some-other-name" but the directory says "wrongname"' "$OUT"
lint "$(fixture nodesc)"
assert_contains "flags an empty folded description:" "description: is empty" "$OUT"
lint "$(fixture reference-doc)"
assert_not_contains "a non-SKILL.md file is not checked for frontmatter" "[frontmatter]" "$OUT"
assert_eq "a reference file with a matching count is clean" "0" "$RC"

echo "== the nested-skill name rule: name: carries the parent prefix =="
lint "$(fixture parent/child)"
assert_not_contains "parent/child SKILL.md is not a name mismatch" "[frontmatter]" "$OUT"
assert_eq "nested skill lints clean" "0" "$RC"

echo "== the clean fixture: documenting markers is not using them =="
# This is the regression that decides whether the linter is usable at all.
# ir:exec/SKILL.md mentions {{TOKEN}}, REPEAT: and OPTIONAL: a dozen times in
# backticks; a linter that reads the raw bytes hard-fails the repo on day one.
lint "$(fixture good)"
assert_eq       "the good fixture produces no findings" "0" "$RC"
assert_contains "and reports zero of each" "0 error(s), 0 warning(s)" "$OUT"

echo "== --strict promotes warnings to failures =="
# The documented path to hardening checks 3-5 once their noise floor is known.
lint --strict "$(fixture listcount)"
assert_eq "--strict turns a warning into a non-zero exit" "1" "$RC"
lint --strict "$(fixture good)"
assert_eq "--strict still passes a clean file" "0" "$RC"

echo "== a missing file is an error, not a silent pass =="
lint "$FIXTURES/does-not-exist/SKILL.md"
assert_eq "a path that does not exist fails" "1" "$RC"

echo "== the default file set: the glob and the preflight gate must agree =="
# With no arguments the linter walks .claude/skills/. An empty walk would make
# the gate green while reading nothing — the exact failure #1209 is about — so
# the script exits 2 there rather than 0, and this pins that the real tree is
# discovered.
lint
assert_eq "the repo's own skill corpus has zero ERRORS" "0" "$RC"
assert_not_contains "no file in .claude/skills/ is left unread" "0 file(s)" "$OUT"

echo ""
if [[ "$fails" -eq 0 ]]; then
  echo "skill-lint_test: ALL PASS"
else
  echo "skill-lint_test: $fails FAILED" >&2
  exit 1
fi
