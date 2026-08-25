#!/usr/bin/env bash
# MUST REFUSE — a quote that is still open when the file ends.
#
# The joiner walks to end of file looking for the closing quote and then simply
# stops, and the word scanner swallows every remaining line into one quoted
# word. Both statements below disappear, including the `tmux kill-session`, so
# without this refusal an unreadable file and a clean file are the same answer.
set -euo pipefail

SESSION="fixture-onboard-$(date +%s)-$$-1"
tmux new-session -d -s "$SESSION" -x 200 -y 50 "fixture-agent"

echo 'this quote is never closed
tmux kill-session -t "$SESSION" 2>/dev/null || true
