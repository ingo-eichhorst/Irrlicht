#!/usr/bin/env bash
# ars-badge-push_test.sh — the lock on .github/workflows/ars.yml's
# "Commit badge update" step (#1641).
#
# ---------------------------------------------------------------------------
# What is being asserted, and why each obligation exists
#
# The defect: the step's push-retry loop was
#
#     for i in 1 2 3 4 5; do
#       git pull --rebase origin main && git push && break
#       echo "Push attempt $i failed, retrying..."
#       sleep $((i * 3))
#     done
#
# The loop's last statement is `sleep`, which succeeds. So when all five
# attempts failed the loop simply ended, `sleep`'s 0 became the step's status,
# and the step, the job and the badge check all went GREEN with the badge
# unpushed. GitHub's implicit `-e` does not save it: `A && B && break` is an
# `&&` list, and errexit is suppressed for every command in such a list except
# the one following the final `&&` — which is exactly what MAKES this a retry
# rather than a single attempt. The suppression is wanted; what was missing is
# anything that then READS the exhausted-retries case.
#
# The obligations, in order:
#
#   1. the defect is re-MEASURED, not described. The pre-fix loop is emitted
#      verbatim and run under the shell GitHub actually uses, so "five failures
#      exit 0" is a fact on every run rather than a sentence in a merged PR
#      body. It doubles as the vacuity guard: if bash ever stopped behaving
#      that way, the fix would be protecting nothing and every arm below would
#      pass for the wrong reason.
#   2. the real step — extracted from the real workflow file and EXECUTED —
#      fails when every attempt fails, and says what did not happen. This is
#      deliberately behavioural rather than a text scan: a scan pins one
#      spelling of a guard, where running the block pins the property.
#   3. the retry is still a retry. A "fix" that dropped the loop, or that
#      stopped suppressing errexit inside the `&&` list, also satisfies
#      obligation 2 — it just gives up after one attempt. So a run whose third
#      attempt succeeds must exit 0 AND show that the first two were retried.
#   4. the clean paths still pass: nothing staged, and a first-attempt push.
#
# Deliberately NOT a general workflow linter, and the measurement is the
# honest part. Over this repo's 19 multi-line `run:` blocks, a rule keyed on
# "the block's last statement is echo/sleep/cat/printf" flags 6 and MISSES
# THIS ONE — ars.yml's retry loop is nested inside an if/else, so the block's
# last line is `fi` — while 4 of the 6 it does flag are correct code. A rule
# keyed on `|| true` flags 2 and also misses this one. A rule keyed on "the
# block contains a loop" flags 3, of which 1 is this defect. No candidate rule
# both catches the subject and stays quiet on the correct blocks, so a
# linter's green would claim coverage it does not have. This is a lock on the
# ONE call site that exists — the same conclusion, reached the same way, as
# #1629's scan in swift-suite_test.sh.
set -uo pipefail

REPO_ROOT=$(git rev-parse --show-toplevel)
cd "$REPO_ROOT" || { echo "FAIL: ars-badge-push_test — cannot cd to $REPO_ROOT" >&2; exit 1; }

# A missing tool is a hard failure, not a skip: exiting 0 here would read as a
# PASS to the runner that drives this file, so it would go green having
# asserted nothing — the failure mode this whole issue family is made of.
need() { command -v "$1" >/dev/null 2>&1 || { echo "FAIL: ars-badge-push_test — $1 not found" >&2; exit 1; }; }
need git
need mktemp
need awk
need sed
need grep
need bash

WF=.github/workflows/ars.yml
STEP='Commit badge update'

# The step's body AND the shell it runs under both come from the workflow file,
# through tools/lib/workflow-step.sh — see the invocation block below.
# shellcheck source=workflow-step.sh
. tools/lib/workflow-step.sh

TMP=$(mktemp -d -t ars-badge-push) || exit 1
trap 'rm -rf "$TMP"' EXIT

rc=0
pass() { echo "  PASS: $1"; return 0; }
fail() { echo "  FAIL: $1 — expected [$2] got [$3]" >&2; rc=1; return 0; }
flat() { echo "$1" | tr '\n' ' '; }

want_status() { # label want got out
  if [[ "$3" == "$2" ]]; then pass "$1 (exit $2)"; else fail "$1" "exit $2" "exit $3 :: $(flat "$4")"; fi
  return 0
}
want_contains() { # label needle haystack
  case "$3" in *"$2"*) pass "$1" ;; *) fail "$1" "output containing: $2" "$(flat "$3")" ;; esac
  return 0
}
want_absent() { # label needle haystack
  case "$3" in *"$2"*) fail "$1" "no output containing: $2" "$(flat "$3")" ;; *) pass "$1" ;; esac
  return 0
}

# ---------------------------------------------------------------------------
# The invocation, DERIVED from the workflow rather than spelled here (#1650).
#
# GitHub has two bash invocations and this file used to state the wrong one as
# fact: a step DECLARING `shell: bash` gets
# `bash --noprofile --norc -e -o pipefail {0}`, but a step declaring nothing —
# which is what ars.yml's steps do — gets `bash -e {0}`. No `--noprofile`, no
# `--norc`, and **no pipefail**; measured off run 31960152598's own step header.
# `-e` is what makes the errexit suppression inside the `&&` list observable at
# all and is present either way, so this file's obligations are unaffected —
# but supplying pipefail to a body production runs without is a FALSE GREEN in
# the direction that matters: a pipeline whose left-hand command fails is
# graded as an abort here and swallowed in CI. So the invocation is read out of
# the workflow, and a step that later gains `shell: bash` moves this harness
# with it. workflow-step.sh REFUSES rather than defaulting when it cannot find
# the step, which is why this is a hard exit and not a fallback.
if ! STEP_SHELL=$(workflow_step_shell "$WF" "$STEP"); then
  echo "FAIL: ars-badge-push_test — could not derive the shell $WF gives '$STEP' (refusal above); nothing below would have graded the real program" >&2
  exit 1
fi
read -r -a STEP_ARGV <<<"$STEP_SHELL"
echo "== $WF :: '$STEP' runs under \`$STEP_SHELL\` (derived) =="

# ---------------------------------------------------------------------------
# The harness: stubs, then a body, run under that shell.
#
# `git` and `sleep` are shell FUNCTIONS rather than PATH shims so the body's
# own lines survive byte-for-byte — in particular `sleep $((i * 3))`, which
# would otherwise cost 45 wall-clock seconds per exhausted-retry case.
# An unexpected git subcommand returns a loud, distinctive 99 instead of a
# quiet 0: a stub that silently answered "fine" to a call it did not model
# would make every arm below pass for a reason unrelated to its obligation.
stub_prelude() { # $1 = attempt the push first succeeds on (99 = never)
                 # $2 = status of `git diff --staged --quiet` (1 = changes staged)
  cat <<STUB
STUB_SUCCEED_ON=$1
STUB_STAGED=$2
STUB_PUSHES=0
git() {
  case "\$1" in
    config|add|commit) return 0 ;;
    diff)  return "\$STUB_STAGED" ;;
    pull)  return 0 ;;
    push)
      STUB_PUSHES=\$((STUB_PUSHES + 1))
      if [ "\$STUB_PUSHES" -ge "\$STUB_SUCCEED_ON" ]; then return 0; fi
      return 1 ;;
    *) echo "STUB: unmodelled call: git \$*" >&2; return 99 ;;
  esac
}
sleep() { :; }
STUB
}

# run_body <file-with-body> <succeed-on> <staged-status> -> sets OUT / ST
run_body() {
  local body="$1" script="$TMP/step.sh"
  { stub_prelude "$2" "$3"; cat "$body"; } >"$script"
  # No `set +e` guard around this: THIS file runs under `set -uo pipefail` and
  # deliberately not `-e`, so a non-zero inner status — which is the expected
  # outcome of half the arms below — is data, not an abort. Toggling errexit
  # here would leave it ON for the rest of the file, which is the
  # option-you-cannot-see family the sibling issues (#1633, #1635) are made of.
  OUT=$("${STEP_ARGV[@]}" "$script" 2>&1)
  ST=$?
  return 0
}

# ---------------------------------------------------------------------------
# Obligation 1 — the defect, re-measured on every run.
#
# The pre-#1641 loop, verbatim, wrapped in nothing. Committed here rather than
# quoted in an issue, per AGENTS.md: "a number which documents behaviour but is
# not produced by it drifts silently".
echo "== the pre-#1641 loop shape (the defect, re-measured) =="
cat >"$TMP/predecessor.sh" <<'OLD'
git commit -m "chore: update ARS badge [skip ci]"
for i in 1 2 3 4 5; do
  git pull --rebase origin main && git push && break
  echo "Push attempt $i failed, retrying..."
  sleep $((i * 3))
done
OLD
run_body "$TMP/predecessor.sh" 99 1
if [[ "$ST" -eq 0 ]]; then
  pass "the old loop still exits 0 after five failed pushes — the hazard is real"
else
  fail "the old loop exits 0 after five failed pushes (the hazard this pins)" \
       "exit 0" "exit $ST — the hazard is GONE, so re-derive the fix rather than trusting it :: $(flat "$OUT")"
fi
want_contains "...having really exhausted all five attempts" "Push attempt 5 failed" "$OUT"

# ---------------------------------------------------------------------------
# Obligations 2-4 — the REAL step, extracted from the REAL workflow and run.
echo ""
echo "== $WF: $STEP =="

if [[ ! -f "$WF" ]]; then
  fail "$WF is readable" "the workflow file" "not found — the step check could not run"
else
  # Extract the named step's `run: |` body and dedent it — through the same
  # library that derived the invocation above, so the shell this file grades
  # under and the body it grades cannot come from two different steps. Keyed on
  # the step NAME, so a body that moved within the file is still found and a
  # step that was renamed is a loud refusal rather than a silent zero-line body.
  if ! workflow_step_body "$WF" "$STEP" >"$TMP/step-body.sh"; then
    fail "the '$STEP' step body was extracted from $WF" \
         "the step's run: | body" "workflow-step refused (above) — the scan has gone blind, not the step clean"
    : >"$TMP/step-body.sh"
  fi

  # The extraction has to have found something. A scan that silently stopped
  # matching reads exactly like a workflow with no hazard in it, and every
  # arm below would then grade an empty file — which exits 0 and looks like a
  # clean push.
  lines=$(grep -cve '^[[:space:]]*$' "$TMP/step-body.sh")
  if [[ "${lines:-0}" -lt 5 ]]; then
    fail "the '$STEP' step body was extracted from $WF" \
         "at least 5 non-blank lines" "$lines — the scan has gone blind, not the step clean"
  else
    pass "extracted the '$STEP' step body from $WF ($lines non-blank lines)"

    # Obligation 2: every attempt fails.
    run_body "$TMP/step-body.sh" 99 1
    want_status "every push attempt fails" 1 "$ST" "$OUT"
    want_contains "...and the step says the badge was NOT pushed" "NOT pushed" "$OUT"
    want_contains "...as a workflow error annotation, so it surfaces on the run" "::error::" "$OUT"
    want_contains "...having really exhausted all five attempts" "Push attempt 5 failed" "$OUT"

    # Obligation 3: the retry is still a retry. This is the arm that a "fix"
    # dropping the loop — or removing the errexit suppression inside the `&&`
    # list, which would abort at the first failed pull — cannot satisfy.
    run_body "$TMP/step-body.sh" 3 1
    want_status "a push that succeeds on the third attempt" 0 "$ST" "$OUT"
    want_contains "...retried after the first failure" "Push attempt 1 failed" "$OUT"
    want_contains "...and after the second" "Push attempt 2 failed" "$OUT"
    want_absent "...without claiming the badge went unpushed" "NOT pushed" "$OUT"

    # Obligation 4: the clean paths.
    run_body "$TMP/step-body.sh" 1 1
    want_status "a push that succeeds first time" 0 "$ST" "$OUT"
    want_absent "...with no retry noise" "Push attempt" "$OUT"

    run_body "$TMP/step-body.sh" 99 0
    want_status "nothing staged to commit" 0 "$ST" "$OUT"
    want_contains "...and it says so" "No badge changes to commit" "$OUT"

    # `|| true` is the false fix for this family and is refused by name: it
    # would let the loop end without aborting while leaving `true`'s status
    # behind, so the exhausted case would read as 0 all over again (#1629
    # measured the same thing one level down, on a `$?` capture).
    #
    # Full-line comments are stripped first, and that is not tidiness: the
    # step's own comment EXPLAINS why `|| true` is not used, so a raw scan
    # fails on the sentence saying the right thing. Measured — it did.
    code=$(grep -v '^[[:space:]]*#' "$TMP/step-body.sh")
    want_absent "the step does not reach for \`|| true\`" "|| true" "$code"
    # ...and the strip must not have eaten the code, or the arm above is
    # vacuous: an empty haystack contains no needle.
    want_contains "...checked against the step's real code, not an empty strip" "git push" "$code"
  fi
fi

# ---------------------------------------------------------------------------
# ...and preflight's `tools` gate has to FIRE on a diff touching only ars.yml,
# or under --changed (the pre-push hook's path) every assertion above is
# skipped on precisely the commit that can break it. That is #1591's, #1629's
# and #1639's shape, each fixed by widening this same trigger. The regex is
# EXTRACTED and matched rather than string-compared, so this is a behavioural
# assertion and not a lock on one spelling of an alternation.
echo ""
echo "== tools/preflight.sh's \`tools\` trigger =="
PF=tools/preflight.sh
if [[ ! -f "$PF" ]]; then
  fail "$PF is readable" "the preflight script" "not found — the trigger check could not run"
else
  tools_re=$(grep -a "run_gate_scoped '\^tools/lib/" "$PF" \
             | sed -E "s/^[[:space:]]*run_gate_scoped '//; s/'[[:space:]]*\\\\?[[:space:]]*$//")
  if [[ -z "$tools_re" ]]; then
    fail "the tools-gate trigger regex could be read from $PF" \
         "one run_gate_scoped line starting ^tools/lib/" "no such line — the scan has gone blind, not the trigger wrong"
  else
    pass "read the tools-gate trigger regex from $PF"
    for probe in "$WF" tools/lib/ars-badge-push_test.sh; do
      if printf '%s\n' "$probe" | grep -qE "$tools_re"; then
        pass "...it fires on a diff touching $probe"
      else
        fail "the tools gate fires on a diff touching $probe" "a match" "no match against: $tools_re"
      fi
    done
    # The vacuity guard: a trigger that matched everything would satisfy both
    # probes above and scope nothing.
    if printf '%s\n' core/domain/session.go | grep -qE "$tools_re"; then
      fail "the tools-gate trigger still scopes" "no match for core/domain/session.go" "it matches everything: $tools_re"
    else
      pass "...and still does not fire on an unrelated core/ file"
    fi
  fi
fi

echo ""
if [[ "$rc" -eq 0 ]]; then
  echo "ars-badge-push_test: ALL PASS"
else
  echo "ars-badge-push_test: FAILURES" >&2
fi
exit "$rc"
