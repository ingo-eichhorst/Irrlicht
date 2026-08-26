#!/usr/bin/env bash
# rebase-conflict-check.sh — scan specific files for an unresolved git
# conflict marker (`^<<<<<<< `) left behind by a `git rebase --continue` that
# staged a hunk before the marker was actually resolved.
#
# WHY THIS EXISTS (#1824, instance 3). During #1824's own implementing batch,
# an agent's `git add -A` staged a conflicted file that still carried
# `<<<<<<<`/`=======`/`>>>>>>>` markers; `git rebase --continue` reported
# success (git only cares that every conflicted path was `git add`ed, never
# that the content is actually resolved), and the defect was caught only
# because `go build` happened to choke on it downstream — nothing in
# `.claude/skills/ir:exec/SKILL.md`'s playbook checked directly. This is the
# mechanical check that playbook step now runs.
#
# Usage:
#   tools/lib/rebase-conflict-check.sh <file> [file...]
#
# Deliberately takes an EXPLICIT file list rather than scanning the whole
# repo. replaydata/agents/aider/scenarios/**/transcript.md commits aider's own
# SEARCH/REPLACE diff format verbatim, which also starts a line with
# `<<<<<<< ` (`<<<<<<< SEARCH`) — a real, intentional recording fixture, not a
# leftover conflict (confirmed: `git grep -n '^<<<<<<<' -- .` finds dozens of
# these under replaydata/agents/aider/ on a clean checkout). A blind
# repo-wide scan would flag those on line one of this check's life. Passing
# `git diff --name-only origin/main...HEAD` — the files THIS branch actually
# touches, which Phase 5 already computes this exact list for its own
# collision probe — keeps the check honest without special-casing aider's
# format:
#
#   git diff --name-only -z origin/main...HEAD | \
#     xargs -0 -r bash tools/lib/rebase-conflict-check.sh
#
# Reuses tools/skill-lint.sh's own conflict-marker semantics
# (`^<<<<<<<([ \t]|$)` in that file's awk, matching a marker with or without
# the trailing ref name, e.g. `<<<<<<< HEAD`, but not `<<<<<<<foo`) rather
# than inventing a second definition of "conflict marker." The character
# class is spelled `[[:space:]]` here, not `[ \t]`: awk's own `\t` escape is
# always tab, but measured against this platform's actual `grep -E` (BSD
# grep, invoked the way this script's own test does — bash executing a
# script, not the interactive shell's `ugrep`-backed `grep` function/alias),
# a literal backslash-t in an ERE does NOT match a tab; `[[:space:]]` does,
# portably.
#
# Exit codes:
#   0  clean — none of the given files contain the marker
#   1  FINDING — at least one does; each is printed as `file:line:text`
#   2  REFUSAL — no files were named, or a named file exists but could not be
#      read (a real I/O problem — NOT "this file was deleted by the diff,"
#      which is a silent no-op below: a deleted file trivially has no marker
#      to find, and `git diff --name-only` lists deletions too)
set -uo pipefail

CONFLICT_MARKER_PATTERN='^<<<<<<<([[:space:]]|$)'

# rebase_conflict_check <file> [file...]
rebase_conflict_check() {
  if [ $# -eq 0 ]; then
    echo "REFUSE: rebase-conflict-check — no files named" >&2
    return 2
  fi

  local p found=0 refused=0 out
  for p in "$@"; do
    # Not an error: git diff --name-only lists deleted files too, and a
    # deleted file has nothing left to scan.
    [ -e "$p" ] || continue

    if [ -d "$p" ]; then
      echo "REFUSE: rebase-conflict-check — '$p' is a directory, not a file" >&2
      refused=1
      continue
    fi
    if [ ! -r "$p" ]; then
      echo "REFUSE: rebase-conflict-check — cannot read '$p'" >&2
      refused=1
      continue
    fi

    out=$(grep -nE "$CONFLICT_MARKER_PATTERN" "$p" 2>/dev/null || true)
    if [ -n "$out" ]; then
      found=1
      while IFS= read -r hit; do
        echo "CONFLICT: $p:$hit"
      done <<<"$out"
    fi
  done

  if [ "$refused" -eq 1 ]; then
    return 2
  fi
  if [ "$found" -eq 1 ]; then
    echo "FAIL: rebase-conflict-check — unresolved conflict marker(s) survived the rebase (see CONFLICT lines above)" >&2
    return 1
  fi

  echo "OK: rebase-conflict-check — no conflict markers in: $*"
  return 0
}

# Only run the CLI form when executed directly — sourcing (the test file does
# `. tools/lib/rebase-conflict-check.sh`) must define the function and return
# control, never run the check or exit the caller's shell.
if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
  rebase_conflict_check "$@"
  exit $?
fi
