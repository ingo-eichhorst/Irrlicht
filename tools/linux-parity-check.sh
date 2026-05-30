#!/usr/bin/env bash
# linux-parity-check.sh — confirm a Linux live recording produces the same
# user-observable state-machine outcome as the committed macOS recording for a
# timing-sensitive cell, WITHOUT adding a per-OS axis to the OS-neutral
# scenario matrix.
#
# Why this exists (see #478): replay is deterministic and OS-independent, so
# replaying a Linux recording still only exercises the Stage-2 state machine —
# it does NOT prove the Stage-1 Linux sensor. A handful of cells depend on live
# sensor timing where the macOS goldens are not faithful Linux artifacts:
#   • codex session-reset  — the ~7ms same-PID #169 cleanup vs pid_discovered lag
#   • a flicker/debounce case (e.g. codex/regressions/02-flicker-start-session)
#     — inotify coalesces writes differently than FSEvents/kqueue, so the
#       transcript_activity / debounce cadence differs.
# Timing is *expected* to differ; the user-observable outcome must not.
#
# This is a ONE-TIME parity proof, run by hand on the Linux box (UTM Ubuntu
# 24.04 ARM per #179). The Linux recording is a throwaway — confirm parity,
# then discard it. Nothing per-OS is committed; the corpus stays OS-neutral.
#
# Procedure on the Linux host:
#   1. Build + run a dev recording daemon (the prod daemon has no --record):
#        tools/build-dev.sh && IRRLICHT_HOME=$(mktemp -d) \
#          IRRLICHT_DAEMON_PORT=7838 core/bin/irrlichd --record /tmp/rec &
#   2. Drive the cell's recipe (tmux send-keys; never human-in-loop) so the
#      live Linux sensor emits the event stream into /tmp/rec/.../events.jsonl.
#   3. Run this script: macOS golden vs the fresh Linux events.jsonl.
#   4. PASS → parity proven, `rm -rf /tmp/rec`. FAIL → a real Linux sensor bug.
#
# Usage:
#   tools/linux-parity-check.sh <macos-events.jsonl> <linux-events.jsonl>
#
# Example:
#   tools/linux-parity-check.sh \
#     replaydata/agents/codex/scenarios/1-5_session-reset/events.jsonl \
#     /tmp/rec/codex/session-reset/events.jsonl
set -euo pipefail

fail() { echo "✗ $*" >&2; exit 1; }

[ $# -eq 2 ] || { echo "usage: $0 <macos-events.jsonl> <linux-events.jsonl>" >&2; exit 2; }
MAC="$1"; LINUX="$2"
command -v jq >/dev/null 2>&1 || { echo "jq is required" >&2; exit 2; }
[ -f "$MAC" ]   || { echo "no such file: $MAC" >&2; exit 2; }
[ -f "$LINUX" ] || { echo "no such file: $LINUX" >&2; exit 2; }

# Project an events.jsonl into the user-observable state-machine sequence:
# per-session state transitions plus a single terminal "removed" marker, with
# sessions renumbered by first-appearance order (so volatile session ids /
# pids / paths don't matter). Deliberately drops transcript_activity,
# debounce_coalesced, pid_discovered and the rest — those carry the sensor
# timing that legitimately differs between inotify and kqueue/FSEvents.
# Consecutive duplicate tokens are collapsed (e.g. repeated transcript_removed).
project() {
    jq -r '
        if .kind == "state_transition" then "\(.session_id)\t\(.new_state)"
        elif (.kind == "transcript_removed" or .kind == "presession_removed" or .kind == "process_exited")
        then "\(.session_id)\tremoved"
        else empty end
    ' "$1" \
    | awk -F'\t' '
        { if (!($1 in seen)) { seen[$1] = n++ }
          tok = seen[$1] ":" $2
          if (tok != prev) { print tok; prev = tok } }
    '
}

echo "macOS golden : $MAC"
echo "linux record : $LINUX"
echo "comparing user-observable state-transition sequence (timing ignored)…"
echo

# Project into variables (not process substitution) so a jq/awk failure on a
# malformed recording aborts here under `set -o pipefail` instead of silently
# yielding an empty FIFO that diff would call "identical". Then refuse to
# compare empty sequences — an empty-vs-empty diff is not a parity proof.
mac_seq=$(project "$MAC")     || fail "could not project $MAC (malformed events.jsonl?)"
linux_seq=$(project "$LINUX") || fail "could not project $LINUX (malformed events.jsonl?)"
[ -n "$mac_seq" ]   || fail "$MAC projected to an empty state sequence — nothing to compare"
[ -n "$linux_seq" ] || fail "$LINUX projected to an empty state sequence — nothing to compare"

if diff -u <(printf '%s\n' "$mac_seq") <(printf '%s\n' "$linux_seq"); then
    echo
    echo "✓ PARITY OK — Linux state transitions match macOS. Discard the Linux"
    echo "  recording; the corpus stays OS-neutral (nothing to commit)."
else
    echo
    echo "✗ PARITY MISMATCH — the Linux sensor produced a different observable"
    echo "  state sequence. This is a real Linux sensor bug, not a timing diff."
    exit 1
fi
