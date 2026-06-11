#!/usr/bin/env bash
# drive-gemini-cli-interactive.sh — drive gemini-cli's REPL via tmux, executing a
# step-script. SCAFFOLDED from scripts/templates/drive-interactive.sh.tmpl
# (#496 RC2): a new adapter starts with EVERY standard step-type arm present
# (stubbed), not a 3-step stub — so the column driver-gap forecast tells you
# which primitives still need porting, and the matrix can't silently freeze a
# cell on a missing arm.
#
# HOW TO USE THIS TEMPLATE
#   cp scripts/templates/drive-interactive.sh.tmpl \
#      scripts/drive-<agent>-interactive.sh
#   sed -i '' 's/gemini-cli/<agent>/g' scripts/drive-<agent>-interactive.sh
#   chmod +x scripts/drive-<agent>-interactive.sh
# Then fill the three AGENT-SPECIFIC SEAMS marked TODO(gemini-cli) below by
# porting from the reference drivers (drive-claudecode-interactive.sh, the
# fullest; drive-codex-interactive.sh adds `fork`).
#
# IMPORTANT — how a stubbed arm is caught: every standard arm is PRESENT here
# (so recipe-lint's GRAMMAR check treats it as handled and will NOT report a
# driver_gap for it). The real backstop is the SEMANTIC lint: the DRIVE_ELICITS
# constant below lists ONLY the step types this driver actually elicits, and
# recipe-lint reads it straight from this file (#508 #4 — no separate manifest)
# and flags any recipe needing a stubbed-but-unlisted primitive as a
# semantic_gap (exit 4) BEFORE recording. Keep DRIVE_ELICITS accurate: add a
# primitive the moment you genuinely port its seam, not when you stub the arm.
#
# Standard step types (port each from the reference driver):
#   send / slash   — type text + Enter (slash is the same keystrokes)
#   wait_turn      — block until the agent finishes the turn (SEAM 2)
#   interrupt      — cancel the in-flight turn (Escape / Ctrl-C)
#   keys           — raw tmux key sequence (arrow-key pickers, etc.)
#   sleep          — pause N seconds
#   reset_session  — in-REPL reset (/clear, /new): same process, new session id
#   restart        — end the session, start a FRESH one (new id, new cwd)
#   resume         — relaunch the SAME id+cwd (daemon sees one session, 2 PIDs)
#   sigkill        — kill -9 the active session's PID
#   exit_clean     — Ctrl-D graceful shutdown
#   start_session  — launch a concurrent session without tearing the first down
#   session        — switch the active slot (carried as {"session": N})
#
# ----------------------------------------------------------------------------
# HEADLESS ESCAPE HATCH
#   If gemini-cli has a true headless-per-turn mode (e.g. `gemini-cli run -p …`
#   that blocks until the turn ends), a tmux-REPL driver may be overkill for
#   the happy path — model the headless path like drive-opencode-interactive.sh
#   instead, where `send` launches a subprocess and `wait_turn` is a no-op. BUT:
#   headless modes usually CANNOT deliver in-REPL slash commands or signals
#   (opencode stores `/new` as literal text), so reset_session/slash/interrupt
#   still need a live-TUI path. opencode's driver carries BOTH: a headless path
#   and a run_live() tmux path the dispatcher picks when a recipe needs a TUI
#   primitive. Copy that hybrid shape if gemini-cli is headless-first.
# ----------------------------------------------------------------------------
#
# Staging contract (identical across all drivers — do NOT change these names):
#   driver.log[.stdout|.stderr]  — captured CLI output
#   driver.exit-reason           — ok | timeout | killed | nonzero(N)
#   session.uuid / session.uuids — the session id(s) the daemon will key on
#   transcript.path / transcript.paths — absolute path(s) to the transcript(s)
#
# Usage:
#   drive-gemini-cli-interactive.sh <staging-dir> <session-uuid> \
#       <timeout-seconds> <settings-path> <script-json>

set -euo pipefail

if [[ $# -ne 5 ]]; then
  echo "usage: drive-gemini-cli-interactive.sh <staging> <uuid> <timeout-s> <settings-path> <script-json>" >&2
  exit 2
fi

STAGING="$1"
UUID="$2"            # preferred session id; some agents mint their own (ignore then)
TIMEOUT_S="$3"
SETTINGS_PATH="$4"   # scenario settings blob; wire into the launch if the agent reads one
SCRIPT_JSON="$5"

mkdir -p "$STAGING"
DRIVER_LOG="$STAGING/driver.log"

# recipe-lint contract (#508 #4): the step types this driver genuinely ELICITS,
# read directly by recipe-lint (no separate manifest). Start with ONLY the seams
# that actually work in this scaffold (send/slash/sleep) and add each primitive
# as you port its seam — a stubbed `not_implemented` arm must NOT be listed, so
# recipe-lint flags a recipe needing it as a semantic_gap before recording. Set
# DRIVE_SLASH_REQUIRES_STEP_TYPE=true if gemini-cli is headless-first (a bare
# send "/cmd" stores literal text instead of reaching the REPL).
DRIVE_ELICITS="send slash wait_turn sleep"
DRIVE_SLASH_REQUIRES_STEP_TYPE=false
RUN_CWD="${IRRLICHT_ONBOARD_CWD:-$STAGING/cwd}"
mkdir -p "$RUN_CWD"
RUN_CWD="$(cd "$RUN_CWD" && pwd -P)"   # canonicalize (resolve symlinks) for the daemon's cwd match
DEADLINE=$(( $(date +%s) + TIMEOUT_S ))
EXIT_REASON="ok"
SESSION=""

# --- Gemini transport (FilesUnderRoot) --------------------------------------
# Gemini writes one append-only JSONL transcript per session under
# ~/.gemini/tmp/<project>/chats/session-<ts>-<id>.jsonl, where <project> is the
# basename of the launch cwd. Line 1 is a session header carrying sessionId (a
# UUID); subsequent lines are bare message objects ({id,type:"user"|"gemini",
# content,toolCalls,…}) or {"$set":{…}} mutation envelopes. The Go adapter
# (core/adapters/inbound/agents/geminicli/parser.go) settles to ready on a
# "gemini" message that carries non-empty text and opens NO tool calls — there
# is no explicit turn_done marker. wait_turn mirrors that classification.
GEMINI_HOME="${GEMINI_DIR:-$HOME/.gemini}"
GEMINI_CHATS_ROOT="$GEMINI_HOME/tmp"
GEMINI_BIN="${GEMINI_BIN:-gemini}"
PROJECT="$(basename "$RUN_CWD")"
TRANSCRIPT=""        # resolved on first wait_turn (SEAM 3)
SESSION_UUID=""      # sessionId from the transcript header
MARKER="$STAGING/.gemini-marker"   # find rollouts created AFTER launch
touch "$MARKER"

remaining_seconds() { local now; now=$(date +%s); (( now >= DEADLINE )) && echo 0 || echo $((DEADLINE - now)); }

not_implemented() { # <step-type>
  echo "[driver] STUB: step type '$1' not yet ported for gemini-cli — see scripts/templates/drive-interactive.sh.tmpl and drive-claudecode-interactive.sh" >&2
  EXIT_REASON="nonzero(3)"
  return 3
}

# Always honor the staging contract: write driver.exit-reason on ANY exit
# (including a `set -e` abort mid-launch) and tear tmux down if a session was
# started. Set EXIT_REASON before a failing `exit` so the reason is accurate.
cleanup() {
  [[ -n "$SESSION" ]] && tmux kill-session -t "$SESSION" 2>/dev/null || true
  echo "$EXIT_REASON" > "$STAGING/driver.exit-reason"
}
trap cleanup EXIT

# --- AGENT-SPECIFIC SEAM 1: launch the REPL under tmux -----------------------
# Launch `gemini -y` (yolo: auto-approves tool calls so recipes never stall on a
# permission dialog). Gemini mints its own sessionId — it does NOT accept an
# external UUID — so $UUID is ignored and SESSION_UUID is harvested from the
# transcript header in resolve_transcript (SEAM 3). On the first run in a fresh
# cwd Gemini shows a trust-folder prompt; we poll the pane and accept it.
launch_repl() {
  command -v tmux >/dev/null 2>&1 || { echo "[driver] tmux required" >&2; EXIT_REASON="nonzero(2)"; exit 1; }
  command -v "$GEMINI_BIN" >/dev/null 2>&1 || { echo "[driver] $GEMINI_BIN not on PATH" >&2; EXIT_REASON="nonzero(2)"; exit 1; }
  # Force API-key auth in this cwd: a workspace .gemini/settings.json overrides
  # the user's global auth type, so recordings draw on the GEMINI_API_KEY quota
  # pool (separate from OAuth, and unaffected by the 2026-06-18 unpaid-tier
  # shutdown) and NEVER silently fall back to the user's OAuth login. With
  # selectedType=gemini-api-key, gemini hard-errors when no key is present — so
  # fail fast here with a clearer message. Model defaults to flash (cheap).
  if [[ -z "${GEMINI_API_KEY:-}" ]]; then
    echo "[driver] GEMINI_API_KEY not set — export it (e.g. 'set -a; . .build/.env; set +a') before recording" >&2
    EXIT_REASON="nonzero(2)"; exit 1
  fi
  mkdir -p "$RUN_CWD/.gemini"
  printf '{ "security": { "auth": { "selectedType": "gemini-api-key" } } }\n' > "$RUN_CWD/.gemini/settings.json"
  SESSION="geminidrv-$$-$(date +%s)"   # tmux session names cannot contain '.'
  tmux kill-session -t "$SESSION" 2>/dev/null || true
  # `|| { … exit … }` keeps a launch failure from aborting under set -e WITHOUT
  # an accurate exit-reason — the cleanup trap then records nonzero(2).
  # Inject the API key + trust into the NEW session's env with `-e`. A
  # pre-existing tmux server hands new sessions ITS stale environment, not the
  # driver's — so a bare `export GEMINI_API_KEY` does not reach gemini and
  # interactive api-key auth would block on a "paste your API key" prompt.
  tmux new-session -d -s "$SESSION" -x 200 -y 50 -c "$RUN_CWD" \
    -e "GEMINI_API_KEY=$GEMINI_API_KEY" -e "GEMINI_CLI_TRUST_WORKSPACE=true" \
    "$GEMINI_BIN" -y \
    >>"$DRIVER_LOG.stdout" 2>>"$DRIVER_LOG.stderr" \
    || { echo "[driver] failed to launch gemini under tmux" >&2; EXIT_REASON="nonzero(2)"; exit 1; }
  tmux pipe-pane -t "$SESSION" -o "cat >> '$DRIVER_LOG.stdout'" 2>/dev/null || true
  echo "[driver] tmux started: $SESSION (cwd=$RUN_CWD, project=$PROJECT)" >&2

  # Accept the trust-folder prompt if it appears (first run in a new cwd).
  local waited=0
  while (( waited < 20 )); do
    if tmux capture-pane -t "$SESSION" -p -S -40 2>/dev/null | grep -qiE 'trust|do you trust'; then
      tmux send-keys -t "$SESSION" Enter   # default highlighted option trusts the folder
      echo "[driver] accepted trust-folder prompt" >&2
      break
    fi
    sleep 0.5; waited=$((waited + 1))
  done
  sleep 2   # grace for the input prompt to settle before the first send
}

# --- SEAM 3: resolve the transcript Gemini just created ----------------------
# Gemini creates the per-session JSONL only after the first user message lands,
# so there's nothing to read at boot. Find the newest session-*.jsonl under
# ~/.gemini/tmp/<project>/chats/ created AFTER launch ($MARKER) and harvest the
# header's sessionId. Caches the path once resolved.
resolve_transcript() {
  [[ -n "$TRANSCRIPT" ]] && return 0
  for _ in $(seq 1 60); do
    local candidate
    # Gemini maps cwd → ~/.gemini/tmp/<basename>/chats, but DEDUPES with a -N
    # suffix when the basename collides with another path it has seen (every
    # recording's staging cwd is named "cwd", so all but the first land in
    # cwd-1, cwd-2, …). Search all <project>* dirs and take the newest session
    # file created after launch ($MARKER), by mtime (-exec ls -t is BSD-safe;
    # macOS find has no -printf). $MARKER scopes it to this run.
    candidate="$(find "$GEMINI_CHATS_ROOT"/"$PROJECT"*/chats -maxdepth 1 -type f \
                   -name 'session-*.jsonl' -newer "$MARKER" \
                   -exec ls -t {} + 2>/dev/null | head -n1)"
    if [[ -n "$candidate" && -s "$candidate" ]]; then
      SESSION_UUID="$(head -n1 "$candidate" | jq -r '.sessionId // empty' 2>/dev/null || true)"
      if [[ -n "$SESSION_UUID" ]]; then
        TRANSCRIPT="$candidate"
        echo "[driver] resolve_transcript: $TRANSCRIPT (sessionId=$SESSION_UUID)" >&2
        return 0
      fi
    fi
    sleep 0.5
  done
  return 1
}

# Count completed turns the way core/adapters/inbound/agents/geminicli/parser.go
# does: a bare "gemini" message with non-empty text and NO tool calls is the
# turn's last word (turn_done). Streaming placeholders (empty content) and
# tool-calling messages keep the session working and are excluded. $set
# envelopes carry no "type" and are skipped by the select.
turn_count() {
  [[ -f "$TRANSCRIPT" ]] || { echo 0; return; }
  jq -r 'select(.type=="gemini"
                and ((.content // "") | gsub("\\s";"") | length) > 0
                and ((.toolCalls // []) | length) == 0) | "x"' \
    "$TRANSCRIPT" 2>/dev/null | wc -l | tr -d ' '
}

# --- SEAM 2: detect a completed turn -----------------------------------------
# Block until turn_count reaches EXPECTED_TURNS (set by each send) or the
# deadline lapses. Resolves the transcript on the first call.
wait_turn() {
  resolve_transcript || {
    echo "[driver] wait_turn: gemini never created a transcript under $GEMINI_CHATS_ROOT/$PROJECT*/chats" >&2
    EXIT_REASON="nonzero(3)"; return 1
  }
  while (( $(remaining_seconds) > 0 )); do
    (( $(turn_count) >= EXPECTED_TURNS )) && { echo "[driver] wait_turn: turn $EXPECTED_TURNS done" >&2; return 0; }
    sleep 0.5
  done
  echo "[driver] wait_turn: timed out waiting for turn $EXPECTED_TURNS" >&2
  EXIT_REASON="timeout"; return 1
}

# --- send text ----------------------------------------------------------------
send_text() { # <text>
  tmux send-keys -t "$SESSION" -l -- "$1"
  sleep 0.3   # let Gemini's Ink input render before Enter, so Enter isn't dropped
  tmux send-keys -t "$SESSION" Enter
  EXPECTED_TURNS=$((EXPECTED_TURNS + 1))
  echo "[driver] send: ${1:0:60} (expecting turn $EXPECTED_TURNS)" >&2
}

# --- Step dispatch: ALL standard arms present; stubs fail loudly -------------
launch_repl
EXPECTED_TURNS=0
while IFS= read -r step; do
  type="$(jq -r '.type' <<<"$step")"
  case "$type" in
    send|slash)      send_text "$(jq -r '.text' <<<"$step")" ;;
    wait_turn)       wait_turn || break ;;
    sleep)           sleep "$(jq -r '.seconds // 1' <<<"$step")" ;;
    interrupt)       not_implemented interrupt || break ;;       # TODO(gemini-cli): Escape/Ctrl-C the in-flight turn
    keys)            not_implemented keys || break ;;            # TODO(gemini-cli): tmux send-keys raw sequence
    reset_session)   not_implemented reset_session || break ;;   # TODO(gemini-cli): in-REPL /clear|/new → new session id
    restart)         not_implemented restart || break ;;         # TODO(gemini-cli): end + fresh session (new id, new cwd)
    resume)          not_implemented resume || break ;;          # TODO(gemini-cli): relaunch same id+cwd
    sigkill)         not_implemented sigkill || break ;;         # TODO(gemini-cli): kill -9 the session PID
    exit_clean)      not_implemented exit_clean || break ;;      # TODO(gemini-cli): Ctrl-D graceful shutdown
    start_session)   not_implemented start_session || break ;;   # TODO(gemini-cli): concurrent session, keep first alive
    session)         not_implemented session || break ;;         # TODO(gemini-cli): switch active slot
    *)               echo "[driver] unknown step type: $type" >&2; EXIT_REASON="nonzero(2)"; break ;;
  esac
  (( $(remaining_seconds) <= 0 )) && { EXIT_REASON="timeout"; break; }
done < <(jq -c '.[]' <<<"$SCRIPT_JSON")

# --- Write the staging contract ----------------------------------------------
# driver.exit-reason is written by the cleanup trap on exit (so it's recorded
# even on an early failure). This single-session scaffold writes session.uuid +
# transcript.path the daemon keyed on. When the multi-session arms
# (reset_session/restart/start_session) are ported, switch to the newline-list
# form session.uuids / transcript.paths, one per session in order.
resolve_transcript || echo "[driver] epilogue: no transcript resolved" >&2
if [[ -n "$SESSION_UUID" ]]; then echo "$SESSION_UUID" > "$STAGING/session.uuid"; fi
if [[ -n "$TRANSCRIPT" ]];   then echo "$TRANSCRIPT"   > "$STAGING/transcript.path"; fi
[[ "$EXIT_REASON" == "ok" ]] && exit 0 || exit 1
