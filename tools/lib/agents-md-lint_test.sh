#!/usr/bin/env bash
# agents-md-lint_test.sh — mutation evidence for tools/agents-md-lint.sh's
# 400-line AGENTS.md budget (#1742).
#
# Per this repo's testing philosophy (AGENTS.md's Testing section /
# docs/testing-philosophy.md): a check a change *adds* has no "before the
# fix" to run red against, so it owes a deliberate mutation instead. The
# mutation here is "AGENTS.md ships one line over its budget," reproduced
# against committed fixtures (tools/lib/testdata/agents-md-lint/) rather than
# by actually growing the real AGENTS.md, so the evidence outlives this PR
# without permanently regressing the file the guard protects.
#
# Four cases, each pinned to the exit code the header documents (0 pass /
# 1 over-budget finding / 2 refusal):
#   under-limit.md (350 lines) — well under the budget: pass.
#   at-limit.md    (400 lines) — the boundary itself: pass, not a fencepost
#                                 off-by-one into "over".
#   over-limit.md  (401 lines) — one line past the boundary: the deliberate
#                                 mutation. Must FAIL, and name the actual
#                                 count (401) and the limit (400).
#   a missing path              — the file cannot be read at all: REFUSE
#                                 (exit 2, distinct from a real finding),
#                                 per "a validator that cannot parse its
#                                 input checks MORE, never less."
#
# Plus a vacuity guard: the real, current AGENTS.md must itself pass. Without
# it, every case above could be satisfied by a check wired to fixtures alone
# that never actually looks at the file it exists to protect.
set -uo pipefail

REPO_ROOT=$(git rev-parse --show-toplevel)
cd "$REPO_ROOT" || { echo "FAIL: cannot cd to repo root $REPO_ROOT" >&2; exit 1; }

# shellcheck source=../agents-md-lint.sh
. tools/agents-md-lint.sh   # defines agents_md_line_limit_check + AGENTS_MD_LINE_LIMIT

FIXTURE_DIR=tools/lib/testdata/agents-md-lint
rc=0
fail() { echo "FAIL: $1" >&2; rc=1; }

# assert_rc <label> <expected-rc> <path> [must-contain-in-stderr]
assert_rc() {
  local label="$1" want="$2" path="$3" want_text="${4:-}"
  local out got
  out=$(agents_md_line_limit_check "$path" "$AGENTS_MD_LINE_LIMIT" 2>&1)
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

# --- boundary and mutation cases --------------------------------------------
assert_rc "350-line fixture is well under budget"   0 "$FIXTURE_DIR/under-limit.md"
assert_rc "400-line fixture sits exactly AT budget" 0 "$FIXTURE_DIR/at-limit.md"
# The deliberate mutation: one line over the boundary must go red, and the
# failure must name both the actual count and the limit — not just "FAIL".
assert_rc "401-line fixture is one line OVER budget (the mutation)" \
  1 "$FIXTURE_DIR/over-limit.md" "401 lines"
assert_rc "401-line fixture's failure names the limit too" \
  1 "$FIXTURE_DIR/over-limit.md" "400-line budget"

# --- boundary case: a file with no trailing newline on its last line -------
# `wc -l` counts NEWLINE CHARACTERS, so a 401-line file whose last line has no
# trailing newline undercounts to 400 and would silently PASS — found in
# review of #1742 itself. This fixture is 401 lines by `awk 'END{print NR}'`
# (which counts a trailing unterminated line as a record) and 400 by `wc -l`;
# the check must use the former and FAIL here, the same as the ordinary
# over-limit.md case.
assert_rc "401 lines with no trailing newline still FAILS (wc -l would undercount to 400)" \
  1 "$FIXTURE_DIR/over-limit-no-trailing-newline.md" "401 lines"

# --- refusal case: cannot be read at all ------------------------------------
assert_rc "a path that does not exist is a REFUSAL, not a silent pass" \
  2 "$FIXTURE_DIR/does-not-exist.md" "cannot read"

# --- vacuity guard: the real file this whole check exists to protect -------
# Without this, every case above could pass while the check itself was wired
# to nothing but fixtures. AGENTS.md is expected to already be within budget
# — that is the fix #1742 shipped — so this must currently PASS.
assert_rc "the repo's real, current AGENTS.md passes its own budget" \
  0 "AGENTS.md"

# --- red-before-green record: the file this guard was written against -----
# #1742's pre-fix AGENTS.md was 2625 lines (dense incident-laden prose, no
# topic split). It is not committed as a fixture — regrowing a 2625-line
# copy of AGENTS.md into testdata would defeat the point of the split it
# documents — but the number is pinned here so the red-before-green proof
# this repo's testing philosophy requires stays checkable without relying on
# a PR description nothing re-runs: reconstruct it with
# `git show <pre-#1742-sha>:AGENTS.md | wc -l` and it must report a count
# over 400 — this test's own logic, applied to that historical count,
# predicts FAIL. That reconstruction is not repeated here as a live check
# because it would pin this test to one historical commit forever; the
# fixture-based over-limit.md case above is the permanent, generic version
# of the same mutation.

[[ "$rc" -eq 0 ]] && echo "OK: agents-md-lint_test — 400-line AGENTS.md budget check holds at the boundary and the mutation"
exit "$rc"
