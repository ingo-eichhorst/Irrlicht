#!/usr/bin/env bash
# swift-test-step_test.sh — the lock on .github/workflows/macos-swift.yml's
# "Test (bounded, streamed under a pty)" step (#1702).
#
# ---------------------------------------------------------------------------
# The defect
#
# That step opens `set +e` — correctly, and with a long comment saying why
# (#1629): without it GitHub's implicit `-e` kills the step at the failing
# subshell, `rc=$?` never runs, and `swift_suite_verdict` never speaks. It then
# sources the harness that defines both of the functions it goes on to call:
#
#     . tools/lib/swift-suite.sh
#
# and that statement's status was read by NOTHING. With the library absent,
# both calls become "command not found" and the step exits 127. That is LOUD —
# `Process completed with exit code 127` is not a pass — and it is UNDIAGNOSED:
# the missing function IS `swift_suite_verdict`, so the one thing that would
# have printed a named `::error::` line is the thing that is gone. Every other
# way this step can fail (the 240s hang, `last test to start`, TRUNCATED,
# `Executed 0 tests`, signal 6) prints a headline; this one prints three
# `command not found` lines that are legible only to someone who opens the log.
#
# It is NOT the sibling's defect. In `swift-snapshot-evidence` the predicates
# are consulted individually, so a harness that never loaded made four guards
# fire and MISdiagnosed the run four ways (#1678). Here nothing is misdiagnosed
# because nothing is diagnosed at all. Strictly better, and still a "could not
# determine" with no refusal (#1645).
#
# ---------------------------------------------------------------------------
# What this file achieves, and what it does not
#
# EXTRACT-AND-EXECUTE, per the family this belongs to (#1641, #1645, #1646):
# the real step body is read out of the real workflow file and RUN, under the
# shell GitHub actually gives that step — derived through
# tools/lib/workflow-step.sh, never typed. Running the block pins the property;
# a text scan pins one spelling of a guard.
#
# Only ONE thing is substituted, and it is the half that cannot be reached
# without grading a different program:
#
#   the library   is the REAL one wherever an arm is not deliberately breaking
#                 it. The fixture checkout's `tools/lib/swift-suite.sh` sources
#                 the repo's own file by absolute path and overrides exactly
#                 one function, so `swift_suite_verdict` — the only predicate
#                 this step consults — is the production one, reading a real
#                 committed log fixture from tools/lib/testdata/swift-suite/.
#   `swift test`  is not run. `swift_suite_run` is the pty runner that drives
#                 it; the override writes a chosen log fixture and returns a
#                 chosen status, which is what makes each outcome reachable in
#                 a second rather than in a 20-minute macOS job.
#
# So what is graded is this step's LOAD ACCOUNTING against the real body, the
# real shell and the real verdict. What is NOT graded, said plainly rather than
# implied: whether the verdict judges a real log correctly (that is
# swift-suite_test.sh, which owns the committed log corpus this file borrows),
# and whether a real `swift test` behaves as the log fixtures record.
#
# ---------------------------------------------------------------------------
# The obligations, in order
#
#   1. the pre-fix hazard is re-MEASURED, not described: the old two-line
#      spelling is emitted verbatim and run under the same derived shell with
#      the library absent, and it must still exit 127 having printed no
#      `::error::` line at all. It doubles as the vacuity guard for the whole
#      fix — if a missing library ever started diagnosing itself, the checks
#      below would be protecting nothing.
#   2. a healthy run still exits 0 and prints no refusal. Without this an arm
#      that refuses unconditionally is indistinguishable from one that works.
#   3. a run the harness DOES judge is still judged: a hang reaches
#      `swift_suite_verdict` and is reported as one, with neither refusal's
#      wording present. This is the other direction of 2 and the reason the
#      refusals cannot be "always on".
#   4. the harness cannot be loaded → refused BY NAME, at the workflow's own
#      failure status, before either call is reached (so no `command not
#      found`, and no 127).
#   5. the harness loads and defines NOTHING → refused too. `. an-empty-file`
#      exits 0, so no status check anywhere can see this one; it is what makes
#      the fix two checks rather than one.
#   6. the required set is THIS step's, not the sibling's. A library defining
#      the sibling's three names (`swift_suite_run`, `swift_suite_completed`,
#      `swift_suite_ran_tests`) and not `swift_suite_verdict` must still be
#      refused — a list copied from `swift-snapshot-evidence` passes that one
#      and lands straight back in the defect. Driven in both directions, so
#      neither name can quietly leave the list.
#   7. 4, 5 and 6 are told APART: each asserts the others' wording is absent. A
#      shared exit 1 is satisfied by refusals that all fire together — measured
#      that way in #1644, and again in #1678 where four fired at once.
#
# ---------------------------------------------------------------------------
# Which statement decides the status
#
# `swift_suite_verdict` is the step's LAST command, so its status is the step's
# status, and it returns 0 or 1 and nothing else. There is no `bad` accumulator
# here and nothing after it to reach. The two load refusals therefore exit
# directly, and they exit 1: that is what this step already means by "failed",
# and it is the difference between a chosen status and 127-by-accident, which
# is the shell reporting the last "command not found" rather than the workflow
# reporting anything.
set -uo pipefail

REPO_ROOT=$(git rev-parse --show-toplevel)
cd "$REPO_ROOT" || { echo "FAIL: swift-test-step_test — cannot cd to $REPO_ROOT" >&2; exit 1; }

# A missing tool is a hard failure, not a skip: exiting 0 here would read as a
# PASS to shell-lib-suite.sh, so the gate would go green having asserted
# nothing — the failure mode this whole issue family is made of.
need() { command -v "$1" >/dev/null 2>&1 || { echo "FAIL: swift-test-step_test — $1 not found" >&2; exit 1; }; }
need git
need mktemp
need bash
need grep

WF=.github/workflows/macos-swift.yml
STEP="Test (bounded, streamed under a pty)"
REAL_LIB="$REPO_ROOT/tools/lib/swift-suite.sh"
LOGDATA="$REPO_ROOT/tools/lib/testdata/swift-suite"

# The functions THIS step calls. Named here once and asserted against the body
# below, so an arm cannot silently grade a set the step stopped using.
STEP_FNS=(swift_suite_run swift_suite_verdict)

# shellcheck source=workflow-step.sh
. tools/lib/workflow-step.sh

TMP=$(mktemp -d -t swift-test-step) || exit 1
trap 'rm -rf "$TMP"' EXIT

rc=0
pass() { echo "  PASS: $1"; return 0; }
fail() { echo "  FAIL: $1 — expected [$2] got [$3]" >&2; rc=1; return 0; }
flat() { echo "$1" | tr '\n' ' ' | cut -c1-400; }

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
# Preconditions. Each of these is something that, if it quietly stopped being
# true, would leave every arm below grading something other than what it says.
for f in clean hung; do
  [[ -s "$LOGDATA/$f.log" ]] || { echo "FAIL: swift-test-step_test — log fixture $LOGDATA/$f.log missing or empty" >&2; exit 1; }
done
[[ -f "$REAL_LIB" ]] || { echo "FAIL: swift-test-step_test — $REAL_LIB not found" >&2; exit 1; }

# ---------------------------------------------------------------------------
# The invocation and the body, DERIVED from the workflow (#1650).
#
# macos-swift.yml is the ONE workflow in this repo that declares `shell: bash`
# on a step, so this step's invocation genuinely differs from every other
# harnessed step's — `bash --noprofile --norc -e -o pipefail` against the
# `bash -e` the others get. Typing either would be a coin flip that reads as
# fact; workflow-step.sh REFUSES rather than defaulting, which is why both of
# these are hard exits and not fallbacks.
if ! STEP_SHELL=$(workflow_step_shell "$WF" "$STEP"); then
  echo "FAIL: swift-test-step_test — could not derive the shell $WF gives '$STEP' (refusal above); nothing below would have graded the real program" >&2
  exit 1
fi
read -r -a STEP_ARGV <<<"$STEP_SHELL"
if ! STEP_BODY=$(workflow_step_body "$WF" "$STEP"); then
  echo "FAIL: swift-test-step_test — could not extract the body of '$STEP' from $WF (refusal above)" >&2
  exit 1
fi
printf '%s\n' "$STEP_BODY" >"$TMP/step.sh"
echo "== $WF :: '$STEP' runs under \`$STEP_SHELL\` (derived) =="

# The substitution has to still be the substitution, and the set the arms below
# drive has to still be the set the step calls. If the body stopped calling one
# of these — or the library stopped defining it — the fixture library would be
# overriding nothing, or shadowing something real, and every arm would grade a
# different program while still going green.
for fn in "${STEP_FNS[@]}"; do
  grep -q "$fn" "$TMP/step.sh" \
    || { echo "FAIL: swift-test-step_test — the step body no longer calls $fn; this file is grading something else" >&2; exit 1; }
  grep -qE "^$fn\(\)" "$REAL_LIB" \
    || { echo "FAIL: swift-test-step_test — $REAL_LIB no longer defines $fn" >&2; exit 1; }
done

# ---------------------------------------------------------------------------
# The fixture checkout.
#
# The body sources `tools/lib/swift-suite.sh` relative to its cwd and then cds
# into `platforms/macos`, so every arm executes against a throwaway tree under
# $TMP. Nothing here reads or writes the repo's real platforms/macos.
CHECKOUT="$TMP/checkout"

# The sibling step's required set, verbatim. Obligation 6 plants exactly these
# and asserts the step still refuses: this is the list a copy-paste fix would
# have carried, and it does not name `swift_suite_verdict`.
SIBLING_FNS=(swift_suite_run swift_suite_completed swift_suite_ran_tests)

# build_checkout <lib-mode>   real | absent | empty | omit:<fn>
#
# `platforms/macos` itself always exists — the body cds into it, and a cd that
# failed would give an arm a status unrelated to its obligation.
build_checkout() {
  rm -rf "$CHECKOUT"
  mkdir -p "$CHECKOUT/tools/lib" "$CHECKOUT/platforms/macos"
  local lib="$CHECKOUT/tools/lib/swift-suite.sh"
  case "$1" in
    real)
      # The REAL library with the pty runner replaced. Sourcing by absolute
      # path is what keeps `swift_suite_verdict` production code reading a
      # production log fixture.
      cat >"$lib" <<'STUBLIB'
. "$SWT_REAL_LIB"
swift_suite_run() {
  local log="$1"
  cp "$SWT_LOG_SRC" "$log" || return 1
  return "${SWT_RC:-1}"
}
STUBLIB
      ;;
    absent) : ;;
    empty)  : >"$lib" ;;
    omit:*)
      # A library that LOADS and defines everything except one name. The REAL
      # library with the pty runner replaced, then exactly one function
      # removed — so the omission is the only difference from the healthy arm
      # and every remaining predicate is production code. Stubbing the
      # survivors instead would let a stub's chosen return value, rather than
      # the missing name, decide the arm.
      local skip="${1#omit:}"
      cat >"$lib" <<STUBOMIT
. "\$SWT_REAL_LIB"
swift_suite_run() {
  local log="\$1"
  cp "\$SWT_LOG_SRC" "\$log" || return 1
  return "\${SWT_RC:-1}"
}
unset -f $skip
STUBOMIT
      ;;
    *) echo "FAIL: swift-test-step_test — unmodelled lib mode: $1" >&2; exit 1 ;;
  esac
  return 0
}

ARM=0
# run_step <script> <log-fixture> <rc>  -> sets OUT / ST / RT
run_step() {
  ARM=$((ARM + 1))
  RT="$TMP/rt.$ARM"
  mkdir -p "$RT"
  # No `set +e` around this. THIS file runs under `set -uo pipefail` and
  # deliberately not `-e`, so a non-zero inner status — the expected outcome of
  # most arms — is data, not an abort. Toggling errexit here would leave it on
  # for the rest of the file, which is the option-you-cannot-see family the
  # sibling issues (#1633, #1635) are made of.
  OUT=$(cd "$CHECKOUT" && env \
          RUNNER_TEMP="$RT" \
          SWT_REAL_LIB="$REAL_LIB" \
          SWT_LOG_SRC="$LOGDATA/$2.log" \
          SWT_RC="$3" \
          "${STEP_ARGV[@]}" "$1" 2>&1)
  ST=$?
  return 0
}

# The two load refusals, and what they must not borrow a verdict from.
NOLOAD='could not load the test harness tools/lib/swift-suite.sh'
UNUSABLE='was read but defines no'
HUNG='HUNG: no exit within'
NOT_FOUND='command not found'

# ---------------------------------------------------------------------------
# Obligation 1 — the pre-#1702 hazard, re-measured on every run.
#
# The old statement in the context that decides its fate: the block's own
# `set +e`, the source whose status nothing reads, and the two calls that
# become "command not found". The suite arguments are replaced by `true`
# because they are not what is being graded — the accounting is — and a real
# `swift test` here would grade a toolchain.
echo ""
echo "== the pre-#1702 unread source (the defect, re-measured) =="
build_checkout absent
cat >"$TMP/predecessor-source.sh" <<'OLD'
set +e
set -uo pipefail
. tools/lib/swift-suite.sh
log="$RUNNER_TEMP/swift-test.log"
( cd platforms/macos && swift_suite_run "$log" true )
rc=$?
swift_suite_verdict "$rc" "$log"
OLD
run_step "$TMP/predecessor-source.sh" clean 0
if [[ "$ST" -eq 127 ]]; then
  pass "the old unread source still exits 127 when the library is absent — the hazard is real"
else
  fail "the pre-fix source exits 127 (the hazard this pins)" \
       "exit 127" "exit $ST — the hazard has CHANGED, so re-derive the fix rather than trusting it :: $(flat "$OUT")"
fi
want_contains "...with the calls having failed as 'command not found'" "$NOT_FOUND" "$OUT"
want_absent "...and NOT one named ::error:: line anywhere (loud, undiagnosed)" "::error::" "$OUT"

# ---------------------------------------------------------------------------
# Obligation 2 — the vacuity guard: a healthy run still passes and refuses
# nothing.
echo ""
echo "== the real step, healthy run =="
build_checkout real
run_step "$TMP/step.sh" clean 0
want_status "a healthy run passes" 0 "$ST" "$OUT"
want_absent "...and reports neither load refusal" "::error::" "$OUT"

# ---------------------------------------------------------------------------
# Obligation 3 — the verdict is still reached and still speaks.
#
# The other direction of obligation 2, and the one that would catch a refusal
# wired to fire unconditionally: a real hang must still be diagnosed as a hang,
# by the production `swift_suite_verdict`, with neither refusal's wording in
# the output.
echo ""
echo "== a real hang still reaches swift_suite_verdict =="
build_checkout real
run_step "$TMP/step.sh" hung 124
want_status "a hung run fails the step" 1 "$ST" "$OUT"
want_contains "...diagnosed as a hang by the production verdict" "$HUNG" "$OUT"
want_absent "...and NOT as a harness that could not be loaded" "$NOLOAD" "$OUT"
want_absent "...nor as a harness that loaded and is unusable" "$UNUSABLE" "$OUT"

# ---------------------------------------------------------------------------
# Obligations 4 and 7 — the harness cannot be loaded at all.
echo ""
echo "== a harness that does not load is refused BY NAME (#1702) =="
build_checkout absent
run_step "$TMP/step.sh" clean 0
want_status "a missing swift-suite.sh fails at the workflow's own failure status, not 127" 1 "$ST" "$OUT"
want_contains "...naming the harness it could not load" "$NOLOAD" "$OUT"
want_absent "...and not the other load refusal's wording (obligation 7)" "$UNUSABLE" "$OUT"
# The refusal is an early exit, not an addition: nothing downstream is reached,
# so neither call can report itself missing and the 127 is gone by
# construction rather than by being overwritten.
want_absent "...having refused BEFORE either call (no 'command not found')" "$NOT_FOUND" "$OUT"
want_absent "...and without borrowing the verdict's diagnosis" "$HUNG" "$OUT"

# ---------------------------------------------------------------------------
# Obligations 5 and 7 — the harness loads and defines nothing.
#
# The outcome no status check can see, and the reason the fix is two checks
# rather than one: `. an-empty-file` exits 0. A truncated, emptied or
# half-written library satisfies the source's own status while defining none of
# the functions, and the step lands straight back in 127.
echo ""
echo "== a harness that loads and defines nothing is refused too (#1702) =="
build_checkout empty
run_step "$TMP/step.sh" clean 0
want_status "a swift-suite.sh that defines nothing fails the step" 1 "$ST" "$OUT"
want_contains "...naming the function it does not define" "$UNUSABLE" "$OUT"
want_absent "...and not the 'could not load' diagnosis (obligation 7)" "$NOLOAD" "$OUT"
want_absent "...having refused BEFORE either call (no 'command not found')" "$NOT_FOUND" "$OUT"
# ...and the premise of the arm: sourcing an empty file really does succeed, so
# the status check alone could not have caught this one.
if bash -c '. "$1"' _ "$CHECKOUT/tools/lib/swift-suite.sh"; then
  pass "...with sourcing the empty library having demonstrably SUCCEEDED (exit 0), which is why the second check exists"
else
  fail "the unusable-library arm's premise" "sourcing an empty file to exit 0" \
       "it exited non-zero — this arm graded obligation 4"
fi

# ---------------------------------------------------------------------------
# Obligation 6 — the required set is THIS step's, not the sibling's.
#
# Each row plants the REAL library with exactly one function removed. The
# `swift_suite_verdict` row is the one a copy-pasted fix fails: the sibling
# step does not consult the verdict at all, so its three names are all present
# here, its list is green, and the step goes on to exit 127 on the very call
# this whole issue is about.
#
# The STATUS alone does not tell the two rows apart, and that is stated rather
# than glossed: with `swift_suite_run` gone the production verdict still runs,
# finds no log and fails the step at 1 — the same 1 a correct refusal produces.
# Only the wording discriminates, which is obligation 7's argument arriving one
# arm early.
echo ""
echo "== each function this step calls is required by name (#1702) =="
for fn in "${STEP_FNS[@]}"; do
  build_checkout "omit:$fn"
  # The premise, asserted rather than assumed: every name the SIBLING step
  # checks — bar the one this row deliberately removed — really is defined by
  # this library, so a list copied from that step would have waved it through.
  missing=$(env SWT_REAL_LIB="$REAL_LIB" bash -c '
    . "$1" >/dev/null 2>&1
    for f in $3; do
      [ "$f" = "$2" ] && continue
      command -v "$f" >/dev/null 2>&1 || printf "%s " "$f"
    done' _ "$CHECKOUT/tools/lib/swift-suite.sh" "$fn" "${SIBLING_FNS[*]}")
  if [[ -z "$missing" ]]; then
    pass "omitting $fn: the library still defines every sibling-checked name it can"
  else
    fail "omitting $fn: the arm's premise" "the sibling's other names defined by the fixture" \
         "these are missing too: $missing — this row is not testing the list"
  fi
  run_step "$TMP/step.sh" clean 0
  want_status "a library missing only $fn fails the step" 1 "$ST" "$OUT"
  want_contains "...naming $fn as the one it does not define" "defines no $fn" "$OUT"
  want_absent "...and not the 'could not load' diagnosis (obligation 7)" "$NOLOAD" "$OUT"
  want_absent "...having refused BEFORE either call (no 'command not found')" "$NOT_FOUND" "$OUT"
done

echo ""
if [[ "$rc" -eq 0 ]]; then
  echo "swift-test-step_test: OK"
else
  echo "swift-test-step_test: FAILED" >&2
fi
exit "$rc"
