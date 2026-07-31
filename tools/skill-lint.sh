#!/usr/bin/env bash
# skill-lint.sh — mechanical checks over .claude/skills/**/*.md, the files that
# tell agents how to triage, plan, implement and review. Those files are
# executable instructions, but until issue #1209 no gate read them: PR #1204
# changed two skill files and `tools/preflight.sh --changed` skipped all ten
# gates, so thirteen green checks proved nothing about the only files that
# changed.
#
# This linter does not review wording — that stays a human judgement call. It
# catches the defects a reviewer's eye slides over precisely because they look
# like prose, and that a machine can decide without taste:
#
#   1. conflict-marker    ERROR  unresolved <<<<<<< / ======= / >>>>>>> / |||||||
#   2. template-token     ERROR  {{TOKEN}} left unfilled
#      template-scaffold  ERROR  <!-- REPEAT:x --> / <!-- OPTIONAL:x --> left in
#   3. ref-direction      WARN   "X above" where heading X is below (or vice versa)
#      ref-dangling       WARN   a delimited "X above/below" naming no heading
#   4. list-count         WARN   "Three moves:" followed by a list of four
#   5. frontmatter        WARN   SKILL.md name: disagrees with its directory,
#                                or description: missing/empty
#
# Checks 1–2 are unambiguous, so they fail the gate. 3–5 are heuristics with a
# real false-positive rate and only warn; `--strict` promotes them to failures,
# which is the path to hardening one once its noise floor is known. Exit status
# is 1 only when something failed at the severity in force.
#
# What the checks read
# -------------------
# Markdown that *talks about* markers is not markdown that *has* them.
# `.claude/skills/ir:exec/SKILL.md` documents the plan.html template and so
# mentions `{{TOKEN}}`, `REPEAT:step` and `OPTIONAL:ui` a dozen times — always
# inside backticks. So checks 2 and 4 read a prose view of each file with
# fenced code blocks, inline code spans and YAML frontmatter blanked out.
# Check 3 keeps inline code (a reference can legitimately be written
# `` `Auto mode` below ``) and check 1 reads raw lines outside fences, because
# a conflict marker inside a fence is illustrative but anywhere else is a
# corrupted file.
#
# Known limits (deliberate, not oversights)
# ----------------------------------------
# * Check 4 counts list markers. The ir:triage defect that motivated it — "Two
#   flavors:" followed by three, the third orphaned *outside* the list — has
#   two markers and so passes. Counting what a reader would call an item is a
#   judgement call; counting markers is not.
# * Check 3 only recognises a reference when the name is delimited
#   (`"X"`, `` `X` ``, `**X**`) or matches a heading verbatim. "see the rule
#   above" names nothing checkable and is ignored by design.
# * Setext headings (underlined with === or ---) are not indexed as headings;
#   every heading in this repo's skill files is ATX (#).
#
# Usage:
#   tools/skill-lint.sh                  # lint every .claude/skills/**/*.md
#   tools/skill-lint.sh --strict         # warnings fail too
#   tools/skill-lint.sh FILE...          # lint just these files (used by tests)
#   tools/skill-lint.sh --strict FILE...
#
# The expected frontmatter `name:` for a SKILL.md is its directory path below
# the nearest `skills/` ancestor — `ir:exec`, or `ir:onboarding-factory/assess`
# for a nested one. Deriving it from the path rather than a hard-coded list is
# what lets the fixture corpus under tools/lib/testdata/ exercise check 5
# without living in .claude/skills/.
set -uo pipefail

STRICT=0
FILES=()
while [[ $# -gt 0 ]]; do
  case "$1" in
    --strict) STRICT=1; shift ;;
    # Print the whole leading comment block, the same self-documenting idiom
    # tools/preflight.sh uses, so --help can't drift from the header.
    -h|--help) awk 'NR>1 && /^#/ {print; next} NR>1 {exit}' "$0"; exit 0 ;;
    --) shift; FILES+=("$@"); break ;;
    -*) echo "unknown arg: $1" >&2; exit 2 ;;
    *) FILES+=("$1"); shift ;;
  esac
done

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

if [[ ${#FILES[@]} -eq 0 ]]; then
  cd "$ROOT_DIR" || exit 2
  while IFS= read -r f; do
    FILES+=("$f")
  done < <(find .claude/skills -name '*.md' -type f | LC_ALL=C sort)
fi

if [[ ${#FILES[@]} -eq 0 ]]; then
  # Not a pass: the gate only runs when a skill file changed, so an empty file
  # set means the glob and the gate's trigger disagree about where skills live.
  echo "skill-lint: no skill files found — nothing was checked" >&2
  exit 2
fi

# expected_skill_name <path> — the directory path below the nearest `skills/`
# ancestor, which is what a SKILL.md's frontmatter `name:` must equal. Empty
# when the path has no `skills/` ancestor, which turns the name comparison off
# rather than inventing an expectation.
expected_skill_name() {
  local path="$1" dir
  dir="$(dirname "$path")"
  case "$dir" in
    */skills/*) echo "${dir##*/skills/}" ;;
    *) echo "" ;;
  esac
}

# `read -d ''` hits EOF without ever seeing its delimiter and so returns 1 even
# though LINT_AWK is assigned in full. That is fine here only because this
# script runs without `set -e`; do not add it without changing this line.
read -r -d '' LINT_AWK <<'AWK_END'
# One pass per file. Emits findings as SEVERITY<TAB>line<TAB>check<TAB>message
# on stdout; the shell wrapper formats and tallies them.

function normalize(s,   t) {
  t = tolower(s)
  gsub(/[`*_]/, "", t)
  gsub(/^[ \t]+/, "", t)
  gsub(/[ \t]+$/, "", t)
  gsub(/[ \t]+/, " ", t)
  return t
}
function finding(sev, line, check, msg) {
  printf "%s\t%d\t%s\t%s\n", sev, line, check, msg
}
function err(line, check, msg)  { finding("ERROR", line, check, msg) }
function warn(line, check, msg) { finding("WARN",  line, check, msg) }

function indent_of(s,   t) {
  t = s
  sub(/[^ \t].*$/, "", t)
  gsub(/\t/, "    ", t)
  return length(t)
}
function is_list_marker(s) {
  return (s ~ /^[ \t]*([-*+]|[0-9]+[.)])[ \t]+/)
}
# Word-boundary containment: index() would match "modes" inside "submodes".
function phrase_at(hay, needle, pos,   before, after) {
  before = (pos > 1) ? substr(hay, pos - 1, 1) : " "
  after  = substr(hay, pos + length(needle), 1)
  if (after == "") after = " "
  return (before !~ /[A-Za-z0-9]/ && after !~ /[A-Za-z0-9]/)
}

{ n++; raw[n] = $0 }

END {
  # ---- build the three views ---------------------------------------------
  # raw[]   verbatim, for conflict markers
  # text[]  fences + frontmatter blanked, inline code KEPT   (checks 3, 4)
  # prose[] text[] with inline code spans blanked too        (check 2)
  fm_end = 0
  if (n >= 1 && raw[1] ~ /^---[ \t\r]*$/) {
    for (i = 2; i <= n; i++)
      if (raw[i] ~ /^---[ \t\r]*$/) { fm_end = i; break }
  }

  fence = 0
  for (i = 1; i <= n; i++) {
    line = raw[i]
    sub(/\r$/, "", line)

    if (line ~ /^[ \t]*(```|~~~)/) {
      infence[i] = 1          # the delimiter itself is never prose
      fence = !fence
      text[i] = ""; prose[i] = ""
      continue
    }
    infence[i] = fence
    if (fence || (fm_end > 0 && i <= fm_end)) { text[i] = ""; prose[i] = ""; continue }

    text[i] = line
    p = line
    gsub(/`[^`]*`/, " ", p)
    prose[i] = p

    if (line ~ /^#{1,6}[ \t]+/) {
      h = line
      sub(/^#+[ \t]+/, "", h)
      sub(/[ \t]+#+[ \t]*$/, "", h)
      nh++
      hline[nh] = i
      htext[nh] = normalize(h)
    }
  }

  # ---- 1. unresolved conflict markers (ERROR) -----------------------------
  open_conflict = 0
  for (i = 1; i <= n; i++) {
    if (infence[i]) continue
    if (raw[i] ~ /^<<<<<<<([ \t]|$)/) {
      err(i, "conflict-marker", "unresolved merge conflict marker <<<<<<<")
      open_conflict = 1
    } else if (raw[i] ~ /^>>>>>>>([ \t]|$)/) {
      err(i, "conflict-marker", "unresolved merge conflict marker >>>>>>>")
      open_conflict = 0
    } else if (open_conflict && raw[i] ~ /^=======[ \t]*$/) {
      # Only inside an open conflict: a bare row of = is also a setext H2
      # underline, and flagging those would be a false positive on real prose.
      err(i, "conflict-marker", "unresolved merge conflict marker =======")
    } else if (open_conflict && raw[i] ~ /^\|\|\|\|\|\|\|([ \t]|$)/) {
      err(i, "conflict-marker", "unresolved merge conflict marker ||||||| (diff3 base)")
    }
  }

  # ---- 2. unfilled template tokens / leftover scaffolding (ERROR) ---------
  for (i = 1; i <= n; i++) {
    if (prose[i] == "") continue
    if (match(prose[i], /\{\{[A-Za-z0-9_.-]+\}\}/))
      err(i, "template-token", "unfilled template token " substr(prose[i], RSTART, RLENGTH))
    if (prose[i] ~ /<!--[ \t]*\/?(REPEAT|OPTIONAL):/)
      err(i, "template-scaffold", "template scaffolding comment left in output (REPEAT:/OPTIONAL:)")
  }

  # ---- 3a. reference direction (WARN) -------------------------------------
  # For every heading, find prose that names it verbatim and then says
  # above/below within the same clause. Only a disagreeing direction is
  # reported — a correct forward reference is the overwhelmingly common case
  # and saying so would drown the real ones.
  for (i = 1; i <= n; i++) {
    if (text[i] == "") continue
    lp = normalize(text[i])
    for (j = 1; j <= nh; j++) {
      if (hline[j] == i) continue
      if (length(htext[j]) < 4) continue
      pos = index(lp, htext[j])
      if (pos == 0) continue
      if (!phrase_at(lp, htext[j], pos)) continue
      tail = substr(lp, pos + length(htext[j]), 40)
      # The direction word has to follow the name directly — at most one
      # structural noun and a comma in between. Anything looser binds a stray
      # "below" to whatever heading-shaped word happened to appear earlier in
      # the sentence: "runs in two modes — the workflow below covers both"
      # is about the workflow, not about the `## Modes` heading.
      if (!match(tail, /^[ \t]*((sub-?)?section|table|list|rule|step|block|note|heading)?[ \t]*,?[ \t]*(above|below)([^a-z]|$)/)) continue
      # Read the direction off the matched prefix, not off `tail` — a later
      # "above" further down the line must not overrule the "below" that
      # actually binds to this name.
      said = (substr(tail, RSTART, RLENGTH) ~ /above/) ? "above" : "below"
      actual = (hline[j] < i) ? "above" : "below"
      if (said != actual)
        warn(i, "ref-direction", "says \"" htext[j] "\" " said ", but that heading is " actual " (line " hline[j] ")")
    }
  }

  # ---- 3b. dangling delimited reference (WARN) ----------------------------
  # A quoted or bolded name reads as a title, so `"X" below` alone is enough to
  # claim it points at a section. A BACKTICKED name is a code identifier, and
  # prose about identifiers routinely says "…the caveat `KIRO_HOME` below
  # carries" while pointing at a bullet, not a heading — so backticks need an
  # explicit structural noun ("`X` section below") before this fires.
  for (i = 1; i <= n; i++) {
    if (text[i] == "") continue
    s = text[i]
    while (match(s, /("[^"]+"|`[^`]+`|\*\*[^*]+\*\*)[ \t]*((sub-?)?section|table|heading|chapter)?[ \t]*,?[ \t]*(above|below)([^A-Za-z]|$)/)) {
      m = substr(s, RSTART, RLENGTH)
      s = substr(s, RSTART + RLENGTH)
      if (m ~ /^`/ && m !~ /((sub-?)?section|table|heading|chapter)/) continue
      name = m
      sub(/[ \t]*((sub-?)?section|table|heading|chapter)?[ \t]*,?[ \t]*(above|below)([^A-Za-z]|$)$/, "", name)
      gsub(/^[`"*]+/, "", name); gsub(/[`"*]+$/, "", name)
      name = normalize(name)
      if (name == "") continue
      found = 0
      for (j = 1; j <= nh; j++)
        if (htext[j] == name) { found = 1; break }
      if (!found)
        warn(i, "ref-dangling", "reference to \"" name "\" " (m ~ /above([^A-Za-z]|$)$/ ? "above" : "below") " names no heading in this file")
    }
  }

  # ---- 4. announced count vs list length (WARN) ---------------------------
  split("two three four five six seven eight nine ten", words, " ")
  for (w = 1; w <= 9; w++) numword[words[w]] = w + 1

  for (i = 1; i <= n; i++) {
    if (text[i] == "" || text[i] !~ /:[ \t]*$/) continue
    want = 0; wantword = ""
    # Only the announcing sentence counts — the last one before the colon. A
    # count word stranded in an earlier sentence is describing something else:
    # "Unlike the other six axes … Price the gap instead, using the menu
    # below:" introduces three items and announces none of them. Sentence
    # breaks only: splitting on an em dash too would lose the common
    # "**Two flavors** — pick one:" shape, which does announce its list.
    lp = tolower(text[i])
    if (match(lp, /^.*(\. |; )/)) lp = substr(lp, RSTART + RLENGTH)
    for (w = 1; w <= 9; w++) {
      # Not hyphenated ("three-tier" is an adjective, not a count) and not
      # inside a longer word.
      if (match(lp, "(^|[^a-z-])" words[w] "([^a-z-]|$)")) {
        if (want == 0 || RSTART < firstpos) { want = numword[words[w]]; wantword = words[w]; firstpos = RSTART }
      }
    }
    if (want == 0) continue

    j = i + 1
    while (j <= n && raw[j] ~ /^[ \t]*$/) j++
    if (j > n || infence[j] || !is_list_marker(raw[j])) continue

    base = indent_of(raw[j])
    got = 1
    blanks = 0
    for (k = j + 1; k <= n; k++) {
      if (raw[k] ~ /^[ \t]*$/) { blanks++; if (blanks >= 2) break; continue }
      ind = indent_of(raw[k])
      if (ind > base) { blanks = 0; continue }           # continuation or nested
      if (ind == base && is_list_marker(raw[k])) { got++; blanks = 0; continue }
      break
    }
    if (got != want)
      warn(i, "list-count", "lead-in announces \"" wantword "\" (" want ") but " got " list item" (got == 1 ? "" : "s") " follow")
  }

  # ---- 5. frontmatter sanity (WARN, SKILL.md only) ------------------------
  if (is_skill_md) {
    if (fm_end == 0) {
      warn(1, "frontmatter", "SKILL.md has no YAML frontmatter block")
    } else {
      gotname = ""; namelines = 0; desc_line = 0; desc_value = ""
      for (i = 2; i < fm_end; i++) {
        if (raw[i] ~ /^name:[ \t]*/) {
          gotname = raw[i]; sub(/^name:[ \t]*/, "", gotname)
          gsub(/^["']|["'][ \t\r]*$/, "", gotname)
          gsub(/[ \t\r]+$/, "", gotname)
          namelines++
        }
        if (raw[i] ~ /^description:[ \t]*/) {
          desc_line = i
          desc_value = raw[i]; sub(/^description:[ \t]*/, "", desc_value)
          gsub(/^["']|["'][ \t\r]*$/, "", desc_value)
          gsub(/^[ \t\r]+|[ \t\r]+$/, "", desc_value)
        }
      }
      if (namelines == 0)
        warn(1, "frontmatter", "frontmatter has no name: key")
      else if (expected_name != "" && gotname != expected_name)
        warn(1, "frontmatter", "name: is \"" gotname "\" but the directory says \"" expected_name "\"")

      if (desc_line == 0) {
        warn(1, "frontmatter", "frontmatter has no description: key")
      } else if (desc_value == "" || desc_value == ">" || desc_value == "|" || desc_value == ">-" || desc_value == "|-") {
        # A folded/literal scalar is empty only when no indented body follows
        # it before the closing ---.
        body = 0
        for (i = desc_line + 1; i < fm_end; i++) {
          if (raw[i] ~ /^[ \t]*$/) continue
          if (raw[i] ~ /^[ \t]+[^ \t]/) { body = 1 }
          break
        }
        if (!body) warn(desc_line, "frontmatter", "description: is empty")
      }
    }
  }
}
AWK_END

RED=""; YELLOW=""; BOLD=""; RESET=""
if [[ -t 1 ]]; then RED=$'\033[31m'; YELLOW=$'\033[33m'; BOLD=$'\033[1m'; RESET=$'\033[0m'; fi

errors=0
warnings=0
for f in "${FILES[@]}"; do
  if [[ ! -f "$f" ]]; then
    echo "skill-lint: no such file: $f" >&2
    errors=$((errors + 1))
    continue
  fi
  is_skill_md=0
  expected=""
  if [[ "$(basename "$f")" == "SKILL.md" ]]; then
    is_skill_md=1
    expected="$(expected_skill_name "$f")"
  fi
  while IFS=$'\t' read -r sev line check msg; do
    [[ -z "$sev" ]] && continue
    if [[ "$sev" == "ERROR" ]]; then
      errors=$((errors + 1))
      printf '%s%s:%s:%s %s [%s]\n' "$RED" "$f" "$line" "$RESET" "$msg" "$check"
    else
      warnings=$((warnings + 1))
      printf '%s%s:%s:%s %s [%s]\n' "$YELLOW" "$f" "$line" "$RESET" "$msg" "$check"
    fi
  done < <(awk -v is_skill_md="$is_skill_md" -v expected_name="$expected" "$LINT_AWK" "$f")
done

printf '\n%sskill-lint:%s %d file(s), %d error(s), %d warning(s)\n' \
  "$BOLD" "$RESET" "${#FILES[@]}" "$errors" "$warnings"

if [[ "$errors" -gt 0 ]]; then
  exit 1
fi
if [[ "$STRICT" == 1 && "$warnings" -gt 0 ]]; then
  echo "skill-lint: --strict — warnings are failures" >&2
  exit 1
fi
exit 0
