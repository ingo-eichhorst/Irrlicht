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
#      unbalanced-fence   ERROR  an odd number of ``` / ~~~ delimiters
#   3. ref-direction      WARN   "X above" where heading X is below (or vice versa)
#      ref-dangling       WARN   a delimited "X above/below" naming no heading
#   4. list-count         WARN   "Three moves:" followed by a list of four
#   5. frontmatter        WARN   SKILL.md name: disagrees with its directory,
#                                description: missing/empty, or the block never
#                                closes / holds something that is not YAML
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
# inside backticks. So checks 2 and 4 skip fenced blocks and blank inline code
# spans. Check 3 keeps inline code (a reference can legitimately be written
# `` `Auto mode` below ``) and check 1 reads raw lines outside fences, because
# a conflict marker inside a fence is illustrative but anywhere else is a
# corrupted file. Frontmatter is
# structured data, never prose that documents a marker, so check 2 reads it —
# an unfilled {{TOKEN}} in `description:` is what the skill loader matches on
# — while checks 3 and 4 skip it, since a description legitimately contains
# quoted phrases and counted lead-ins.
#
# Skipping is the whole mechanism, which makes an unbalanced delimiter a
# silencer: every skipped line is a line no check reads, so one missing closing
# fence used to hide the entire rest of the file, and an unterminated
# frontmatter used to swallow however much of the body sat above the next `---`.
# Both are therefore detected first, reported, and then not allowed to suppress
# anything — a corrupted skill file reporting 0 errors is the exact failure
# this script exists to remove.
#
# Known limits (deliberate, not oversights)
# ----------------------------------------
# * Check 4 counts list markers. The ir:triage defect that motivated it — "Two
#   flavors:" followed by three, the third orphaned *outside* the list — has
#   two markers and so passes. Counting what a reader would call an item is a
#   judgement call; counting markers is not.
# * Check 3 only recognises a reference when the name is delimited
#   (`"X"`, `` `X` ``, `**X**`) or matches a heading — verbatim, or up to a
#   subtitle separator, so "Readiness Rubric" finds
#   `## Readiness Rubric (7 axes)` and "Phase 1" finds `## Phase 1 — Worktree`.
#   "see the rule above" names nothing checkable and is ignored by design.
# * Setext headings (underlined with === or ---) are not indexed as headings;
#   every heading in this repo's skill files is ATX (#).
#
# Scope: every `.md` under `.claude/skills/`, plus any other tracked `SKILL.md`
# in the repo (`tools/irrlicht-design-system/SKILL.md` is one — a real skill
# that lived outside every gate). `testdata/` is excluded: the fixture corpus is
# deliberately broken and is driven by the unit test, not by the gate.
#
# Usage:
#   tools/skill-lint.sh                  # lint every skill markdown file
#   tools/skill-lint.sh --strict         # warnings fail too
#   tools/skill-lint.sh FILE...          # lint just these files (used by tests)
#   tools/skill-lint.sh --strict FILE...
#
# The expected frontmatter `name:` for a SKILL.md is its directory path below
# the nearest `skills/` ancestor — `ir:exec`, or `ir:onboarding-factory/assess`
# for a nested one. Deriving it from the path rather than a hard-coded list is
# what lets the fixture corpus under tools/lib/testdata/ exercise check 5
# without living in .claude/skills/, and what lets a skill outside
# `.claude/skills/` be linted with the name check simply switched off.
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

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"

if [[ ${#FILES[@]} -eq 0 ]]; then
  cd "$ROOT_DIR" || exit 2
  # Two roots, not one: `.claude/skills/` is where skills normally live, but a
  # SKILL.md anywhere else is still a skill by every definition these checks
  # use, and scoping to one directory left `tools/irrlicht-design-system/`
  # outside every gate. `testdata/` is excluded because the fixture corpus is
  # deliberately corrupt — the unit test drives it, the gate must not.
  #
  # The index rather than `find`: `find .` would descend into
  # `.claude/worktrees/`, which holds entire checkouts of this repo, and lint
  # every other branch's skill files as if they were this one's. Staged files
  # are index entries, so a newly added skill is covered on the push that adds
  # it.
  while IFS= read -r f; do
    [[ "$f" == */testdata/* ]] && continue
    FILES+=("$f")
  done < <(git ls-files -- '.claude/skills/*.md' '*/SKILL.md' 'SKILL.md' | LC_ALL=C sort -u)
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
  local dir="${1%/*}"
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

# One parser for both keys. As two near-identical blocks they had drifted:
# `name:` trimmed trailing whitespace only, so a leading space survived into
# the mismatch message while `description:` trimmed both ends.
function fm_value(s, key,   v) {
  v = s
  sub("^" key ":[ \t]*", "", v)
  gsub(/^["']|["'][ \t]*$/, "", v)
  gsub(/^[ \t]+|[ \t]+$/, "", v)
  return v
}
function indent_of(s,   t) {
  t = s
  sub(/[^ \t].*$/, "", t)
  gsub(/\t/, "    ", t)
  return length(t)
}
function is_list_marker(s) {
  return (s ~ /^[ \t]*([-*+]|[0-9]+[.)])[ \t]+/)
}
# nonprose(i) — lines no prose check may read: inside a fence, or inside the
# frontmatter span. Checks 3 and 4 skip frontmatter because a description
# legitimately contains quoted phrases and counted lead-ins; check 2 does NOT
# skip it, since frontmatter is structured data that never *documents* a
# marker, and an unfilled {{TOKEN}} in `description:` is the field the skill
# loader matches on.
function nonprose(i) { return (infence[i] || (fm_end > 0 && i <= fm_end)) }
# Word-boundary containment: index() would match "modes" inside "submodes".
function phrase_at(hay, needle, pos,   before, after) {
  before = (pos > 1) ? substr(hay, pos - 1, 1) : " "
  after  = substr(hay, pos + length(needle), 1)
  if (after == "") after = " "
  return (before !~ /[A-Za-z0-9]/ && after !~ /[A-Za-z0-9]/)
}

# `\r` is stripped once, here, rather than defended against in a dozen
# regexes downstream — a per-regex `[ \t\r]` vs `[ \t]` decision that every
# future check would have to re-make, and get silently wrong. Fence balance is
# counted on the way past for the same reason: it needs a whole traversal, and
# this is already one.
{ sub(/\r$/, ""); n++; raw[n] = $0; if ($0 ~ /^[ \t]*(```|~~~)/) fences++ }

END {
  # ---- classify each line -------------------------------------------------
  # Checks read raw[] and ask nonprose() what to skip, rather than each
  # consulting its own blanked copy of the file. Skipping is how the linter
  # tells "documents a marker" from "has one", and every skipped line is a line
  # no check reads. That makes an unbalanced delimiter a silencer: one missing
  # closing fence, or a frontmatter that never closes, and the rest of the file
  # passes unread — a corrupted skill file linting 0/0, which is the exact
  # failure #1209 is about. So both are detected up front, reported, and then
  # NOT allowed to suppress anything.

  # Frontmatter must close within FM_MAX lines. Unbounded, the closing `---`
  # binds to the first thematic break in the body — arbitrarily far down —
  # and swallows everything in between. The longest real one in this repo is
  # a folded description of ~10 lines.
  FM_MAX = 40
  fm_end = 0; fm_unterminated = 0
  if (n >= 1 && raw[1] ~ /^---[ \t]*$/) {
    for (i = 2; i <= n && i <= FM_MAX; i++)
      if (raw[i] ~ /^---[ \t]*$/) { fm_end = i; break }
    if (fm_end == 0) fm_unterminated = 1
  }

  # An odd number of fence delimiters means the file's own view of what is code
  # is untrustworthy, so treat none of it as fenced and let every check read
  # everything. The invariant for any suppression-based linter: when the file
  # cannot be parsed with confidence, degrade toward MORE checking, never less.
  fence_broken = (fences % 2)

  fence = 0
  for (i = 1; i <= n; i++) {
    if (!fence_broken && raw[i] ~ /^[ \t]*(```|~~~)/) {
      infence[i] = 1          # the delimiter itself is never prose
      fence = !fence
      continue
    }
    infence[i] = fence
    if (fence) continue

    # `#+` rather than `#{1,6}`: ERE interval expressions are not supported by
    # every awk this may run on, and an awk that silently matched no heading
    # would leave check 3a dead and check 3b flagging every reference.
    if (raw[i] ~ /^#+[ \t]+/) {
      h = raw[i]
      sub(/^#+[ \t]+/, "", h)
      sub(/[ \t]+#+[ \t]*$/, "", h)
      nh++; hline[nh] = i; htext[nh] = normalize(h)
      # A subtitled heading gets a second ROW in the same table rather than a
      # parallel column, so both forms match through one code path. Prose says
      # "Readiness Rubric" for `## Readiness Rubric (7 axes)` and "Phase 1" for
      # `## Phase 1 — Worktree`; the corpus has 91 of the first shape and 48 of
      # the second, so handling only parentheses covered a third of nothing.
      # Written as separate sub()s, not one bracket class: the dashes are
      # multi-byte, and a byte-oriented awk would match their bytes piecemeal
      # inside an unrelated character.
      #
      # Parentheses and dashes only — NOT a colon. `## Step 6: Build Artifacts`
      # would shorten to "step 6", and ir:release's prose says "step 6" a dozen
      # times meaning *something inside* step 6, not the heading: it produced a
      # false "says step 6 below, but that heading is above" on the first run.
      # A parenthetical or dashed tail is a subtitle; a colon here yields a
      # label the body then reuses as an ordinary noun.
      short = htext[nh]
      sub(/[ \t]*\(.*$/, "", short)
      sub(/[ \t]*—.*$/, "", short)
      sub(/[ \t]*–.*$/, "", short)
      if (short != "" && short != htext[nh] && !seen_heading[short]) {
        nh++; hline[nh] = i; htext[nh] = short
      }
      seen_heading[htext[nh]] = 1
    }
  }

  if (fence_broken)
    err(1, "unbalanced-fence", "odd number of code-fence delimiters — the file's code blocks cannot be identified, so nothing was treated as fenced")
  if (fm_unterminated)
    warn(1, "frontmatter", "frontmatter opens with --- but never closes within the first " FM_MAX " lines")

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
    } else if (open_conflict && raw[i] ~ /^=======[ \t\r]*$/) {
      # Only inside an open conflict: a bare row of = is also a setext H2
      # underline, and flagging those would be a false positive on real prose.
      err(i, "conflict-marker", "unresolved merge conflict marker =======")
    } else if (open_conflict && raw[i] ~ /^\|\|\|\|\|\|\|([ \t]|$)/) {
      err(i, "conflict-marker", "unresolved merge conflict marker ||||||| (diff3 base)")
    }
  }

  # ---- 2. unfilled template tokens / leftover scaffolding (ERROR) ---------
  for (i = 1; i <= n; i++) {
    if (infence[i]) continue          # NOT nonprose(): frontmatter is in scope
    p = raw[i]
    gsub(/`[^`]*`/, " ", p)
    if (match(p, /\{\{[A-Za-z0-9_.-]+\}\}/))
      err(i, "template-token", "unfilled template token " substr(p, RSTART, RLENGTH))
    if (p ~ /<!--[ \t]*\/?(REPEAT|OPTIONAL):/)
      err(i, "template-scaffold", "template scaffolding comment left in output (REPEAT:/OPTIONAL:)")
  }

  # ---- 3a. reference direction (WARN) -------------------------------------
  # For every heading, find prose that names it verbatim and then says
  # above/below within the same clause. Only a disagreeing direction is
  # reported — a correct forward reference is the overwhelmingly common case
  # and saying so would drown the real ones.
  for (i = 1; i <= n; i++) {
    if (nonprose(i)) continue
    lp = normalize(raw[i])
    # This check can only fire on a line that says "above" or "below" — 94 of
    # the corpus's 6,416 lines. Without the guard the other 98.5% each pay a
    # full sweep of the heading table to reach `continue`, which is O(lines ×
    # headings): measured at 57% of the awk CPU on the largest skill file, and
    # growing ~3.2x per doubling of file size. Guarding on the normalized line
    # rather than the raw one also catches "Above"/"BELOW".
    if (lp !~ /above|below/) continue
    for (j = 1; j <= nh; j++) {
      if (hline[j] == i || length(htext[j]) < 4) continue
      # The 4-character floor is a noise guard: without it a heading like "the"
      # or "and" would match half the prose in the file. No real heading in
      # this repo is shorter.
      pos = index(lp, htext[j])
      if (pos == 0 || !phrase_at(lp, htext[j], pos)) continue
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
  # Built once and used three times: match(), the backtick exemption and the
  # sub() that strips the suffix back off must describe the SAME span, and as
  # three hand-copied literals an edit to one silently changed what `name`
  # ended up being. 3b's nouns are narrower than 3a's on purpose — 3a's name
  # comes from the heading table and is already known to be a heading, so it
  # can afford "list"/"rule"/"step"; 3b's is arbitrary quoted prose.
  NOUN = "((sub-?)?section|table|heading|chapter)"
  DIRSUF = "[ \t]*" NOUN "?[ \t]*,?[ \t]*(above|below)([^A-Za-z]|$)"
  for (i = 1; i <= n; i++) {
    if (nonprose(i)) continue
    s = raw[i]
    while (match(s, "(\"[^\"]+\"|`[^`]+`|\\*\\*[^*]+\\*\\*)" DIRSUF)) {
      m = substr(s, RSTART, RLENGTH)
      s = substr(s, RSTART + RLENGTH)
      if (m ~ /^`/ && m !~ NOUN) continue
      name = m
      sub(DIRSUF "$", "", name)
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
  # The value IS the index: words[1]="two" means 2, so `w + 1` replaces a
  # lookup table. nw rather than a literal 9, so adding a count word is a
  # one-place edit.
  nw = split("two three four five six seven eight nine ten", words, " ")

  for (i = 1; i <= n; i++) {
    if (nonprose(i) || raw[i] !~ /:[ \t]*$/) continue
    want = 0; wantword = ""
    # Only the announcing sentence counts — the last one before the colon. A
    # count word stranded in an earlier sentence is describing something else:
    # "Unlike the other six axes … Price the gap instead, using the menu
    # below:" introduces three items and announces none of them. Sentence
    # breaks only: splitting on an em dash too would lose the common
    # "**Two flavors** — pick one:" shape, which does announce its list.
    lp = tolower(raw[i])
    if (match(lp, /^.*(\. |; )/)) lp = substr(lp, RSTART + RLENGTH)
    for (w = 1; w <= nw; w++) {
      # Not hyphenated ("three-tier" is an adjective, not a count) and not
      # inside a longer word.
      if (match(lp, "(^|[^a-z-])" words[w] "([^a-z-]|$)")) {
        if (want == 0 || RSTART < firstpos) { want = w + 1; wantword = words[w]; firstpos = RSTART }
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
      if (raw[k] ~ /^[ \t]*$/) { if (++blanks >= 2) break; continue }
      blanks = 0
      ind = indent_of(raw[k])
      if (ind > base) continue                           # continuation or nested
      if (ind < base || !is_list_marker(raw[k])) break
      got++
    }
    if (got != want)
      warn(i, "list-count", "lead-in announces \"" wantword "\" (" want ") but " got " list item" (got == 1 ? "" : "s") " follow")
  }

  # ---- 5. frontmatter sanity (WARN, SKILL.md only) ------------------------
  if (is_skill_md) {
    if (fm_end == 0) {
      warn(1, "frontmatter", "SKILL.md has no YAML frontmatter block")
    } else {
      gotname = ""; has_name = 0; desc_line = 0; desc_value = ""
      for (i = 2; i < fm_end; i++) {
        if (raw[i] ~ /^name:[ \t]*/)        { gotname = fm_value(raw[i], "name"); has_name = 1 }
        if (raw[i] ~ /^description:[ \t]*/) { desc_line = i; desc_value = fm_value(raw[i], "description") }
      }
      # A frontmatter span containing prose is a frontmatter whose closing ---
      # went missing and bound to a thematic break further down: the span then
      # covers body text, and everything in it is read as YAML. Checks 3 and 4
      # skip that span, so without this the only symptom is silence. A line is
      # not YAML when it is unindented (not a continuation), not a `key:`, not
      # a `- ` item, and not a `#` comment.
      for (i = 2; i < fm_end; i++) {
        if (raw[i] ~ /^[ \t]*$/ || raw[i] ~ /^[ \t#]/ || raw[i] ~ /^-[ \t]/) continue
        if (raw[i] ~ /^[A-Za-z_][A-Za-z0-9_.-]*:/) continue
        warn(i, "frontmatter", "frontmatter contains a line that is not YAML — the closing --- is probably missing")
        break
      }

      if (!has_name)
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
  if [[ "${f##*/}" == "SKILL.md" ]]; then
    is_skill_md=1
    expected="$(expected_skill_name "$f")"
  fi
  # Command substitution, NOT process substitution: `done < <(awk …)` discards
  # awk's exit status, so an awk that cannot read the file — or that rejects a
  # construct in the program — yields zero lines and the file is tallied as
  # clean. That would turn any awk incompatibility into a silently green gate,
  # which is the failure mode this whole script exists to remove.
  if ! found="$(awk -v is_skill_md="$is_skill_md" -v expected_name="$expected" "$LINT_AWK" "$f")"; then
    echo "skill-lint: awk failed on $f — treating as a failure, not a clean file" >&2
    errors=$((errors + 1))
    continue
  fi
  while IFS=$'\t' read -r sev line check msg; do
    [[ -z "$sev" ]] && continue
    if [[ "$sev" == "ERROR" ]]; then
      errors=$((errors + 1))
      color="$RED"
    else
      warnings=$((warnings + 1))
      color="$YELLOW"
    fi
    # The severity is printed as a word, not encoded in the color: colors are
    # suppressed whenever stdout is not a TTY, which is exactly the case in a
    # GitHub Actions log — where a developer looking at a failed gate most
    # needs to tell the blocking lines from the advisory ones.
    printf '%s%s:%s:%s %-5s %s [%s]\n' "$color" "$f" "$line" "$RESET" "$sev" "$msg" "$check"
  done <<<"$found"
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
