#!/usr/bin/env bash
# tmux-teardown-check.sh — after a recording driver returns, assert that no tmux
# session belonging to THAT RUN survived it (#1825, AC4).
#
# This file is sourced, not executed:
#
#   source "$SCRIPT_DIR/lib/tmux-teardown-check.sh"
#   rc=0
#   check_tmux_teardown "$DRIVER_PID" "$deadline" "$lifetime" "what bounds it" || rc=$?
#   case "$rc" in
#     0) : ;;                                    # nothing of this run's survived
#     1) fail "$TMUX_TEARDOWN_SURVIVORS" ;;      # it leaked
#     2) fail "$TMUX_TEARDOWN_REASON" ;;         # the check could not be MADE
#   esac
#
# ---------------------------------------------------------------------------
# Why this exists
#
# #1825: every recording whose recipe ended in `exit_clean` left a live agent
# process and its detached tmux session behind, for months, because the only
# thing that ever claimed the session was gone was the driver ASSERTING it —
# `sleep 1; SES_ALIVE=0`. run-cell.sh then wrote a `complete` manifest over the
# top. Nothing in the rig ever asked tmux.
#
# So this is the same treatment #1333 gave `driver.exit-reason`: the driver's
# claim about itself is not evidence, and the rig looks for itself. The three
# rules below are what make the looking worth anything.
#
# 1. THE RUN'S IDENTITY IS THE DRIVER'S PID. "Is any tmux session alive?" is the
#    wrong question — a developer's own tmux, or a concurrent cell, would both
#    answer yes. Every one of the eleven interactive drivers embeds its own `$$`
#    as a whole '-'-delimited field of every session name it creates
#    (`claudecode-onboard-<ts>-<pid>[-<idx>]`, `codex-onboard-<ts>-<pid>-r<n>`,
#    `geminidrv-<pid>-<ts>-r<n>`, `ocdrv-<pid>-<ts>`,
#    `aider-onboard-<uuid8>-<pid>`, …), so the driver's pid is exactly the
#    identity that separates this run's leftovers from everyone else's. The
#    convention is not assumed: a static per-driver tripwire under
#    tools/onboarding-factory/internal/ asserts it for every current and future
#    adapter.
#
#    The other '-'-delimited fields cannot collide with a pid: the timestamp
#    field is `date +%s` (~1.8e9, three orders above any pid on a system whose
#    pid_max is 99999 on macOS / 4194304 on Linux), the slot suffixes are
#    `r1`/`resume1`/small integers that would have to be pid 1 or 2 to match,
#    and aider's is 8 hex characters, which is one character longer than the
#    widest pid it could be confused with.
#
# 2. "COULD NOT LOOK" IS NOT "IT IS GONE" — the rule this repo keeps relearning
#    (#1485/#1492/#1513/#1524/#1533/#1537, AGENTS.md's "a verification mechanism
#    must fail loudly when it cannot run"), and the one #1825's AC4 names
#    explicitly. `tmux list-sessions` has THREE outcomes that a naive
#    `|| true` collapses into one:
#
#      exit 0 + names           → a server is up; match the pid against them.
#      exit 1 + "no server"     → there are legitimately ZERO sessions. Pass.
#      anything else            → the lookup FAILED. Loud refusal, never a pass.
#
#    …plus the one that is not tmux's at all: no `tmux` on PATH. An interactive
#    cell cannot have been recorded without it (six of the eleven drivers open
#    with `command -v tmux || fail`; the other five simply die at their first
#    `tmux new-session`), so a missing binary at this point is a broken lookup
#    and not an empty session list. precheck.sh does not check for tmux, so
#    this is the first place that can notice.
#
#    The polling, the "the predicate must say whether it could look at all"
#    contract, and the refusal-on-a-silent-predicate all come from
#    tools/lib/await-gone.sh rather than being written a fourth time here; this
#    file supplies only the predicate and the pid matching. See that file's
#    header for why a poll that reports through variables (and not stdout)
#    is what keeps rule 2 whole.
#
# 3. GRACE, BOUNDED FROM BOTH ENDS. The driver has already returned, so the
#    honest expectation is that the session is gone on the first look; the
#    deadline is slack for a tmux server still winding down, not a latency
#    being fitted. It is bounded from ABOVE too, by await_gone_bound: if the
#    grace approached the cell's own driver timeout, an agent that took its
#    time dying BY ITSELF would read exactly like a driver that tore its
#    session down, and the check would stop asserting anything.

_tmux_teardown_lib_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# Bounded, loud-when-blind polling. Cross-tree source, same shape as
# run-cell.sh:204 sourcing replaydata/_lib/assert-staging-path.sh — the rules in
# await-gone.sh's header are repo-wide and this is their first production
# caller, so it reuses them rather than growing a fourth copy of the poll.
# shellcheck source=../../../lib/await-gone.sh
. "$_tmux_teardown_lib_dir/../../../lib/await-gone.sh"

# What a caller that names no bound gets in a refusal. A variable and not an
# inline `${4:-…}` default for the reason await-gone.sh:156-160 measured on bash
# 3.2: an apostrophe inside a parameter expansion's word is read as a QUOTE even
# when the whole expansion is double-quoted, and the file fails to parse.
TMUX_TEARDOWN_DEFAULT_WHAT="the cell own driver timeout"

# The run identity the predicate matches session names against. Set by
# check_tmux_teardown; a module-scope variable and not a predicate argument
# because await_gone calls its predicate with no arguments by design.
TMUX_TEARDOWN_PID=""

# Outputs. Set by check_tmux_teardown on every call, read by its caller.
#
# shellcheck disable=SC2034  # SURVIVORS/REASON/ELAPSED are this library's
# OUTPUT, unused within this file by design. Read by
# tools/onboarding-factory/scripts/run-cell.sh (the tmux-teardown gate after
# the driver returns) and asserted by lib/tmux-teardown-check_test.sh.
TMUX_TEARDOWN_SURVIVORS=""
# shellcheck disable=SC2034  # see above
TMUX_TEARDOWN_REASON=""
# shellcheck disable=SC2034  # see above
TMUX_TEARDOWN_ELAPSED=0

# tmux_teardown_look — the await_gone predicate (see that file's "The
# predicate"). Takes no arguments; reports through AWAIT_GONE_LOOKED (was I able
# to look at all) and AWAIT_GONE_ALIVE (what is still there, or why I could
# not). Its own return status is ignored by await_gone on purpose, so every
# branch here returns 0 and says what it means in the two variables.
tmux_teardown_look() {
  # Start from "I could not look". Every branch below either upgrades this to a
  # real look or fills in the reason; starting from the permissive answer is how
  # a branch someone adds later without thinking would default to a pass.
  AWAIT_GONE_LOOKED=0
  AWAIT_GONE_ALIVE=""

  if [[ -z "${TMUX_TEARDOWN_PID:-}" ]]; then
    AWAIT_GONE_ALIVE="no driver pid was recorded, so there is no run identity to match session names against — every surviving session would read as somebody else's"
    return 0
  fi

  if ! command -v tmux >/dev/null 2>&1; then
    AWAIT_GONE_ALIVE="no tmux binary on PATH — an interactive cell's driver cannot have recorded anything without one, so this is a lookup that failed, not an empty session list"
    return 0
  fi

  local out status
  out="$(tmux list-sessions -F '#{session_name}' 2>&1)"
  status=$?

  if [[ "$status" -ne 0 ]]; then
    # The ONE legitimate non-zero: tmux's server exits with its last session, so
    # "no server running" is how tmux spells "there are zero sessions". Every
    # other non-zero is a lookup that failed and must not read as zero sessions.
    case "$out" in
      *"no server running"* | *"no current server"* | *"error connecting to"* | *"failed to connect to server"*)
        AWAIT_GONE_LOOKED=1
        AWAIT_GONE_ALIVE=""
        return 0
        ;;
    esac
    AWAIT_GONE_ALIVE="tmux list-sessions exited $status: ${out:-<no output at all>}"
    return 0
  fi

  # Exit 0 with nothing named is NOT "zero sessions": a live server always owns
  # at least one (it exits with the last), and it is exit 1 + "no server
  # running" that reports emptiness. Empty output here means something answered
  # for tmux without listing anything — unparseable, so a refusal.
  if [[ -z "$out" ]]; then
    AWAIT_GONE_ALIVE="tmux list-sessions succeeded but named no session — a running server always owns at least one, so this cannot be read as an empty list"
    return 0
  fi

  # Match the pid as a WHOLE '-'-delimited field, never as a substring: pid 123
  # must not match `…-1234-…` or a timestamp that happens to contain it. Padding
  # both ends with '-' makes the first and last fields match the same way the
  # middle ones do.
  local name survivors=""
  while IFS= read -r name; do
    [[ -z "$name" ]] && continue
    case "-${name}-" in
      *-"$TMUX_TEARDOWN_PID"-*) survivors="${survivors:+$survivors }$name" ;;
    esac
  done <<< "$out"

  # shellcheck disable=SC2034  # this pair IS the predicate's return value: the
  # await_gone loop in tools/lib/await-gone.sh:259-283 reads both after calling
  # us, and refuses the poll outright if either is left unset. The linter does
  # not follow the `. …/await-gone.sh` above, so it cannot see the consumer.
  AWAIT_GONE_LOOKED=1
  # shellcheck disable=SC2034  # see above — a directive covers only the next
  # command, and this half of the pair is the one carrying the survivors.
  AWAIT_GONE_ALIVE="$survivors"
  return 0
}

# tmux_teardown_deadline_for <lifetime-s> — the deadline/lifetime pair
# await_gone_bound checks (rule 3 above), computed in ONE place so run-cell.sh
# and run-cell-multi.sh cannot drift apart on the arithmetic. Both used to
# inline this clamp themselves (#1828); the deadline this produces is what a
# caller then hands to check_tmux_teardown below as ITS second argument.
#
# <lifetime-s> is the cell's own driver timeout — the upper bound on how long
# the session being asked about could have lived. The result is a TENTH of
# that, CAPPED at 5s so a 900s cell does not buy a 90s wait for a session that
# should already be gone, and FLOORED at 1s so the arithmetic can never
# produce the "look exactly once" deadline await_gone_bound refuses. A cell
# whose timeout is under 10s therefore fails the bound and is reported LOUDLY
# as unreadable, which is the honest answer: at that ratio the check would
# assert nothing. (Every applicable cell today declares 60s or more.)
#
# Prints the deadline to stdout; callers capture it with command substitution.
tmux_teardown_deadline_for() {
  local lifetime="${1:-0}" deadline
  deadline=$(( lifetime / 10 ))
  if [[ "$deadline" -gt 5 ]]; then deadline=5; fi
  if [[ "$deadline" -lt 1 ]]; then deadline=1; fi
  printf '%s\n' "$deadline"
}

# check_tmux_teardown <driver-pid> <deadline-s> <lifetime-s> [what-the-lifetime-is]
#
#   0  no tmux session carrying <driver-pid> survives. TMUX_TEARDOWN_ELAPSED
#      says how long that took to establish.
#   1  at least one did. TMUX_TEARDOWN_SURVIVORS names them, so the caller's
#      failure line can be acted on (`tmux kill-session -t …`) rather than
#      merely believed.
#   2  the check could not be MADE — a bad deadline/lifetime pair, or a lookup
#      that failed. TMUX_TEARDOWN_REASON says which. Distinct from 1 because
#      "it leaked" and "nothing was checked" are different answers, and the
#      whole point of #1825 is that they must not print the same thing.
#
# <lifetime-s> is the cell's own driver timeout: the upper bound on how long the
# session it is asking about could have lived. await_gone_bound refuses a
# deadline that is not an order of magnitude under it — see rule 3 above.
check_tmux_teardown() {
  local pid="${1:-}" deadline="${2:-}" lifetime="${3:-}"
  local what="${4:-$TMUX_TEARDOWN_DEFAULT_WHAT}"

  TMUX_TEARDOWN_SURVIVORS=""
  TMUX_TEARDOWN_REASON=""
  TMUX_TEARDOWN_ELAPSED=0

  case "$pid" in
    '' | *[!0-9]*)
      TMUX_TEARDOWN_REASON="tmux-teardown: refusing — the driver pid must be a whole number, got '$pid'. Without the run's identity there is nothing to tell this run's leftovers from another run's, and a check that cannot identify its subject must not report it gone"
      printf '%s\n' "$TMUX_TEARDOWN_REASON" >&2
      return 2
      ;;
  esac

  TMUX_TEARDOWN_PID="$pid"

  local rc=0
  await_gone "$deadline" "$lifetime" tmux_teardown_look "$what" || rc=$?
  # shellcheck disable=SC2034  # an OUTPUT, unused in this file by design —
  # printed by run-cell.sh's tmux-teardown gate and asserted by
  # lib/tmux-teardown-check_test.sh ("a survivor is polled for, not slept on").
  TMUX_TEARDOWN_ELAPSED="$AWAIT_GONE_ELAPSED"

  case "$rc" in
    0) return 0 ;;
    1)
      # shellcheck disable=SC2034  # an OUTPUT: run-cell.sh puts it in the
      # ERROR manifest's tmux_teardown_detail and on stderr, so the operator is
      # told which session to kill rather than that one exists.
      TMUX_TEARDOWN_SURVIVORS="$AWAIT_GONE_LAST"
      return 1
      ;;
    *)
      TMUX_TEARDOWN_REASON="$AWAIT_GONE_REASON"
      return 2
      ;;
  esac
}
