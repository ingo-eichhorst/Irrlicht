#!/usr/bin/env bash
# ars-badge-push_test.sh — the lock on .github/workflows/ars.yml's badge job.
#
# The filename predates its scope. It was written for the "Commit badge update"
# step (#1641) and grew to cover "Run ARS scan" and "Extract and update ARS
# badge" (#1644) — the two steps that PRODUCE the badge that step pushes. One
# harness per workflow rather than one per step, because the three share a
# workflow file, a `workflow_step_shell` derivation, an assertion vocabulary
# and one preflight trigger; splitting them would duplicate all four and let
# the copies disagree, which is what tools/lib/workflow-step.sh exists to stop.
# Renaming the file was declined as pure churn against five commits of history.
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
# ---------------------------------------------------------------------------
# #1644 — the two steps ABOVE it, same job, same symptom
#
# The badge that step pushes is produced by "Run ARS scan" and "Extract and
# update ARS badge", and both used to answer "I could not look" with the same
# bytes as "there was nothing to do":
#
#     ars scan ./core --no-llm --badge > ars-output.txt 2>&1 || true
#     cat ars-output.txt
#
# `|| true` swallows any failure and the block's last statement is `cat`, which
# succeeds. `ars-output.txt` then holds an error, the extract step's `grep`
# matches nothing, its `if [ -n "$ARS_BADGE" ]` is skipped in silence, README is
# untouched, and "Commit badge update" reports `No badge changes to commit`.
# Three green steps and a badge frozen at its previous score.
#
# The obligations added for it:
#
#   5. the defect is re-MEASURED, not described: both pre-#1644 bodies are
#      emitted verbatim and run against a failing `ars`, and both still exit 0
#      with README unchanged. The permanent vacuity guard for 6-11 — if that
#      ever stopped reproducing, they would all pass for the wrong reason.
#   6. a FAILED scan is no longer green, and the step says the badge was not
#      updated. The scan's own output is still printed, on both paths, or the
#      failure has no diagnosis in the log.
#   7. a scan that SUCCEEDS is still green (the vacuity guard for 6 — a step
#      that failed unconditionally would satisfy 6 perfectly).
#   8. a NORMAL run still rewrites the badge: the real README.md, the real
#      extract body, a new score in, the new URL in the file and the old one
#      gone. The vacuity guard for 9-11, and the only arm that would notice the
#      whole step being replaced by `exit 1`.
#   9. output with NO badge line is a named failure — the third outcome the
#      issue is about, since it means `--badge` stopped emitting one rather
#      than that the score was unchanged.
#  10. a badge line carrying no extractable URL is its OWN named failure.
#  11. a rewrite that did not land in README.md is its OWN named failure. `sed`
#      exits 0 having matched nothing, so this is the same defect one level
#      down, and re-reading the file for the URL just written is what
#      distinguishes it from the legitimate no-op of an unchanged score.
#
#  9-11 additionally assert that each refusal does NOT print the other two's
#  wording. Three refusals that all fire together, or all print the same
#  sentence, would satisfy every arm above while leaving the operator exactly
#  where the `if` skips left them: a red step and no idea which thing broke.
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
SCAN_STEP='Run ARS scan'
EXTRACT_STEP='Extract and update ARS badge'

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
#
# Derived once per step, never once per file: the three steps are independent
# declarations and any one of them could gain a `shell:` on its own.
#
# `derive_or_die` writes to a GLOBAL rather than printing its answer, because
# the obvious `x=$(derive_or_die …)` spelling runs the function in a subshell,
# where its `exit 1` exits only that subshell and the caller carries on with an
# empty invocation — a refusal that does not refuse, in a file about exactly
# that.
DERIVED=
derive_or_die() { # <step-name> -> sets DERIVED, or exits
  if ! DERIVED=$(workflow_step_shell "$WF" "$1"); then
    echo "FAIL: ars-badge-push_test — could not derive the shell $WF gives '$1' (refusal above); nothing below would have graded the real program" >&2
    exit 1
  fi
}
use_shell() { read -r -a STEP_ARGV <<<"$1"; }

derive_or_die "$STEP";         STEP_SHELL="$DERIVED"
derive_or_die "$SCAN_STEP";    SCAN_SHELL="$DERIVED"
derive_or_die "$EXTRACT_STEP"; EXTRACT_SHELL="$DERIVED"
use_shell "$STEP_SHELL"
echo "== $WF: derived step invocations =="
echo "   '$STEP' -> \`$STEP_SHELL\`"
echo "   '$SCAN_STEP' -> \`$SCAN_SHELL\`"
echo "   '$EXTRACT_STEP' -> \`$EXTRACT_SHELL\`"

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
# #1644 — the two steps that PRODUCE the badge "Commit badge update" pushes.
#
# Every case runs in a throwaway directory holding exactly what the runner's
# workspace holds at that point (an `ars-output.txt`, a `README.md`), so the
# repo's real README.md is never written to. The one arm that grades the real
# README COPIES it in — that arm is the reason `sed`'s pattern is checked
# against the file it actually has to match rather than against a fixture
# written to match it.

want_file_contains() { # label needle file
  if grep -qF "$2" "$3" 2>/dev/null; then pass "$1"
  else fail "$1" "$3 containing: $2" "$(flat "$(cat "$3" 2>/dev/null)")"; fi
  return 0
}
want_file_absent() { # label needle file
  if grep -qF "$2" "$3" 2>/dev/null; then fail "$1" "$3 NOT containing: $2" "$(flat "$(cat "$3" 2>/dev/null)")"
  else pass "$1"; fi
  return 0
}

# The `ars` stub prints ./.ars-stdout and returns the status it was built with.
# A subcommand it does not model answers a loud, distinctive 99 naming the call,
# and a MISSING ./.ars-stdout a 98 — never a quiet 0, which is what would make
# every arm below pass for a reason unrelated to its obligation.
ars_stub() { # $1 = status the stub returns for `ars scan`
  cat <<STUB
ars() {
  case "\$1" in
    scan) cat ./.ars-stdout || { echo "STUB: no ./.ars-stdout in \$(pwd)" >&2; return 98; }
          return $1 ;;
    *)    echo "STUB: unmodelled call: ars \$*" >&2; return 99 ;;
  esac
}
STUB
}

# GNU vs BSD `sed -i`. ars.yml runs on ubuntu-latest, where `-i` takes no
# argument; this harness runs wherever the shell-lib suite runs, which includes
# test.yml's macos-latest runner and every developer's Mac, where BSD sed reads
# the SCRIPT as `-i`'s backup suffix. Measured on this machine:
# `sed -i "s|a|b|g" f` -> `sed: 1: "…": invalid command code f`, file unchanged.
# So `-i` is re-expressed portably over the REAL sed, preserving the script
# semantics this file grades — in particular that a script matching nothing
# rewrites the file with identical bytes and exits 0, which is obligation 11's
# whole subject. Any other `-i` form is a loud refusal, not a quiet 0.
sed_shim() {
  cat <<'STUB'
sed() {
  if [ "$1" = "-i" ]; then
    if [ "$#" -ne 3 ]; then echo "STUB: unmodelled sed -i form: sed $*" >&2; return 99; fi
    command sed "$2" "$3" >"$3.stub.new" || return $?
    command mv "$3.stub.new" "$3" || return $?
    return 0
  fi
  command sed "$@"
}
STUB
}

CASE=
new_case() { # <name> -> sets CASE to a fresh, empty directory
  CASE="$TMP/case-$1"
  rm -rf "$CASE"
  mkdir -p "$CASE" || { echo "FAIL: ars-badge-push_test — cannot create case dir $CASE" >&2; exit 1; }
  { ars_stub "${2:-0}"; sed_shim; } >"$CASE/.prelude"
}

run_step() { # <workdir> <shell-string> <body-file> -> sets OUT / ST
  local dir="$1" body="$3" script="$1/.step.sh"
  use_shell "$2"
  { cat "$dir/.prelude"; cat "$body"; } >"$script"
  # Same reasoning as run_body: no `set +e` toggle, because a non-zero inner
  # status is the EXPECTED outcome of most arms here and is data, not an abort.
  OUT=$(cd "$dir" && "${STEP_ARGV[@]}" .step.sh 2>&1)
  ST=$?
  return 0
}

extract_body() { # <step-name> <min-non-blank-lines> <outfile> -> 0 on success
  local name="$1" min="$2" out="$3" lines
  if ! workflow_step_body "$WF" "$name" >"$out"; then
    fail "the '$name' step body was extracted from $WF" \
         "the step's run: | body" "workflow-step refused (above) — the scan has gone blind, not the step clean"
    return 1
  fi
  lines=$(grep -cve '^[[:space:]]*$' "$out")
  if [[ "${lines:-0}" -lt "$min" ]]; then
    fail "the '$name' step body was extracted from $WF" \
         "at least $min non-blank lines" "$lines — the scan has gone blind, not the step clean"
    return 1
  fi
  pass "extracted the '$name' step body from $WF ($lines non-blank lines)"
  return 0
}

# The four refusals must be told apart by their WORDING, not only by a shared
# non-zero. Three refusals printing the same sentence would satisfy every
# status arm below and leave an operator exactly where the silent `if` skips
# left them: something is red, and no idea which thing broke.
P_SCANFAIL='ARS scan FAILED'
P_NOBADGE='carries no img.shields.io/badge/ARS line'
P_NOURL='could be read out of it'
P_NOWRITE='does not contain the badge URL this step just wrote'

SCAN_ERR='ars: command failed: no such subcommand'
NEW_URL='https://img.shields.io/badge/ARS-Agent--Ready%209.9%2F10-brightgreen'
badge_output() { # <url> -> a plausible `ars scan --badge` tail
  printf 'Badge\n----------------------------------------\n[![ARS](%s)](https://github.com/ingo-eichhorst/agent-readyness)\n' "$1"
}

# The real README is read, not assumed: if it carries no ARS badge URL there is
# nothing for this workflow to rewrite, and obligations 8 and 5 would both be
# grading a fixture written to match the pattern under test.
OLD_URL=$(grep -o 'https://img\.shields\.io/badge/ARS[^)]*' README.md | head -1 || echo "")

echo ""
echo "== $WF: $SCAN_STEP / $EXTRACT_STEP =="

if [[ -z "$OLD_URL" ]]; then
  fail "README.md carries an ARS badge URL for the badge job to rewrite" \
       "one https://img.shields.io/badge/ARS-… URL in README.md" \
       "none — either the badge was removed (this job then has nothing to do and should say so) or this scan has gone blind"
elif [[ "$OLD_URL" == "$NEW_URL" ]]; then
  fail "the fixture score differs from README's, or the rewrite arm is vacuous" \
       "a NEW_URL unlike README's current badge" "both are $OLD_URL"
else
  pass "read README.md's current badge URL ($OLD_URL)"

  # -------------------------------------------------------------------------
  # Obligation 5 — the pre-#1644 bodies, verbatim, re-measured on every run.
  #
  # Committed rather than quoted in the issue, per AGENTS.md: "a number which
  # documents behaviour but is not produced by it drifts silently". This is
  # also the vacuity guard for 6-11: if `|| true` + a trailing `cat` ever
  # stopped exiting 0, the fix would be protecting nothing.
  cat >"$TMP/pre1644-scan.sh" <<'OLD'
ars scan ./core --no-llm --badge > ars-output.txt 2>&1 || true
cat ars-output.txt
OLD
  cat >"$TMP/pre1644-extract.sh" <<'OLD'
ARS_BADGE=$(grep "img.shields.io/badge/ARS" ars-output.txt | head -1 || echo "")
echo "ARS Badge line: $ARS_BADGE"

if [ -n "$ARS_BADGE" ]; then
  ARS_URL=$(echo "$ARS_BADGE" | grep -o 'https://img\.shields\.io/badge/ARS[^)]*' | head -1 || echo "")
  echo "ARS URL: $ARS_URL"
  if [ -n "$ARS_URL" ]; then
    sed -i "s|https://img.shields.io/badge/ARS-[^)]*|${ARS_URL}|g" README.md
    echo "README updated with ARS badge URL"
  fi
fi
OLD

  echo ""
  echo "-- the pre-#1644 bodies (the defect, re-measured) --"
  new_case pre1644 1
  printf '%s\n' "$SCAN_ERR" >"$CASE/.ars-stdout"
  cp README.md "$CASE/README.md"
  run_step "$CASE" "$SCAN_SHELL" "$TMP/pre1644-scan.sh"
  if [[ "$ST" -eq 0 ]]; then
    pass "the old scan body still exits 0 with a failing \`ars\` — the hazard is real"
  else
    fail "the old scan body exits 0 with a failing \`ars\` (the hazard this pins)" \
         "exit 0" "exit $ST — the hazard is GONE, so re-derive the fix rather than trusting it :: $(flat "$OUT")"
  fi
  run_step "$CASE" "$EXTRACT_SHELL" "$TMP/pre1644-extract.sh"
  want_status "...and the old extract body over that error output" 0 "$ST" "$OUT"
  want_file_contains "...leaving README.md's badge exactly as it was" "$OLD_URL" "$CASE/README.md"

  # -------------------------------------------------------------------------
  # Obligations 6-7 — the REAL "Run ARS scan" step, extracted and executed.
  echo ""
  echo "-- $SCAN_STEP --"
  # A floor of 2, not of "however many lines the fixed step has": the pre-#1644
  # body was two lines, so a higher floor would report the mutation that
  # RESTORES it as "the scan has gone blind" rather than as obligation 6 going
  # red — a negative result for the wrong reason reads as coverage it is not.
  # `workflow_step_body` already refuses a zero-line body, which is the blindness
  # this floor exists on top of.
  if extract_body "$SCAN_STEP" 2 "$TMP/scan-body.sh"; then
    new_case scanfail 3
    printf '%s\n' "$SCAN_ERR" >"$CASE/.ars-stdout"
    run_step "$CASE" "$SCAN_SHELL" "$TMP/scan-body.sh"
    if [[ "$ST" -eq 0 ]]; then
      fail "a failing \`ars scan\` fails the step" "a non-zero exit" "exit 0 :: $(flat "$OUT")"
    else
      pass "a failing \`ars scan\` fails the step (exit $ST)"
    fi
    want_contains "...as a workflow error annotation, so it surfaces on the run" "::error::" "$OUT"
    want_contains "...naming the scan as what failed" "$P_SCANFAIL" "$OUT"
    want_contains "...and that the badge was therefore not updated" "NOT updated" "$OUT"
    # Without this the failure has no diagnosis anywhere: `ars`'s own message is
    # redirected into the file, so a step that exits 1 without printing it is a
    # red X over an empty log.
    want_contains "...with the scan's own output still in the log" "$SCAN_ERR" "$OUT"

    # Obligation 7, the vacuity guard: a step that failed unconditionally would
    # satisfy every arm above.
    new_case scanok 0
    badge_output "$NEW_URL" >"$CASE/.ars-stdout"
    run_step "$CASE" "$SCAN_SHELL" "$TMP/scan-body.sh"
    want_status "a succeeding \`ars scan\` still passes" 0 "$ST" "$OUT"
    want_absent "...with no error annotation" "::error::" "$OUT"
    want_file_contains "...having captured the scan output for the next step" "$NEW_URL" "$CASE/ars-output.txt"

    # `|| true` is the false fix for this family and is refused by name — it is
    # what the step used to carry. Comments are stripped first for the same
    # reason the push arm strips them: the workflow's prose explains why it is
    # not used, and a raw scan fails on the sentence saying the right thing.
    code=$(grep -v '^[[:space:]]*#' "$TMP/scan-body.sh")
    want_absent "the scan step does not reach for \`|| true\`" "|| true" "$code"
    want_contains "...checked against the step's real code, not an empty strip" "ars scan" "$code"
  fi

  # -------------------------------------------------------------------------
  # Obligations 8-11 — the REAL "Extract and update ARS badge" step.
  echo ""
  echo "-- $EXTRACT_STEP --"
  if extract_body "$EXTRACT_STEP" 8 "$TMP/extract-body.sh"; then
    # Obligation 8, the vacuity guard for 9-11: a fix that refuses everything
    # is indistinguishable from one that works until something still works.
    # Graded against the REAL README.md, so the `sed` pattern is checked
    # against the file it has to match.
    new_case extractok 0
    badge_output "$NEW_URL" >"$CASE/ars-output.txt"
    cp README.md "$CASE/README.md"
    run_step "$CASE" "$EXTRACT_SHELL" "$TMP/extract-body.sh"
    want_status "a normal run still updates the badge" 0 "$ST" "$OUT"
    want_contains "...and says so" "README updated with ARS badge URL" "$OUT"
    want_file_contains "...having written the new URL into README.md" "$NEW_URL" "$CASE/README.md"
    want_file_absent "...and replaced the old one rather than appending" "$OLD_URL" "$CASE/README.md"
    want_absent "...with no error annotation" "::error::" "$OUT"

    # Obligation 9 — output with no badge line at all.
    new_case nobadge 0
    printf '%s\n' "$SCAN_ERR" >"$CASE/ars-output.txt"
    cp README.md "$CASE/README.md"
    run_step "$CASE" "$EXTRACT_SHELL" "$TMP/extract-body.sh"
    if [[ "$ST" -eq 0 ]]; then
      fail "output with no badge line fails the step" "a non-zero exit" "exit 0 :: $(flat "$OUT")"
    else
      pass "output with no badge line fails the step (exit $ST)"
    fi
    want_contains "...as a workflow error annotation" "::error::" "$OUT"
    want_contains "...naming the missing badge line specifically" "$P_NOBADGE" "$OUT"
    want_contains "...and printing the output it could not read a badge out of" "$SCAN_ERR" "$OUT"
    want_absent "...not confused with the unreadable-URL refusal" "$P_NOURL" "$OUT"
    want_absent "...nor with the rewrite-did-not-land refusal" "$P_NOWRITE" "$OUT"
    want_file_contains "...leaving README.md's badge untouched" "$OLD_URL" "$CASE/README.md"

    # Obligation 10 — a badge line whose URL cannot be read. Same shape one
    # level down, and the issue asked for it by name.
    new_case nourl 0
    printf '[![ARS](img.shields.io/badge/ARS-unparseable)](x)\n' >"$CASE/ars-output.txt"
    cp README.md "$CASE/README.md"
    run_step "$CASE" "$EXTRACT_SHELL" "$TMP/extract-body.sh"
    if [[ "$ST" -eq 0 ]]; then
      fail "a badge line with no extractable URL fails the step" "a non-zero exit" "exit 0 :: $(flat "$OUT")"
    else
      pass "a badge line with no extractable URL fails the step (exit $ST)"
    fi
    want_contains "...naming the unreadable URL specifically" "$P_NOURL" "$OUT"
    want_absent "...not confused with the missing-badge-line refusal" "$P_NOBADGE" "$OUT"
    want_absent "...nor with the rewrite-did-not-land refusal" "$P_NOWRITE" "$OUT"
    want_file_contains "...leaving README.md's badge untouched" "$OLD_URL" "$CASE/README.md"

    # Obligation 11 — the rewrite that matched nothing. `sed` exits 0 having
    # matched nothing, so before #1644 this printed "README updated" over an
    # unchanged file and the push step then reported `No badge changes`.
    new_case nowrite 0
    badge_output "$NEW_URL" >"$CASE/ars-output.txt"
    printf '# Irrlicht\n\nA README carrying no ARS badge at all.\n' >"$CASE/README.md"
    run_step "$CASE" "$EXTRACT_SHELL" "$TMP/extract-body.sh"
    if [[ "$ST" -eq 0 ]]; then
      fail "a rewrite that matched nothing fails the step" "a non-zero exit" "exit 0 :: $(flat "$OUT")"
    else
      pass "a rewrite that matched nothing fails the step (exit $ST)"
    fi
    want_contains "...naming the unwritten README specifically" "$P_NOWRITE" "$OUT"
    want_absent "...and does NOT claim the README was updated" "README updated with ARS badge URL" "$OUT"
    want_absent "...not confused with the missing-badge-line refusal" "$P_NOBADGE" "$OUT"
    want_absent "...nor with the unreadable-URL refusal" "$P_NOURL" "$OUT"

    # The step's two `|| echo ""` are deliberate and stay — they keep an empty
    # capture readable so the named refusal above can report it, rather than
    # letting errexit abort with a line number. `|| true` is the different
    # thing, and the one this family keeps having to refuse.
    code=$(grep -v '^[[:space:]]*#' "$TMP/extract-body.sh")
    want_absent "the extract step does not reach for \`|| true\`" "|| true" "$code"
    want_contains "...checked against the step's real code, not an empty strip" "sed -i" "$code"
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
