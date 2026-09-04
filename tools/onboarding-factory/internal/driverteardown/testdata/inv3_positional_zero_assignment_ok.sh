#!/usr/bin/env bash
set -euo pipefail

TMUX_NAME="fixture-onboard-$$"
SESSION="$TMUX_NAME"

cleanup() {
  [[ -n "$SESSION" ]] && tmux kill-session -t "$SESSION" 2>/dev/null || true
}
trap cleanup EXIT

tmux new-session -d -s "$TMUX_NAME" "fixture-agent"

# $0 is a positional expansion with index zero, not a named shell variable.
# Neither assignment can make NOT_A_SESSION carry the tmux session name.
TMUX_NAME=$0
NOT_A_SESSION=$0
NOT_A_SESSION="not-a-session"

tmux kill-session -t "$SESSION" 2>/dev/null || true
