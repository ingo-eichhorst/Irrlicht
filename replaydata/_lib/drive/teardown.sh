#!/usr/bin/env bash
# teardown.sh — shared process/session-death polls for the interactive
# recording drivers (#1018). drive-mistral-vibe-interactive.sh's
# step_exit_clean already replaced a flat `sleep N` (after signalling a
# graceful TUI exit) with an inline poll on `tmux has-session` — the #1018
# retrospective calls that out as the best existing pattern in the fleet and
# asks for it to be generalized outward. This lib extracts that poll (plus
# the equivalent pid-death poll for sigkill sites) so every driver can use
# the real completion signal instead of guessing a fixed sleep duration.
#
# Deliberately NOT touched by this extraction: the settle sleep that follows
# an explicit `tmux kill-session` in step_restart (and the second sleep in
# some step_resumes). `tmux kill-session` is synchronous, so a has-session
# poll placed right after it would collapse to ~0s instead of the settle
# window those sleeps actually provide — and #1018 documents a daemon-side
# presession/PID-identity reconciliation race as the single biggest cost
# driver of the mistral-vibe onboarding run, not something to shave without a
# live re-recording to confirm it's safe. Same reasoning for step_interrupt
# (#1018 notes it needs its own, different completion condition).
#
# Sourced as a library; MUST NOT call `set` at top level.

# wait_tmux_session_gone <session> [max_wait_secs]
#   Poll every 0.2s until tmux session <session> no longer exists, capped at
#   <max_wait_secs> (default 2) — the same duration as the sleep it replaces,
#   so worst-case timing never regresses versus a flat sleep.
wait_tmux_session_gone() { # <session> [max_wait_secs]
  local session="$1" max_wait="${2:-2}"
  local ticks=$(( max_wait * 5 )) w=0
  while [[ $w -lt $ticks ]] && tmux has-session -t "$session" 2>/dev/null; do
    sleep 0.2
    w=$((w + 1))
  done
  return 0   # best-effort poll: hitting the cap is not a caller-visible failure
}

# require_tmux_session_gone <session> [max_wait_secs]
#   STRICT sibling of wait_tmux_session_gone (#1825). Same 0.2s cadence, same
#   default cap, same worst-case timing — the ONLY difference is the return
#   contract: 0 exclusively when `tmux has-session` has actually been observed
#   to fail (the session is gone), NON-ZERO when the cap expired with the
#   session still present. It never reports success without an observation.
#
#   WHY BOTH EXIST, and which to reach for:
#     * wait_tmux_session_gone is a SETTLE. The caller has already made the
#       session's death certain by other means — it killed the session itself
#       (copilot's restart arm, `tmux kill-session` then wait) or the process
#       is expected to be gone already — and only wants to avoid racing the
#       next step. Hitting the cap there is genuinely not a failure, which is
#       why it returns 0 unconditionally, and drive-lib_test.sh locks that.
#     * require_tmux_session_gone is a VERIFICATION. The caller asked a live
#       TUI to exit and has no other evidence it obeyed. #1825: claude answers
#       a single Ctrl-D with "Press Ctrl-D again to exit" and then lets the
#       confirmation expire, so nine recording runs sent the key, slept, marked
#       the slot dead and leaked a live process plus its tmux session while
#       reporting driver.exit-reason=ok. A poll that cannot fail turns "the
#       exit key stopped working" into "the exit key worked" — the exact shape
#       AGENTS.md's "a verification mechanism must fail loudly when it cannot
#       run" forbids. Every graceful-exit site uses THIS one and acts on the
#       non-zero: log, kill the session explicitly, set a non-zero EXIT_REASON.
#
#   Costs one extra `has-session` call versus the best-effort poll in the
#   timeout case (the cap is checked INSIDE the loop, after an observation,
#   so the answer is always a fresh observation rather than an inference from
#   the tick counter). The sleep budget is identical: max_wait*5 ticks.
#
#   Assumes `tmux` is on PATH — every driver hard-fails at launch if it is not
#   (`command -v tmux || exit 1`). A missing tmux binary would make has-session
#   fail and read as "gone"; that is out of this poll's reach by design, and is
#   why the post-run assertion in run-cell.sh checks it can look at all.
require_tmux_session_gone() { # <session> [max_wait_secs]
  local session="$1" max_wait="${2:-2}"
  local ticks=$(( max_wait * 5 )) w=0
  while tmux has-session -t "$session" 2>/dev/null; do
    if [[ $w -ge $ticks ]]; then
      return 1   # cap expired and the session is STILL there — a real failure
    fi
    sleep 0.2
    w=$((w + 1))
  done
  return 0       # has-session failed: the session is OBSERVED gone
}

# wait_pid_gone <pid> [max_wait_secs]
#   Poll every 0.2s until <pid> no longer exists (kill -0 fails), capped at
#   <max_wait_secs> (default 1). No-op if <pid> is empty — callers that
#   couldn't resolve a pid should fall back to their original flat sleep.
wait_pid_gone() { # <pid> [max_wait_secs]
  local pid="$1" max_wait="${2:-1}"
  [[ -z "$pid" ]] && return 0
  local ticks=$(( max_wait * 5 )) w=0
  while [[ $w -lt $ticks ]] && kill -0 "$pid" 2>/dev/null; do
    sleep 0.2
    w=$((w + 1))
  done
}

# sigkill_and_wait <pid> [max_wait_secs]
#   The step_sigkill mechanics shared by every driver: kill -9 <pid> then
#   wait_pid_gone, or a flat sleep if <pid> couldn't be resolved. Callers keep
#   their own diagnostic echo (the PID lookup and log wording are
#   adapter-specific) — this only dedupes the kill+wait-or-sleep shape that
#   was copy-pasted near-identically across every driver's step_sigkill.
sigkill_and_wait() { # <pid> [max_wait_secs]
  local pid="$1" max_wait="${2:-1}"
  if [[ -n "$pid" ]]; then
    kill -9 "$pid" 2>/dev/null || true
    wait_pid_gone "$pid" "$max_wait"
  else
    sleep "$max_wait"
  fi
  return 0   # best-effort teardown: the kill is already `|| true`
}
