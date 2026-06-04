#!/usr/bin/env bash
# drive-kiro-cli-interactive.sh — drive kiro-cli's TUI via tmux, executing a
# step-script. SCAFFOLDED from scripts/templates/drive-interactive.sh.tmpl
# (#496 RC2): a new adapter starts with EVERY standard step-type arm present
# (stubbed), not a 3-step stub — so the column driver-gap forecast tells you
# which primitives still need porting, and the matrix can't silently freeze a
# cell on a missing arm.
#
# Ported seams (this column's create-agent scaffold): send / slash / wait_turn /
# sleep work; the multi-session / signal primitives (interrupt, keys,
# reset_session, restart, resume, sigkill, exit_clean, start_session, session)
# remain stubbed — `record` ports each from drive-claudecode-interactive.sh /
# drive-codex-interactive.sh when a recipe first needs it.
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
# NO HEADLESS PATH (deliberate)
#   kiro-cli DOES have a headless mode (`kiro-cli chat --no-interactive …`) but
#   it writes NO session file under ~/.kiro/sessions/cli/ — so headless runs are
#   invisible to the daemon and USELESS for recording (FINDINGS.md §7). Only the
#   TUI (`kiro-cli chat --trust-all-tools`) persists a transcript, so this
#   driver is TUI-only; there is no opencode-style headless escape hatch.
# ----------------------------------------------------------------------------
#
# Staging contract (identical across all drivers — do NOT change these names):
#   driver.log[.stdout|.stderr]  — captured CLI output
#   driver.exit-reason           — ok | timeout | killed | nonzero(N)
#   session.uuid / session.uuids — the session id(s) the daemon will key on
#   transcript.path / transcript.paths — absolute path(s) to the transcript(s)
#
# Usage:
#   drive-kiro-cli-interactive.sh <staging-dir> <session-uuid> \
#       <timeout-seconds> <settings-path> <script-json>

set -euo pipefail

if [[ $# -ne 5 ]]; then
  echo "usage: drive-kiro-cli-interactive.sh <staging> <uuid> <timeout-s> <settings-path> <script-json>" >&2
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
# DRIVE_SLASH_REQUIRES_STEP_TYPE=true if kiro-cli is headless-first (a bare
# send "/cmd" stores literal text instead of reaching the REPL).
# kiro-cli is TUI-first: `kiro-cli chat --trust-all-tools` is the ONLY mode that
# persists a session file under ~/.kiro/sessions/cli/. Headless
# `--no-interactive` writes NO session file, so it is invisible to the daemon
# and MUST NOT be used for recording (FINDINGS.md §7). slash commands reach the
# live TUI directly (a bare send "/cmd" is NOT stored as literal text), so
# DRIVE_SLASH_REQUIRES_STEP_TYPE stays false.
DRIVE_ELICITS="send slash wait_turn sleep"
DRIVE_SLASH_REQUIRES_STEP_TYPE=false
RUN_CWD="${IRRLICHT_ONBOARD_CWD:-$STAGING/cwd}"
mkdir -p "$RUN_CWD"
RUN_CWD="$(cd "$RUN_CWD" && pwd -P)"   # canonicalize (resolve symlinks) for the daemon's cwd match
DEADLINE=$(( $(date +%s) + TIMEOUT_S ))
EXIT_REASON="ok"
SESSION=""

# Transcript export state (SEAM 3). kiro-cli mints its OWN session UUID per
# `chat` launch and writes ~/.kiro/sessions/cli/<uuid>.jsonl. We discover it as
# the newest .jsonl whose mtime is after our pre-launch MARKER. TRANSCRIPT is
# the absolute path; UUID is the bare session id (the `--resume-id` arg).
KIRO_SESSIONS_DIR="$HOME/.kiro/sessions/cli"
MARKER="$STAGING/.kiro-launch-marker"
TRANSCRIPT=""
UUID=""

remaining_seconds() { local now; now=$(date +%s); (( now >= DEADLINE )) && echo 0 || echo $((DEADLINE - now)); }

not_implemented() { # <step-type>
  echo "[driver] STUB: step type '$1' not yet ported for kiro-cli — see scripts/templates/drive-interactive.sh.tmpl and drive-claudecode-interactive.sh" >&2
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
# Launch `kiro-cli chat --trust-all-tools` (the TUI; --trust-all-tools assumes
# the persisted trust-all consent so no per-tool picker stalls the run). A
# MARKER touched immediately BEFORE launch gates transcript discovery (SEAM 3):
# kiro creates <uuid>.jsonl only after the first prompt, so we pick the newest
# session file with mtime > MARKER. Wait for the idle input line so keystrokes
# aren't swallowed during TUI boot.
launch_repl() {
  command -v tmux >/dev/null 2>&1 || { echo "[driver] tmux required" >&2; EXIT_REASON="nonzero(2)"; exit 1; }
  mkdir -p "$KIRO_SESSIONS_DIR"
  SESSION="kiro-clidrv-$$-$(date +%s)"
  tmux kill-session -t "$SESSION" 2>/dev/null || true
  # Stamp the discovery floor just before launch (1s mtime granularity — sleep
  # 1 first so a session file written in the same second still sorts after it).
  touch "$MARKER"; sleep 1
  # `|| { … exit … }` keeps a launch failure from aborting under set -e WITHOUT
  # an accurate exit-reason — the cleanup trap then records nonzero(2).
  tmux new-session -d -s "$SESSION" -x 200 -y 50 -c "$RUN_CWD" \
    "kiro-cli chat --trust-all-tools" \
    >>"$DRIVER_LOG.stdout" 2>>"$DRIVER_LOG.stderr" \
    || { echo "[driver] failed to launch kiro-cli under tmux" >&2; EXIT_REASON="nonzero(2)"; exit 1; }
  tmux pipe-pane -t "$SESSION" -o "cat >> '$DRIVER_LOG.stdout'"
  echo "[driver] tmux started: $SESSION (cwd=$RUN_CWD)" >&2
  # Wait for the idle input line ("ask a question or describe a task") so the
  # TUI is ready to accept keystrokes before the first send.
  local waited=0
  while (( waited < 60 )); do
    if tmux capture-pane -t "$SESSION" -p -S -40 2>/dev/null | grep -q 'ask a question or describe a task'; then
      break
    fi
    sleep 0.5; waited=$((waited + 1))
  done
  sleep 1  # extra grace for the prompt to settle
}

# resolve_transcript binds TRANSCRIPT + UUID to this run's session file: the
# newest ~/.kiro/sessions/cli/<uuid>.jsonl with mtime newer than MARKER. kiro
# writes it lazily (only after the first prompt), so this is called from
# wait_turn (after a send) and polls briefly. The session id is the filename
# stem — the daemon keys on it and it is the `--resume-id` arg.
resolve_transcript() {
  [[ -n "$TRANSCRIPT" ]] && return 0
  local f line
  for _ in $(seq 1 60); do
    # newest .jsonl with mtime > MARKER. `find -newer` filters by the floor;
    # the per-line `ls -dt` chooses the newest (avoids xargs -r, unportable on
    # BSD, and the empty-input → list-cwd footgun of a bare `ls -t`).
    f=""
    while IFS= read -r line; do
      [[ -z "$line" ]] && continue
      if [[ -z "$f" || "$line" -nt "$f" ]]; then f="$line"; fi
    done < <(find "$KIRO_SESSIONS_DIR" -maxdepth 1 -type f -name '*.jsonl' \
                  -newer "$MARKER" 2>/dev/null)
    if [[ -n "$f" && -s "$f" ]]; then
      TRANSCRIPT="$f"
      UUID="$(basename "$f" .jsonl)"
      echo "[driver] resolve_transcript: $TRANSCRIPT (uuid=$UUID)" >&2
      return 0
    fi
    sleep 0.5
  done
  return 1
}

# turn_count counts COMPLETED turns: AssistantMessage lines whose data.content[]
# has NO toolUse block (text-only). Mirrors kirocli/parser.go — a text-only
# AssistantMessage is turn_done; mid-turn AssistantMessages carry toolUse blocks
# and are NOT counted. (FINDINGS.md §5.)
turn_count() {
  if [[ -f "$TRANSCRIPT" ]]; then
    jq -r 'select(.kind=="AssistantMessage")
           | select([.data.content[]? | select(.kind=="toolUse")] | length == 0)
           | "x"' "$TRANSCRIPT" 2>/dev/null | wc -l | tr -d ' '
  else
    echo 0
  fi
}

# --- AGENT-SPECIFIC SEAM 2: detect a completed turn --------------------------
# Transcript-based: block until turn_count reaches EXPECTED_TURNS (one text-only
# AssistantMessage per completed user turn). Returns 0 on a new completed turn,
# 1 on readiness/turn timeout.
wait_turn() {
  resolve_transcript || {
    echo "[driver] wait_turn: kiro never created a session file under $KIRO_SESSIONS_DIR" >&2
    EXIT_REASON="readiness_timeout"
    return 1
  }
  local now=0
  while (( $(remaining_seconds) > 0 )); do
    now=$(turn_count)
    if (( now >= EXPECTED_TURNS )); then
      echo "[driver] wait_turn: count=$now (expected >= $EXPECTED_TURNS)" >&2
      return 0
    fi
    sleep 1
  done
  echo "[driver] wait_turn: timeout (count=$now, expected >= $EXPECTED_TURNS)" >&2
  EXIT_REASON="timeout"
  return 1
}

# --- AGENT-SPECIFIC SEAM 3: send text -----------------------------------------
# Type text + Enter into the live TUI. A brief pause lets the input handler
# render the typed text before Enter lands (otherwise Enter can race the render
# and be dropped). Each send bumps EXPECTED_TURNS so wait_turn knows how many
# text-only AssistantMessages to expect.
send_text() { # <text>
  tmux send-keys -t "$SESSION" -l -- "$1"
  sleep 0.3
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
    interrupt)       not_implemented interrupt || break ;;       # TODO(kiro-cli): Escape/Ctrl-C the in-flight turn
    keys)            not_implemented keys || break ;;            # TODO(kiro-cli): tmux send-keys raw sequence
    reset_session)   not_implemented reset_session || break ;;   # TODO(kiro-cli): in-REPL /clear|/new → new session id
    restart)         not_implemented restart || break ;;         # TODO(kiro-cli): end + fresh session (new id, new cwd)
    resume)          not_implemented resume || break ;;          # TODO(kiro-cli): relaunch same id+cwd
    sigkill)         not_implemented sigkill || break ;;         # TODO(kiro-cli): kill -9 the session PID
    exit_clean)      not_implemented exit_clean || break ;;      # TODO(kiro-cli): Ctrl-D graceful shutdown
    start_session)   not_implemented start_session || break ;;   # TODO(kiro-cli): concurrent session, keep first alive
    session)         not_implemented session || break ;;         # TODO(kiro-cli): switch active slot
    *)               echo "[driver] unknown step type: $type" >&2; EXIT_REASON="nonzero(2)"; break ;;
  esac
  (( $(remaining_seconds) <= 0 )) && { EXIT_REASON="timeout"; break; }
done < <(jq -c '.[]' <<<"$SCRIPT_JSON")

# --- Write the staging contract ----------------------------------------------
# driver.exit-reason is written by the cleanup trap on exit (so it's recorded
# even on an early failure). Single-session driver: write session.uuid +
# transcript.path the daemon keyed on. (This scaffold is single-session only;
# the multi-session lists session.uuids / transcript.paths arrive when
# restart/reset_session/start_session seams are ported.)
resolve_transcript || true
if [[ -n "$UUID" ]]; then
  echo "$UUID" > "$STAGING/session.uuid"
  echo "[driver] session.uuid=$UUID" >&2
fi
if [[ -n "$TRANSCRIPT" ]]; then
  echo "$TRANSCRIPT" > "$STAGING/transcript.path"
  echo "[driver] transcript.path=$TRANSCRIPT" >&2
fi

[[ "$EXIT_REASON" == "ok" ]] && exit 0 || exit 1
