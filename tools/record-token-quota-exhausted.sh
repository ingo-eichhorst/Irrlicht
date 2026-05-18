#!/usr/bin/env bash
# record-token-quota-exhausted.sh — record claudecode/token-quota-exhausted
# against a mocked Anthropic endpoint that 429s on the second request.
#
# Bypasses run-cell.sh because the standard driver does NOT pass `--bare`,
# which is required so claude reads ANTHROPIC_API_KEY + ANTHROPIC_BASE_URL
# from env (OAuth/keychain are otherwise always preferred).
#
# Flow:
#   1. Build mock-anthropic-429 + start it on a free local port.
#   2. Generate fresh UUID + cwd under .build/refresh/claudecode/<scenario>-<TS>/.
#   3. Launch tmux: claude --bare --settings <empty> --session-id <UUID>
#      with ANTHROPIC_API_KEY=sk-fake ANTHROPIC_BASE_URL=http://127.0.0.1:<port>
#   4. Send prompt 1 (mock returns 200 → end_turn → state ready).
#   5. Send prompt 2 (mock returns 429 → state working→ready, no stick).
#   6. Sleep, exit cleanly, resolve transcript path.
#   7. Curate lifecycle fixture from the user's running --record daemon.
#   8. Hand the staged dir to promote-recording.sh.

set -uo pipefail
cd "$(git rev-parse --show-toplevel)"

SCEN=token-quota-exhausted
TS=$(date -u +%Y%m%dT%H%M%S)
STAGING=".build/refresh/claudecode/${SCEN}-${TS}"
mkdir -p "$STAGING/cwd"

UUID=$(uuidgen | tr '[:upper:]' '[:lower:]')
PORT=18765
MOCK_BIN=.build/refresh/bin/mock-anthropic-429
TMUX_SESSION="claudecode-mock429-$$"

# 1. Build mock.
mkdir -p .build/refresh/bin
go build -o "$MOCK_BIN" ./tools/mock-anthropic-429.go
[ -x "$MOCK_BIN" ] || { echo "mock build failed"; exit 1; }

# 2. Start mock.
"$MOCK_BIN" --addr "127.0.0.1:$PORT" >"$STAGING/mock.log" 2>&1 &
MOCK_PID=$!
trap 'kill $MOCK_PID 2>/dev/null; tmux kill-session -t "$TMUX_SESSION" 2>/dev/null; exit' EXIT INT TERM
sleep 0.5

# Sanity: mock is up.
if ! curl -sfo /dev/null -X POST -H 'Content-Type: application/json' \
       -d '{}' "http://127.0.0.1:$PORT/v1/messages" 2>/dev/null; then
  : # 429-side ok; we're checking the port is alive. /v1/messages with empty
    # body returns a body either way — we just want a TCP response.
fi

# Recreate mock state for the real recording (we just consumed request #1
# during the sanity check).
kill $MOCK_PID 2>/dev/null
wait $MOCK_PID 2>/dev/null
"$MOCK_BIN" --addr "127.0.0.1:$PORT" >"$STAGING/mock.log" 2>&1 &
MOCK_PID=$!
sleep 0.3

# Empty settings file so claude doesn't try to read user/project settings.
cat >"$STAGING/settings.json" <<'EOF'
{}
EOF

# 3. Launch claude in tmux under --bare with mocked env.
ABS_CWD="$(cd "$STAGING/cwd" && pwd)"
ABS_SETTINGS="$(cd "$STAGING" && pwd)/settings.json"
DRIVER_LOG="$STAGING/driver.log"
tmux kill-session -t "$TMUX_SESSION" 2>/dev/null || true
tmux new-session -d -s "$TMUX_SESSION" -c "$ABS_CWD" \
  -e "ANTHROPIC_API_KEY=sk-mock-key-for-recording-only" \
  -e "ANTHROPIC_BASE_URL=http://127.0.0.1:$PORT" \
  -e "CLAUDE_CODE_LOOP_PERSISTENT=" \
  -- claude --bare --settings "$ABS_SETTINGS" --session-id "$UUID"
tmux pipe-pane -t "$TMUX_SESSION" -o "cat >> '$DRIVER_LOG.stdout'"
echo "[recorder] tmux=$TMUX_SESSION uuid=$UUID cwd=$ABS_CWD"

# --bare skips workspace trust dialog AND auto mode, but claude STILL
# prompts to confirm a custom ANTHROPIC_API_KEY found in the environment
# ("Detected a custom API key in your environment ... Do you want to use
# this API key?  1. Yes  2. No (recommended)"). The default selection is
# "No" — typing text into the dialog filters the choices. We accept by
# typing "1" + Enter explicitly before the first real prompt.
sleep 3
tmux send-keys -t "$TMUX_SESSION" "1"
sleep 0.3
tmux send-keys -t "$TMUX_SESSION" Enter
echo "[recorder] accepted custom-API-key dialog"
sleep 3

# 4. Prompt 1: mock returns 200 + end_turn.
tmux send-keys -t "$TMUX_SESSION" -l -- "Reply with exactly the word: ok"
sleep 0.3
tmux send-keys -t "$TMUX_SESSION" Enter
echo "[recorder] sent prompt 1 (expect end_turn)"

# Resolve transcript path.
TRANSCRIPT=""
for _ in $(seq 1 30); do
  for slug_dir in "$HOME"/.claude/projects/*/; do
    candidate="$slug_dir$UUID.jsonl"
    if [ -f "$candidate" ] && [ -s "$candidate" ]; then
      TRANSCRIPT="$candidate"
      break 2
    fi
  done
  sleep 0.5
done
echo "[recorder] transcript=$TRANSCRIPT"

# Wait until first end_turn appears in the transcript.
turn_count() {
  if [ -f "$TRANSCRIPT" ]; then
    jq -r 'select(.type=="assistant" and .message.stop_reason=="end_turn") | "x"' \
      "$TRANSCRIPT" 2>/dev/null | wc -l | tr -d ' '
  else echo 0; fi
}
WAITED=0
while [ $WAITED -lt 30 ] && [ "$(turn_count)" -lt 1 ]; do
  sleep 1; WAITED=$((WAITED+1))
done
echo "[recorder] first turn_done observed (turns=$(turn_count))"
sleep 3  # let the daemon's classifier settle the ready transition

# 5. Prompt 2: mock returns 429 → claude's quota-exhausted path fires.
tmux send-keys -t "$TMUX_SESSION" -l -- "Reply with exactly the word: again"
sleep 0.3
tmux send-keys -t "$TMUX_SESSION" Enter
echo "[recorder] sent prompt 2 (expect 429 from mock)"

# Let claude observe the 429 + start retrying. Then Escape to abort the
# retry loop so the state transitions back to ready cleanly (without that,
# claude can spend ~10s in retry-working state and the recording window may
# close before the final ready lands).
sleep 5
tmux send-keys -t "$TMUX_SESSION" Escape
echo "[recorder] sent Escape to abort 429 retry loop"
sleep 8

# 6. Exit cleanly (Ctrl-D).
tmux send-keys -t "$TMUX_SESSION" C-d
sleep 4
tmux kill-session -t "$TMUX_SESSION" 2>/dev/null

# 7. Curate the staged fixture from the user's running daemon's recording.
RECORDING_DIR="${IRRLICHT_RECORDINGS_DIR:-$HOME/.local/share/irrlicht/recordings}"
LATEST_RECORDING=$(ls -t "$RECORDING_DIR"/*.jsonl 2>/dev/null | head -1)
if [ -z "$LATEST_RECORDING" ]; then
  echo "no recording file found under $RECORDING_DIR" >&2; exit 1
fi
echo "[recorder] using recording: $LATEST_RECORDING"

# Wait a beat for the recorder's periodic flush + safety slack.
sleep 6

mkdir -p "$STAGING/replaydata/agents"
tools/curate-lifecycle-fixture.sh \
  -d "$STAGING/replaydata/agents" \
  "$LATEST_RECORDING" \
  "$UUID" \
  "$TRANSCRIPT" \
  claudecode \
  "$SCEN"

# Drop a manifest so promote-recording.sh has something to look at.
cat >"$STAGING/run-manifest.json" <<EOF
{"adapter":"claudecode","scenario":"$SCEN","uuid":"$UUID","mock_port":$PORT,"error":null}
EOF

echo "[recorder] staged at $STAGING"
echo "next: tools/promote-recording.sh \"$STAGING\" claudecode $SCEN"
