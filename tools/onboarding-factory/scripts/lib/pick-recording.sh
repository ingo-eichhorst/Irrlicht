#!/usr/bin/env bash
# pick-recording.sh — choose the .jsonl the daemon wrote for THIS run.
#
# Sourced by run-cell.sh. Two modes, matching the two ways a recording daemon
# can be reached:
#   - attached: the user's already-running irrlichd owns the recordings dir, so
#     the file for this run must be identified by freshness against a marker
#     dropped before the driver started;
#   - isolated/coexist: run-cell.sh spawned the daemon with its own staging
#     recordings dir, so whatever is in there is ours by construction.
#
# Why the attach path FAILS instead of falling back (#1333, finding B6). It used
# to end with:
#
#   if [[ -z "$RECORDING" ]]; then
#     # Fall back to the most-recent file regardless of mtime …
#     RECORDING="$(find … | xargs ls -1t | head -n1)"
#   fi
#
# But "nothing is newer than the attach marker" is PRECISELY the signal that the
# attached daemon is not recording this run — it is the detection, not a
# glitch to route around. The fallback discarded it and curated whatever ran
# last: the hermes onboarding run was handed a recording from the previous day
# and told the run had succeeded. The presence check further up (`ls
# "$dir"/*.jsonl`) cannot catch this either, and its own comment concedes as
# much ("prior recordings satisfy it").
#
# A guard that confirms PRESENCE has not confirmed FRESHNESS.

# pick_attached_recording <recordings-dir> <glob> <marker-file>
# Echoes the recording path on stdout; on failure writes a diagnosis to stderr
# and returns 1.
pick_attached_recording() {
  local dir="$1" glob="$2" marker="$3" rec
  # The daemon rotates by start-time, so a recording in progress always has the
  # newest name; several may match if it rotated mid-run. Take the last.
  rec="$(find "$dir" -maxdepth 1 -name "$glob" -type f -newer "$marker" 2>/dev/null | sort | tail -n1)"
  if [[ -z "$rec" ]]; then
    {
      echo "attach: no recording in $dir is newer than the attach marker"
      echo "        => the attached irrlichd was not recording during this run."
      echo "        Either it is not in --record mode, or it writes somewhere"
      echo "        other than \$IRRLICHT_RECORDINGS_DIR."
      echo "        Refusing to curate a stale recording from an earlier run."
      echo "        Fix the daemon, or record with an isolated/coexist daemon:"
      echo "          IRRLICHT_ONBOARD_HOME=/tmp/irr-onb \\"
      echo "          IRRLICHT_ONBOARD_BIND_ADDR=127.0.0.1:7838 \\"
      echo "          IRRLICHT_PERMISSION_MODE=grant-all  <run-cell/of record run …>"
    } >&2
    return 1
  fi
  echo "$rec"
}

# pick_isolated_recording <staging-recordings-dir> <glob>
pick_isolated_recording() {
  local dir="$1" glob="$2" rec
  rec="$(find "$dir" -maxdepth 1 -name "$glob" -type f 2>/dev/null | head -n1)"
  [[ -n "$rec" ]] || return 1
  echo "$rec"
}
