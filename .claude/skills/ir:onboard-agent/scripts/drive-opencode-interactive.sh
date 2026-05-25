#!/usr/bin/env bash
# drive-opencode-interactive.sh — drive opencode via headless `opencode run`
# subprocess invocations, executing a step-script (send / wait_turn / sleep).
#
# OpenCode has a true headless mode (`opencode run`) — each `send` step
# launches an `opencode run` subprocess and waits for it to complete.
# `wait_turn` becomes a no-op because `opencode run` already blocks until
# the turn ends. This is structurally simpler than the claudecode tmux+TUI
# driver and matches how opencode is most often automated.
#
# Session continuity: the first `send` launches a fresh session in
# <staging>/cwd; subsequent `send` steps use `--session <id>` with the
# captured id so the conversation chains within one session record.
#
# Contract files written to <staging-dir>:
#   driver.log[.stdout|.stderr]  — captured CLI output
#   driver.exit-reason           — ok|timeout|killed|nonzero(N)
#   transcript.path              — absolute path to the exported parts JSONL
#   session.uuid                 — opencode session id (ses_…)
#
# Usage:
#   drive-opencode-interactive.sh <staging-dir> <preferred-uuid> \
#       <timeout-seconds> <settings-path> <script-json>
#
# The preferred-uuid is ignored — opencode auto-assigns session ids and
# does not accept a caller-chosen one.

set -euo pipefail

if [[ $# -ne 5 ]]; then
  echo "usage: drive-opencode-interactive.sh <staging> <uuid-ignored> <timeout-s> <settings-path> <script-json>" >&2
  exit 2
fi

STAGING="$1"
# UUID ($2) ignored — opencode mints its own session id.
TIMEOUT_S="$3"
# SETTINGS_PATH ($4) accepted for contract parity; opencode reads its
# config from ~/.config/opencode/opencode.json today, not a per-run
# settings file. Reserved for future use.
SCRIPT_JSON="$5"

mkdir -p "$STAGING"
DRIVER_LOG="$STAGING/driver.log"

# Per-run cwd so each scenario launches a fresh opencode project context.
# OpenCode keys sessions on the directory column in the SQLite session
# table; isolating cwd guarantees the session-lookup query at the end
# finds OUR session even if the user has other recent opencode runs.
# A CROSS-ADAPTER cell (multiple-agents-same-workspace) forces a SHARED
# workspace via $IRRLICHT_ONBOARD_CWD so a different adapter coexists in
# the same cwd — the daemon then keys both sessions to the same cwd slug.
# The session-lookup query still finds OUR session: opencode.db's session
# table only ever holds opencode sessions, so directory = $RUN_CWD +
# ORDER BY time_created DESC picks our row regardless of the partner agents
# (which write to their own stores, never opencode.db).
RUN_CWD="${IRRLICHT_ONBOARD_CWD:-$STAGING/cwd}"
mkdir -p "$RUN_CWD"

OPENCODE_DB="$HOME/.local/share/opencode/opencode.db"
if [[ ! -f "$OPENCODE_DB" ]]; then
  echo "opencode database not found at $OPENCODE_DB — is opencode installed?" >&2
  exit 1
fi

DEADLINE=$(( $(date +%s) + TIMEOUT_S ))
EXIT_REASON="ok"
SESSION_ID=""

remaining_seconds() {
  local now
  now=$(date +%s)
  if (( now >= DEADLINE )); then
    echo 0
  else
    echo $((DEADLINE - now))
  fi
}

run_send() {
  local text="$1"
  local args=()
  if [[ -n "$SESSION_ID" ]]; then
    args+=(--session "$SESSION_ID")
  fi

  local remaining
  remaining=$(remaining_seconds)
  if (( remaining <= 0 )); then
    EXIT_REASON="timeout"
    return 1
  fi

  echo "[driver] send (session=${SESSION_ID:-<new>}, remaining=${remaining}s): $text" >&2
  set +e
  ( cd "$RUN_CWD" && \
    timeout --signal=SIGINT --kill-after=10 "$remaining" \
      opencode run --format default ${args[@]+"${args[@]}"} -- "$text" \
      >>"$DRIVER_LOG.stdout" 2>>"$DRIVER_LOG.stderr" )
  local rc=$?
  set -e

  case "$rc" in
    0)   ;;
    124) EXIT_REASON="timeout"; return 1 ;;
    137) EXIT_REASON="killed";  return 1 ;;
    *)   EXIT_REASON="nonzero($rc)"; return 1 ;;
  esac

  # Capture the session id after the first send. The session row is
  # created by `opencode run` and keyed on directory = $RUN_CWD; order
  # by time_created DESC so retries reusing a stale staging dir pick
  # the NEW session, not a leftover row whose time_updated may briefly
  # outrank the fresh row before its first part lands.
  if [[ -z "$SESSION_ID" ]]; then
    SESSION_ID=$(sqlite3 "$OPENCODE_DB" \
      "SELECT id FROM session WHERE directory = '$RUN_CWD' ORDER BY time_created DESC LIMIT 1;")
    if [[ -z "$SESSION_ID" ]]; then
      echo "[driver] WARN: no session row found for cwd=$RUN_CWD" >&2
    else
      echo "[driver] captured session_id=$SESSION_ID" >&2
    fi
  fi
  return 0
}

# Iterate steps.
STEP_COUNT=$(jq 'length' <<<"$SCRIPT_JSON")
for (( i = 0; i < STEP_COUNT; i++ )); do
  STEP=$(jq -c ".[$i]" <<<"$SCRIPT_JSON")
  TYPE=$(jq -r '.type' <<<"$STEP")
  case "$TYPE" in
    send)
      TEXT=$(jq -r '.text' <<<"$STEP")
      run_send "$TEXT" || break
      ;;
    wait_turn)
      # opencode run blocks until the turn ends — wait_turn is a no-op.
      :
      ;;
    sleep)
      SECONDS_=$(jq -r '.seconds // empty' <<<"$STEP")
      # Reject missing/non-numeric values so an authoring typo
      # (`{"type":"sleep"}` without seconds) doesn't silently abort the
      # whole script under `set -e` with no exit-reason file written.
      if ! [[ "$SECONDS_" =~ ^[0-9]+(\.[0-9]+)?$ ]]; then
        echo "[driver] ERROR: sleep step missing or non-numeric 'seconds': $STEP" >&2
        EXIT_REASON="nonzero(2)"
        break
      fi
      echo "[driver] sleep ${SECONDS_}s" >&2
      sleep "$SECONDS_"
      ;;
    *)
      echo "[driver] ERROR: unknown step type '$TYPE'" >&2
      EXIT_REASON="nonzero(2)"
      break
      ;;
  esac
done

echo "$EXIT_REASON" > "$STAGING/driver.exit-reason"
echo "${SESSION_ID:-}" > "$STAGING/session.uuid"

# Export the parent session's parts as a JSONL stream with the synthetic
# `_role`, `_cwd`, `_ts`, `_model` fields the OpenCode parser expects.
# This is what the replay tool reads from transcript.jsonl in the
# committed fixture.
TRANSCRIPT_OUT="$STAGING/opencode-transcript.jsonl"
: > "$TRANSCRIPT_OUT"
if [[ -n "$SESSION_ID" ]]; then
  # Role lives inside message.data JSON (no top-level column), so extract
  # it with json_extract. modelID lives in message.data.model.modelID.
  # Concurrent reads against opencode's running DB are safe — opencode
  # writes in WAL mode and sqlite3's default open mode tolerates a
  # parallel writer. The -readonly flag fails on this DB because it
  # disables the WAL fallback path; omit it.
  sqlite3 "$OPENCODE_DB" <<SQL >> "$TRANSCRIPT_OUT"
.mode list
.separator ""
SELECT json_set(
  p.data,
  '\$._role',  json_extract(m.data, '\$.role'),
  '\$._cwd',   s.directory,
  '\$._ts',    p.time_updated,
  '\$._model', json_extract(m.data, '\$.model.modelID')
)
FROM part p
JOIN message m ON p.message_id = m.id
JOIN session s ON p.session_id = s.id
WHERE p.session_id = '$SESSION_ID'
ORDER BY p.time_created ASC, p.id ASC;
SQL
fi
echo "$TRANSCRIPT_OUT" > "$STAGING/transcript.path"

# Combined log for easier review.
{
  echo "=== stdout ==="
  cat "$DRIVER_LOG.stdout" 2>/dev/null || true
  echo
  echo "=== stderr ==="
  cat "$DRIVER_LOG.stderr" 2>/dev/null || true
  echo
  echo "=== driver exit reason: $EXIT_REASON ==="
  echo "=== session_id: ${SESSION_ID:-<none>} ==="
  echo "=== transcript: $TRANSCRIPT_OUT ($(wc -l < "$TRANSCRIPT_OUT" | tr -d ' ') lines) ==="
} > "$DRIVER_LOG"

echo "drive-opencode-interactive: $EXIT_REASON (session=${SESSION_ID:-<none>}, transcript=$TRANSCRIPT_OUT)"

case "$EXIT_REASON" in
  ok) exit 0 ;;
  *)  exit 1 ;;
esac
