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
