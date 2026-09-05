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
#
#   THE CAP CALLERS SHOULD PASS IS DRIVE_EXIT_CLEAN_CAP_S, below — not a literal,
#   and NOT this function's `max_wait` default of 2, which exists only so the
#   best-effort sibling's signature stays unchanged.
require_tmux_session_gone() { # <session> [max_wait_secs]
  local session="$1" max_wait="${2:-2}"
  local ticks=$(( max_wait * 5 )) w=0
  while tmux has-session -t "$session" 2>/dev/null; do
    if [[ $w -ge $ticks ]]; then
      _drive_record_teardown_timing "$session" "$w" capped
      return 1   # cap expired and the session is STILL there — a real failure
    fi
    sleep 0.2
    w=$((w + 1))
  done
  _drive_record_teardown_timing "$session" "$w" observed
  return 0       # has-session failed: the session is OBSERVED gone
}

# TMUX_SESSION_GONE_ELAPSED_S — how long the poll above actually waited, in
# seconds to one decimal. Set by every require_tmux_session_gone call, on both
# the observed and the capped path.
#
# WHY THIS EXISTS (#1828 item 5). DRIVE_EXIT_CLEAN_CAP_S below is a stated
# BOUND, not a measurement, and its own comment says what a real number would
# take: "per adapter, wall-clock from the exit key to `tmux has-session`
# failing, on a loaded host, over enough runs to see the tail". That reads like
# a recording campaign nobody has time for. It is not — the loop above already
# counts exactly that wall-clock in `w`, and threw the number away on every run
# the rig has ever done. Recording it costs nothing and makes the measurement a
# by-product of ordinary recording rather than a campaign of its own.
TMUX_SESSION_GONE_ELAPSED_S=0

# _drive_record_teardown_timing <session> <ticks> <observed|capped>
#
# Publishes the elapsed time, and appends one row to $DRIVE_TEARDOWN_TIMINGS
# when the rig set that path. Deliberately env-driven rather than a per-driver
# call: nine drivers already hand this cap to require_tmux_session_gone, and a
# per-driver recording line is the duplication #1828 item 4 existed to remove.
# run-cell.sh exports the path, so every adapter is instrumented by sourcing
# this file — including adapters added later, which is the point.
#
# A row is `<epoch>\t<session>\t<seconds>\t<outcome>`. `capped` rows matter
# MORE than `observed` ones: they are the runs where the deadline truncated a
# TUI mid-flush, and a table fitted without them would be fitted on exactly the
# cases the cap is supposed to bound.
#
# An append that FAILS says so on stderr rather than returning quietly. Per
# AGENTS.md a mechanism that cannot run must not read like one that found
# nothing — and "no rows" is a legitimate answer here (a recipe with no
# exit_clean step), so silence would be indistinguishable from an unwritable
# staging dir. It never fails the run: this is instrumentation beside the
# teardown, and a recording is not worth losing over a timings file.
_drive_record_teardown_timing() { # <session> <ticks> <outcome>
  local session="$1" ticks="$2" outcome="$3"
  # Integer arithmetic only: each tick is 0.2s, so ticks/5 seconds and
  # (ticks%5)*2 tenths. bash 3.2 (the recording host's /bin/bash) has no
  # floating point, and shelling out to bc or awk per teardown would be a real
  # cost on a poll that otherwise costs one has-session call.
  TMUX_SESSION_GONE_ELAPSED_S="$(( ticks / 5 )).$(( (ticks % 5) * 2 ))"
  [ -n "${DRIVE_TEARDOWN_TIMINGS:-}" ] || return 0
  if ! printf '%s\t%s\t%s\t%s\n' \
       "$(date +%s)" "$session" "$TMUX_SESSION_GONE_ELAPSED_S" "$outcome" \
       >>"$DRIVE_TEARDOWN_TIMINGS" 2>/dev/null; then
    echo "[driver] WARNING: could not append a teardown timing to" \
         "$DRIVE_TEARDOWN_TIMINGS — this run contributes no data point for the" \
         "DRIVE_EXIT_CLEAN_CAP_S measurement (#1828). The teardown itself was" \
         "unaffected: the session was $outcome after ${TMUX_SESSION_GONE_ELAPSED_S}s." >&2
  fi
  return 0
}

# DRIVE_EXIT_CLEAN_CAP_S — the cap every driver's step_exit_clean hands to
# require_tmux_session_gone. Defined ONCE: this is a figure that documents
# behaviour, and a number re-typed at eight call sites plus eight log strings
# drifts away from the justification below (AGENTS.md, "a number typed once and
# repeated by hand drifts silently away from what it measured").
#
# HOW THIS NUMBER WAS ARRIVED AT — it is NOT a measurement, and saying so is the
# whole point (#1825 review, finding 3).
#
# Six drivers carried `2`. That 2 was inherited verbatim from the flat `sleep 2`
# the #1018 poll extraction replaced — see wait_tmux_session_gone above, whose
# own doc says "the same duration as the sleep it replaces, so worst-case timing
# never regresses". Under that sleep, 2s was a SETTLE BUDGET and overrunning it
# was FREE: a TUI that took 3s to die still died, the wait just ended early, and
# nothing failed. #1825 gave that same number a second and far harsher meaning —
# at the cap the driver now SIGHUPs the pane with `tmux kill-session` and sets a
# non-zero EXIT_REASON, which run-cell-multi.sh escalates into failing the whole
# cross-adapter run. A duration chosen when overrunning was free had never been
# justified as a deadline that truncates a transcript and fails a run, and the
# fleet's own inconsistency was the tell: copilot and mistral-vibe were given 15.
#
# WHY NOT A MEASURED PER-ADAPTER TABLE. That needs live recording runs per
# adapter on a loaded host, repeated enough to see the tail — a recording-rig
# exercise, not something this change can produce. It would also have holes in
# exactly the adapters whose slow case is being bounded: gemini-cli cannot be
# driven on this host at all (its account is tier-ineligible — see that driver's
# own note at driver-interactive.sh, "gemini-cli is NOT recordable on the machine
# this was written on"), and it is one of the two Ink/node TUIs whose V8 teardown
# plus final transcript flush is the latency actually at issue.
#
# So the cap is a deliberately GENEROUS bound rather than a fitted one: 15s, the
# value copilot and mistral-vibe already carried (mistral-vibe's step_exit_clean
# calls its teardown "the slowest in the fleet"), applied uniformly so that no
# adapter is held to a tighter deadline than the slowest one anyone has looked
# at. Raising it is close to free because the poll returns on the FIRST
# successful observation: a clean exit costs one `tmux has-session` that fails,
# exactly what `2` cost. The cap only bounds the pathological case — and there
# the outcome it prevents is a truncated transcript plus a spurious driver fault
# on an agent that was still flushing.
#
# Lowering it again needs a measurement this comment could not take: per
# adapter, wall-clock from the exit key to `tmux has-session` failing, on a
# loaded host, over enough runs to see the tail.
#
# THAT DATA IS NOW BEING COLLECTED (#1828 item 5). The number was never out of
# reach — require_tmux_session_gone above always counted it and always discarded
# it. It now publishes TMUX_SESSION_GONE_ELAPSED_S and appends a row per
# teardown to $DRIVE_TEARDOWN_TIMINGS, which run-cell.sh and run-cell-multi.sh
# both set and fold into the run manifest. So every ordinary recording run
# contributes a data point, and fitting a per-adapter table becomes a question
# of reading manifests rather than of mounting a campaign.
#
# What is still open, stated so nobody reads the collection as the conclusion:
# nothing has been fitted yet, this host contributes no gemini-cli rows at all
# (its account is tier-ineligible), and gemini-cli is one of the two Ink/node
# TUIs whose teardown is the latency actually being bounded. Until there are
# rows across adapters AND a tail worth looking at, 15 stays what it says it
# is: a generous bound, not a fitted one.
# shellcheck disable=SC2034  # read by the DRIVERS that source this lib (each
# step_exit_clean), never by this file.
DRIVE_EXIT_CLEAN_CAP_S=15

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
