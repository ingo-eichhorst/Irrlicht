#!/usr/bin/env bash
# record-turn-aborted-by-error.sh — record claudecode/turn-aborted-by-error
# (Bucket A: provider 5xx AFTER partial stream → synthesized error text +
# terminal stop_reason in transcript → IsAgentDone() fires) against a
# mocked Anthropic endpoint that streams a partial response then emits a
# mid-stream `event: error` (overloaded_error).
#
# Sibling of record-token-quota-exhausted.sh — same scaffolding because
# claude --bare is needed for ANTHROPIC_API_KEY + ANTHROPIC_BASE_URL to
# take effect (OAuth/keychain are otherwise always preferred). See
# FOLLOWUP in the 429 recorder for the architectural fix.

set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

SCEN=turn-aborted-by-error
TS=$(date -u +%Y%m%dT%H%M%S)
STAGING=".build/refresh/claudecode/${SCEN}-${TS}"
mkdir -p "$STAGING/cwd"

UUID=$(uuidgen | tr '[:upper:]' '[:lower:]')
PORT="${MOCK_PORT:-18766}"
MOCK_BIN=.build/refresh/bin/mock-anthropic-5xx
TMUX_SESSION="claudecode-mock5xx-$$"

if lsof -iTCP:"$PORT" -sTCP:LISTEN -n -P >/dev/null 2>&1; then
  echo "port $PORT is already in use; aborting (kill the prior mock or set MOCK_PORT)" >&2
  exit 1
fi

mkdir -p .build/refresh/bin
go build -o "$MOCK_BIN" ./tools/onboarding-factory/recording/mock-anthropic-5xx/main.go

"$MOCK_BIN" --addr "127.0.0.1:$PORT" >"$STAGING/mock.log" 2>&1 &
MOCK_PID=$!

cleanup() {
  trap - EXIT INT TERM
  if [[ -n "${MOCK_PID:-}" ]]; then
    kill "$MOCK_PID" 2>/dev/null || true
    wait "$MOCK_PID" 2>/dev/null || true
  fi
  tmux kill-session -t "$TMUX_SESSION" 2>/dev/null || true
}
trap cleanup EXIT INT TERM

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

# Same custom-API-key dialog handling as the 429 recorder.
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
echo "[recorder] sent prompt (expect mid-stream error → synthesized abort)" >&2

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

# Wait for SOMETHING terminal to land in the transcript. The exact
# shape (refusal stop_reason, synthesized assistant text, system event)
# varies by claude version — we just need the working→ready transition
# to be triggerable from the JSONL stream. Time out at 30s.
WAITED=0
while [[ "$WAITED" -lt 30 ]]; do
  # Any assistant line with a stop_reason field (any value), or any
  # system event whose subtype implies turn termination.
  if [[ -f "$TRANSCRIPT" ]] && jq -e 'select(.type=="assistant" and .message.stop_reason!=null) | .' "$TRANSCRIPT" >/dev/null 2>&1; then
    break
  fi
  sleep 1; WAITED=$((WAITED+1))
done
echo "[recorder] terminal event observed (waited=${WAITED}s)"

# Brief settle for the daemon to record the state transition.
sleep 4

tmux send-keys -t "$TMUX_SESSION" C-d
sleep 4
tmux kill-session -t "$TMUX_SESSION" 2>/dev/null

RECORDING_DIR="${IRRLICHT_RECORDINGS_DIR:-$HOME/.local/share/irrlicht/recordings}"
LATEST_RECORDING=$(ls -t "$RECORDING_DIR"/*.jsonl 2>/dev/null | head -1)
if [[ -z "$LATEST_RECORDING" ]]; then
  echo "no recording file found under $RECORDING_DIR" >&2; exit 1
fi
echo "[recorder] using recording: $LATEST_RECORDING"

sleep 6

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
echo "next: inspect $STAGING/replaydata/agents/claudecode/scenarios/${SCEN}/ + run expected-validate against it"
