#!/usr/bin/env bash
# ref-exists.sh — answer whether ONE named branch exists on a remote, with an
# exit status that keeps "absent" and "could not look" apart.
#
# WHY THIS EXISTS (#2022). During the wave-1 fleet run on 2026-09-20 the
# orchestrator checked whether an agent's push had landed with
#
#     git ls-remote --heads origin | grep 2005
#
# and told the agent its branch was on the remote. It was not. The digits
# "2005" had matched inside another branch's 40-hex object id. A substring
# search over `ls-remote` output answers "do these characters appear anywhere
# in this table", which is a different question from "does this ref exist",
# and the two answers diverge exactly when an object id carries the digits.
#
# `grep -w` narrows it without closing it, and the difference was MEASURED
# rather than reasoned about — the first draft of this comment claimed the
# opposite and tools/lib/ref-exists_test.sh failed it. Against an object id,
# `-w` is safe: a four-digit run inside a 40-hex id always has a hex digit on
# at least one side, and a hex digit is a word character. Against a branch
# NAME it is not: `refs/heads/feat/1999-backport-2005-fix` puts the digits
# between two hyphens, which are not word characters, so `grep -w 2005`
# matches a branch belonging to a different ticket. Both rows live in
# tools/lib/testdata/ref-exists/git-stub.sh and both directions are asserted.
#
# That measurement is why ir:exec section 2 keeps its `grep -w "<N>"`: at that
# point the branch slug is unknown, a pattern search is the only option, and
# the object-id shape cannot fool it. This file is for every check that
# already knows the branch it is asking about.
#
# It is a file rather than a documented one-liner because a one-liner in skill
# prose cannot be executed, so nothing re-runs it.
#
# Usage:
#   tools/lib/ref-exists.sh <remote> <branch>
#   tools/lib/ref-exists.sh origin feat/2005-pi-provider-model-change
#
# Sourced form, which defines the function and runs nothing:
#   . tools/lib/ref-exists.sh
#   ref_exists origin feat/2005-pi-provider-model-change
#
# Exit codes:
#   0  the branch exists on the remote
#   1  the branch does not exist on the remote
#   2  REFUSAL — could not look. Wrong argument count, an empty argument, a
#      branch name carrying a glob character, or git failing to reach the
#      remote. Absence of a ref and inability to ask must never produce the
#      same status (AGENTS.md: "A verification mechanism must fail loudly when
#      it cannot run").
#
# The glob refusal is not defensive padding. `git ls-remote --exit-code
# --heads origin 'refs/heads/*'` exits 0 whenever the remote has ANY branch,
# so a caller that passed an unexpanded variable would read "exists" for a
# branch nobody pushed — the same false green this file was written to end.
set -uo pipefail

# ref_exists <remote> <branch>
ref_exists() {
  if [ "$#" -ne 2 ]; then
    echo "REFUSE: ref-exists — need exactly <remote> <branch>, got $#" >&2
    return 2
  fi

  local remote="$1" branch="$2"

  if [ -z "$remote" ] || [ -z "$branch" ]; then
    echo "REFUSE: ref-exists — empty remote or branch name" >&2
    return 2
  fi

  case "$branch" in
    *'*'* | *'?'* | *'['*)
      echo "REFUSE: ref-exists — '$branch' carries a glob character; this check answers one exact ref, never a pattern" >&2
      return 2
      ;;
  esac

  local out status
  # Bounded with git's own low-speed knobs rather than timeout(1), which is
  # not on a stock macOS (AGENTS.md and docs/ci-gates.md both say so). Under
  # `--budget`, an unbounded round trip is charged to the whole gate.
  out=$(git -c http.lowSpeedLimit=1000 -c http.lowSpeedTime=10 \
    ls-remote --exit-code --heads "$remote" "refs/heads/$branch" 2>&1)
  status=$?

  case "$status" in
    0)
      echo "OK: ref-exists — $remote has refs/heads/$branch"
      return 0
      ;;
    2)
      echo "FAIL: ref-exists — $remote has no refs/heads/$branch" >&2
      return 1
      ;;
    *)
      echo "REFUSE: ref-exists — could not ask $remote about refs/heads/$branch (git exit $status): $out" >&2
      return 2
      ;;
  esac
}

# Only run the CLI form when executed directly — sourcing (the test file does
# `. tools/lib/ref-exists.sh`) must define the function and return control,
# never run the check or exit the caller's shell.
if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
  ref_exists "$@"
  exit $?
fi
