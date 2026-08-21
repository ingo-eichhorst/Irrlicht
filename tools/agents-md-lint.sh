#!/usr/bin/env bash
# agents-md-lint.sh — refuse when AGENTS.md exceeds its line-count budget.
#
# AGENTS.md is pulled into EVERY agent session's context by CLAUDE.md's
# `@AGENTS.md` import — a whole-file, force-injected pull, unlike a plain
# markdown link, which only loads on demand. #1742 split a 2571-line AGENTS.md
# (dense, incident-laden prose — every paragraph typically a specific
# issue/measurement/rationale) down to a lean index, moving the deep detail
# verbatim into topic-scoped docs/*.md files that AGENTS.md now points at with
# ordinary relative links rather than a second `@`-import, so a session pays
# for that detail only when the task at hand actually follows the link. This
# script is the tripwire that keeps AGENTS.md from silently regrowing past the
# budget one "just one more paragraph" edit at a time — the fate the file was
# just rescued from.
#
# Usage:
#   tools/agents-md-lint.sh [path-to-AGENTS.md]   # defaults to the repo's own
#
# Exit 0 and print OK when the file is at or under the budget. Exit 1 and name
# the actual count and the limit when it is over — an ordinary finding, not a
# refusal, because the check has a clear answer, it just doesn't like it. Exit
# 2 (a REFUSAL, not a finding) when the file cannot be read or produces no
# parseable line count at all: per this repo's testing philosophy, a check
# that cannot read its input checks MORE, never less, so an unreadable target
# must not read as a quiet pass.
set -uo pipefail

# A separate variable, not a literal buried in the comparison below, so the
# CLI wrapper and the function tools/lib/agents-md-lint_test.sh sources
# (`. tools/agents-md-lint.sh`) cannot silently disagree about the number.
AGENTS_MD_LINE_LIMIT=400

# agents_md_line_limit_check <path> <limit>
# Returns 0 (pass), 1 (over budget) or 2 (refusal — could not measure).
agents_md_line_limit_check() {
  local path="$1"
  local limit="$2"

  if [[ ! -r "$path" ]]; then
    echo "REFUSE: agents-md-lint — cannot read $path" >&2
    return 2
  fi

  local count
  count=$(wc -l < "$path" 2>/dev/null | tr -d '[:space:]')
  if [[ -z "$count" || ! "$count" =~ ^[0-9]+$ ]]; then
    echo "REFUSE: agents-md-lint — wc produced no usable line count for $path" >&2
    return 2
  fi

  if (( count > limit )); then
    echo "FAIL: $path is $count lines — over the $limit-line budget (#1742)." \
         "Move the new content into a topic-scoped docs/*.md file and" \
         "reference it from AGENTS.md with a plain relative markdown link" \
         "(never another @-import — that would force-inject it every session" \
         "the same way this budget exists to prevent)." >&2
    return 1
  fi

  echo "OK: agents-md-lint — $path is $count lines (limit $limit)"
  return 0
}

# Only run the CLI form when executed directly — sourcing (the test file
# does `. tools/agents-md-lint.sh`) must define the function and the limit
# and return control, never run the check or exit the caller's shell.
if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
  REPO_ROOT=$(git rev-parse --show-toplevel 2>/dev/null) || {
    echo "REFUSE: agents-md-lint — not inside a git repository" >&2
    exit 2
  }
  target="${1:-$REPO_ROOT/AGENTS.md}"
  agents_md_line_limit_check "$target" "$AGENTS_MD_LINE_LIMIT"
  exit $?
fi
