#!/usr/bin/env bash
# record-token-quota-exhausted.sh — record claudecode/token-quota-exhausted
# against a mocked Anthropic endpoint that 429s on the second request.
#
# Bypasses run-cell.sh because the standard driver does NOT pass `--bare`,
# which is required so claude reads ANTHROPIC_API_KEY + ANTHROPIC_BASE_URL
# from env (OAuth/keychain are otherwise always preferred). See FOLLOWUP
# below for the architectural fix (lift this into the canonical driver).
#
# FOLLOWUP: ~80% of this script duplicates drive-claudecode-interactive.sh
# (init_session, resolve_transcript, turn_count, exit_clean) and run-cell.sh's
# --attach mode (latest-recording resolution, curate, manifest). The right
# refactor is to (1) add `bare_mode` + `env` to the per-cell scenarios.json
# block, (2) patch drive-claudecode-interactive.sh init_session to honor
# them and to auto-accept the custom-API-key dialog, (3) launch the mock
# binary as a pre-driver hook in run-cell.sh. Tracked as a follow-up; the
# scenario itself works end-to-end with the bespoke script today.

set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

SCEN=token-quota-exhausted
TS=$(date -u +%Y%m%dT%H%M%S)
STAGING=".build/refresh/claudecode/${SCEN}-${TS}"
mkdir -p "$STAGING/cwd"

UUID=$(uuidgen | tr '[:upper:]' '[:lower:]')
PORT="${MOCK_PORT:-18765}"
MOCK_BIN=.build/refresh/bin/mock-anthropic-429
TMUX_SESSION="claudecode-mock429-$$"

# Refuse if the port is already bound — an orphaned mock from a crashed
# prior run would silently serve stale state and produce a garbage recording.
if lsof -iTCP:"$PORT" -sTCP:LISTEN -n -P >/dev/null 2>&1; then
  echo "port $PORT is already in use; aborting (kill the prior mock or set MOCK_PORT)" >&2
  exit 1
fi

mkdir -p .build/refresh/bin
go build -o "$MOCK_BIN" ./tools/onboarding-factory/recording/mock-anthropic-429/main.go

"$MOCK_BIN" --addr "127.0.0.1:$PORT" >"$STAGING/mock.log" 2>&1 &
MOCK_PID=$!

# Disarm the trap during cleanup so a double-Ctrl-C doesn't re-enter
# mid-teardown and leak the tmux session or mock process.
cleanup() {
  trap - EXIT INT TERM
  if [[ -n "${MOCK_PID:-}" ]]; then
    kill "$MOCK_PID" 2>/dev/null || true
    wait "$MOCK_PID" 2>/dev/null || true
  fi
  tmux kill-session -t "$TMUX_SESSION" 2>/dev/null || true
}
trap cleanup EXIT INT TERM

# TCP-readiness probe — non-consuming (does not increment the request
# counter, unlike the previous curl-based check which forced a mock restart
# to reset state).
for _ in $(seq 1 20); do
  if (echo >/dev/tcp/127.0.0.1/"$PORT") 2>/dev/null; then break; fi
  sleep 0.1
done
kill -0 "$MOCK_PID" 2>/dev/null || { echo "mock died on startup" >&2; cat "$STAGING/mock.log" >&2; exit 1; }

cat >"$STAGING/settings.json" <<'EOF'
{}
EOF

ABS_CWD="$(cd "$STAGING/cwd" && pwd)"
ABS_SETTINGS="$(cd "$STAGING" && pwd)/settings.json"
DRIVER_LOG="$STAGING/driver.log"
tmux new-session -d -s "$TMUX_SESSION" -c "$ABS_CWD" \
  -e "ANTHROPIC_API_KEY=sk-mock-key-for-recording-only" \
  -e "ANTHROPIC_BASE_URL=http://127.0.0.1:$PORT" \
  -e "CLAUDE_CODE_LOOP_PERSISTENT=" \
  -- claude --bare --settings "$ABS_SETTINGS" --session-id "$UUID"
tmux pipe-pane -t "$TMUX_SESSION" -o "cat >> '$DRIVER_LOG.stdout'"
echo "[recorder] tmux=$TMUX_SESSION uuid=$UUID cwd=$ABS_CWD"

# --bare skips workspace trust dialog AND auto mode, but claude STILL
# prompts to confirm a custom ANTHROPIC_API_KEY found in the environment
# ("Detected a custom API key in your environment ... 1. Yes  2. No
# (recommended)"). Default selection is "No"; typing "1" + Enter accepts.
sleep 3
tmux send-keys -t "$TMUX_SESSION" "1"
sleep 0.3
tmux send-keys -t "$TMUX_SESSION" Enter
echo "[recorder] accepted custom-API-key dialog"
sleep 3

send_line() {
  local line="$1"
  tmux send-keys -t "$TMUX_SESSION" -l -- "$line"
  sleep 0.3
  tmux send-keys -t "$TMUX_SESSION" Enter
}

send_line "Reply with exactly the word: ok"
echo "[recorder] sent prompt 1 (expect end_turn)"

TRANSCRIPT=""
for _ in $(seq 1 30); do
  for slug_dir in "$HOME"/.claude/projects/*/; do
    candidate="$slug_dir$UUID.jsonl"
    if [[ -f "$candidate" ]] && [[ -s "$candidate" ]]; then
      TRANSCRIPT="$candidate"
      break 2
    fi
  done
  sleep 0.5
done
echo "[recorder] transcript=$TRANSCRIPT"

turn_count() {
  if [[ -f "$TRANSCRIPT" ]]; then
    jq -r 'select(.type=="assistant" and .message.stop_reason=="end_turn") | "x"' \
      "$TRANSCRIPT" 2>/dev/null | wc -l | tr -d ' '
  else echo 0; fi
}
WAITED=0
while [[ "$WAITED" -lt 30 ]] && [[ "$(turn_count)" -lt 1 ]]; do
  sleep 1; WAITED=$((WAITED+1))
done
echo "[recorder] first turn_done observed (turns=$(turn_count))"
sleep 3

send_line "Reply with exactly the word: again"
echo "[recorder] sent prompt 2 (expect 429 from mock)"

# Let claude observe the 429 + start retrying, then Escape to abort the
# retry loop so the state cleanly settles. Without the Escape, claude
# stays in retry-working for ~10s and the recording window may close
# before the final ready transition lands.
sleep 5
tmux send-keys -t "$TMUX_SESSION" Escape
echo "[recorder] sent Escape to abort 429 retry loop"
sleep 8

tmux send-keys -t "$TMUX_SESSION" C-d
sleep 4
tmux kill-session -t "$TMUX_SESSION" 2>/dev/null

RECORDING_DIR="${IRRLICHT_RECORDINGS_DIR:-$HOME/.local/share/irrlicht/recordings}"
LATEST_RECORDING=$(ls -t "$RECORDING_DIR"/*.jsonl 2>/dev/null | head -1)
if [[ -z "$LATEST_RECORDING" ]]; then
  echo "no recording file found under $RECORDING_DIR" >&2; exit 1
fi
echo "[recorder] using recording: $LATEST_RECORDING"

sleep 6  # daemon's periodic flush + slack

mkdir -p "$STAGING/replaydata/agents"
tools/curate-lifecycle-fixture.sh \
  -d "$STAGING/replaydata/agents" \
  "$LATEST_RECORDING" \
  "$UUID" \
  "$TRANSCRIPT" \
  claudecode \
  "$SCEN"

jq -n --arg adapter claudecode --arg scen "$SCEN" --arg uuid "$UUID" --argjson port "$PORT" \
  '{adapter:$adapter, scenario:$scen, uuid:$uuid, mock_port:$port, error:null}' \
  >"$STAGING/run-manifest.json"

echo "[recorder] staged at $STAGING"
echo "next: tools/promote-recording.sh \"$STAGING\" claudecode $SCEN"
