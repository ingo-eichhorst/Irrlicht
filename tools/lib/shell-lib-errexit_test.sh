#!/usr/bin/env bash
# shell-lib-errexit_test.sh — a tripwire over EVERY sourced shell library in
# tools/lib/: each function it can drive must, under a caller's `set -e`,
# return what its library documents rather than aborting the caller, and must
# leave the caller's shell options byte-identical.
#
# ---------------------------------------------------------------------------
# Why this exists
#
# Three issues in a row were the same defect in a different file. #1629:
# .github/workflows/macos-swift.yml read `$?` on a line GitHub's implicit `-e`
# never reached. #1633: swift_suite_run's post-timeout kill sequence aborted
# before `return 124`, so a hang was reported as an ordinary failure. #1635:
# budget_run's backgrounded child inherited errexit and died before writing the
# status file that is its "it finished" signal, so a gate that failed instantly
# was reported as a TIMEOUT that burned the whole remaining budget, with
# BUDGET_LAST_TIMED_OUT never set.
#
# Every one of them was found by hand, and the sibling in each case was found
# only because someone happened to look. These files are SOURCED, so they run
# with whatever options their caller has, and none of them said anything about
# that. This file is the "covered by existing rather than by remembering" half:
# a fourth library, or a fifth function in an existing one, is graded by being
# discovered, not by anyone wiring it.
#
# ---------------------------------------------------------------------------
# Three things about the shape are load-bearing
#
# 1. BARE STATEMENT POSITION IS THE WHOLE POINT. Every other calling shape —
#    `if f`, `f || x`, `x=$(f)`, `f && y` — makes bash ignore errexit for the
#    entire function body, and, measured here on bash 3.2.57, for a subshell
#    the body backgrounds as well. So the hazard cannot fire in those shapes:
#    the very call that returned 1 instead of 3 against the unfixed
#    gate-budget.sh returns 3 when written as `budget_run … || rc=$?`. A lock
#    written that way is green against a broken library. Obligation (a) below
#    therefore emits the call as the LAST statement of an inner script and
#    reads the inner shell's exit status — errexit exits with the status of
#    the command that tripped it, so in that shape the shell's status IS what
#    the function returned, whether it returned or was aborted.
#
# 2. `$(set +o)` CANNOT BE USED TO CAPTURE THE OPTIONS. bash 3.2 — /bin/bash
#    on macOS, and what this suite runs under — reports errexit and nounset as
#    OFF inside a command substitution no matter what the parent has. Measured
#    on 3.2.57, same shell, same instant:
#
#        inside $( ) : set +o errexit    redirected to a file : set -o errexit
#
#    A probe built the obvious way is therefore byte-identical before and after
#    any leak and can never fail. Obligation (b) uses `set +o > "$file"`, which
#    is fork-free (`set` is a builtin) and covers every `set -o` option, where
#    `$-` has no flag character for pipefail. The committed fixture
#    `tw_fixture_leaks` is that probe's own guard: it leaks deliberately, and
#    obligation (b) is required to report it.
#
# 3. NO FUNCTION IS BLIND-CALLED. Some have side effects, some need a fixture,
#    one would run for a minute. Each is driven by a named recipe — a setup
#    line and a call line — and anything the tripwire cannot safely drive is
#    NAMED in TW_EXEMPT_KEYS with its reason. Both key sets are existence-
#    checked against the walk in both directions, so a recipe or an exemption
#    that stops naming a real function is a failure rather than a silent no-op
#    (the idiom core/'s nilTolerant / mustBeNonZero / applyWritesNoUserFile
#    already use). Silent skipping is the failure mode this whole issue family
#    is made of.
#
# The walk is `tools/lib/*.sh` minus `*_test.sh`. That glob does not recurse,
# so the deliberately-corrupt fixtures under testdata/ never enter it — the
# same split skill-lint.sh and posix-lint.sh already draw.
#
# ---------------------------------------------------------------------------
# Its own mutation evidence is committed, not described (AGENTS.md, #1479)
#
# tw_fixture_correct / tw_fixture_aborts / tw_fixture_leaks are driven through
# the same two obligations as the real libraries, with the correct one as the
# vacuity guard — an obligation that reported unconditionally would satisfy
# every mutation and read as excellent coverage. tw_bijection is self-tested
# against four synthetic key sets, and the discovery half is mutated for real:
# the walk is pointed at a scratch directory holding three of the four
# libraries, and every recipe of the missing one must be reported ORPHAN.

# SC2016 is file-level and is the design rather than an oversight: every
# recipe's setup and call line, and every line of the two probe builders, is
# shell SOURCE TEXT emitted verbatim into an inner script. `$TW_REPO`,
# `$TW_REACHED`, `$!` and `$?` inside those strings must be expanded by THAT
# shell, from the exported environment, not by this one. Nothing under
# tools/lib/ is linted by any gate today — checked: preflight.sh's posix gate
# keys on a `#!/bin/sh` shebang and runs `--shell=sh`, and no workflow runs a
# bash lint — so this is for whoever runs one by hand.
# shellcheck disable=SC2016

set -uo pipefail   # NOT -e: this file's whole subject is what -e does to a
                   # callee, and the assertions capture non-zero statuses.

REPO_ROOT=$(git rev-parse --show-toplevel)
cd "$REPO_ROOT" || { echo "FAIL: shell-lib-errexit_test — cannot cd to $REPO_ROOT" >&2; exit 1; }

# A missing tool is a hard failure, not a skip: exiting 0 here would read as a
# PASS to preflight's shell_lib_tests and to test.yml's loop, so the gate would
# go green having asserted nothing.
need() { command -v "$1" >/dev/null 2>&1 || { echo "FAIL: shell-lib-errexit_test — $1 not found" >&2; exit 1; }; }
need git
need grep
need mktemp
need jq      # gosec_report_check refuses (2) without it, which would fail that
             # row for an unrelated reason and read as this one.
need pgrep   # _budget_tree_pids / _swift_suite_descendants walk with it.
need diff    # obligation (b) IS a diff; without it every call reads as a leak.

LIBDIR="$REPO_ROOT/tools/lib"
TW_TMP=$(mktemp -d -t shell-lib-errexit) || exit 1
trap 'rm -rf "$TW_TMP"' EXIT
export TW_TMP

fails=0
pass() { echo "  PASS: $1"; return 0; }
fail() { echo "  FAIL: $1 — expected [$2] got [$3]"; fails=$((fails + 1)); return 0; }

# ---------------------------------------------------------------------------
# The reporting seam.
#
# The obligations below report through pass/fail like every other test here,
# except while TW_RECORDING is set — then a failure is appended to TW_RECORD
# instead. That is what lets the committed fixtures grade the obligations
# themselves: a self-test asserts that an obligation REPORTED, and names a
# fragment of its own failure message, because "something failed" and "THIS
# obligation failed" are different claims.
TW_RECORDING=""
TW_RECORD=""
tw_pass() { [[ -n "$TW_RECORDING" ]] && return 0; pass "$1"; }
tw_fail() {
  if [[ -n "$TW_RECORDING" ]]; then TW_RECORD="$TW_RECORD$1 — expected [$2] got [$3]"$'\n'; return 0; fi
  fail "$1" "$2" "$3"
}
tw_record_begin() { TW_RECORDING=1; TW_RECORD=""; }
tw_record_end()   { TW_RECORDING=""; }

# mustReport <label> <fragment> — the self-test verdict. A bare failure is
# refused: the record has to name a fragment of the obligation's own message.
mustReport() {
  if [[ -z "$TW_RECORD" ]]; then
    fail "$1" "the obligation to report" "it stayed silent"
    return 0
  fi
  case "$TW_RECORD" in
    *"$2"*) pass "$1" ;;
    *) fail "$1" "a report naming: $2" "$(echo "$TW_RECORD" | tr '\n' ' ')" ;;
  esac
  return 0
}
# mustBeSilent <label> — the vacuity guard's verdict.
mustBeSilent() {
  if [[ -z "$TW_RECORD" ]]; then pass "$1"
  else fail "$1" "silence" "$(echo "$TW_RECORD" | tr '\n' ' ')"; fi
  return 0
}

# ---------------------------------------------------------------------------
# Discovery
#
# tw_discover <dir> — one `lib.sh::func` line per function found. Lines
# beginning with `!` are refusals, not results: a library the scan read and
# found no function in, and a `function name {` declaration, which this scan
# does not understand. Both are hard failures rather than an empty result,
# because "found nothing" and "could not look" must never produce the same
# output.
tw_discover() {
  local dir="$1" f base names alt
  for f in "$dir"/*.sh; do
    [[ -e "$f" ]] || continue
    case "$f" in *_test.sh) continue ;; esac
    base=$(basename "$f")
    alt=$(grep -acE '^[[:space:]]*function[[:space:]]+[A-Za-z_]' "$f")
    if [[ "$alt" -gt 0 ]]; then
      echo "! $base declares $alt function(s) with the \`function name {\` spelling, which this scan does not understand — it would be walked and silently found empty"
    fi
    names=$(grep -aE '^[A-Za-z_][A-Za-z0-9_]*[[:space:]]*\(\)[[:space:]]*\{?[[:space:]]*$' "$f" | sed -E 's/[[:space:]]*\(\).*$//')
    if [[ -z "$names" ]]; then
      echo "! $base was walked and no function was found in it — the scan cannot read this file"
      continue
    fi
    while IFS= read -r n; do
      [[ -n "$n" ]] && echo "$base::$n"
    done <<<"$names"
  done
}

# tw_bijection <discovered> <recipe-keys> <exempt-keys> — one complaint per
# line, silence when the three agree. Checked in BOTH directions on purpose:
# the undriven half catches a function nobody wrote a recipe for, and the
# orphan halves catch a library that stopped being walked at all — the recipes
# then name nothing, which is the failure this whole file exists to prevent
# reading as coverage.
tw_bijection() {
  local discovered="$1" recipes="$2" exempt="$3" k
  while IFS= read -r k; do
    [[ -z "$k" ]] && continue
    case $'\n'"$recipes"$'\n' in *$'\n'"$k"$'\n'*) continue ;; esac
    case $'\n'"$exempt"$'\n' in *$'\n'"$k"$'\n'*) continue ;; esac
    echo "UNDRIVEN $k — no driving recipe and no entry in TW_EXEMPT_KEYS"
  done <<<"$discovered"
  while IFS= read -r k; do
    [[ -z "$k" ]] && continue
    case $'\n'"$discovered"$'\n' in *$'\n'"$k"$'\n'*) continue ;; esac
    echo "ORPHAN-RECIPE $k — a recipe naming no function the walk found"
  done <<<"$recipes"
  while IFS= read -r k; do
    [[ -z "$k" ]] && continue
    case $'\n'"$discovered"$'\n' in *$'\n'"$k"$'\n'*) continue ;; esac
    echo "ORPHAN-EXEMPTION $k — an exemption naming no function the walk found"
  done <<<"$exempt"
}

# ---------------------------------------------------------------------------
# The two obligations
#
# Both build an inner script and run it under `bash --noprofile --norc -e -o
# pipefail`. That is GitHub's `shell: bash` invocation, and it is chosen here as
# the STRICTEST caller any of these libraries has rather than as "the" step
# shell: these files are sourced from three places with three different option
# sets — macos-swift.yml's two steps (`shell: bash`, so exactly this),
# test.yml's step (no `shell:`, so `bash -e` — errexit only, no pipefail; that
# is #1650's correction, and this file used to imply the two were the same) and
# tools/preflight.sh (`set -uo pipefail`, no `-e` at all). The obligations here
# are about what `-e` does to a callee, and `-e` is in all three; the extra
# `pipefail` can only make a callee's own pipeline abort EARLIER, so the error
# runs toward a false accusation (loud) rather than a false pass (silent).
# A harness EXECUTING one named step's body derives its invocation instead —
# see tools/lib/workflow-step.sh — because there the direction reverses.
# `: > "$TW_REACHED"`
# is emitted immediately before the call, so an inner shell that died in its
# setup (a bad path, a source that failed) is reported as a probe that could
# not run rather than as the defect — those exit 1 too, which is byte-identical
# to several of the failures being graded.
export TW_REACHED="$TW_TMP/reached"
export TW_OPTS_BEFORE="$TW_TMP/opts.before"
export TW_OPTS_AFTER="$TW_TMP/opts.after"

# tw_obligation_status <label> <want> <setup> <call>
tw_obligation_status() {
  local label="$1" want="$2" setup="$3" call="$4" probe out got
  probe="$TW_TMP/probe.status.sh"
  rm -f "$TW_REACHED"
  printf '%s\n' 'set -uo pipefail' "$setup" ': > "$TW_REACHED"' "$call" >"$probe"
  out=$(bash --noprofile --norc -e -o pipefail "$probe" 2>&1); got=$?
  if [[ ! -e "$TW_REACHED" ]]; then
    tw_fail "$label: the probe never reached the call, so nothing was graded" \
            "a probe that ran its setup" "it printed: $(echo "$out" | tr '\n' ' ')"
    return 0
  fi
  if [[ "$got" == "$want" ]]; then
    tw_pass "$label: returned $want in a bare statement under the caller's -e"
  else
    tw_fail "$label: in a bare statement under \`bash --noprofile --norc -e -o pipefail\` it returned $got, not the documented $want — it aborted the caller instead of returning" \
            "$want" "$got ($(echo "$out" | tr '\n' ' '))"
  fi
  return 0
}

# tw_obligation_leak <label> <setup> <call>
#
# Read in a `|| rc=$?` position, which is the ONLY place it can be read: under
# -e a bare non-zero return aborts the caller, so there is no line after the
# call to take the second dump from. A `||` position still EXECUTES any `set`
# the body performs, so a library that turned an option off and forgot is
# exactly as visible there.
tw_obligation_leak() {
  local label="$1" setup="$2" call="$3" probe out
  probe="$TW_TMP/probe.leak.sh"
  rm -f "$TW_REACHED" "$TW_OPTS_BEFORE" "$TW_OPTS_AFTER"
  printf '%s\n' 'set -uo pipefail' "$setup" \
    'set +o > "$TW_OPTS_BEFORE"' ': > "$TW_REACHED"' \
    'tw_rc=0; { '"$call"'; } || tw_rc=$?' \
    'set +o > "$TW_OPTS_AFTER"' >"$probe"
  out=$(bash --noprofile --norc -e -o pipefail "$probe" 2>&1)
  if [[ ! -e "$TW_REACHED" || ! -s "$TW_OPTS_BEFORE" || ! -s "$TW_OPTS_AFTER" ]]; then
    tw_fail "$label: the option-leak probe recorded no before/after dump — it could not run" \
            "two non-empty dumps and a reached marker" "it printed: $(echo "$out" | tr '\n' ' ')"
    return 0
  fi
  if diff "$TW_OPTS_BEFORE" "$TW_OPTS_AFTER" >/dev/null 2>&1; then
    tw_pass "$label: left the caller's shell options byte-identical"
  else
    tw_fail "$label: LEAKED a shell option change back to its caller" "no diff" \
            "$(diff "$TW_OPTS_BEFORE" "$TW_OPTS_AFTER" | tr '\n' ' ')"
  fi
  return 0
}

# ---------------------------------------------------------------------------
# The driving recipes
#
# One row per (function, documented outcome). Fields: key, label, expected
# status, setup line, call line. Both lines are emitted VERBATIM into the inner
# script, so `$TW_REPO` and friends are expanded by that shell from the
# environment, never here.
#
# Expected statuses are chosen to be DISTINCTIVE wherever the function offers
# the choice — 0, 2, 3, 124. A recipe whose documented answer is 1 would be
# indistinguishable from an errexit abort, which also exits 1, so predicates
# are driven in the state where they answer true.
SEP=$'\034'
TW_ROWS=""
row() { TW_ROWS="$TW_ROWS$1$SEP$2$SEP$3$SEP$4$SEP$5"$'\n'; }

# A throwaway repo with an origin/main ref, so changed_files_vs_origin_main is
# driven against a baseline it can resolve without depending on this clone
# having fetched one (a CI checkout may not have).
TW_REPO="$TW_TMP/repo"; export TW_REPO
mkdir -p "$TW_REPO"
(
  cd "$TW_REPO" &&
  git init -q . &&
  git config user.email tripwire@example.invalid &&
  git config user.name tripwire &&
  git config commit.gpgsign false &&
  : > a.txt && git add a.txt && git commit -qm baseline &&
  git update-ref refs/remotes/origin/main HEAD
) >/dev/null 2>&1 || { echo "FAIL: shell-lib-errexit_test — could not build the scratch git repo" >&2; exit 1; }

# await_gone's third documented status, 1 (the subject survived to the
# deadline), is deliberately NOT driven here: a documented 1 is
# indistinguishable from an errexit abort, which is this file's own rule about
# distinctive statuses. That path is graded by tools/lib/await-gone_test.sh,
# which is not under `-e`; what is asserted here is the option property on the
# two statuses a caller can tell apart.
row 'await-gone.sh::await_gone_bound' 'await_gone_bound (an order of magnitude apart)' 0 \
    '. tools/lib/await-gone.sh' \
    'await_gone_bound 3 30'
row 'await-gone.sh::await_gone_bound' 'await_gone_bound (refuses a deadline near the lifetime)' 2 \
    '. tools/lib/await-gone.sh' \
    'await_gone_bound 15 30 2>/dev/null'
row 'await-gone.sh::await_gone' 'await_gone (a subject already gone)' 0 \
    '. tools/lib/await-gone.sh; tw_gone() { AWAIT_GONE_LOOKED=1; AWAIT_GONE_ALIVE=""; }' \
    'await_gone 3 30 tw_gone'
row 'await-gone.sh::await_gone' 'await_gone (refuses a predicate that could not look)' 2 \
    '. tools/lib/await-gone.sh; tw_blind() { AWAIT_GONE_LOOKED=0; AWAIT_GONE_ALIVE="pgrep is not on PATH"; }' \
    'await_gone 3 30 tw_blind 2>/dev/null'
row 'await-gone.sh::_await_gone_refuse' '_await_gone_refuse' 2 \
    '. tools/lib/await-gone.sh' \
    '_await_gone_refuse "driven by the tripwire" 2>/dev/null'

row 'changed-files.sh::changed_files_vs_origin_main' 'changed_files_vs_origin_main' 0 \
    '. tools/lib/changed-files.sh; cd "$TW_REPO"' \
    'changed_files_vs_origin_main >/dev/null'
row 'changed-files.sh::go_module_touched' 'go_module_touched' 0 \
    '. tools/lib/changed-files.sh' \
    'go_module_touched core "core/x.go"'
row 'changed-files.sh::web_tree_touched' 'web_tree_touched' 0 \
    '. tools/lib/changed-files.sh' \
    'web_tree_touched platforms/web "platforms/web/package-lock.json"'

row 'gate-budget.sh::budget_open' 'budget_open (refuses a non-numeric bound)' 2 \
    '. tools/lib/gate-budget.sh' \
    'budget_open 10m 2>/dev/null'
row 'gate-budget.sh::budget_is_bounded' 'budget_is_bounded' 0 \
    '. tools/lib/gate-budget.sh; budget_open 5' \
    'budget_is_bounded'
row 'gate-budget.sh::budget_remaining' 'budget_remaining' 0 \
    '. tools/lib/gate-budget.sh; budget_open 5' \
    'budget_remaining >/dev/null'
row 'gate-budget.sh::budget_exhausted' 'budget_exhausted' 0 \
    '. tools/lib/gate-budget.sh; budget_open 5; _BUDGET_STARTED_AT=$((SECONDS - 10))' \
    'budget_exhausted'
# Its documented contract is `return 0` unconditionally, and signalling a pid
# that is already gone is its NORMAL case — the second pass after SIGKILL, and
# every command that exited on its own. Measured: 1 against the unfixed library.
row 'gate-budget.sh::_budget_kill_tree' '_budget_kill_tree (already-dead pid)' 0 \
    '. tools/lib/gate-budget.sh; sh -c "exit 0" & tw_dead=$!; wait "$tw_dead" || :' \
    '_budget_kill_tree TERM "$tw_dead"'
# Driven on a pid whose `pgrep -P` finds NOTHING, which is the errexit-exposed
# half: pgrep exits 1 when a process has no children, and that is the ordinary
# case for the deepest pid of every walk. A pid WITH children is exercised by
# the timeout row below, which now reaches this function with a real tree under
# it (#1681).
row 'gate-budget.sh::_budget_tree_pids' '_budget_tree_pids (a pid with no children)' 0 \
    '. tools/lib/gate-budget.sh; sh -c "exit 0" & tw_leaf=$!; wait "$tw_leaf" || :' \
    '_budget_tree_pids "$tw_leaf" >/dev/null'
# Its other documented status, 1 (they are all gone), is deliberately NOT
# driven, for the reason this file states about await_gone's 1: a documented 1
# is indistinguishable from an errexit abort. The 0 arm is the one a caller can
# tell apart, and it is also the one the grace loop spins on.
row 'gate-budget.sh::_budget_any_alive' '_budget_any_alive (a pid that is alive)' 0 \
    '. tools/lib/gate-budget.sh' \
    '_budget_any_alive "$$"'
row 'gate-budget.sh::budget_run' 'budget_run (ordinary path)' 3 \
    '. tools/lib/gate-budget.sh; BUDGET_POLL_SECONDS=0.05' \
    'budget_run 20 bash -c "exit 3"'
row 'gate-budget.sh::budget_run' 'budget_run (timeout path)' 124 \
    '. tools/lib/gate-budget.sh; BUDGET_POLL_SECONDS=0.05; BUDGET_TERM_GRACE_SECONDS=1' \
    'budget_run 2 bash -c "sleep 60" 2>/dev/null'

row 'gosec-report.sh::gosec_report_check' 'gosec_report_check (a clean report)' 0 \
    '. tools/lib/gosec-report.sh' \
    'gosec_report_check tools/lib/testdata/gosec-report/clean.json tripwire >/dev/null 2>&1'

# rebase_conflict_check documents three statuses (0 clean, 1 FINDING, 2
# REFUSAL); only 0 and 2 are distinctive here, for the same reason
# await_gone's and gate-budget's own true-predicate statuses are (above): a
# documented 1 is indistinguishable from an errexit abort, which also exits
# 1. The FINDING path — and the exact `CONFLICT:` output it produces — is
# graded by its own tools/lib/rebase-conflict-check_test.sh, which is not
# under `-e`; what is asserted here is the errexit/option property on the two
# statuses a caller under `-e` can actually tell apart.
row 'rebase-conflict-check.sh::rebase_conflict_check' 'rebase_conflict_check (a clean file)' 0 \
    '. tools/lib/rebase-conflict-check.sh' \
    'rebase_conflict_check tools/lib/testdata/rebase-conflict-check/clean.txt >/dev/null'
row 'rebase-conflict-check.sh::rebase_conflict_check' 'rebase_conflict_check (refuses when no files are named)' 2 \
    '. tools/lib/rebase-conflict-check.sh' \
    'rebase_conflict_check 2>/dev/null'

# Driven against a THROWAWAY corpus, never against tools/lib itself: this
# function runs every `*_test.sh` it finds, and this file is one of them, so
# pointing it at the real directory would re-enter the whole suite (and this
# tripwire) once per row. Both statuses are distinctive — 0 for a corpus that
# passes, 2 for the refusal — so neither can be confused with an errexit abort.
row 'shell-lib-suite.sh::shell_lib_suite_run' 'shell_lib_suite_run (a corpus that all passes)' 0 \
    '. tools/lib/shell-lib-suite.sh; mkdir -p "$TW_TMP/suite-ok"; printf "exit 0\n" >"$TW_TMP/suite-ok/a_test.sh"' \
    'shell_lib_suite_run "$TW_TMP/suite-ok" >/dev/null'
row 'shell-lib-suite.sh::shell_lib_suite_run' 'shell_lib_suite_run (refuses an empty corpus)' 2 \
    '. tools/lib/shell-lib-suite.sh; mkdir -p "$TW_TMP/suite-empty"' \
    'shell_lib_suite_run "$TW_TMP/suite-empty" >/dev/null 2>&1'

# Driven against the committed fixture corpus rather than against a real
# workflow: these rows grade what the functions do to their CALLER under -e,
# and a fixture cannot be edited out from under them by an unrelated workflow
# change. Both statuses are distinctive — 0 for an answer, 2 for a refusal —
# so neither can be confused with an errexit abort, and the refusal rows are
# the load-bearing half here: refusing rather than defaulting is the whole
# contract (#1650), and a refusal that aborted its caller would take the
# harness down instead of being reported.
WS_DATA=tools/lib/testdata/workflow-step
row 'workflow-step.sh::workflow_step_shell' 'workflow_step_shell (a step declaring no shell:)' 0 \
    ". tools/lib/workflow-step.sh" \
    "workflow_step_shell $WS_DATA/no-shell.yml 'Plain step' >/dev/null"
row 'workflow-step.sh::workflow_step_shell' 'workflow_step_shell (refuses a step it cannot find)' 2 \
    ". tools/lib/workflow-step.sh" \
    "workflow_step_shell $WS_DATA/no-shell.yml 'No such step' >/dev/null 2>&1"
row 'workflow-step.sh::workflow_step_body' 'workflow_step_body (a step with a run: | block)' 0 \
    ". tools/lib/workflow-step.sh" \
    "workflow_step_body $WS_DATA/no-shell.yml 'Plain step' >/dev/null"
row 'workflow-step.sh::workflow_step_body' 'workflow_step_body (refuses a uses: step)' 2 \
    ". tools/lib/workflow-step.sh" \
    "workflow_step_body $WS_DATA/no-run-block.yml 'Uses step' >/dev/null 2>&1"
row 'workflow-step.sh::_workflow_step_record' '_workflow_step_record (refuses an unreadable file)' 2 \
    ". tools/lib/workflow-step.sh" \
    "_workflow_step_record $WS_DATA/no-such-file.yml 'Plain step' >/dev/null 2>&1"
row 'workflow-step.sh::_workflow_step_scan' '_workflow_step_scan' 0 \
    ". tools/lib/workflow-step.sh" \
    "_workflow_step_scan $WS_DATA/no-shell.yml 'Plain step' >/dev/null"
# The field readers are driven off a REAL record — `_workflow_step_scan`'s own
# output — because a hand-written prefix like `matches 1` is exactly what they
# now refuse: a record cut short reads fine as far as it goes, and its empty
# fields are indistinguishable from a step that declares no `shell:`. So the
# refusal rows below drive that prefix deliberately.
WS_REC=". tools/lib/workflow-step.sh; WSREC=\$(_workflow_step_scan $WS_DATA/no-shell.yml 'Plain step')"
row 'workflow-step.sh::_workflow_step_field' '_workflow_step_field' 0 \
    "$WS_REC" \
    '_workflow_step_field "$WSREC" matches >/dev/null'
row 'workflow-step.sh::_workflow_step_field' '_workflow_step_field (refuses a truncated record)' 2 \
    '. tools/lib/workflow-step.sh' \
    '_workflow_step_field "matches 1" matches >/dev/null 2>&1'
row 'workflow-step.sh::_workflow_step_field_of' '_workflow_step_field_of' 0 \
    "$WS_REC" \
    "_workflow_step_field_of \"\$WSREC\" matches $WS_DATA/no-shell.yml 'Plain step' >/dev/null"
row 'workflow-step.sh::_workflow_step_field_of' '_workflow_step_field_of (refuses a truncated record)' 2 \
    '. tools/lib/workflow-step.sh' \
    "_workflow_step_field_of 'matches 1' matches $WS_DATA/no-shell.yml 'Plain step' >/dev/null 2>&1"

row 'swift-suite.sh::swift_suite_completed' 'swift_suite_completed' 0 \
    '. tools/lib/swift-suite.sh' \
    'swift_suite_completed tools/lib/testdata/swift-suite/clean.log'
row 'swift-suite.sh::swift_suite_ran_tests' 'swift_suite_ran_tests' 0 \
    '. tools/lib/swift-suite.sh' \
    'swift_suite_ran_tests tools/lib/testdata/swift-suite/clean.log'
row 'swift-suite.sh::swift_suite_bundle_failed' 'swift_suite_bundle_failed' 0 \
    '. tools/lib/swift-suite.sh' \
    'swift_suite_bundle_failed tools/lib/testdata/swift-suite/bundle-failed.log'
row 'swift-suite.sh::swift_suite_last_test' 'swift_suite_last_test' 0 \
    '. tools/lib/swift-suite.sh' \
    'swift_suite_last_test tools/lib/testdata/swift-suite/clean.log >/dev/null'
row 'swift-suite.sh::_swift_suite_hung_headline' '_swift_suite_hung_headline' 0 \
    '. tools/lib/swift-suite.sh; SWIFT_SUITE_TIMEOUT=1' \
    '_swift_suite_hung_headline 2>/dev/null'
row 'swift-suite.sh::swift_suite_verdict' 'swift_suite_verdict (a clean run)' 0 \
    '. tools/lib/swift-suite.sh' \
    'swift_suite_verdict 0 tools/lib/testdata/swift-suite/clean.log >/dev/null 2>&1'
row 'swift-suite.sh::_swift_suite_descendants' '_swift_suite_descendants' 0 \
    '. tools/lib/swift-suite.sh' \
    '_swift_suite_descendants $$ >/dev/null'
# The timeout path of this one is #1633's own dedicated lock in
# swift-suite_test.sh, which drives a real hang through a pty; driving it here
# too would spend the same seconds twice for the same assertion.
row 'swift-suite.sh::swift_suite_run' 'swift_suite_run (ordinary path)' 3 \
    '. tools/lib/swift-suite.sh; SWIFT_SUITE_TIMEOUT=5' \
    'swift_suite_run "$TW_TMP/swift-suite-run.log" bash -c "exit 3" >/dev/null 2>&1'

# The real-home witness (#1669, #1670). Every recipe below drives a FIXTURE
# home under $TW_TMP through SWIFT_SUITE_WITNESS_HOME — never the real one,
# since planting a file in the developer's home to grade a guard against
# planting files in the developer's home is the incident, not a test of it.
#
# All five are driven at 0, and that is a limitation stated rather than papered
# over: each documents 1 for its failure paths, and a documented 1 is
# indistinguishable from an errexit abort, which is this file's own rule about
# distinctive statuses. Those paths are graded — eight of them, with their
# messages — by swift-suite_test.sh, which is not under `-e`; what is asserted
# here is the errexit/option property on the path a caller takes.
row 'swift-suite.sh::swift_suite_witness_home' 'swift_suite_witness_home' 0 \
    '. tools/lib/swift-suite.sh' \
    'swift_suite_witness_home >/dev/null'
row 'swift-suite.sh::_swift_suite_witness_slug' '_swift_suite_witness_slug' 0 \
    '. tools/lib/swift-suite.sh' \
    '_swift_suite_witness_slug Library/Preferences >/dev/null'
row 'swift-suite.sh::_swift_suite_witness_snapshot' '_swift_suite_witness_snapshot' 0 \
    '. tools/lib/swift-suite.sh; mkdir -p "$TW_TMP/wh-snap/Library/Preferences" "$TW_TMP/ws-snap"; : >"$TW_TMP/wh-snap/Library/Preferences/a.plist"; SWIFT_SUITE_WITNESS_HOME="$TW_TMP/wh-snap"' \
    '_swift_suite_witness_snapshot "$TW_TMP/ws-snap" before'
# The domain half (#1688) reaches an external reader, so the two entry points
# below and the five rows after them all answer through the COMMITTED stub —
# never the real defaults(1). Two reasons, and the second is the binding one: a
# row asserting 0 while riding on the developer's live io.irrlicht.app would be
# a coin flip (the running app rewrites SULastCheckTime on Sparkle's schedule),
# and the only way to make a REAL domain deterministic is to write it, which is
# the incident this half exists to report rather than to reproduce.
SS_DATA=tools/lib/testdata/swift-suite
DOM_SETUP=". tools/lib/swift-suite.sh; export SWIFT_SUITE_STUB_DIR=\"\$TW_TMP/dom\"; bash $SS_DATA/seed-domains.sh \"\$TW_TMP/dom\"; SWIFT_SUITE_WITNESS_DEFAULTS=$SS_DATA/defaults-stub.sh"

row 'swift-suite.sh::swift_suite_witness_before' 'swift_suite_witness_before' 0 \
    "$DOM_SETUP"'; mkdir -p "$TW_TMP/wh-before/Library/Preferences" "$TW_TMP/ws-before"; : >"$TW_TMP/wh-before/Library/Preferences/a.plist"; SWIFT_SUITE_WITNESS_HOME="$TW_TMP/wh-before"' \
    'swift_suite_witness_before "$TW_TMP/ws-before"'
row 'swift-suite.sh::swift_suite_witness_verdict' 'swift_suite_witness_verdict (nothing changed)' 0 \
    "$DOM_SETUP"'; mkdir -p "$TW_TMP/wh-verdict/Library/Preferences" "$TW_TMP/ws-verdict"; : >"$TW_TMP/wh-verdict/Library/Preferences/a.plist"; SWIFT_SUITE_WITNESS_HOME="$TW_TMP/wh-verdict"; swift_suite_witness_before "$TW_TMP/ws-verdict"' \
    'swift_suite_witness_verdict "$TW_TMP/ws-verdict" >/dev/null'

row 'swift-suite.sh::_swift_suite_domain_flatten' '_swift_suite_domain_flatten' 0 \
    "$DOM_SETUP" \
    '_swift_suite_domain_flatten < "$TW_TMP/dom/com.apple.dt.xctest.tool" >/dev/null'
row 'swift-suite.sh::_swift_suite_domain_state' '_swift_suite_domain_state' 0 \
    "$DOM_SETUP" \
    '_swift_suite_domain_state com.apple.dt.xctest.tool >/dev/null'
row 'swift-suite.sh::_swift_suite_witness_domain_snapshot' '_swift_suite_witness_domain_snapshot' 0 \
    "$DOM_SETUP"'; mkdir -p "$TW_TMP/dom-snap"' \
    '_swift_suite_witness_domain_snapshot "$TW_TMP/dom-snap" before'
row 'swift-suite.sh::_swift_suite_witness_domain_report' '_swift_suite_witness_domain_report' 0 \
    "$DOM_SETUP"'; printf "a\tv\n" > "$TW_TMP/dom-body"; printf "a\n" > "$TW_TMP/dom-keys"' \
    '_swift_suite_witness_domain_report + "$TW_TMP/dom-keys" "$TW_TMP/dom-body" >/dev/null'
row 'swift-suite.sh::_swift_suite_witness_domain_verdict' '_swift_suite_witness_domain_verdict (nothing changed)' 0 \
    "$DOM_SETUP"'; mkdir -p "$TW_TMP/dom-state"; _swift_suite_witness_domain_snapshot "$TW_TMP/dom-state" before; _swift_suite_witness_domain_snapshot "$TW_TMP/dom-state" after' \
    '_swift_suite_witness_domain_verdict "$TW_TMP/dom-state" >/dev/null'

# ---------------------------------------------------------------------------
# The exemption map
#
# Keys are existence-checked against the walk whether or not the exemption is
# ACTIVE on this host, so an entry that stops naming a real function fails
# rather than quietly doing nothing. tw_exempt_reason returns non-zero when the
# function is drivable here, in which case its recipe runs normally.
TW_EXEMPT_KEYS='swift-suite.sh::swift_suite_run mutation-assert.sh::assert_mutation_is_red'
tw_exempt_reason() {
  case "$1" in
    'swift-suite.sh::swift_suite_run')
      [[ "$(uname -s)" == "Darwin" ]] && return 1
      echo "needs BSD script(1) for its pty; the spelling and argument order differ on $(uname -s), and the only production caller is a Darwin-guarded gate. Driven on macOS, which is where test.yml runs this suite."
      return 0
      ;;
    'mutation-assert.sh::assert_mutation_is_red')
      echo "drives tools/mutate.sh against a REAL tracked file and requires a clean git worktree as mutate.sh's own precondition (exit 4 otherwise) — the fixture-dependency category this file's own header names as exempt-worthy (#1823), not something safe to blind-call from a generic per-function walk. Its two callers (preflight-groups-skill-mutations_test.sh, simplify-angle-guard-mutations_test.sh) already exercise it directly and run under CI; both run under 'set -uo pipefail', never '-e', so the errexit contract this tripwire checks is not live for it in production use either."
      return 0
      ;;
  esac
  return 1
}

# ---------------------------------------------------------------------------
echo "== the walk reads every sourced library in tools/lib/ =="
discovery=$(tw_discover "$LIBDIR")
refusals=$(echo "$discovery" | grep '^!' || :)
discovered=$(echo "$discovery" | grep -v '^!' || :)
if [[ -n "$refusals" ]]; then
  while IFS= read -r r; do
    [[ -n "$r" ]] && fail "the scan refused a library" "a readable library" "${r#! }"
  done <<<"$refusals"
else
  pass "every library the walk read yielded functions, in a spelling it understands"
fi
lib_count=$(echo "$discovered" | sed -E 's/::.*$//' | sort -u | grep -c . || :)
fn_count=$(echo "$discovered" | grep -c . || :)
echo "     walked $lib_count librar(ies), $fn_count function(s)"

recipe_keys=$(echo "$TW_ROWS" | cut -d"$SEP" -f1 | sort -u | grep . || :)
exempt_keys=$(echo "$TW_EXEMPT_KEYS" | tr ' ' '\n' | grep . | sort -u || :)
row_count=$(echo "$TW_ROWS" | grep -c . || :)
# The table's own vacuity guard. Without it a `row` that stopped appending —
# a renamed helper, a lost SEP — would leave every complaint below to be
# reported as "UNDRIVEN", i.e. as a fault of the libraries rather than of this
# file.
[[ "$row_count" -ge "$fn_count" ]] \
  && pass "the recipe table carries at least one row per discovered function ($row_count rows / $fn_count functions)" \
  || fail "the recipe table carries at least one row per discovered function" ">= $fn_count rows" "$row_count"

complaints=$(tw_bijection "$discovered" "$recipe_keys" "$exempt_keys")
if [[ -z "$complaints" ]]; then
  pass "every discovered function has a driving recipe or a named exemption, and every key names a real function"
else
  while IFS= read -r c; do
    [[ -n "$c" ]] && fail "recipes and the walk disagree" "a bijection" "$c"
  done <<<"$complaints"
fi

echo ""
echo "== each function, under \`bash --noprofile --norc -e -o pipefail\` =="
while IFS="$SEP" read -r key label want setup call; do
  [[ -z "$key" ]] && continue
  if reason=$(tw_exempt_reason "$key"); then
    echo "  EXEMPT: $key — $reason"
    continue
  fi
  tw_obligation_status "$label" "$want" "$setup" "$call"
  tw_obligation_leak "$label" "$setup" "$call"
done <<<"$TW_ROWS"

# ---------------------------------------------------------------------------
echo ""
echo "== the tripwire's own mutation evidence =="
# Committed rather than described: a paragraph in a merged PR body is re-run by
# nothing, and an obligation that silently stopped discriminating looks exactly
# like health. Each fixture is wrong in exactly ONE way, and each case names
# both what must be reported and what must stay silent — three mutations
# against two obligations are equally satisfied by two obligations that report
# on everything.
TW_FIXTURES="$TW_TMP/fixtures.sh"; export TW_FIXTURES
cat >"$TW_FIXTURES" <<'FIXTURES'
# Correct: guards its expected-to-fail command and touches no shell option.
tw_fixture_correct() { false || :; return 0; }
# Wrong in one way: unguarded, so under a caller's -e it aborts before its
# `return 0`. This is #1633's and #1635's shape reduced to two lines.
tw_fixture_aborts() { false; return 0; }
# Wrong in one way: "fixes" the abort by disarming errexit and never restoring
# it. Returns the documented 0, so obligation (a) is satisfied — the leak lock
# is the only thing that objects.
tw_fixture_leaks() { set +e; false; return 0; }
FIXTURES
fx='. "$TW_FIXTURES"'

tw_record_begin
tw_obligation_status 'selftest/correct' 0 "$fx" 'tw_fixture_correct'
tw_obligation_leak   'selftest/correct'   "$fx" 'tw_fixture_correct'
tw_record_end
mustBeSilent "the vacuity guard: a correct function passes both obligations"

tw_record_begin
tw_obligation_status 'selftest/aborts' 0 "$fx" 'tw_fixture_aborts'
tw_record_end
mustReport "obligation (a) reports a function that aborts its caller under -e" \
           "it returned 1, not the documented 0"

tw_record_begin
tw_obligation_leak 'selftest/aborts' "$fx" 'tw_fixture_aborts'
tw_record_end
mustBeSilent "...and obligation (b) stays silent about it — it leaks nothing"

tw_record_begin
tw_obligation_leak 'selftest/leaks' "$fx" 'tw_fixture_leaks'
tw_record_end
mustReport "obligation (b) reports a leaked shell option — so the \`set +o >file\` spelling really can see one" \
           "LEAKED a shell option change"

tw_record_begin
tw_obligation_status 'selftest/leaks' 0 "$fx" 'tw_fixture_leaks'
tw_record_end
mustBeSilent "...and obligation (a) stays silent about it — a leaked \`set +e\` returns the documented 0, which is exactly why (b) is not redundant"

tw_record_begin
tw_obligation_status 'selftest/unreachable' 0 '. "$TW_TMP/there-is-no-such-file.sh"' 'tw_fixture_correct'
tw_record_end
mustReport "a probe that could not run is reported as that, not as the defect" \
           "the probe never reached the call"

# --- the bijection, over synthetic key sets --------------------------------
three=$'a.sh::one\na.sh::two\nb.sh::three'
b=$(tw_bijection "$three" "$three" "")
[[ -z "$b" ]] && pass "bijection vacuity guard: agreeing sets produce no complaint" \
              || fail "bijection vacuity guard" "silence" "$b"

b=$(tw_bijection "$three" $'a.sh::one\na.sh::two' "")
case "$b" in
  *"UNDRIVEN b.sh::three"*) pass "bijection reports a function with no recipe and no exemption" ;;
  *) fail "bijection reports a function with no recipe and no exemption" "UNDRIVEN b.sh::three" "$b" ;;
esac

b=$(tw_bijection $'a.sh::one\na.sh::two' "$three" "")
case "$b" in
  *"ORPHAN-RECIPE b.sh::three"*) pass "bijection reports a recipe naming nothing — how a library that stopped being walked surfaces" ;;
  *) fail "bijection reports a recipe naming nothing" "ORPHAN-RECIPE b.sh::three" "$b" ;;
esac

b=$(tw_bijection "$three" "$three" 'a.sh::gone')
case "$b" in
  *"ORPHAN-EXEMPTION a.sh::gone"*) pass "bijection reports an exemption naming nothing — the existence check on TW_EXEMPT_KEYS" ;;
  *) fail "bijection reports an exemption naming nothing" "ORPHAN-EXEMPTION a.sh::gone" "$b" ;;
esac

# --- and the discovery half, mutated for real ------------------------------
# A scratch lib dir carrying three of the libraries, swift-suite.sh
# deliberately not among them — phrased that way rather than as "three of the
# four" because a fifth library (#1639) made that count stale. Every recipe of the
# missing one must come back ORPHAN, which is what "a library stopped being
# walked" looks like from here. Without this the bijection is only ever graded
# against hand-written lists.
mutdir="$TW_TMP/libs-minus-one"
mkdir -p "$mutdir"
cp "$LIBDIR"/changed-files.sh "$LIBDIR"/gate-budget.sh "$LIBDIR"/gosec-report.sh "$mutdir/"
mut=$(tw_discover "$mutdir" | grep -v '^!' || :)
missing=$(tw_bijection "$mut" "$recipe_keys" "$exempt_keys" | grep -c '^ORPHAN-RECIPE swift-suite\.sh::' || :)
swift_recipes=$(echo "$recipe_keys" | grep -c '^swift-suite\.sh::' || :)
if [[ "$swift_recipes" -lt 1 ]]; then
  fail "the discovery mutation had something to remove" "at least one swift-suite recipe" "$swift_recipes"
elif [[ "$missing" -eq "$swift_recipes" ]]; then
  pass "a library dropped from the walk is reported once per orphaned recipe ($missing of $swift_recipes)"
else
  fail "a library dropped from the walk is reported once per orphaned recipe" "$swift_recipes" "$missing"
fi
# ...and the same walk over the untouched directory must NOT report them, or
# the case above would be satisfied by a check that complains unconditionally.
still=$(tw_bijection "$discovered" "$recipe_keys" "$exempt_keys" | grep -c '^ORPHAN-RECIPE swift-suite\.sh::' || :)
[[ "$still" -eq 0 ]] && pass "...and the real walk reports none of them" \
                     || fail "the real walk must not report swift-suite recipes as orphans" "0" "$still"

# --- the two scan refusals --------------------------------------------------
scandir="$TW_TMP/scan-refusals"
mkdir -p "$scandir"
printf '#!/usr/bin/env bash\n# no functions here at all\necho hi\n' >"$scandir/empty-lib.sh"
printf '#!/usr/bin/env bash\nfunction ksh_style {\n  :\n}\n' >"$scandir/ksh-style.sh"
scanout=$(tw_discover "$scandir")
case "$scanout" in
  *"empty-lib.sh was walked and no function was found"*) pass "the scan refuses a library it read and found empty, rather than returning nothing" ;;
  *) fail "the scan refuses a library it read and found empty" "a refusal naming empty-lib.sh" "$scanout" ;;
esac
case "$scanout" in
  *"ksh-style.sh declares 1 function(s) with the \`function name {\` spelling"*) pass "the scan refuses a spelling it does not understand, rather than walking past it" ;;
  *) fail "the scan refuses a spelling it does not understand" "a refusal naming ksh-style.sh" "$scanout" ;;
esac

echo ""
if [[ "$fails" -eq 0 ]]; then
  echo "shell-lib-errexit_test: ALL PASS"
else
  echo "shell-lib-errexit_test: $fails FAILED" >&2
  exit 1
fi
