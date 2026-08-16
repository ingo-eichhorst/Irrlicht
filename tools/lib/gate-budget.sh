#!/usr/bin/env bash
# gate-budget.sh — a wall-clock budget for a run of gates, plus the bounded
# runner that enforces it. Sourced by tools/preflight.sh; this file is never
# executed directly.
#
#   . "$SCRIPT_DIR/lib/gate-budget.sh"
#   budget_open 540 || exit 2
#   budget_exhausted && ...            # nothing left — do not start a gate
#   budget_run "$(budget_remaining)" go test ./...
#
# Shell options, both directions, because the convention above depends on them
# and used to say nothing about them (#1635, the sibling of #1633):
#
#   this file  requires nothing of the caller's options and changes none of
#              them. budget_run returns the command's own status, or
#              BUDGET_TIMEOUT_RC with BUDGET_LAST_TIMED_OUT=1 for a kill,
#              whether or not `-e` is set.
#   the caller must keep `$?` reachable. Under `-e` a non-zero return aborts
#              the caller on the line above, so nothing reads
#              BUDGET_LAST_TIMED_OUT and a timeout and a failure are the same
#              thing — write `set +e` first, or `|| rc=$?` (#1629).
#
# ---------------------------------------------------------------------------
# Why this exists (#1570)
#
# The pre-push hook runs tools/preflight.sh --changed. On a diff touching one
# package under core/ that run measured 621s — go 250s, arch 16s, security
# 355s — against an automated caller's 600s command budget. The caller kills
# the tool call from OUTSIDE, so the hook dies with no summary, no gate name
# and no exit code anyone reads: the push does not land, the commit already
# has, and the documented recovery is `git push --no-verify`, which disables
# every gate rather than the slow one. Six of thirteen PRs in a single day
# went out that way.
#
# That is AGENTS.md's "a verification mechanism must fail loudly when it
# cannot run" violated in its purest form — the mechanism fails SILENTLY, from
# the outside, and the silence is indistinguishable from a hook that was never
# installed. A budget the run enforces itself converts that into a named,
# non-zero refusal: this gate TIMED OUT after N seconds, these gates NEVER RAN,
# and neither is a pass.
#
# ---------------------------------------------------------------------------
# Why not `timeout(1)`
#
# GNU coreutils' `timeout` is not on a stock macOS — it arrives with
# `brew install coreutils`, which is exactly the kind of optional dependency
# that turns a gate into a skip on the machines that do not have it. The
# bounded runner below is pure bash 3.2 (what /bin/bash is on macOS: no
# `wait -n`, no associative arrays, no `mapfile`), so it cannot be the reason
# a budget silently stops being enforced.

# Exit status budget_run reports for a command it killed. 124 matches
# timeout(1) so the number is familiar, but callers should test
# BUDGET_LAST_TIMED_OUT instead — see below.
BUDGET_TIMEOUT_RC=124

# Set to 1 by budget_run when, and only when, it killed the command for
# exceeding its bound; 0 otherwise. This is the signal callers classify on,
# NOT the 124 exit status: a real gate is free to exit 124 on its own, and a
# scanner that happened to pick that number would otherwise be reported as a
# timeout and send the reader looking for time they did not spend.
BUDGET_LAST_TIMED_OUT=0

# How long budget_run polls between checks, and how long it waits after
# SIGTERM before SIGKILL. Overridable so the unit tests can run in
# milliseconds instead of seconds.
: "${BUDGET_POLL_SECONDS:=0.2}"
: "${BUDGET_TERM_GRACE_SECONDS:=5}"

# Total budget, in seconds, for everything after budget_open. 0 means
# unbounded, which is what an unflagged `tools/preflight.sh` run gets — the
# manual full run must stay byte-for-byte what it was.
BUDGET_TOTAL_SECONDS=0
_BUDGET_STARTED_AT=0

# budget_open <seconds> — start the clock. 0 (or no call at all) leaves the
# run unbounded. A non-numeric value is refused with status 2 rather than
# coerced: `--budget 10m` silently meaning "unbounded" would be a bound that
# quietly is not one, which is the failure this file exists to remove.
budget_open() {
  local secs="${1:-}"
  case "$secs" in
    '' | *[!0-9]*)
      echo "gate-budget: budget must be a non-negative whole number of seconds, got '$secs'" >&2
      return 2
      ;;
  esac
  BUDGET_TOTAL_SECONDS="$secs"
  # bash's own second counter, so the poll loop below costs no fork per tick.
  _BUDGET_STARTED_AT=$SECONDS
  return 0
}

# budget_is_bounded — true when a budget is in force.
budget_is_bounded() {
  [[ "$BUDGET_TOTAL_SECONDS" -gt 0 ]]
}

# budget_remaining — whole seconds left, floored at 0. An unbounded run prints
# 0, which is also what budget_run reads as "no bound" — so the two compose
# without the caller special-casing. That collision is safe ONLY because an
# exhausted budget is distinguished by budget_exhausted, never by this value.
budget_remaining() {
  if ! budget_is_bounded; then
    echo 0
    return 0
  fi
  local left=$((BUDGET_TOTAL_SECONDS - (SECONDS - _BUDGET_STARTED_AT)))
  [[ "$left" -lt 0 ]] && left=0
  echo "$left"
  return 0
}

# budget_exhausted — true when a budget is in force and has run out. Callers
# check this BEFORE starting a gate, so a gate with nothing left is reported
# as "did not run" rather than started and instantly killed.
budget_exhausted() {
  budget_is_bounded && [[ "$(budget_remaining)" -le 0 ]]
}

# _budget_kill_tree <signal> <pid> — signal a process and every descendant.
#
# Killing only the direct child is not enough: `go test` forks a compiler and
# a test binary per package, `npm` forks node, and gosec forks nothing but
# holds the terminal — leaving those alive after the hook returns is how a
# "bounded" run keeps burning the machine. Descendants are signalled first so
# a shell that would exit on its own does not orphan them mid-walk.
#
# pgrep is on both macOS and Linux. If it is somehow absent the walk degrades
# to the direct child, which still ends the wait and still reports TIMEOUT —
# the verdict stays correct, only the cleanup is weaker.
#
# lib/swift-suite.sh has the same recursion (`_swift_suite_descendants`) and
# they stay separate deliberately. That one exists to kill a tree that
# `script -q` has moved into another SESSION and other process groups, so it
# collects the pids first and signals them as a flat list with its own grace
# period, inside a `{ … } 2>/dev/null` that hides the shell's job notice at the
# one moment the gate is explaining itself. This one signals depth-first as it
# walks and lets that notice through, because here it lands beside a named
# TIMEOUT block rather than in place of one. Folding them together would mean
# one of the two gets the wrong half. They are, however, verified to compose:
# the swift gate under `--budget 45` kills the whole pty-wrapped tree and
# leaves no xctest process behind (measured, #1570).
#
# The `|| :` guards the whole walk rather than the `kill`, and it is not about
# the group's status — `return 0` discards that anyway. Signalling a pid that
# is already gone is the NORMAL case here (a command that exited on its own,
# and the second pass after SIGKILL), so `kill` exits non-zero as a matter of
# course; under a CALLER's `-e` the shell then dies inside this helper and the
# `return 0` two lines down is a lie the caller never gets to read (#1635).
# A compound command that is the left operand of `||` runs with errexit
# ignored throughout, so one token covers the region including whatever is
# added to it later. Guarding the commands individually is a measured
# near-miss, not an equivalent: in #1633's sibling defect `|| :` on the kills
# alone moved the abort one line down onto `wait` and returned 143 — as
# plausible a wrong answer as the 1 it replaced.
_budget_kill_tree() {
  local sig="$1" pid="$2" child
  {
    for child in $(pgrep -P "$pid" 2>/dev/null); do
      _budget_kill_tree "$sig" "$child"
    done
    kill "-$sig" "$pid" 2>/dev/null
  } || :
  return 0
}

# budget_run <seconds> <cmd...> — run <cmd...> bounded to <seconds> of wall
# clock. Returns the command's own status, or BUDGET_TIMEOUT_RC with
# BUDGET_LAST_TIMED_OUT=1 when the bound expired. <seconds> of 0 runs the
# command directly, with no wrapper process and no polling at all.
budget_run() {
  local secs="${1:-}"
  shift || true
  BUDGET_LAST_TIMED_OUT=0
  case "$secs" in
    '' | *[!0-9]*)
      echo "gate-budget: budget_run needs a non-negative whole number of seconds, got '$secs'" >&2
      return 2
      ;;
  esac
  if [[ $# -eq 0 ]]; then
    echo "gate-budget: budget_run needs a command to run" >&2
    return 2
  fi
  # Deliberately NOT guarded, unlike the two regions below. `"$@"` is the
  # last command before the return, so under a caller's `-e` "errexit aborts
  # here" and "the function returns this status" are the same number to every
  # caller — errexit exits with the status of the command that tripped it —
  # and BUDGET_LAST_TIMED_OUT is already 0 from above. Guarding it would add
  # code no test can distinguish (#1633 reached the same conclusion about
  # swift_suite_run's final `wait`).
  if [[ "$secs" -eq 0 ]]; then
    "$@"
    return $?
  fi

  # The child writes its own exit status here as its last act, and that file —
  # not `kill -0 $pid` — is the "it finished" signal. A background child bash
  # has already reaped still answers `kill -0` successfully while it is a
  # zombie, so a pid-only poll can spin until the deadline on a command that
  # exited normally seconds earlier, turning every fast gate into a TIMEOUT.
  # The status is written to a sibling and renamed, so a reader can never
  # observe a half-written file (rename within a directory is atomic).
  local statusfile
  statusfile=$(mktemp -t irrlicht-gate-budget) || return 2

  #
  # `set +e` first, and it is what makes the ordinary path work at all under a
  # caller's `-e` (#1635). `{ …; } &` runs in a SUBSHELL, so the change cannot
  # reach the caller — that is the whole reason this one region is spelled with
  # a `set` where the two in-process ones (the timeout region below, and
  # _budget_kill_tree's walk above) use `{ … } || :`, which is the same guard
  # by the only means available there. Without it the child
  # inherits errexit and dies on a command that exits non-zero BEFORE the write
  # below, so the "it finished" signal never appears, budget_run polls to the
  # full deadline and reports a TIMEOUT for a command that exited in
  # milliseconds — measured: `exit 3` came back 1 after 19.55s. There is no
  # spelling that both preserves the caller's errexit inside a shell-function
  # command and still captures that command's status: `"$@" || rc=$?` disables
  # it for the body too, by the same rule.
  { set +e; "$@"; echo "$?" >"$statusfile.part" && mv -f "$statusfile.part" "$statusfile"; } &
  local pid=$!

  local started=$SECONDS rc
  while :; do
    if [[ -s "$statusfile" ]]; then
      # Deliberately UNGUARDED, unlike the timeout region below, and the
      # reasoning is worth keeping because the opposite looks obviously right:
      # `wait` reports the CHILD's status, and the child is the group above, so
      # its status is the `mv`'s — not the command's. This branch is only
      # entered once that `mv` has landed, so `wait` here is provably 0.
      # Measured: WAIT-STATUS=0 for a command exiting 0, 3 and 127 alike. A
      # `|| :` here would be a guard no mutation can redden, which AGENTS.md
      # treats the same as a defect test that passes on main. `-s` is likewise
      # what makes `cat` safe to read as a status, and `rm -f` cannot fail.
      # If the child's body ever changes so that its status IS the command's,
      # tools/lib/shell-lib-errexit_test.sh's `budget_run (ordinary path)` row
      # is what catches it.
      rc=$(cat "$statusfile")
      rm -f "$statusfile" "$statusfile.part"
      wait "$pid" 2>/dev/null
      return "$rc"
    fi
    if [[ $((SECONDS - started)) -ge "$secs" ]]; then
      # One last look before killing. The deadline and the child's final write
      # can land in the same poll interval, and reporting TIMEOUT for a command
      # that finished is a false accusation of the exact kind this file is
      # supposed to replace.
      if [[ -s "$statusfile" ]]; then
        # Unguarded for the same reason as its twin above — the child reached
        # its `mv`, so `wait` is 0.
        rc=$(cat "$statusfile")
        rm -f "$statusfile" "$statusfile.part"
        wait "$pid" 2>/dev/null
        return "$rc"
      fi
      # bash prints its own "…Terminated: 15" job notice on stderr when it
      # reaps a signalled background job. It is left alone deliberately: it
      # lands immediately beside the caller's named TIMEOUT block, so it reads
      # as corroboration, and silencing shell diagnostics wholesale to tidy one
      # line would hide the next one nobody predicted.
      #
      # The whole kill-and-reap region is guarded, not its individual
      # commands (#1635, and #1633 in the sibling library). Every command in
      # here is EXPECTED to fail: after SIGTERM plus the grace the victim is
      # gone, so the second `kill` exits non-zero and `wait` reports the
      # signal. Under a caller's `-e` the shell died on the first of them, so
      # BUDGET_LAST_TIMED_OUT was never set and `return "$BUDGET_TIMEOUT_RC"`
      # never reached — the two things preflight.sh reads from this library,
      # both wrong, and neither with anything to look at.
      {
        _budget_kill_tree TERM "$pid"
        local hard=$SECONDS
        while [[ $((SECONDS - hard)) -lt "$BUDGET_TERM_GRACE_SECONDS" ]] && kill -0 "$pid" 2>/dev/null; do
          sleep "$BUDGET_POLL_SECONDS"
        done
        _budget_kill_tree KILL "$pid"
        wait "$pid" 2>/dev/null
        rm -f "$statusfile" "$statusfile.part"
      } || :
      # shellcheck disable=SC2034  # read by the caller (tools/preflight.sh), not here
      BUDGET_LAST_TIMED_OUT=1
      return "$BUDGET_TIMEOUT_RC"
    fi
    sleep "$BUDGET_POLL_SECONDS"
  done
}
