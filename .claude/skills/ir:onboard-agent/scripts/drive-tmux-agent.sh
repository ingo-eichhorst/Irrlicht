#!/usr/bin/env bash
# drive-tmux-agent.sh — drive an interactive REPL agent in a detached tmux
# session: send a prompt, poll for completion, capture output, tear down.
#
# Generic over the agent CLI — used by the post-discovery live-recording
# smoke (see .claude/skills/ir:onboard-agent/discovery-instructions.md).
# Designed to be reused for any future REPL-style agent.
#
# Usage:
#   drive-tmux-agent.sh <session-name> <staging-dir> <prompt> -- <cmd> [args...]
#
# Example:
#   drive-tmux-agent.sh aider-smoke .build/manual-aider \
#     "create hello.py with print hello world" -- aider --no-auto-commits --yes
#
# Outputs:
#   <staging-dir>/pane.log         — final tmux pane buffer (cumulative output)
#   <staging-dir>/pane-history.log — full scrollback if available

set -euo pipefail

if [[ $# -lt 4 ]]; then
  sed -n '1,17p' "$0" >&2
  exit 2
fi

SESSION="$1"; STAGING="$2"; PROMPT="$3"; shift 3
[[ "${1:-}" == "--" ]] && shift
[[ $# -ge 1 ]] || { echo "missing agent command" >&2; exit 2; }

mkdir -p "$STAGING"
PANE_LOG="$STAGING/pane.log"
HIST_LOG="$STAGING/pane-history.log"

# Tear down any prior session with the same name (defensive).
if tmux has-session -t "$SESSION" 2>/dev/null; then
  tmux kill-session -t "$SESSION"
fi

# Start detached session running the agent in the staging dir's cwd.
# The session inherits the caller's PATH so the agent CLI resolves.
tmux new-session -d -s "$SESSION" -c "$STAGING" "$@"
echo "tmux session started: $SESSION (cmd: $*)" >&2

# Wait briefly for the REPL to be ready.
sleep 3

# Send the prompt + Enter.
tmux send-keys -t "$SESSION" -- "$PROMPT" Enter
echo "prompt sent: $PROMPT" >&2

# Poll the pane buffer for completion. Heuristic: agent is "done" when the
# last few lines look like the agent is back at its prompt indicator.
# Cap the wait at 90s — long enough for an agent turn including a tool call.
deadline=$(( $(date +%s) + 90 ))
while [[ $(date +%s) -lt $deadline ]]; do
  tmux capture-pane -t "$SESSION" -p > "$PANE_LOG" 2>/dev/null || break
  # Heuristic: agent is back at prompt when last non-empty line ends with > or $
  # OR contains a known idle marker.
  last=$(tail -5 "$PANE_LOG" | grep -v '^[[:space:]]*$' | tail -1 || true)
  if [[ "$last" =~ \>[[:space:]]*$ ]] || \
     [[ "$last" =~ \$[[:space:]]*$ ]] || \
     [[ "$last" =~ "Tokens:" ]] || \
     [[ "$last" =~ "Cost:" ]]; then
    echo "completion detected: $last" >&2
    break
  fi
  sleep 2
done

# Final capture (full scrollback when supported).
tmux capture-pane -t "$SESSION" -p -S - > "$HIST_LOG" 2>/dev/null || cp "$PANE_LOG" "$HIST_LOG"

# Clean exit: send Ctrl-C then Ctrl-D to close the agent gracefully.
tmux send-keys -t "$SESSION" C-c 2>/dev/null || true
sleep 1
tmux send-keys -t "$SESSION" C-d 2>/dev/null || true
sleep 1
tmux kill-session -t "$SESSION" 2>/dev/null || true

echo "captured: $PANE_LOG ($(wc -l < "$PANE_LOG" | tr -d ' ') lines), $HIST_LOG ($(wc -l < "$HIST_LOG" | tr -d ' ') lines)" >&2
