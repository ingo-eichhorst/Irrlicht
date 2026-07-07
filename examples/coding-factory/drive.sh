#!/usr/bin/env bash
# Coding-factory tmux auto-driver: launch codex in a tmux session and feed it a
# small task on a loop, so the agent's row cycles working→ready live without a
# human. The cadence (trust dialog, boot waits, type→pause→Enter, task_complete
# turn detection) mirrors the project's proven codex driver
# (.claude/skills/ir:onboard-agent/scripts/drive-codex-interactive.sh).
#
# Attach to watch/take over:  docker compose exec -it <svc> tmux attach -t codex
set -uo pipefail   # -e deliberately omitted: the while loop handles errors itself

SESSION="codex"
CWD="$HOME/work"
LOG="$HOME/codex.pane.log"
TASK="${AGENT_TASK:-Make a small improvement to README.md and commit it.}"
SESSIONS_DIR="${CODEX_HOME:-$HOME/.codex}/sessions"

# Count completed turns across the session's rollout(s): the canonical turn-done
# signal is event_msg/task_complete.
turns() {
  find "$SESSIONS_DIR" -name 'rollout-*.jsonl' -exec cat {} + 2>/dev/null \
    | jq -r 'select(.type=="event_msg" and .payload.type=="task_complete") | "x"' 2>/dev/null \
    | wc -l | tr -d ' '
  return 0
}

# Start (or restart) codex in tmux and wait until it is ready for input.
# Truncates the log so banner-detection doesn't match an old run.
boot_codex() {
  tmux kill-session -t "$SESSION" 2>/dev/null || true
  > "$LOG"   # fresh log so 'OpenAI Codex' detection doesn't hit the old banner

  # --no-alt-screen keeps codex inline so tmux pipe-pane can capture it (alt-screen
  # would clear the pane and hide the banner/trust prompt).
  tmux new-session -d -s "$SESSION" -c "$CWD" codex --no-alt-screen
  tmux pipe-pane -t "$SESSION" -o "cat >> '$LOG'"

  # Accept the one-time "Do you trust the contents of this directory?" prompt
  # (poll the LIVE pane — it can render before pipe-pane flushes to the log).
  for _ in $(seq 1 60); do
    if tmux capture-pane -t "$SESSION" -p -S -40 2>/dev/null | grep -q 'Do you trust'; then
      tmux send-keys -t "$SESSION" "1"
      sleep 0.3
      tmux send-keys -t "$SESSION" Enter
      echo "[drive] accepted trust dialog" >&2
      break
    fi
    sleep 1
  done

  # Wait for the "OpenAI Codex" banner, then for the "Booting MCP" phase to clear
  # (keystrokes typed during boot have their Enter silently swallowed).
  for _ in $(seq 1 120); do grep -aq 'OpenAI Codex' "$LOG" 2>/dev/null && break; sleep 1; done
  for _ in $(seq 1 60); do
    tmux capture-pane -t "$SESSION" -p -S -20 2>/dev/null | grep -q 'Booting MCP' || break
    sleep 1
  done
  echo "[drive] codex booted — starting task loop" >&2
  return 0
}

boot_codex

# Coding-factory loop: send the task, wait for the turn to complete, let the row
# settle to `ready` so it's visible, then go again.
while true; do
  # If codex exited (user Ctrl-C, crash, API error), restart it instead of
  # crashing the container (which would cause a restart loop).
  if ! tmux has-session -t "$SESSION" 2>/dev/null; then
    echo "[drive] codex exited — restarting in 5s" >&2
    sleep 5
    boot_codex
    continue
  fi

  before="$(turns)"
  if ! tmux send-keys -t "$SESSION" -l -- "$TASK" 2>/dev/null; then
    sleep 2
    continue
  fi
  sleep 0.3                                   # let the text land before Enter (Ink input race)
  tmux send-keys -t "$SESSION" Enter 2>/dev/null || true
  echo "[drive] sent task; waiting for turn to complete" >&2
  for _ in $(seq 1 180); do
    [[ "$(turns)" -gt "$before" ]] && break
    # If codex died mid-task, stop waiting and let the outer loop restart it
    tmux has-session -t "$SESSION" 2>/dev/null || break
    sleep 1
  done
  sleep 20                                    # dwell on `ready` before the next task
done
