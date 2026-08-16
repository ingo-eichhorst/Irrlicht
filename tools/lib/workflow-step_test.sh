#!/usr/bin/env bash
# workflow-step_test.sh — the tests for tools/lib/workflow-step.sh, the one
# implementation that answers "which shell does the runner give THIS step, and
# what is in it" for every extract-and-execute harness in tools/lib/ (#1650).
#
# ---------------------------------------------------------------------------
# What is being asserted, and why each obligation exists
#
# The defect this library removes was a PREMISE. Four harnesses, three
# workflow comments and three AGENTS.md bullets stated as fact that a step
# declaring no `shell:` runs under `bash --noprofile --norc -eo pipefail`. It
# does not — that is what `shell: bash` gives; the default is `bash -e {0}`,
# measured off run 31960152598's own step header. So the harnesses were
# supplying `pipefail` to bodies production runs without, each while saying in
# its header that running the body under anything else "would grade a different
# program".
#
# The obligations, in order:
#
#   1. the derivation is right for every declaration shape, driven against a
#      COMMITTED fixture corpus (testdata/workflow-step/) rather than against
#      strings built in the test — one file per shape, so the mutation evidence
#      outlives this PR the way tools/lib/testdata/posix-lint/'s does.
#   2. every way of NOT being able to answer is a loud refusal naming what
#      could not be done, never a fallback to the default. A harness handed a
#      plausible `bash -e` for a step that no longer exists would go on to
#      grade an empty body, which exits 0 and reads as a clean run.
#   3. the body is extracted verbatim, including lines that look like YAML
#      structure — and a `shell:` INSIDE a body does not change the answer.
#   4. the two invocations really do grade the same body differently. This is
#      the vacuity guard for the whole library: if they did not, deriving would
#      be ceremony, and a derivation that always answered `bash -e` would look
#      exactly as correct as one that reads the file.
#   5. the derivation FOLLOWS a workflow that changes. A real workflow is
#      copied, a `shell:` declaration is inserted and then removed, and the
#      answer has to move both ways. That is the mutation AGENTS.md asks of
#      anything a change adds, committed here rather than described in a PR
#      body that nothing re-runs.
#   6. the five real steps this repo harnesses resolve to what the runner
#      really gives them. That is the existence check the brief asks for: a
#      harness (or a workflow comment) still spelling its invocation by hand —
#      swift-suite_test.sh does, correctly, for macos-swift.yml's two
#      `shell: bash` steps — cannot silently stop matching, because this file
#      goes red and names the file if that declaration is ever dropped.
set -uo pipefail

REPO_ROOT=$(git rev-parse --show-toplevel)
cd "$REPO_ROOT" || { echo "FAIL: workflow-step_test — cannot cd to $REPO_ROOT" >&2; exit 1; }

# A missing tool is a hard failure, not a skip: exiting 0 here would read as a
# PASS to the runner that drives this file, so it would go green having
# asserted nothing — the failure mode this whole issue family is made of.
need() { command -v "$1" >/dev/null 2>&1 || { echo "FAIL: workflow-step_test — $1 not found" >&2; exit 1; }; }
need git
need awk
need sed
need mktemp
need bash

LIB=tools/lib/workflow-step.sh
[[ -f "$LIB" ]] || { echo "FAIL: workflow-step_test — $LIB not found" >&2; exit 1; }
# shellcheck source=workflow-step.sh
. "$LIB"

DATA=tools/lib/testdata/workflow-step
[[ -d "$DATA" ]] || { echo "FAIL: workflow-step_test — the fixture corpus $DATA is missing" >&2; exit 1; }

TMP=$(mktemp -d -t workflow-step) || exit 1
trap 'rm -rf "$TMP"' EXIT

rc=0
pass() { echo "  PASS: $1"; return 0; }
fail() { echo "  FAIL: $1 — expected [$2] got [$3]" >&2; rc=1; return 0; }
flat() { echo "$1" | tr '\n' ' '; }

# want_shell <fixture> <step> <expected argv>
want_shell() {
  local got st
  got=$(workflow_step_shell "$DATA/$1" "$2" 2>&1); st=$?
  if [[ "$st" -ne 0 ]]; then
    fail "$1 :: $2" "$3" "refused($st): $(flat "$got")"
  elif [[ "$got" == "$3" ]]; then
    pass "$1 :: $2 -> $3"
  else
    fail "$1 :: $2" "$3" "$got"
  fi
  return 0
}

# want_refusal <label> <needle> <cmd...> — status 2 AND a message naming why.
# A bare non-zero is refused: "it failed" and "it refused FOR THIS REASON" are
# different claims, and only the second is what a caller reads.
want_refusal() {
  local label="$1" needle="$2"; shift 2
  local got st
  got=$("$@" 2>&1); st=$?
  if [[ "$st" -ne 2 ]]; then
    fail "$label" "status 2 and a message naming: $needle" "status $st :: $(flat "$got")"
    return 0
  fi
  case "$got" in
    *"$needle"*) pass "$label (refused 2, naming: $needle)" ;;
    *) fail "$label" "a refusal naming: $needle" "$(flat "$got")" ;;
  esac
  return 0
}

# ---------------------------------------------------------------------------
# Obligation 1 — every declaration shape, off the committed corpus.
echo "== the derivation, per declaration shape (testdata/workflow-step/) =="
DEFAULT='bash -e'
BASH='bash --noprofile --norc -e -o pipefail'

want_shell no-shell.yml                 'Plain step'                  "$DEFAULT"
want_shell step-shell-bash.yml          'Declaring step'              "$BASH"
# ...and the step BESIDE it in the same job is unaffected. A derivation that
# leaked one step's declaration onto its neighbours would satisfy every other
# row here.
want_shell step-shell-bash.yml          'Plain step'                  "$DEFAULT"
want_shell shell-after-run.yml          'Declaring step'              "$BASH"
want_shell job-defaults.yml             'Inheriting step'             "$BASH"
want_shell job-defaults-after-steps.yml 'Inheriting step'             "$BASH"
want_shell job-defaults-no-shell.yml    'Plain step'                  "$DEFAULT"
want_shell workflow-defaults.yml        'Inheriting step'             "$BASH"
want_shell two-jobs.yml                 'Step in the strict job'      "$BASH"
want_shell two-jobs.yml                 'Step in the loose job'       "$DEFAULT"
want_shell shell-sh.yml                 'Posix step'                  'sh -e'

# ---------------------------------------------------------------------------
# Obligation 2 — the refusals.
echo ""
echo "== everything it cannot answer is a refusal, never a default =="
want_refusal "a workflow file that does not exist" "is not a readable file" \
  workflow_step_shell "$DATA/no-such-workflow.yml" 'Plain step'
want_refusal "a step name no step carries" "has no step named" \
  workflow_step_shell "$DATA/no-shell.yml" 'Not a step in there'
want_refusal "a name carried by TWO steps" "steps named" \
  workflow_step_shell "$DATA/duplicate-names.yml" 'Checkout code'
want_refusal "a shell keyword this library does not model" "does not model" \
  workflow_step_shell "$DATA/shell-unmodelled.yml" 'Powershell step'
want_refusal "a custom shell template" "does not model" \
  workflow_step_shell "$DATA/shell-custom-template.yml" 'Templated step'
want_refusal "a \`uses:\` step has no body to extract" "no \`run: |\` block" \
  workflow_step_body "$DATA/no-run-block.yml" 'Uses step'
want_refusal "a single-line \`run:\` is not modelled either" "no \`run: |\` block" \
  workflow_step_body "$DATA/no-run-block.yml" 'Single-line run step'
want_refusal "the body of a step that does not exist" "has no step named" \
  workflow_step_body "$DATA/no-shell.yml" 'Not a step in there'

# ...and the refusals are not the whole answer set: a shape that IS modelled
# still comes back. Without this, a library that refused everything would
# satisfy every arm above.
if body=$(workflow_step_body "$DATA/no-shell.yml" 'Plain step' 2>&1); then
  pass "...while a modelled step still yields its body"
else
  fail "a modelled step still yields its body" "the body" "refused: $(flat "$body")"
fi

# ---------------------------------------------------------------------------
# Obligation 3 — the body, verbatim, including lines that look like structure.
echo ""
echo "== the body is extracted verbatim =="
body=$(workflow_step_body "$DATA/body-lookalikes.yml" 'Tricky body')
want_line() {
  case $'\n'"$body"$'\n' in
    *$'\n'"$1"$'\n'*) pass "the body carries: $1" ;;
    *) fail "the body carries: $1" "that line, dedented to column 0" "$(flat "$body")" ;;
  esac
  return 0
}
want_line '# a comment inside the body'
want_line 'violations=""'
want_line 'violations="$violations  - name: not a step"$'"'"'\n'"'"''
want_line 'echo "shell: pwsh"'
want_line 'if true; then'
want_line '  echo indented'      # relative indentation survives
want_line 'fi'
want_line 'echo after a blank line'
# The body must stop at the step boundary, or a harness would execute the NEXT
# step's lines as part of this one.
case "$body" in
  *'echo two'*) fail "the body stops at the next step" "no 'echo two'" "$(flat "$body")" ;;
  *) pass "...and stops at the next step" ;;
esac
# A `shell:` line INSIDE a body is text, not a declaration. A parser that read
# structure out of block scalars would answer `pwsh` here and refuse.
want_shell body-lookalikes.yml 'Tricky body' "$DEFAULT"
want_shell body-lookalikes.yml 'The step after it' "$BASH"

# ---------------------------------------------------------------------------
# Obligation 4 — the two invocations grade the same body DIFFERENTLY, and in
# which direction.
#
# This is the whole reason the library exists rather than a comment fix. The
# probe is the shape any step is one edit away from growing:
#
#     out=$(false | cat)      # the left-hand command failed; `cat` did not
#
# Under `bash -e` — what a step declaring no `shell:` really gets — the
# pipeline's status is `cat`'s 0, the assignment succeeds, and the body sails
# past a command that failed. Under `--noprofile --norc -e -o pipefail`, the
# same line reports `false`'s 1 and errexit aborts the body there.
#
# So a harness supplying pipefail to a body production runs without grades that
# body as SAFE while production swallows the failure: a false green, in the one
# direction that matters. Measured here on every run rather than argued.
echo ""
echo "== the two invocations really do grade the same body differently =="
cat >"$TMP/pipefail-probe.sh" <<'PROBE'
out=$(false | cat)
echo "REACHED"
PROBE
read -r -a argv_default <<<"$DEFAULT"
read -r -a argv_bash <<<"$BASH"
d_out=$("${argv_default[@]}" "$TMP/pipefail-probe.sh" 2>&1); d_st=$?
b_out=$("${argv_bash[@]}" "$TMP/pipefail-probe.sh" 2>&1); b_st=$?
if [[ "$d_st" -eq 0 && "$d_out" == *REACHED* ]]; then
  pass "under \`$DEFAULT\` a mid-pipeline failure is SWALLOWED (exit 0, body continues)"
else
  fail "under \`$DEFAULT\` a mid-pipeline failure is swallowed" "exit 0 and REACHED" "exit $d_st :: $(flat "$d_out")"
fi
if [[ "$b_st" -ne 0 && "$b_out" != *REACHED* ]]; then
  pass "under \`$BASH\` the same body ABORTS there (exit $b_st, nothing after it runs)"
else
  fail "under \`$BASH\` the same body aborts" "a non-zero exit and no REACHED" "exit $b_st :: $(flat "$b_out")"
fi

# ---------------------------------------------------------------------------
# Obligation 5 — the derivation FOLLOWS the workflow, both ways.
#
# A copy of a REAL workflow, mutated. Both directions are driven, because a
# derivation hard-wired to `bash -e` passes the removal and only the insertion
# catches it, and one hard-wired to the pipefail spelling passes the insertion
# and only the removal catches it.
#
# The mutation is asserted to have APPLIED before its effect is read (#1390:
# a mutation harness whose mutation silently no-ops reports the unmutated
# result as evidence).
echo ""
echo "== the derivation follows a step that gains or loses \`shell:\` =="
REAL=.github/workflows/ars.yml
REALSTEP='Commit badge update'
if [[ ! -f "$REAL" ]]; then
  fail "$REAL is readable" "the workflow file" "not found — the mutation could not run"
else
  want_shell_file() { # <file> <step> <expected> <label>
    local got st
    got=$(workflow_step_shell "$1" "$2" 2>&1); st=$?
    if [[ "$st" -eq 0 && "$got" == "$3" ]]; then pass "$4 -> $3"
    else fail "$4" "$3" "status $st :: $(flat "$got")"; fi
    return 0
  }
  cp "$REAL" "$TMP/mutant.yml"
  want_shell_file "$TMP/mutant.yml" "$REALSTEP" "$DEFAULT" "the unmutated copy"

  # Insert `shell: bash` immediately after the step's own `- name:` line, at
  # the indentation a sibling key uses.
  awk -v want="$REALSTEP" '
    { print }
    $0 ~ /^[[:space:]]*-[[:space:]]+name:[[:space:]]*/ {
      n = $0; sub(/^[[:space:]]*-[[:space:]]+name:[[:space:]]*/, "", n)
      if (n == want) { ind = match($0, /[^ ]/); printf "%*sshell: bash\n", ind + 1, "" }
    }
  ' "$TMP/mutant.yml" >"$TMP/mutant-shell.yml"
  if ! grep -q '^ *shell: bash$' "$TMP/mutant-shell.yml"; then
    fail "the \`shell: bash\` insertion applied" "a mutated copy carrying it" "the awk inserted nothing — the mutation below measured the unmutated file"
  else
    pass "the \`shell: bash\` insertion applied to the copy"
    want_shell_file "$TMP/mutant-shell.yml" "$REALSTEP" "$BASH" "...and the step that GAINED \`shell: bash\`"
  fi

  # ...and back: removing it returns the answer to the default. Driven on the
  # mutated copy so the removal has something to remove.
  grep -v '^ *shell: bash$' "$TMP/mutant-shell.yml" >"$TMP/mutant-removed.yml"
  if grep -q '^ *shell: bash$' "$TMP/mutant-removed.yml"; then
    fail "the \`shell: bash\` removal applied" "a copy without it" "it is still there — the arm below measured the wrong file"
  else
    pass "the \`shell: bash\` removal applied to the copy"
    want_shell_file "$TMP/mutant-removed.yml" "$REALSTEP" "$DEFAULT" "...and the step that LOST it again"
  fi

  # The same, one level up: a JOB that gains a `defaults: run: shell:`.
  awk '
    { print }
    $0 ~ /^[[:space:]]*runs-on:/ && !done {
      ind = match($0, /[^ ]/)
      printf "%*sdefaults:\n", ind - 1 + 1, ""
      printf "%*srun:\n",      ind + 1 + 1, ""
      printf "%*sshell: bash\n", ind + 3 + 1, ""
      done = 1
    }
  ' "$TMP/mutant.yml" >"$TMP/mutant-jobdefaults.yml"
  if ! grep -q '^ *defaults:$' "$TMP/mutant-jobdefaults.yml"; then
    fail "the job-level \`defaults:\` insertion applied" "a mutated copy carrying it" "the awk inserted nothing"
  else
    pass "the job-level \`defaults:\` insertion applied to the copy"
    want_shell_file "$TMP/mutant-jobdefaults.yml" "$REALSTEP" "$BASH" "...and a step whose JOB gained \`defaults: run: shell: bash\`"
  fi
fi

# ---------------------------------------------------------------------------
# Obligation 6 — the real steps this repo harnesses.
#
# Not a second copy of the mapping: these rows are read FROM the workflows by
# the same code the harnesses use, so they cannot state a shell the derivation
# does not produce. What they pin is that each harnessed step still EXISTS
# under the name its harness looks up, and which side of the pipefail split it
# is on — including macos-swift.yml's two `shell: bash` steps, whose invocation
# swift-suite_test.sh still spells by hand (correctly). If that declaration is
# ever dropped, this file names the file rather than swift-suite_test.sh
# quietly grading under the wrong shell.
echo ""
echo "== the real harnessed steps =="
real_row() { # <workflow> <step> <expected>
  local got st
  got=$(workflow_step_shell "$1" "$2" 2>&1); st=$?
  if [[ "$st" -eq 0 && "$got" == "$3" ]]; then
    pass "$1 :: $2 -> $3"
  else
    fail "$1 :: $2" "$3" "status $st :: $(flat "$got")"
  fi
  return 0
}
real_row .github/workflows/ars.yml                        'Commit badge update'                         "$DEFAULT"
real_row .github/workflows/test.yml                       'Test the shared shell libs'                  "$DEFAULT"
real_row .github/workflows/replaydata-deletion-guard.yml  'Detect deletions of load-bearing replaydata' "$DEFAULT"
real_row .github/workflows/macos-swift.yml                'Test (bounded, streamed under a pty)'        "$BASH"
real_row .github/workflows/macos-swift.yml                "Collect the skipped suites' pixels"          "$BASH"

# ---------------------------------------------------------------------------
# ...and preflight's `tools` gate has to FIRE on a diff touching this library,
# its corpus, or any workflow it reads — or under --changed (the pre-push
# hook's path) every assertion above is skipped on precisely the commit that
# can break it. That is #1591's, #1629's, #1639's, #1641's and #1645's shape,
# each fixed by widening this same trigger. The regex is EXTRACTED and matched
# rather than string-compared, so this is a behavioural assertion and not a
# lock on one spelling of an alternation.
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
    for probe in tools/lib/workflow-step.sh tools/lib/workflow-step_test.sh \
                 "$DATA/no-shell.yml" .github/workflows/macos-swift.yml; do
      if printf '%s\n' "$probe" | grep -qE "$tools_re"; then
        pass "...it fires on a diff touching $probe"
      else
        fail "the tools gate fires on a diff touching $probe" "a match" "no match against: $tools_re"
      fi
    done
    if printf '%s\n' core/domain/session.go | grep -qE "$tools_re"; then
      fail "the tools-gate trigger still scopes" "no match for core/domain/session.go" "it matches everything: $tools_re"
    else
      pass "...and still does not fire on an unrelated core/ file"
    fi
  fi
fi

echo ""
if [[ "$rc" -eq 0 ]]; then
  echo "workflow-step_test: ALL PASS"
else
  echo "workflow-step_test: FAILURES" >&2
fi
exit "$rc"
