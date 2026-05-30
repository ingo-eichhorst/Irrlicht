#!/usr/bin/env bash
# reconcile.sh — session-id reconciliation helpers for the recording rig.
# Sourced by run-cell-multi.sh; unit-tested by lib/reconcile_test.sh.
#
# The daemon's session_id often differs from the agent's driver-written id:
# the daemon keys codex/pi by the transcript-file STEM (rollout-…, <ts>_<uuid>)
# while drivers write the bare header `.id` for fixture-naming parity. These
# helpers map a driver id to the daemon-recorded session_id, verify a
# reconciled id is real, and reconcile a whole multi-slot driver run while
# keeping uuid<->path pairing.
#
# All three read the recording from the RECORDING global (a single
# events.jsonl path) — set it before calling. No side effects beyond stdout.
# This file is a pure library: it defines functions only and MUST NOT call
# `set` (that would leak options into the sourcing shell).

# daemon_sid_for_transcript <transcript-path> <adapter-slug> <fallback-sid>
#   → the session_id the daemon recorded for this transcript (matched by
#     transcript_path AND adapter in the transcript_new event), else
#     <fallback-sid>.
#   The .adapter pin mirrors run-cell.sh: it disambiguates a shared
#   transcript_path across adapters, and is a deliberate no-op for claudecode
#   (its daemon adapter name is "claude-code" ≠ the "claudecode" slug, so the
#   pin finds nothing and the fallback — claudecode's UUID, which already
#   equals the daemon session_id — is preserved). Callers MUST then confirm
#   the result actually appears in the recording (sid_in_recording) rather
#   than trusting a silent fallback.
daemon_sid_for_transcript() {
  local path="$1" ad="$2" fallback="$3" sid
  [[ -n "$path" ]] || { echo "$fallback"; return; }
  sid="$(jq -r --arg path "$path" --arg ad "$ad" '
    select(.adapter==$ad and .kind=="transcript_new" and .transcript_path==$path)
    | [.seq, .session_id] | @tsv' "$RECORDING" 2>/dev/null \
    | sort -n | head -n1 | cut -f2)"
  [[ -n "$sid" ]] && echo "$sid" || echo "$fallback"
}

# sid_in_recording <session-id> → exit 0 iff the id appears as a session_id in
# the recording. Catches a daemon_sid_for_transcript FALLBACK the daemon never
# recorded (transcript_path mismatch / unobserved transcript), which would
# curate a silently-empty per-adapter arc.
sid_in_recording() {
  local sid="$1"
  [[ -n "$sid" ]] || return 1
  [[ -n "$(jq -r --arg sid "$sid" \
        'select(.session_id==$sid) | .session_id' "$RECORDING" 2>/dev/null \
        | head -n1)" ]]
}

# reconcile_slot_csv <uuids-file> <paths-file> <adapter-slug>
#   → one reconciled session_id per line, for every NON-EMPTY uuid slot.
#   Reads both files into LINE-INDEXED arrays WITHOUT compacting, so an empty
#   entry in one file keeps its slot and uuid[i] still pairs with path[i].
#   (Compacting only one stream desyncs every later pairing when a slot has a
#   non-empty uuid but an unresolved/empty transcript line — the bug this
#   replaced.) bash 3.2 — no readarray.
reconcile_slot_csv() {
  local uuids_file="$1" paths_file="$2" ad="$3"
  local slot s sp
  local slot_uuids=() slot_paths=()
  while IFS= read -r s; do slot_uuids+=("$s"); done < "$uuids_file"
  while IFS= read -r sp; do slot_paths+=("$sp"); done < "$paths_file"
  for slot in ${slot_uuids[@]+"${!slot_uuids[@]}"}; do
    s="${slot_uuids[$slot]}"
    [[ -z "$s" ]] && continue
    sp="${slot_paths[$slot]:-}"
    daemon_sid_for_transcript "$sp" "$ad" "$s"
  done
}
