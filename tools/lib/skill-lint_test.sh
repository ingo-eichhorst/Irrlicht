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
# Two properties are asserted everywhere, both learned from review:
#
#   * the SUMMARY LINE, not just the exit status. A fixture trips several
#     sub-checks at once, so `$RC == 1` alone is satisfied by any one of them
#     still being an ERROR — three of the five hard-fail sub-checks could be
#     demoted to WARN with the suite staying green. Pinning
#     "N error(s), M warning(s)" makes each severity load-bearing.
#   * the SEVERITY WORD on the finding line. It is printed as text rather than
#     encoded in ANSI color precisely so it survives a pipe — which is what a
#     GitHub Actions log is, and what these assertions read.
#
# The clean fixtures carry as much weight as the broken ones. `good/SKILL.md`
# is built out of exactly the constructs that would make a naive linter
# unusable here: prose that *documents* {{TOKEN}} and REPEAT:/OPTIONAL: markers
# inside backticks (which is how .claude/skills/ir:exec/SKILL.md really reads),
# a conflict marker shown inside a fence, correct above/below references
# (including one to a heading with a parenthetical), and a lead-in count that
# matches its list once nested items are excluded.
#
# The `unclosed-fence`, `openfm`, `nofmclose` and `fmtoken` fixtures exist for
# one shared reason: blanking is how this linter tells "documents a marker"
# from "has one", and every blanked line is a line no check reads. An
# unbalanced fence or an unterminated frontmatter therefore used to SILENCE the
# rest of the file — a corrupted skill file linting 0 errors and exiting 0,
# which is the exact failure #1209 exists to remove.

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
# match what a developer would see. OUT is a pipe, never a TTY, so colors are
# off and severity words are the only severity signal — deliberately the same
# view CI gets.
lint() {
  OUT=$(cd "$ROOT" && bash "$LINT" "$@" 2>&1)
  RC=$?
  return 0
}

# fixture <name> — the SKILL.md of a fixture skill directory. The one fixture
# that is deliberately NOT a SKILL.md is spelled out at its call site, where
# that is the whole point of the assertion.
fixture() { echo "$FIXTURES/$1/SKILL.md"; }

# assert_tally <label> <errors> <warnings> — pin the summary line, so no
# individual sub-check can change severity unnoticed.
assert_tally() {
  assert_contains "$1" "$2 error(s), $3 warning(s)" "$OUT"
  return 0
}

echo "== check 1: unresolved conflict markers are ERRORS =="
lint "$(fixture conflict)"
assert_contains "flags <<<<<<< as an ERROR" "ERROR unresolved merge conflict marker <<<<<<<" "$OUT"
assert_contains "flags ======= as an ERROR" "ERROR unresolved merge conflict marker =======" "$OUT"
assert_contains "flags >>>>>>> as an ERROR" "ERROR unresolved merge conflict marker >>>>>>>" "$OUT"
assert_tally    "all three markers, nothing else" 3 0
assert_eq       "a conflict marker fails the gate" "1" "$RC"

echo "== check 2: unfilled tokens and leftover scaffolding are ERRORS =="
lint "$(fixture template)"
assert_contains "flags an unfilled {{TOKEN}} as an ERROR" "ERROR unfilled template token {{TLDR}}" "$OUT"
assert_contains "flags a REPEAT:/OPTIONAL: comment as an ERROR" \
  "ERROR template scaffolding comment left in output" "$OUT"
assert_tally    "one token plus four scaffolding comments" 5 0
assert_eq       "leftover scaffolding fails the gate" "1" "$RC"

echo "== check 3: internal references are WARNINGS =="
lint "$(fixture refs)"
assert_contains "flags a backwards direction as a WARN" "WARN  says \"scoring rubric\" below" "$OUT"
assert_contains "names the heading and its real side" 'that heading is above (line 8)' "$OUT"
assert_contains "flags a reference naming no heading" "WARN  reference to \"instrument menu\" above" "$OUT"
assert_contains "matches a heading through its parenthetical" \
  'says "readiness rubric" above, but that heading is below' "$OUT"
assert_tally    "three reference warnings, no errors" 0 3
assert_eq       "warnings alone do not fail the gate" "0" "$RC"

echo "== check 4: an announced count that disagrees with the list is a WARNING =="
lint "$(fixture listcount)"
assert_contains "reports both numbers, as a WARN" \
  'WARN  lead-in announces "three" (3) but 4 list item' "$OUT"
assert_tally    "one list-count warning" 0 1
assert_eq       "warnings alone do not fail the gate" "0" "$RC"

echo "== check 5: frontmatter sanity is a WARNING, and SKILL.md-only =="
lint "$(fixture wrongname)"
assert_contains "flags name: disagreeing with the directory" \
  'WARN  name: is "some-other-name" but the directory says "wrongname"' "$OUT"
assert_tally "one frontmatter warning" 0 1
lint "$(fixture nodesc)"
assert_contains "flags an empty folded description:" "WARN  description: is empty" "$OUT"
assert_tally "one frontmatter warning" 0 1
lint "$FIXTURES/reference-doc/reference.md"
assert_not_contains "a non-SKILL.md file is not checked for frontmatter" "[frontmatter]" "$OUT"
# This fixture is also the positive case for check 4: "Three moves:" over three
# items must produce NO warning. Asserting RC alone would not see a warning.
assert_tally "a matching announced count produces no finding" 0 0
assert_eq    "and the file passes" "0" "$RC"

echo "== the nested-skill name rule: name: carries the parent prefix =="
lint "$(fixture parent/child)"
assert_tally "parent/child SKILL.md is not a name mismatch" 0 0
assert_eq    "nested skill lints clean" "0" "$RC"

echo "== an unbalanced code fence must not silence the rest of the file =="
# The silencer: every line after an unclosed fence used to be blanked, so both
# ERROR checks were defeated by one missing backtick line and the file reported
# 0 errors, exit 0.
lint "$(fixture unclosed-fence)"
assert_contains "reports the unbalanced fence itself" \
  "ERROR odd number of code-fence delimiters" "$OUT"
assert_contains "still sees the conflict markers past the fence" \
  "ERROR unresolved merge conflict marker <<<<<<<" "$OUT"
assert_contains "still sees the token past the fence" \
  "ERROR unfilled template token {{UNFILLED}}" "$OUT"
assert_contains "still sees the scaffolding past the fence" \
  "ERROR template scaffolding comment left in output" "$OUT"
assert_tally "fence + 3 markers + token + scaffolding" 6 0
assert_eq    "an unbalanced fence fails the gate" "1" "$RC"

echo "== unterminated frontmatter must not swallow the body =="
lint "$(fixture openfm)"
assert_contains "diagnoses the frontmatter itself" \
  "WARN  frontmatter contains a line that is not YAML" "$OUT"
assert_contains "still sees the token inside the swallowed span" \
  "ERROR unfilled template token {{UNFILLED}}" "$OUT"
assert_tally "token + scaffolding as errors, frontmatter as a warning" 2 1
assert_eq    "and the file fails the gate" "1" "$RC"

lint "$(fixture nofmclose)"
assert_contains "a frontmatter that never closes is bounded, not run to EOF" \
  "WARN  frontmatter opens with --- but never closes within the first 40 lines" "$OUT"
assert_tally "unterminated plus the resulting missing-frontmatter warning" 0 2

echo "== frontmatter is structured data, so check 2 reads it =="
# {{DESCRIPTION}} is not empty, so the frontmatter check alone passes it — and
# description: is the field the skill loader matches a skill on.
lint "$(fixture fmtoken)"
assert_contains "an unfilled token in description: is an ERROR" \
  "ERROR unfilled template token {{DESCRIPTION}}" "$OUT"
assert_tally "exactly that one error" 1 0
assert_eq    "and it fails the gate" "1" "$RC"

echo "== the clean fixture: documenting markers is not using them =="
# This is the regression that decides whether the linter is usable at all.
# ir:exec/SKILL.md mentions {{TOKEN}}, REPEAT: and OPTIONAL: a dozen times in
# backticks; a linter that reads the raw bytes hard-fails the repo on day one.
lint "$(fixture good)"
assert_tally "the good fixture produces no findings" 0 0
assert_eq    "and passes" "0" "$RC"

echo "== --strict promotes warnings to failures =="
# The documented path to hardening checks 3-5 once their noise floor is known.
lint --strict "$(fixture listcount)"
assert_eq "--strict turns a warning into a non-zero exit" "1" "$RC"
lint --strict "$(fixture good)"
assert_eq "--strict still passes a clean file" "0" "$RC"

echo "== a file the linter cannot read is a failure, not a clean file =="
lint "$FIXTURES/does-not-exist/SKILL.md"
assert_contains "a path that does not exist is named" "no such file" "$OUT"
assert_eq       "and fails" "1" "$RC"
# awk's exit status used to be discarded by process substitution, so an awk
# that could not read the file — or that rejected a construct in the program —
# produced zero findings and the file was tallied clean. Root can read a 000
# file, so skip the assertion there rather than assert something untrue.
if [[ "$(id -u)" != "0" ]]; then
  UNREADABLE="$(mktemp -d)/SKILL.md"
  cp "$(fixture conflict)" "$UNREADABLE" && chmod 000 "$UNREADABLE"
  lint "$UNREADABLE"
  assert_contains "an awk failure is surfaced, not swallowed" "awk failed on" "$OUT"
  assert_eq       "and counts as an error" "1" "$RC"
  chmod 644 "$UNREADABLE"; rm -rf "$(dirname "$UNREADABLE")"
else
  echo "  SKIP: running as root, a 000-mode file is still readable"
fi

echo "== the default file set: the glob and the preflight gate must agree =="
# With no arguments the linter walks the repo's skill files. An empty walk
# would make the gate green while reading nothing — the exact failure #1209 is
# about — so the script exits 2 there rather than 0.
#
# Only the SIZE of the walk is asserted, not its verdict. Whether the real
# corpus is clean is the gate's job (preflight's `skill-file lint` and
# test.yml's "Lint skill files"), and asserting it here too would report a
# defect in a real skill file as a unit-test failure of the linter — the worst
# of the three places to see it — while contradicting this file's own rule that
# assertions run against fixtures so they do not move when a skill is edited.
lint
found=$(sed -n 's/^skill-lint: \([0-9][0-9]*\) file(s).*/\1/p' <<<"$OUT")
if [[ -n "$found" && "$found" -ge 10 ]]; then
  pass "the walk found the real corpus ($found files)"
else
  fail "the walk found the real corpus" ">= 10 files" "${found:-<no summary line>}"
fi

echo ""
if [[ "$fails" -eq 0 ]]; then
  echo "skill-lint_test: ALL PASS"
else
  echo "skill-lint_test: $fails FAILED" >&2
  exit 1
fi
