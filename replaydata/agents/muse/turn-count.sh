#!/usr/bin/env bash
# turn-count.sh — muse's turn counter, sourced by driver-interactive.sh.
# Extracted so it can be unit-tested without executing the driver, which walks
# its recipe at source time (adapter-tables_test.sh B4 — an inline turn_count
# is invisible to replaydata/_lib/drive/turn-count_test.sh: nothing fails, it
# is simply not covered). Reads $TRANSCRIPT, echoes the completed-turn count.
# Rationale + the fleet's four historical defects in this seam:
# replaydata/_lib/drive/turn-count_test.sh
#
# Canonical muse turn-done shape, read off live session.jsonl files (#1960):
# one user prompt opens exactly one run (payload_type=runtime.session,
# kind=run, event=started, with its own run_id) and the run's last word is
# event=terminal (carries turn_duration_ms; terminal=cancelled when the turn
# was interrupted). model_completed fires per model call and task/completed
# per sub-task — both too granular to gate on. The predicate itself was
# verified live against a finalized single-prompt session: it matched exactly
# the one true ("run","terminal") record out of 88 runtime.session records
# with 20 distinct nested event.kind values — the defect below is entirely in
# what INPUT this reads (the driver's resolve_transcript can land on a
# subagent transcript instead of the driven session's own — see that
# function's comment), not in this predicate's logic.
#
# retained_frame wrapper check (#1960 driver audit): muse sometimes wraps a
# record's JSON as a *string* inside a `retained_frame` line's
# `children[].record_json`, which a naive line-per-record select could miss.
# Checked live across every session.jsonl on this machine (54 files, one
# retained_frame instance each observed in 43 of them): every retained_frame
# always wrapped exactly `runtime.session.permission_format_declared` /
# `runtime.session.permission_profile_committed`, never a run/terminal
# record — so this predicate is not vulnerable to it TODAY. That is an
# empirical observation over the corpus available on this machine, not a
# documented contract of muse's format: UNVERIFIED whether a future muse
# version ever wraps a run/terminal record the same way.
#
# FAIL-LOUD (#1960 driver audit defect #5, live-reproduced): a transcript
# whose only content was a malformed trailing JSONL fragment made jq exit
# nonzero with zero output — and the prior `2>/dev/null` swallowed that
# unconditionally, so "jq could not read the transcript at all" (missing
# binary, unreadable file, a malformed FIRST record) printed the identical
# "0" a genuine zero-turns read prints. Caveat, also verified live: when the
# malformed fragment follows an ALREADY-valid record (the realistic
# append-in-progress case of reading a live, growing file mid-flush), jq
# still emits the earlier match before it errors, so a nonzero exit with a
# match already counted is not a read failure — only nonzero-exit-with-
# zero-matches is.
#
# turn_count's stdout contract is unchanged (still just the numeric count —
# turn-count_test.sh's `count()` helper calls this as the last statement of a
# subshell and captures stdout, and every assert_count there expects an exact
# numeric match). The failure signal is carried on the RETURN CODE instead of
# a global: a global set inside this function while it's invoked as
# `now=$(turn_count)` would be invisible to the caller (checked live —
# variable writes inside a command-substitution subshell never propagate
# out), and piping through `wc -l` would lose jq's own exit status the same
# way (checked live — PIPESTATUS does not survive a command-substitution
# subshell boundary either). A function's own return code DOES cross that
# boundary via `$?` read immediately after `x=$(fn)` (checked live), so that
# is the channel used: return 1 exactly when jq's exit was nonzero AND it
# matched zero lines; return 0 otherwise (including the legitimate
# missing/empty-transcript "0" case, which turn-count_test.sh requires).
turn_count() {
  local t="${TRANSCRIPT:-}"
  if [[ -z "$t" || ! -f "$t" ]]; then
    echo 0
    return 0
  fi
  local out rc n
  out="$(jq -r 'select(.payload_type=="runtime.session" and ((.payload // {}).kind=="run") and ((((.payload // {}).event) // {}).kind=="terminal")) | "x"' \
    "$t" 2>/dev/null)"
  rc=$?
  if [[ -z "$out" ]]; then
    n=0
  else
    n="$(printf '%s\n' "$out" | wc -l | tr -d ' ')"
  fi
  echo "$n"
  if [[ $rc -ne 0 && $n -eq 0 ]]; then
    return 1
  fi
  return 0
}
