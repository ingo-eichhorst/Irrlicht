#!/usr/bin/env bash
# pr-exists.sh — answer whether ONE named branch has ever had a pull request,
# with an exit status that keeps "no PR" and "could not look" apart. The
# PR-level twin of tools/lib/ref-exists.sh.
#
# WHY THIS EXISTS (#2029). A branch can be committed and pushed and never
# turned into a PR, and nothing surfaces it. On 2026-09-22 two such branches
# sat on origin: `feat/1887-desktop-driver-stability`, 43 commits ahead of
# main for 16 days after its issue was closed, and
# `fix/dsh-watch-deadline-reconcile`, which held the only fix for a red main
# for about nine hours before anyone found it through `git worktree list`.
# ir:exec section 6 asserted that the REF landed (ref-exists.sh) and then ran
# `gh pr create`; a run that stopped between the two left exactly that state,
# and nothing asked the second question. This file asks it.
#
# It asks about one exact head, never a table. `gh pr list --head <branch>`
# filters on the exact head ref name — checked live on 2026-09-24 against this
# repo: `--head feat/2022` returned `[]` while `feat/2022-…` has a PR. So no
# page cap applies (the unfiltered `gh pr list --limit 1000` already returns
# 1000 rows here, i.e. a single page is truncated), and a head that is a
# prefix of another PR's head is not mistaken for it. That prefix row is
# asserted in tools/lib/pr-exists_test.sh, against the stub in
# tools/lib/testdata/pr-exists/gh-stub.sh, and again against the real gh.
#
# Any state counts: open, closed or merged. The question is "was this work
# ever made visible", not "is it still under review".
#
# The repository is whatever gh resolves for the current directory (the
# checkout's remote, or GH_REPO when set) — the same resolution every other
# `gh pr` call in the skills relies on.
#
# Usage:
#   tools/lib/pr-exists.sh <branch>
#   tools/lib/pr-exists.sh feat/2029-pr-exists-orphan-lint
#
# Sourced form, which defines the function and runs nothing:
#   . tools/lib/pr-exists.sh
#   pr_exists feat/2029-pr-exists-orphan-lint
#
# Exit codes:
#   0  at least one PR (any state) has exactly this head branch
#   1  no PR has ever had this head branch
#   2  REFUSAL — could not look. Wrong argument count, an empty branch name, a
#      branch name carrying a glob character, gh failing (auth, network, rate
#      limit), or gh printing something that is not a JSON array. Absence of a
#      PR and inability to ask must never produce the same status (AGENTS.md:
#      "A verification mechanism must fail loudly when it cannot run").
set -uo pipefail

# pr_exists <branch>
pr_exists() {
  if [ "$#" -ne 1 ]; then
    echo "REFUSE: pr-exists — need exactly <branch>, got $#" >&2
    return 2
  fi

  local branch="$1"

  if [ -z "$branch" ]; then
    echo "REFUSE: pr-exists — empty branch name" >&2
    return 2
  fi

  # Refused for the same reason ref-exists.sh refuses it: an unexpanded
  # variable or a pattern answers a question about many branches, and this
  # check answers one.
  case "$branch" in
    *'*'* | *'?'* | *'['*)
      echo "REFUSE: pr-exists — '$branch' carries a glob character; this check answers one exact head, never a pattern" >&2
      return 2
      ;;
  esac

  local out status count
  out=$(gh pr list --state all --head "$branch" --json number 2>&1)
  status=$?
  if [ "$status" -ne 0 ]; then
    echo "REFUSE: pr-exists — could not ask gh about head '$branch' (gh exit $status): $out" >&2
    return 2
  fi

  # jq decides the shape: anything that is not a JSON array is a refusal, never
  # an empty answer. `gh` printing a warning, an HTML error page or nothing at
  # all must not read as "no PR".
  count=$(printf '%s' "$out" | jq -e 'if type == "array" then length else error("not an array") end' 2>/dev/null)
  status=$?
  if [ "$status" -ne 0 ] || [ -z "$count" ]; then
    echo "REFUSE: pr-exists — gh answered with something that is not a JSON array for head '$branch': $out" >&2
    return 2
  fi

  if [ "$count" -gt 0 ]; then
    echo "OK: pr-exists — $count PR(s) have head $branch"
    return 0
  fi
  echo "FAIL: pr-exists — no PR has ever had head $branch" >&2
  return 1
}

# Only run the CLI form when executed directly — sourcing must define the
# function and return control, never run the check or exit the caller's shell.
if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
  pr_exists "$@"
  exit $?
fi
