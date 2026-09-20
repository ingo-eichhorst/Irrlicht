#!/usr/bin/env bash
# fleet-review-evidence.sh — decide whether a delegated review DEMONSTRABLY
# ran, by requiring one machine-readable line in the reviewer's hand-back.
#
# WHY THIS EXISTS (#2022). On #2002 during the wave-1 fleet run of
# 2026-09-20, both review subagents died on a session rate limit. The ir:exec
# agent was honest and handed back marked "Incomplete", but nothing
# mechanical would have caught it: a less careful agent marks the PR ready and
# the review gate is skipped in silence.
# `.claude/skills/ir:code-review/SKILL.md` names the same failure in its own
# words — a reply like "reviewed at medium effort, 3 findings reported"
# "delivers nothing to the caller, and worse, reads to it as a **clean**
# gate".
#
# WHY IT REQUIRES A LINE RATHER THAN READING PROSE. The first version of this
# file inferred the verdict from free text: an effort word anywhere, plus a
# category word anywhere, plus anything path-shaped. Review of #2022 broke it
# with three inputs, all re-run here as committed fixtures:
#
#   * "Folded the wip checkpoint into a conventional commit and pushed
#     tools/preflight.sh. Review: the subagent returned nothing (rate limit)."
#     — PROVEN, because "conventional" contains "convention".
#   * "Reviewed at high effort. I could not complete the correctness pass on
#     core/.../parser.go before the rate limit hit." — PROVEN, on a sentence
#     that says in words that the review did not finish.
#   * A one-line "3 findings reported" fixture passed only because it was one
#     bare line; one ordinary sentence naming a path flipped it to PROVEN.
#
# No amount of pattern tightening fixes that, because "did this run" is not
# recoverable from prose that says it did not. So the caller pins the shape
# instead, and this file checks the shape. ir:exec section 6 and ir:fleet both
# brief the reviewer to emit it.
#
# THE REQUIRED LINE, on its own line, anywhere in the hand-back:
#
#   review: effort=<low|medium|high|xhigh|max> findings=<N>
#
# PROVEN needs that line, and then:
#   * findings=0 — the literal words "no findings", which ir:code-review
#     section 5 already requires ("a silent report is indistinguishable from a
#     skipped gate");
#   * findings=N>0 — at least N occurrences of `category:` followed by one of
#     that skill's five values. `category` is matched as a whole word, which
#     is what "conventional commit" defeated.
#
# This asks "did it run", never "was it any good". Judging a review's quality
# is the orchestrator's own reading, and a script that pretended to do it
# would be the same false green in a new place.
#
# Usage:
#   tools/lib/fleet-review-evidence.sh <handback-file> [handback-file...]
#
# Sourced form, which defines the function and runs nothing:
#   . tools/lib/fleet-review-evidence.sh
#   fleet_review_evidence handback-2002.txt
#
# Exit codes:
#   0  every named hand-back is PROVEN
#   1  FINDING — at least one hand-back is UNPROVEN, an EMPTY file included.
#      An empty hand-back is a finding and not a refusal: the file was read,
#      and the answer is "this carries no evidence". That is the shape a
#      killed review agent leaves behind.
#   2  REFUSAL — could not look: no files named, or a named file is missing,
#      unreadable, or a directory.
set -uo pipefail

# The header line the caller briefs the reviewer to emit.
FLEET_REVIEW_HEADER_PATTERN='^[[:space:]]*review:[[:space:]]+effort=(low|medium|high|xhigh|max)[[:space:]]+findings=([0-9]+)[[:space:]]*$'
# `category:` as a whole word, followed by one of ir:code-review's five values.
FLEET_REVIEW_CATEGORY_PATTERN='(^|[^[:alnum:]_])category:[[:space:]]*(correctness|convention|test-coverage|efficiency|simplification)([^[:alnum:]_-]|$)'

# fleet_review_evidence <handback-file> [handback-file...]
fleet_review_evidence() {
  if [ "$#" -eq 0 ]; then
    echo "REFUSE: fleet-review-evidence — no files named" >&2
    return 2
  fi

  local refused=0 found=0
  local path text reason header declared seen

  for path in "$@"; do
    if [ -d "$path" ]; then
      echo "REFUSE: fleet-review-evidence — '$path' is a directory, not a file" >&2
      refused=1
      continue
    fi
    if [ ! -r "$path" ]; then
      echo "REFUSE: fleet-review-evidence — cannot read '$path'" >&2
      refused=1
      continue
    fi

    text=$(cat "$path")
    reason=""

    if [ -z "$text" ]; then
      reason="the hand-back is empty, which is what a killed review agent leaves"
    else
      # Matched case-insensitively, then normalised to lower case BEFORE the
      # count is read. Leaving the extraction case-sensitive let
      # "REVIEW: EFFORT=high FINDINGS=0" match the pattern, fail to parse, and
      # fall through to PROVEN with two `integer expression expected` errors on
      # stderr — a false green in the one checker that exists to stop them.
      # `-m1` rather than `| head -1`: one fork instead of two.
      header=$(grep -iEm1 "$FLEET_REVIEW_HEADER_PATTERN" <<<"$text" |
        tr '[:upper:]' '[:lower:]')
      if [ -z "$header" ]; then
        reason="no 'review: effort=<tier> findings=<N>' line; prose alone cannot show that a review ran"
      else
        declared=${header##*findings=}
        declared=${declared%%[![:digit:]]*}
        if [ -z "$declared" ]; then
          echo "REFUSE: fleet-review-evidence — matched a header line but could not read its count: $header" >&2
          refused=1
          continue
        fi
        if [ "$declared" -eq 0 ]; then
          grep -qi 'no findings' <<<"$text" ||
            reason="the line declares findings=0, but the hand-back never says 'no findings'"
        else
          seen=$(grep -ciE "$FLEET_REVIEW_CATEGORY_PATTERN" <<<"$text")
          if [ "$seen" -lt "$declared" ]; then
            reason="the line declares findings=$declared, but the hand-back carries only $seen 'category:' value"
          fi
        fi
      fi
    fi

    if [ -n "$reason" ]; then
      echo "UNPROVEN: $path — $reason"
      found=1
    else
      echo "PROVEN: $path — $header"
    fi
  done

  if [ "$refused" -eq 1 ]; then
    echo "REFUSE: fleet-review-evidence — at least one hand-back could not be read (see REFUSE lines)" >&2
    return 2
  fi
  if [ "$found" -eq 1 ]; then
    echo "FAIL: fleet-review-evidence — a review gate that did not demonstrably run is UNPROVEN, never clean (see UNPROVEN lines)" >&2
    return 1
  fi
  echo "OK: fleet-review-evidence — every hand-back carries its evidence: $*"
  return 0
}

# Only run the CLI form when executed directly — sourcing (the test file does
# `. tools/lib/fleet-review-evidence.sh`) must define the function and return
# control, never run the check or exit the caller's shell.
if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
  fleet_review_evidence "$@"
  exit $?
fi
