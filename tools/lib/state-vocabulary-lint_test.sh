#!/usr/bin/env bash
# state-vocabulary-lint_test.sh — mutation evidence for
# tools/state-vocabulary-lint.sh (#1804).
#
# Per this repo's testing philosophy (AGENTS.md's Testing section /
# docs/testing-philosophy.md): a check a change *adds* has no "before the fix"
# to run red against, so it owes a deliberate mutation instead. The mutation
# here is a file that hand-types three of the four canonical states and omits
# the fourth — the exact shape that shipped four defects in #1796 — reproduced
# against committed fixtures under tools/lib/testdata/state-vocabulary-lint/
# rather than by planting a stale enumeration in a real file, so the evidence
# outlives this PR and is re-run on every push.
#
# Cases, each pinned to the exit code the gate's header documents
# (0 clean / 1 finding / 2 refusal):
#
#   THE THRESHOLD
#     three-of-four.md    3 of 4 named — the mutation. Must FAIL and must name
#                         the offending line, not merely exit non-zero.
#     two-of-four.md      2 of 4 — a deliberate concurrency partition. Must
#                         PASS: this is the boundary the threshold of 3 buys,
#                         and a regression to 2 shows up here first.
#     all-four.md         the whole vocabulary — current by construction, must
#                         PASS. This is also the self-maintaining half: were a
#                         fifth state added, this fixture becomes a proper
#                         subset and this case would legitimately flip.
#
#   ONE FIXTURE PER MATCHER ARM
#     A 24-mutation battery run during review found that four of the six arms
#     — markup bodies, identifier components, and both prose-run directions —
#     were killed ONLY by the real-repo vacuity guard at the bottom of this
#     file. That is coverage by coincidence: it depends on today's repo
#     contents and evaporates the moment those particular sites are fixed.
#     Each of the three fixtures added here uses exactly one spelling, so
#     deleting its arm fails a named case rather than a census.
#
#   THE TWO GUARDS THAT STOP A FALSE CLEAN
#     Also from that battery — the only two mutations that survived it. Each
#     could be deleted outright with the whole suite still green, while the
#     gate reported "clean" over a scan that had not happened:
#     the scan-status check (an awk that DIED and an awk that found NOTHING
#     both produce empty output), and the exclusion-staleness refusal (an
#     exclusion that stopped excluding reads exactly like coverage).
#
#   THE WAIVER LIST, BOTH DIRECTIONS
#     covered             a live waiver suppresses its site — PASS.
#     stale               a waiver matching no site — FAIL. A waiver that
#                         stopped matching and a clean run must not look the
#                         same (tools/lib/shell-lib-suite.sh refuses a skip
#                         that names no file for the same reason).
#     unreadable          the waiver file cannot be read at all — REFUSE, not
#                         "everything is unwaived" and not a quiet pass.
#
#   THE DERIVED VOCABULARY — the part most likely to rot silently
#     no-literal          `canonicalStates` moved or was renamed — REFUSE.
#     unresolvable        the slice names a constant with no declaration —
#                         REFUSE on a HALF-parsed vocabulary rather than
#                         scanning for the part that parsed.
#     too-small           a vocabulary at or below the threshold makes the
#                         "names >= 3 but not all" rule vacuous, so a scan
#                         would report a serene zero over the whole repo —
#                         REFUSE.
#
#   VACUITY GUARDS — without these, everything above could pass while the gate
#   was wired to fixtures and never looked at the repo it exists to protect:
#     * the real repo passes its own gate;
#     * the real vocabulary parses out of the real session.go and holds more
#       values than the threshold;
#     * the real scan actually FINDS sites (a matcher that silently stopped
#       matching would otherwise read as a clean repo).
set -uo pipefail

REPO_ROOT=$(git rev-parse --show-toplevel)
cd "$REPO_ROOT" || { echo "FAIL: cannot cd to repo root $REPO_ROOT" >&2; exit 1; }

GATE=tools/state-vocabulary-lint.sh
FIXTURE_DIR=tools/lib/testdata/state-vocabulary-lint

if [[ ! -x "$GATE" ]]; then
  echo "FAIL: state-vocabulary-lint_test — subject not found or not executable at $GATE" >&2
  exit 1
fi
if [[ ! -d "$FIXTURE_DIR" ]]; then
  echo "FAIL: state-vocabulary-lint_test — fixture corpus missing at $FIXTURE_DIR" >&2
  exit 1
fi

rc=0
fail() { echo "FAIL: $1" >&2; rc=1; }

# assert_gate <label> <expected-rc> <source> <waivers> <file...> -- [must-contain]
# The expected text, when given, is checked against combined output: an exit
# code alone does not prove the gate reported the RIGHT thing.
assert_gate() {
  local label="$1" want="$2" src="$3" waivers="$4" file="$5" want_text="${6:-}"
  local out got
  out=$("$GATE" --source "$src" --waivers "$waivers" -- "$file" 2>&1)
  got=$?
  if [[ "$got" -ne "$want" ]]; then
    fail "$label: expected exit $want, got $got (output: $out)"
    return
  fi
  if [[ -n "$want_text" && "$out" != *"$want_text"* ]]; then
    fail "$label: exit $got was right but output missing '$want_text' — got: $out"
    return
  fi
  echo "  PASS: $label (exit $got)"
}

SRC="$FIXTURE_DIR/vocab-source.go"

# --- the threshold ----------------------------------------------------------
assert_gate "3-of-4 is the mutation and must be caught" \
  1 "$SRC" /dev/null "$FIXTURE_DIR/three-of-four.md" "three-of-four.md:7"
# The same mutation spelled as Go constants, where the state names appear ONLY
# inside `State<Capitalised>` identifiers. A pre-filter that matches
# case-sensitively skips this shape silently while still exiting 0 — which the
# scan's index() optimisation did on its first cut. Not a duplicate of the case
# above: that one would stay green through the same bug.
assert_gate "the State<Cap> constant spelling is caught too (case-folding)" \
  1 "$SRC" /dev/null "$FIXTURE_DIR/three-of-four-constants.go" "three-of-four-constants.go:13"
assert_gate "2-of-4 stays below the threshold (the deliberate partition)" \
  0 "$SRC" /dev/null "$FIXTURE_DIR/two-of-four.md"
assert_gate "naming the whole vocabulary is not a finding" \
  0 "$SRC" /dev/null "$FIXTURE_DIR/all-four.md"

# --- one fixture per matcher arm --------------------------------------------
# Without these, four of the six arms are killed ONLY by the real-repo vacuity
# guard below — i.e. their coverage depends on today's repo contents and
# evaporates the moment those particular sites are fixed. Each fixture below
# uses exactly one spelling, so removing its arm fails here by construction.
assert_gate "markup bodies (>value<) are a naming, not just quoted strings" \
  1 "$SRC" /dev/null "$FIXTURE_DIR/three-of-four-markup.html" "three-of-four-markup.html:6"
assert_gate "identifier components (badge-working) count as a naming" \
  1 "$SRC" /dev/null "$FIXTURE_DIR/three-of-four-cssclass.md" "three-of-four-cssclass.md:6"
assert_gate "bare prose enumerations count, in both separator directions" \
  1 "$SRC" /dev/null "$FIXTURE_DIR/three-of-four-prose-run.md" "three-of-four-prose-run.md"

# --- the waiver list, both directions ---------------------------------------
assert_gate "a live waiver suppresses its site" \
  0 "$SRC" "$FIXTURE_DIR/waivers-covering.txt" "$FIXTURE_DIR/three-of-four.md" "all waived"
assert_gate "a waiver matching no site is itself a failure" \
  1 "$SRC" "$FIXTURE_DIR/waivers-stale.txt" "$FIXTURE_DIR/all-four.md" "match no flagged site"
assert_gate "an unreadable waiver file is a REFUSAL, not 'nothing is waived'" \
  2 "$SRC" "$FIXTURE_DIR/does-not-exist.txt" "$FIXTURE_DIR/three-of-four.md" "cannot read the waiver file"

# --- the derived vocabulary -------------------------------------------------
assert_gate "a missing canonicalStates literal is a REFUSAL" \
  2 "$FIXTURE_DIR/vocab-source-no-literal.go" /dev/null "$FIXTURE_DIR/three-of-four.md" "no \`canonicalStates"
assert_gate "a constant the slice names but does not declare is a REFUSAL" \
  2 "$FIXTURE_DIR/vocab-source-unresolvable.go" /dev/null "$FIXTURE_DIR/three-of-four.md" "StateMissing"
assert_gate "a vocabulary at the threshold makes the rule vacuous — REFUSAL" \
  2 "$FIXTURE_DIR/vocab-source-too-small.go" /dev/null "$FIXTURE_DIR/three-of-four.md" "cannot match anything"

# --- the two guards that stop a FALSE CLEAN ---------------------------------
# Both were unpinned until a mutation battery found them (#1804 review): each
# could be deleted outright and the whole suite stayed green, while the gate
# silently reported "clean" over a scan that had not happened.

# The scan-status check. An awk that DIED and an awk that found NOTHING both
# produce empty output; only the exit status separates them. This is the exact
# case the script header records as a real incident (macOS awk rejecting a
# newline in the vocabulary reported a clean tree over a repo full of sites).
assert_gate "a file the scanner cannot read is a REFUSAL, not a clean scan" \
  2 "$SRC" /dev/null "$FIXTURE_DIR/no-such-file-exists.md" "the scan itself failed"

# The exclusion-staleness refusal. Driven by sourcing the gate and rewriting
# its exclusion array in-process — no test-only hook in the production path.
# Without this, renaming an excluded tree turns its exclusion into a silent
# no-op, which reads from the log exactly like coverage.
(
  # shellcheck source=../state-vocabulary-lint.sh
  . "$GATE"
  # shellcheck disable=SC2034  # Read by state_vocab_files in the gate sourced above, not in this file — shellcheck cannot see across the `.` with external-sources off.
  STATE_VOCAB_EXCLUDE=(
    'replaydata/'                              'still real, so the refusal below is about the NEXT entry only'
    'no/such/tree/this/repo/has/never/had/'    'deliberately dead'
  )
  out=$(state_vocab_files "working waiting ready error" 2>&1)
  got=$?
  if [[ "$got" -ne 2 ]]; then
    echo "FAIL: an exclusion matching nothing must REFUSE (exit 2), got $got: $out" >&2
    exit 1
  fi
  if [[ "$out" != *"matched no file"* || "$out" != *"deliberately dead"* ]]; then
    echo "FAIL: the refusal must name the dead exclusion and repeat its stated reason — got: $out" >&2
    exit 1
  fi
  echo "  PASS: an exclusion that stopped excluding is a REFUSAL, and names its reason"
) || rc=1

# A file containing a NUL byte, but still text, must stay in the corpus.
#
# platforms/web/irrlicht.js holds a literal NUL at offset 67966 (a `'\0'`
# string separator) and carries a real flagged site. Text-vs-binary heuristics
# disagree about it: GNU grep 3.12's `-I` calls it binary in every locale,
# while the ugrep that shadows `grep` on macOS does not. When the narrowing
# step used `grep -I`, the corpus therefore differed by one real source file
# between a laptop and the Linux CI runner — the local run being the permissive
# one, so the gap was invisible locally. `git grep -I` decides with git's own
# rule (NUL within the first 8000 bytes), identically everywhere.
#
# This locks the outcome rather than the implementation: swap the narrowing
# back to a libc-heuristic grep and this fails on Linux, which is where it
# matters. It is a LOCK — it passes on macOS by construction either way.
(
  # shellcheck source=../state-vocabulary-lint.sh
  . "$GATE"
  corpus=$(state_vocab_files "working waiting ready error" 2>/dev/null)
  if ! printf '%s\n' "$corpus" | grep -qxF 'platforms/web/irrlicht.js'; then
    echo "FAIL: platforms/web/irrlicht.js is missing from the scanned corpus — a NUL-containing TEXT file is being rejected as binary (see the narrowing step's comment)." >&2
    exit 1
  fi
  echo "  PASS: a NUL-containing text file stays in the corpus (lock)"
) || rc=1

# --- vacuity guards: the gate must be looking at the real repo ---------------
real_out=$("$GATE" 2>&1)
real_rc=$?
if [[ "$real_rc" -ne 0 ]]; then
  fail "the repo does not pass its own state-vocabulary gate (exit $real_rc): $real_out"
else
  echo "  PASS: the real repo passes its own gate"
fi

# The real vocabulary must parse, and hold MORE than the threshold — otherwise
# every case above could be green while the repo scan matched by construction
# nothing at all.
# shellcheck source=../state-vocabulary-lint.sh
. "$GATE"
real_vocab=$(state_vocab_read "$STATE_VOCAB_SOURCE") || real_vocab=""
real_count=$(printf '%s' "$real_vocab" | grep -c .)
if (( real_count <= STATE_VOCAB_MIN_NAMED )); then
  fail "the real vocabulary parsed $real_count value(s) from $STATE_VOCAB_SOURCE; the gate cannot mean anything at or below its threshold of $STATE_VOCAB_MIN_NAMED"
else
  echo "  PASS: the real vocabulary parses ($real_count values, threshold $STATE_VOCAB_MIN_NAMED)"
fi

# …and the scan must actually FIND things in the real repo. Every site is
# waived today, so "0 site(s)" would still exit 0 — which is exactly what a
# matcher that silently stopped matching would produce. Absence of a finding
# and inability to look must not print the same thing.
real_sites=$(printf '%s' "$real_out" | sed -n 's/.*; \([0-9][0-9]*\) site(s).*/\1/p')
if [[ -z "$real_sites" ]]; then
  fail "could not read a site count out of the gate's own summary line: $real_out"
elif (( real_sites == 0 )); then
  fail "the real scan found 0 sites — the matcher has stopped matching, or the corpus is empty. It has never legitimately been 0 (57 at #1804)."
else
  echo "  PASS: the real scan is live ($real_sites site(s) found and waived)"
fi

[[ "$rc" -eq 0 ]] && echo "OK: state-vocabulary-lint_test — threshold, both waiver directions, vocabulary refusals, and the real repo all hold"
exit "$rc"
