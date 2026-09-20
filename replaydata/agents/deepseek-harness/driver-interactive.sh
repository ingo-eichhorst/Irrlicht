#!/usr/bin/env bash
# Drive the DeepSeek Harness TUI through tmux for onboarding recordings.
# The driver uses the operator's installed TUI profile, but applies model and
# endpoint settings through a temporary patch under the recording staging dir.

set -euo pipefail

if [[ $# -ne 5 ]]; then
  echo "usage: driver-interactive.sh <staging> <uuid> <timeout-s> <settings-path> <script-json>" >&2
  exit 2
fi

STAGING="$1"
# shellcheck disable=SC2034 # protocol input; DSH mints its own session id.
UUID="$2"
TIMEOUT_S="$3"
SETTINGS_PATH="$4"
SCRIPT_JSON="$5"

mkdir -p "$STAGING"
DRIVER_LOG="$STAGING/driver.log"
: > "$DRIVER_LOG.stderr"

_DRIVE_LIB="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../_lib/drive" && pwd)"
# shellcheck source=/dev/null
source "$_DRIVE_LIB/slots.sh"
# shellcheck source=/dev/null
source "$_DRIVE_LIB/contracts.sh"
# shellcheck source=/dev/null
source "$_DRIVE_LIB/teardown.sh"

# The factory reads this value directly from the source. List only primitives
# that the dispatch loop below implements.
# shellcheck disable=SC2034
DRIVE_ELICITS="send slash wait_turn wait_compaction sleep interrupt keys reset_session restart resume fork sigkill exit_clean start_session session seed_instruction"
# shellcheck disable=SC2034
DRIVE_SLASH_REQUIRES_STEP_TYPE=false

RUN_CWD="${IRRLICHT_ONBOARD_CWD:-$STAGING/cwd}"
mkdir -p "$RUN_CWD"
RUN_CWD="$(cd "$RUN_CWD" && pwd -P)"
DSH_ROOT="${DSH_HOME:-$HOME/.dsh}"
DSH_SESSIONS_DIR="$DSH_ROOT/sessions"
# shellcheck disable=SC2034 # read by the sourced slots.sh alloc_slot function.
DRIVE_MARKER_PREFIX="$STAGING/.dsh-marker"
DEADLINE=$(( $(date +%s) + TIMEOUT_S ))
EXIT_REASON="ok"
REACHED_EPILOGUE=0
FORK_WEB_PID=""

N_SLOTS=0
ACTIVE=0
SESSION=""
TRANSCRIPT=""
MARKER=""
EXPECTED_TURNS=0
SES_SESSION=()
# shellcheck disable=SC2034 # read by the sourced contracts.sh library.
SES_TRANSCRIPT=()
# shellcheck disable=SC2034 # shared slot library state.
SES_UUID=()
# shellcheck disable=SC2034 # shared slot library state.
SES_EXPECTED=()
# shellcheck disable=SC2034 # shared slot library state.
SES_MARKER=()
SES_CWD=()
# shellcheck disable=SC2034 # written by the sourced slots.sh library.
SES_OWNED=()
SES_PANE_PID=()

remaining_seconds() {
  local now
  now=$(date +%s)
  (( now >= DEADLINE )) && echo 0 || echo $((DEADLINE - now))
}

setting() {
  local key="$1" fallback="$2"
  if [[ -f "$SETTINGS_PATH" ]]; then
    jq -r --arg key "$key" --arg fallback "$fallback" '.[$key] // $fallback' "$SETTINGS_PATH"
  else
    printf '%s\n' "$fallback"
  fi
}

MODEL="$(setting model qwen/qwen3.5-9b)"
BASE_URL="$(setting base_url http://127.0.0.1:1234/v1)"
CONTEXT_WINDOW="$(setting context_window 262144)"
PERMISSION_MODE="$(setting permission_mode danger-full-access)"
LMSTUDIO_API_KEY_VALUE="$(setting api_key irrlicht-local-recording)"
PATCH_PATH="$STAGING/dsh-recording.patch.yml"

# This file is a per-run overlay. It never modifies the installed profile.
printf '%s\n' \
  '- id: llm-pi-ai' \
  '  config:' \
  '    providers:' \
  '      lmstudio:' \
  '        displayName: LM Studio' \
  '        apiKeyEnv: LMSTUDIO_API_KEY' \
  '        api: openai-completions' \
  "        baseURL: $BASE_URL" \
  '        models:' \
  "          - id: $MODEL" \
  "            name: $MODEL" \
  "            contextWindow: $CONTEXT_WINDOW" \
  '- id: agent-default-model' \
  '  config:' \
  '    provider: lmstudio' \
  "    model: $MODEL" > "$PATCH_PATH"

stop_fork_web() {
  local pid="${FORK_WEB_PID:-}" signal ticks i
  [[ -n "$pid" ]] || return 0
  for signal in INT TERM KILL; do
    kill -0 "$pid" 2>/dev/null || break
    kill -"$signal" "$pid" 2>/dev/null || true
    case "$signal" in
      INT)  ticks=40 ;;
      TERM) ticks=20 ;;
      *)    ticks=8 ;;
    esac
    for (( i = 0; i < ticks; i++ )); do
      kill -0 "$pid" 2>/dev/null || break 2
      sleep 0.25
    done
  done
  if kill -0 "$pid" 2>/dev/null; then
    echo "[driver] fork web process $pid survived shutdown" >&2
    return 1
  fi
  wait "$pid" 2>/dev/null || true
  FORK_WEB_PID=""
  return 0
}

# BEGIN cleanup
cleanup() {
  local i
  stop_fork_web || true
  for (( i = 1; i <= N_SLOTS; i++ )); do
    [[ -n "${SES_SESSION[$i]:-}" ]] && tmux kill-session -t "${SES_SESSION[$i]}" 2>/dev/null || true
  done
  if [[ "$REACHED_EPILOGUE" != "1" && "$EXIT_REASON" == "ok" ]]; then
    EXIT_REASON="nonzero(2)"
    echo "[driver] aborted before epilogue; recording $EXIT_REASON" >&2
  fi
  printf '%s\n' "$EXIT_REASON" > "$STAGING/driver.exit-reason"
}
# END cleanup
trap cleanup EXIT

decode_transcript() { # <path> <destination>
  local path="$1" destination="$2"
  local temporary="$destination.tmp"
  if ! zstd -qdc "$path" > "$temporary" 2>"$STAGING/zstd-error.$ACTIVE"; then
    return 1
  fi
  mv "$temporary" "$destination"
}

resolve_transcript() {
  [[ -n "$TRANSCRIPT" ]] && return 0
  mkdir -p "$DSH_SESSIONS_DIR"
  local candidate decoded header id cwd best
  decoded="$STAGING/decoded-discovery.$ACTIVE.jsonl"
  while (( $(remaining_seconds) > 0 )); do
    best=""
    while IFS= read -r candidate; do
      [[ -f "$candidate" ]] || continue
      decode_transcript "$candidate" "$decoded" || continue
      header="$(sed -n '1p' "$decoded")"
      [[ "$(jq -r '.type // empty' <<<"$header" 2>/dev/null)" == "session" ]] || continue
      id="$(jq -r '.id // empty' <<<"$header")"
      cwd="$(jq -r '.cwd // empty' <<<"$header")"
      [[ "$id" =~ ^session-[0-9a-f-]{36}$ ]] || continue
      [[ "$(basename "$(dirname "$candidate")")" == "$id" ]] || continue
      [[ "$cwd" == "${SES_CWD[$ACTIVE]}" ]] || continue
      if [[ -z "$best" || "$candidate" -nt "$best" ]]; then
        best="$candidate"
      fi
    done < <(find "$DSH_SESSIONS_DIR" -type f -name 'session.v*.jsonl.zstd' -newer "$MARKER" 2>/dev/null)
    if [[ -n "$best" ]]; then
      TRANSCRIPT="$best"
      UUID="$(basename "$(dirname "$TRANSCRIPT")")"
      echo "[driver] transcript[s$ACTIVE]: $TRANSCRIPT (session=$UUID)" >&2
      return 0
    fi
    sleep 0.2
  done
  echo "[driver] no readable DSH transcript appeared after marker $MARKER for cwd ${SES_CWD[$ACTIVE]}" >&2
  EXIT_REASON="transcript_missing"
  return 1
}

# shellcheck source=turn-count.sh
source "$(dirname "${BASH_SOURCE[0]}")/turn-count.sh"

boot_slot() { # [resume-session-id]
  local resume_id="${1:-}" command_text pane
  command -v tmux >/dev/null 2>&1 || { echo "[driver] tmux required" >&2; EXIT_REASON="nonzero(2)"; return 1; }
  command -v dsh >/dev/null 2>&1 || { echo "[driver] dsh required" >&2; EXIT_REASON="nonzero(2)"; return 1; }
  command -v zstd >/dev/null 2>&1 || { echo "[driver] zstd required" >&2; EXIT_REASON="nonzero(2)"; return 1; }
  touch "$DRIVER_LOG.stdout.$ACTIVE"
  tmux kill-session -t "$SESSION" 2>/dev/null || true

  local -a argv=(env "LMSTUDIO_API_KEY=$LMSTUDIO_API_KEY_VALUE" "DSH_PERMISSION_MODE=$PERMISSION_MODE" dsh --profile tui --patch "$PATCH_PATH")
  if [[ -n "$resume_id" ]]; then
    argv+=(--resume "$resume_id")
  fi
  printf -v command_text '%q ' "${argv[@]}"
  tmux new-session -d -s "$SESSION" -x 200 -y 50 -c "${SES_CWD[$ACTIVE]}" "$command_text" \
    || { echo "[driver] failed to launch DSH TUI" >&2; EXIT_REASON="nonzero(2)"; return 1; }
  tmux pipe-pane -t "$SESSION" -o "cat >> '$DRIVER_LOG.stdout.$ACTIVE'"
  pane="$(tmux list-panes -t "$SESSION" -F '#{pane_pid}' | head -n1)"
  SES_PANE_PID[$ACTIVE]="$pane"
  echo "[driver] tmux started: $SESSION (slot=$ACTIVE, cwd=${SES_CWD[$ACTIVE]})" >&2

  if [[ -z "$resume_id" ]]; then
    resolve_transcript
  else
    # A resume keeps the same durable transcript. Observe the live pane and
    # session lock instead of guessing a startup delay.
    while (( $(remaining_seconds) > 0 )); do
      if tmux has-session -t "$SESSION" 2>/dev/null && [[ -f "$TRANSCRIPT" ]]; then
        return 0
      fi
      sleep 0.2
    done
    echo "[driver] resumed TUI did not become observable" >&2
    EXIT_REASON="readiness_timeout"
    return 1
  fi
}

step_send() {
  local text="$1"
  tmux send-keys -t "$SESSION" -l -- "$text"
  tmux send-keys -t "$SESSION" Enter
  EXPECTED_TURNS=$((EXPECTED_TURNS + 1))
  echo "[driver] send[s$ACTIVE]: expecting turn $EXPECTED_TURNS" >&2
}

step_slash() {
  tmux send-keys -t "$SESSION" -l -- "$1"
  tmux send-keys -t "$SESSION" Enter
}

step_seed_instruction() { # <path> <text>
  local path="$1" text="$2"
  printf '%s' "$text" > "$RUN_CWD/$path"
  echo "[driver] seed_instruction: wrote $(printf '%s' "$text" | wc -c | tr -d ' ') bytes to $RUN_CWD/$path" >&2
}

step_wait_turn() {
  resolve_transcript || return 1
  local now=0 unreadable_since=0
  while (( $(remaining_seconds) > 0 )); do
    if now="$(turn_count)"; then
      unreadable_since=0
      if [[ "$now" -ge "$EXPECTED_TURNS" ]]; then
        echo "[driver] turn complete[s$ACTIVE]: $now/$EXPECTED_TURNS" >&2
        return 0
      fi
    else
      [[ "$unreadable_since" -ne 0 ]] || unreadable_since=$(date +%s)
      if (( $(date +%s) - unreadable_since >= 10 )); then
        echo "[driver] DSH transcript stayed unreadable for 10 seconds: $TRANSCRIPT" >&2
        EXIT_REASON="unreadable_transcript"
        return 1
      fi
    fi
    sleep 0.25
  done
  echo "[driver] wait_turn timed out at $now/$EXPECTED_TURNS" >&2
  EXIT_REASON="timeout"
  return 1
}

step_wait_compaction() {
  resolve_transcript || return 1
  local unreadable_since=0
  while (( $(remaining_seconds) > 0 )); do
    if zstd -dc -- "$TRANSCRIPT" | jq -e 'select(.type == "compaction/end")' >/dev/null; then
      echo "[driver] compaction complete[s$ACTIVE]" >&2
      return 0
    fi
    [[ "$?" -eq 1 ]] && unreadable_since=0 || {
      [[ "$unreadable_since" -ne 0 ]] || unreadable_since=$(date +%s)
      if (( $(date +%s) - unreadable_since >= 10 )); then
        echo "[driver] DSH transcript stayed unreadable for 10 seconds: $TRANSCRIPT" >&2
        EXIT_REASON="unreadable_transcript"
        return 1
      fi
    }
    sleep 0.25
  done
  echo "[driver] wait_compaction timed out without compaction/end" >&2
  EXIT_REASON="timeout"
  return 1
}

step_interrupt() {
  tmux send-keys -t "$SESSION" C-c
}

step_keys() {
  local keys="$1"
  [[ "$keys" != *';'* ]] || { echo "[driver] refusing tmux command separator in keys" >&2; EXIT_REASON="nonzero(2)"; return 1; }
  # Word splitting is intentional: each recipe token is one tmux key name.
  # shellcheck disable=SC2086
  tmux send-keys -t "$SESSION" $keys
}

step_exit_clean() {
  step_slash "/exit"
  if require_tmux_session_gone "$SESSION" "$DRIVE_EXIT_CLEAN_CAP_S"; then
    return 0
  fi
  echo "[driver] /exit did not terminate $SESSION within ${DRIVE_EXIT_CLEAN_CAP_S}s" >&2
  tmux kill-session -t "$SESSION" 2>/dev/null || true
  EXIT_REASON="nonzero(2)"
  return 1
}

step_sigkill() {
  local pane="${SES_PANE_PID[$ACTIVE]:-}"
  echo "[driver] sigkill[s$ACTIVE]: pane pid ${pane:-unknown}" >&2
  sigkill_and_wait "$pane" 3
  tmux kill-session -t "$SESSION" 2>/dev/null || true
}

step_restart() {
  save_active
  tmux kill-session -t "$SESSION" 2>/dev/null || true
  local idx=$((N_SLOTS + 1)) cwd="$STAGING/cwd-restart-$((N_SLOTS + 1))"
  mkdir -p "$cwd"
  alloc_slot "dshdrv-$$-$(date +%s)-$idx" "$cwd"
  boot_slot
}

step_start_session() {
  local requested_cwd="$1"
  save_active
  local idx=$((N_SLOTS + 1)) cwd="${requested_cwd:-$RUN_CWD}"
  mkdir -p "$cwd"
  cwd="$(cd "$cwd" && pwd -P)"
  alloc_slot "dshdrv-$$-$(date +%s)-$idx" "$cwd"
  boot_slot
}

step_reset_session() {
  local old_slot="$ACTIVE" old_tmux="$SESSION" old_cwd="${SES_CWD[$ACTIVE]}" old_pane="${SES_PANE_PID[$ACTIVE]:-}"
  save_active
  # The same TUI owns the new slot. Clear the old slot's tmux ownership so
  # cleanup cannot kill a later session through an aliased name.
  SES_SESSION[$old_slot]=""
  SES_PANE_PID[$old_slot]=""
  alloc_slot "$old_tmux" "$old_cwd"
  SES_PANE_PID[$ACTIVE]="$old_pane"
  step_slash "/clear"
  resolve_transcript
}

step_resume() {
  local resume_id="$UUID"
  [[ -n "$resume_id" && -n "$TRANSCRIPT" ]] || { echo "[driver] resume has no resolved session" >&2; EXIT_REASON="nonzero(2)"; return 1; }
  tmux kill-session -t "$SESSION" 2>/dev/null || true
  wait_tmux_session_gone "$SESSION" 3
  boot_slot "$resume_id"
}

step_fork() {
  resolve_transcript || return 1
  command -v curl >/dev/null 2>&1 || { echo "[driver] curl required for the DSH web fork API" >&2; EXIT_REASON="nonzero(2)"; return 1; }

  local parent_id="$UUID" parent_transcript="$TRANSCRIPT" parent_cwd="${SES_CWD[$ACTIVE]}"
  local web_log="$STAGING/dsh-web-fork.log" cookie_jar="$STAGING/dsh-web-fork.cookies"
  local web_url="" web_base rpc_id payload response child_id child_dir child_transcript="" decoded header baseline

  # The supported fork surface is the official web controller. Stop the TUI so
  # its session lock is released, then let a short-lived web profile create the
  # seeded child through session/fork.
  step_exit_clean || return 1
  save_active
  : > "$web_log"
  env "LMSTUDIO_API_KEY=$LMSTUDIO_API_KEY_VALUE" "DSH_PERMISSION_MODE=$PERMISSION_MODE" \
    dsh --profile web --patch "$PATCH_PATH" --no-open --host 127.0.0.1 --port 0 \
    >"$web_log" 2>&1 &
  FORK_WEB_PID=$!

  while (( $(remaining_seconds) > 0 )); do
    web_url="$(sed -n 's/^dsh web: \(http[^ ]*\).*$/\1/p' "$web_log" | tail -n1)"
    [[ -n "$web_url" ]] && break
    if ! kill -0 "$FORK_WEB_PID" 2>/dev/null; then
      echo "[driver] DSH web fork process exited before announcing its URL" >&2
      tail -20 "$web_log" >&2 || true
      EXIT_REASON="nonzero(2)"
      stop_fork_web || true
      return 1
    fi
    sleep 0.25
  done
  if [[ -z "$web_url" ]]; then
    echo "[driver] DSH web fork API did not become ready" >&2
    EXIT_REASON="readiness_timeout"
    stop_fork_web || true
    return 1
  fi

  web_base="${web_url%%\?*}"
  web_base="${web_base%/}"
  if ! curl -fsSL --max-time 30 -c "$cookie_jar" -o /dev/null "$web_url"; then
    echo "[driver] DSH web fork API authentication failed" >&2
    EXIT_REASON="nonzero(2)"
    stop_fork_web || true
    return 1
  fi

  rpc_id="fork-$$-$(date +%s)"
  payload="$(jq -nc --arg rpc_id "$rpc_id" --arg session_id "$parent_id" \
    '{type:"client-request",rpcId:$rpc_id,method:"session/fork",payload:{args:{request:{sessionId:$session_id}}}}')"
  if ! response="$(curl -fsS --max-time 30 -b "$cookie_jar" \
      -H 'content-type: application/json' -H "origin: $web_base" \
      --data-binary "$payload" "$web_base/api/session/fork")"; then
    echo "[driver] DSH web fork API request failed" >&2
    EXIT_REASON="nonzero(2)"
    stop_fork_web || true
    return 1
  fi
  if ! jq -e --arg rpc_id "$rpc_id" \
      '.type == "server-response" and .rpcId == $rpc_id and .result.ok == true' \
      >/dev/null <<<"$response"; then
    echo "[driver] DSH web fork API rejected the request: $(jq -c '.result.error // .' <<<"$response" 2>/dev/null || printf '%s' "$response")" >&2
    EXIT_REASON="nonzero(2)"
    stop_fork_web || true
    return 1
  fi
  child_id="$(jq -r '.result.value.sessionId // empty' <<<"$response")"
  if [[ ! "$child_id" =~ ^session-[0-9a-f-]{36}$ ]]; then
    echo "[driver] DSH web fork API returned an invalid child id: $child_id" >&2
    EXIT_REASON="nonzero(2)"
    stop_fork_web || true
    return 1
  fi
  if ! stop_fork_web; then
    EXIT_REASON="nonzero(2)"
    return 1
  fi

  alloc_slot "dshdrv-$$-$(date +%s)-$((N_SLOTS + 1))" "$parent_cwd"
  UUID="$child_id"
  child_dir="$(dirname "$(dirname "$parent_transcript")")/$child_id"
  decoded="$STAGING/decoded-fork.$ACTIVE.jsonl"
  while (( $(remaining_seconds) > 0 )); do
    child_transcript=""
    while IFS= read -r candidate; do
      [[ -f "$candidate" ]] || continue
      decode_transcript "$candidate" "$decoded" || continue
      header="$(sed -n '1p' "$decoded")"
      if [[ "$(jq -r '.id // empty' <<<"$header")" == "$child_id" \
          && "$(jq -r '.parentSession // empty' <<<"$header")" == "$parent_id" \
          && "$(jq -r '.isSeeded // false' <<<"$header")" == "true" ]]; then
        child_transcript="$candidate"
      fi
    done < <(find "$child_dir" -maxdepth 1 -type f -name 'session.v*.jsonl.zstd' 2>/dev/null)
    [[ -n "$child_transcript" ]] && break
    sleep 0.25
  done
  if [[ -z "$child_transcript" ]]; then
    echo "[driver] forked DSH child transcript did not become readable: $child_id" >&2
    EXIT_REASON="transcript_missing"
    return 1
  fi

  TRANSCRIPT="$child_transcript"
  baseline="$(turn_count)" || { EXIT_REASON="unreadable_transcript"; return 1; }
  [[ "$baseline" =~ ^[0-9]+$ ]] || { echo "[driver] invalid fork turn baseline: $baseline" >&2; EXIT_REASON="nonzero(2)"; return 1; }
  EXPECTED_TURNS="$baseline"
  boot_slot "$child_id" || return 1
  echo "[driver] fork: parent=$parent_id child=$child_id inherited_turns=$baseline" >&2
}

# Seed leading project instructions before the first TUI starts. The agent
# loads them on its first request, so an in-loop write would be too late.
while IFS= read -r leading_step; do
  [[ "$(jq -r '.type' <<<"$leading_step")" == "seed_instruction" ]] || break
  step_seed_instruction "$(jq -r '.path' <<<"$leading_step")" "$(jq -r '.text' <<<"$leading_step")"
done < <(jq -c '.[]' <<<"$SCRIPT_JSON")

# Allocate the first slot before applying the remaining script.
alloc_slot "dshdrv-$$-$(date +%s)-1" "$RUN_CWD"
boot_slot

STEP_OK=true
while IFS= read -r step; do
  $STEP_OK || break
  type="$(jq -r '.type' <<<"$step")"
  target="$(jq -r '.session // empty' <<<"$step")"
  if [[ -n "$target" && "$type" != "start_session" && "$target" != "$ACTIVE" ]]; then
    if [[ "$target" =~ ^[0-9]+$ && "$target" -ge 1 && "$target" -le "$N_SLOTS" ]]; then
      save_active
      load_slot "$target"
      echo "[driver] active slot -> $target" >&2
    else
      echo "[driver] invalid session slot $target" >&2
      EXIT_REASON="nonzero(2)"
      break
    fi
  fi
  case "$type" in
    send)          step_send "$(jq -r '.text' <<<"$step")" ;;
    slash)         step_slash "$(jq -r '.text' <<<"$step")" ;;
    wait_turn)     step_wait_turn || STEP_OK=false ;;
    wait_compaction) step_wait_compaction || STEP_OK=false ;;
    sleep)         sleep "$(jq -r '.seconds // 1' <<<"$step")" ;;
    interrupt)     step_interrupt ;;
    keys)          step_keys "$(jq -r '.keys // .text // empty' <<<"$step")" || STEP_OK=false ;;
    reset_session) step_reset_session || STEP_OK=false ;;
    restart)       step_restart || STEP_OK=false ;;
    resume)        step_resume || STEP_OK=false ;;
    fork)          step_fork || STEP_OK=false ;;
    sigkill)       step_sigkill ;;
    exit_clean)    step_exit_clean || STEP_OK=false ;;
    start_session) step_start_session "$(jq -r '.cwd // empty' <<<"$step")" || STEP_OK=false ;;
    session)       : ;;
    seed_instruction) step_seed_instruction "$(jq -r '.path' <<<"$step")" "$(jq -r '.text' <<<"$step")" ;;
    *)             echo "[driver] unknown step type: $type" >&2; EXIT_REASON="nonzero(2)"; STEP_OK=false ;;
  esac
  (( $(remaining_seconds) > 0 )) || { EXIT_REASON="timeout"; break; }
done < <(jq -c '.[]' <<<"$SCRIPT_JSON")

save_active
emit_session_contract "${SES_UUID[1]}"
REACHED_EPILOGUE=1
drive_exit
