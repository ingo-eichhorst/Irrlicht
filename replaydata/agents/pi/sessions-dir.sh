#!/usr/bin/env bash
# sessions-dir.sh — resolve the pi sessions directory the way pi and the
# irrlicht pi adapter both resolve it, so a driver cannot look for a transcript
# in a different tree than the one the CLI wrote it into.
#
# THE DEFECT THIS REMOVES, which was silent and shipped in both pi drivers.
# agent-home.sh gives pi a rig-home row (PI_CODING_AGENT_DIR, opt-in), and
# drive-pi-interactive.sh already prefixes `env PI_CODING_AGENT_DIR=…` on its
# tmux launch precisely so the CLI honours it. Both drivers then hardcoded
#
#     PI_SESSIONS_DIR="$HOME/.pi/agent/sessions"
#
# to FIND the transcript afterwards. So the moment an operator actually used
# the row the drivers were built for, pi wrote its session into the scratch
# home and the driver searched the operator's real one, found nothing, cleared
# transcript.path and run-cell.sh reported
# `transcript_recording_or_uuid_missing` — with nothing anywhere naming the
# variable as the cause. It is the same daemon/CLI disagreement agent-home.sh's
# header describes for tmux, one layer further down: the isolation was wired
# for the writer and not for the reader.
#
# The resolution order is the adapter's, not an approximation of it —
# core/adapters/inbound/agents/pi/adapter.go's sessionsDir():
#
#   1. $PI_CODING_AGENT_SESSION_DIR names the sessions directory itself.
#   2. $PI_CODING_AGENT_DIR names the agent directory; sessions/ hangs off it.
#   3. Neither set → $HOME/.pi/agent/sessions.
#
# A NON-ABSOLUTE value is IGNORED at each step rather than being used or being
# an error, because that is what agentpaths.FromEnv does with one: it logs and
# falls back. Honouring a relative value here would put the driver on a path
# the daemon refuses, which is the disagreement this file exists to prevent
# rather than a stricter version of it.
#
# Sourced, not executed. Tested by tools/lib/pi-sessions-dir_test.sh.

# pi_sessions_dir prints the absolute directory pi writes its session
# transcripts into for the environment this process is running under.
pi_sessions_dir() {
  local v
  v="${PI_CODING_AGENT_SESSION_DIR:-}"
  if [[ "$v" == /* ]]; then
    printf '%s\n' "${v%/}"
    return 0
  fi
  v="${PI_CODING_AGENT_DIR:-}"
  if [[ "$v" == /* ]]; then
    printf '%s\n' "${v%/}/sessions"
    return 0
  fi
  printf '%s\n' "$HOME/.pi/agent/sessions"
  return 0
}
