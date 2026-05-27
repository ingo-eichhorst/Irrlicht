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
# Per-turn model selection (model-select): a `send` step may carry an
# optional `model` field; when present the headless arm passes
# `opencode run -m <provider/model>` so that turn runs on the named model.
# Omitting it runs on the config default (unchanged for every other cell).
# This is how a single --session chain runs turn 1 on model A and turn 2 on
# model B — a real mid-session switch the daemon observes per turn (it reads
# message.data.model.modelID from the SQLite store, latest-wins). It is a
# headless-path enhancement, NOT a TUI/run_live change — the in-REPL /models
# picker is an arrow-key overlay and stays out of scope.
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
# SETTINGS_PATH ($4): a recipe `settings` blob. opencode resolves config
# from a project-local opencode.json in the run directory (then walks up to
# the global ~/.config/opencode/opencode.json), so a non-empty blob is
# seeded as RUN_CWD/opencode.json below to drive per-run policy — most
# importantly the `permission` classifier (bash/edit/… = allow|ask|deny,
# bash wildcards). An empty blob ({} / absent) writes NO file, preserving
# the prior global-config-only behavior for every other opencode cell.
SETTINGS_PATH="$4"
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
# Canonicalize (resolve symlinks) so the session-lookup WHERE clause matches
# what opencode stores: node's process.cwd() writes the RESOLVED path into
# session.directory, so on macOS a /tmp/... cwd is stored as /private/tmp/...
# — querying the unresolved value would find no row and silently mis-record.
RUN_CWD="$(cd "$RUN_CWD" && pwd -P)"

# Seed a project-local opencode.json from the recipe's `settings` blob when
# it carries config (a non-empty object). opencode loads project config from
# the run directory before the global ~/.config/opencode/opencode.json, so
# this is how a cell pins a per-run `permission` policy (the auto-classifier:
# bash/edit/… = allow|ask|deny + bash wildcards) without touching the user's
# global config. The $schema is injected if the blob omits it so opencode
# validates it as a real config. An empty/absent blob writes NOTHING, so
# every other opencode cell keeps using the global-config-only path.
if [[ -n "${SETTINGS_PATH:-}" && -f "$SETTINGS_PATH" ]]; then
  if [[ "$(jq -r 'if (type=="object" and (.|length)>0) then "yes" else "no" end' "$SETTINGS_PATH" 2>/dev/null)" == "yes" ]]; then
    jq '. + (if has("$schema") then {} else {"$schema":"https://opencode.ai/config.json"} end)' \
      "$SETTINGS_PATH" > "$RUN_CWD/opencode.json"
    echo "[driver] seeded project config $RUN_CWD/opencode.json from recipe settings" >&2
  fi
fi

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

# ---------------------------------------------------------------------------
# LIVE-TUI path (slash commands / session reset)
# ---------------------------------------------------------------------------
# Headless `opencode run` cannot deliver in-REPL slash commands — it stores
# `/new` as ordinary prompt text (proven 2026-05-26). The `session-reset`
# scenario needs a live `/new` that retires the current ses_ row and mints a
# fresh one under the SAME process, so when a recipe carries a `reset_session`
# (or `slash`) step we drive opencode's TUI under tmux instead, exactly like
# the codex/pi/aider drivers. Session identity, the per-cwd lookup, and the
# transcript export are the SAME as the headless path; only the input
# mechanism (tmux send-keys) and turn detection (poll the SQLite store) differ.
# Emits the multi-session contract (session.uuids / transcript.paths) so
# run-cell.sh + curate-lifecycle-fixture.sh include BOTH the pre- and
# post-reset sessions, the same shape codex's reset_session produces.
run_live() {
  set +e   # this path manages failures via EXIT_REASON, not set -e aborts
  command -v tmux >/dev/null 2>&1 || {
    echo "[driver] live mode requires tmux (not found)" >&2
    echo "nonzero(2)" > "$STAGING/driver.exit-reason"
    exit 1
  }

  local SESSION="ocdrv-$$-$(date +%s)"
  # Per-turn baseline: the terminal-step-finish high-water mark captured at the
  # moment a send fires. oc_wait_turn waits for the terminal count to strictly
  # exceed it — exactly one NEW `stop` per turn. (A raw all-step count compared
  # to a turn counter over-counts: opencode emits a step-finish PER STEP, with
  # reason="tool-calls" for every mid-turn tool iteration, so a single
  # multi-step turn would push the total past the turn counter and make the
  # NEXT wait_turn return immediately on an unprocessed prompt.)
  local TURN_BASELINE=0

  echo "[driver] live mode: launching opencode TUI (tmux=$SESSION, cwd=$RUN_CWD)" >&2
  tmux kill-session -t "$SESSION" 2>/dev/null || true
  tmux new-session -d -s "$SESSION" -x 200 -y 50 -c "$RUN_CWD" "opencode" \
    >>"$DRIVER_LOG.stdout" 2>>"$DRIVER_LOG.stderr"
  # Always tear the TUI down, even on an error/timeout exit below.
  trap 'tmux kill-session -t "$SESSION" 2>/dev/null || true' EXIT

  # TERMINAL step-finish parts across OUR cwd's top-level sessions = completed
  # turns. opencode emits a step-finish per STEP, so only the terminal one —
  # reason="stop" — marks a turn boundary (matching parser.go's
  # parseStepFinish: `case "stop": ev.EventType = "turn_done"`). Mid-turn
  # tool-call iterations carry reason="tool-calls" and must NOT count.
  # Counting across all of the cwd's sessions (not just the active one) makes
  # the high-water mark survive the reset boundary: the post-/new session's
  # first terminal step-finish simply bumps the same total.
  oc_turn_total() {
    sqlite3 -cmd ".timeout 5000" "$OPENCODE_DB" \
      "SELECT count(*) FROM part p JOIN session s ON p.session_id = s.id
       WHERE s.directory = '$RUN_CWD' AND (s.parent_id IS NULL OR s.parent_id = '')
         AND json_extract(p.data,'\$.type') = 'step-finish'
         AND json_extract(p.data,'\$.reason') = 'stop';" 2>/dev/null || echo 0
  }

  # Readiness: opencode swallows keystrokes during its boot splash, so wait
  # for the input affordance to render before the first send, then a margin.
  local waited=0 ready=0
  while (( $(remaining_seconds) > 0 )) && (( waited < 40 )); do
    if tmux capture-pane -t "$SESSION" -p 2>/dev/null | grep -q "Ask anything"; then
      ready=1; break
    fi
    sleep 1; waited=$((waited + 1))
  done
  if (( ready == 0 )); then
    echo "[driver] live: opencode TUI never rendered an input prompt" >&2
    EXIT_REASON="readiness_timeout"
  else
    sleep 3
  fi

  # oc_composer_holds <prefix> → 0 if <prefix> is still sitting in the input
  # composer, 1 otherwise. The composer is the bottom box of the TUI (a ┃-margin
  # region above the model footer + ╹▀▀▀ top-border); the typed text lands on a
  # ┃-prefixed line there until Enter flushes it — submitted text moves UP into
  # the transcript (far above) or, for a local slash, vanishes. So we look ONLY
  # at the bottom slice (last 12 lines) for the prefix: that excludes the
  # transcript echo of an earlier prompt, which persists scrolled off the top
  # but never re-enters the composer box. The box is 200 cols wide (-x 200) so a
  # 24-char prefix never wraps; the prompt may carry a leading glyph so we match
  # the prefix substring, not a line anchor.
  oc_composer_holds() {
    tmux capture-pane -t "$SESSION" -p 2>/dev/null | tail -12 \
      | grep -qF "${1:0:24}"
  }

  # oc_flush <prefix> → press Enter and confirm the composer actually FLUSHED
  # (the just-typed text left the input box). Under the slow local model an
  # Enter-submitted prompt can linger; without this confirmation the NEXT
  # oc_send/slash types into a non-empty composer and the two inputs COALESCE
  # into one merged user message (and a following slash is swallowed as literal
  # text, never executing). Bounded by remaining_seconds with a 30s cap; if it
  # has not cleared by then, re-press Enter ONCE and proceed regardless.
  oc_flush() {
    local prefix="$1" waited=0 cap=30
    tmux send-keys -t "$SESSION" Enter
    while (( $(remaining_seconds) > 0 )) && (( waited < cap )); do
      sleep 1; waited=$((waited + 1))
      oc_composer_holds "$prefix" || return 0
      if (( waited == cap / 2 )); then
        echo "[driver] live: composer still holds input after ${waited}s, re-pressing Enter" >&2
        tmux send-keys -t "$SESSION" Enter
      fi
    done
    echo "[driver] live: composer did not confirm flush within ${cap}s; proceeding" >&2
    return 0
  }

  oc_send() {
    local text="$1" tries=0
    while (( tries < 3 )); do
      tmux send-keys -t "$SESSION" -l -- "$text"
      sleep 0.5
      # Confirm the keystrokes landed (boot splash can still eat them). Scan the
      # FULL pane, not just the composer slice: on the first send the composer
      # is the centered welcome screen (the "Ask anything" affordance sits
      # mid-pane, not in the bottom box), so a bottom-only match would miss it
      # and retype. The input box is 200 cols wide so a 24-char prefix never
      # wraps.
      if tmux capture-pane -t "$SESSION" -p 2>/dev/null | grep -qF "${text:0:24}"; then
        break
      fi
      tries=$((tries + 1))
      echo "[driver] live: send did not register (try $tries), retrying" >&2
      sleep 1
    done
    # Snapshot the terminal-step-finish high-water mark BEFORE submitting; the
    # matching wait_turn waits for the count to strictly exceed this, i.e. for
    # the ONE new `stop` this turn will produce. Captured pre-Enter (not in
    # wait_turn, and not after the flush) so that (a) a multi-step turn already
    # in flight can't satisfy the next turn's wait, and (b) a sub-second turn
    # that completes before oc_flush confirms the composer cleared does not get
    # folded INTO its own baseline — which would make its wait_turn need an
    # extra stop that never comes and time out.
    TURN_BASELINE=$(oc_turn_total)
    # Press Enter and wait for the composer to FLUSH (text submitted + cleared)
    # before returning — a lingering composer would let the next input merge
    # into this one. (See oc_flush.)
    oc_flush "$text"
    echo "[driver] live send (terminal baseline=$TURN_BASELINE): ${text:0:60}" >&2
  }

  oc_wait_turn() {
    local now
    while (( $(remaining_seconds) > 0 )); do
      now=$(oc_turn_total)
      if (( now > TURN_BASELINE )); then
        echo "[driver] live wait_turn: terminal step-finish total=$now (> baseline $TURN_BASELINE)" >&2
        return 0
      fi
      sleep 2
    done
    echo "[driver] live wait_turn: timeout (total=$(oc_turn_total), needed > baseline $TURN_BASELINE)" >&2
    EXIT_REASON="timeout"
    return 1
  }

  # /new clears the conversation; the fresh ses_ row is minted lazily on the
  # NEXT send (same as the initial session, and codex's post-/clear rollout).
  # No baseline bump — the reset itself is not a turn, and the next oc_send
  # snapshots the (monotonic, reset-surviving) terminal count for its own turn.
  oc_reset() {
    echo "[driver] live reset_session: delivering /new" >&2
    tmux send-keys -t "$SESSION" -l -- "/new"
    sleep 0.6
    tmux send-keys -t "$SESSION" Enter
    sleep 2
  }

  if [[ "$EXIT_REASON" == "ok" ]]; then
    local STEP_COUNT i STEP TYPE
    STEP_COUNT=$(jq 'length' <<<"$SCRIPT_JSON")
    for (( i = 0; i < STEP_COUNT; i++ )); do
      STEP=$(jq -c ".[$i]" <<<"$SCRIPT_JSON")
      TYPE=$(jq -r '.type' <<<"$STEP")
      case "$TYPE" in
        send)
          oc_send "$(jq -r '.text' <<<"$STEP")"
          ;;
        wait_turn)
          oc_wait_turn || break
          ;;
        reset_session)
          oc_reset
          ;;
        slash)
          local s; s=$(jq -r '.text // .command // empty' <<<"$STEP")
          tmux send-keys -t "$SESSION" -l -- "$s"; sleep 0.5
          # A slash must go into an EMPTY composer and confirm it submitted
          # before the next step runs — otherwise it merges with a lingering
          # prompt and is swallowed as literal text instead of executing. A
          # local no-LLM slash (/undo, /help) produces no model turn, so we
          # confirm via composer-clear (oc_flush), not a turn count.
          oc_flush "$s"
          echo "[driver] live slash: $s" >&2
          ;;
        sleep)
          local secs; secs=$(jq -r '.seconds // empty' <<<"$STEP")
          if ! [[ "$secs" =~ ^[0-9]+(\.[0-9]+)?$ ]]; then
            echo "[driver] ERROR: sleep step missing/non-numeric: $STEP" >&2
            EXIT_REASON="nonzero(2)"; break
          fi
          echo "[driver] live sleep ${secs}s" >&2; sleep "$secs"
          ;;
        *)
          echo "[driver] ERROR: unknown step type '$TYPE'" >&2
          EXIT_REASON="nonzero(2)"; break
          ;;
      esac
    done
  fi

  # Settle so the daemon's recorder observes the final state, then drop the TUI.
  sleep 1
  tmux kill-session -t "$SESSION" 2>/dev/null || true
  trap - EXIT

  # Enumerate OUR cwd's top-level sessions chronologically, skipping any empty
  # stub (a session row with no message). [old, new] after one reset.
  local SIDS=()
  while IFS= read -r sid; do
    [[ -n "$sid" ]] && SIDS+=("$sid")
  done < <(sqlite3 -cmd ".timeout 5000" "$OPENCODE_DB" \
    "SELECT s.id FROM session s
     WHERE s.directory = '$RUN_CWD' AND (s.parent_id IS NULL OR s.parent_id = '')
       AND EXISTS (SELECT 1 FROM message m WHERE m.session_id = s.id)
     ORDER BY s.time_created ASC;" 2>/dev/null)

  : > "$STAGING/session.uuids"
  : > "$STAGING/transcript.paths"
  local n=0 first_uuid="" first_path="" sid tpath
  for sid in ${SIDS[@]+"${SIDS[@]}"}; do
    n=$((n + 1))
    tpath="$STAGING/opencode-transcript.$n.jsonl"
    : > "$tpath"
    sqlite3 "$OPENCODE_DB" >>"$tpath" <<SQL
.timeout 5000
.mode list
.separator ""
SELECT json_set(
  p.data,
  '\$._role',  json_extract(m.data, '\$.role'),
  '\$._cwd',   s.directory,
  '\$._ts',    p.time_updated,
  '\$._model', json_extract(m.data, '\$.model.modelID'),
  '\$._error', json(json_extract(m.data, '\$.error'))
)
FROM part p
JOIN message m ON p.message_id = m.id
JOIN session s ON p.session_id = s.id
WHERE p.session_id = '$sid'
ORDER BY p.time_created ASC, p.id ASC;
SQL
    echo "$sid" >> "$STAGING/session.uuids"
    echo "$tpath" >> "$STAGING/transcript.paths"
    if [[ -z "$first_uuid" ]]; then first_uuid="$sid"; first_path="$tpath"; fi
    echo "[driver] live session #$n: $sid -> $tpath ($(wc -l <"$tpath" | tr -d ' ') parts)" >&2
  done

  if [[ -z "$first_uuid" && "$EXIT_REASON" == "ok" ]]; then
    echo "[driver] live: no session rows materialized under $RUN_CWD" >&2
    EXIT_REASON="nonzero(1)"
  fi

  echo "$EXIT_REASON" > "$STAGING/driver.exit-reason"
  echo "${first_uuid}" > "$STAGING/session.uuid"
  echo "${first_path}" > "$STAGING/transcript.path"

  {
    echo "=== live opencode driver ==="
    echo "exit reason : $EXIT_REASON"
    echo "sessions    : ${SIDS[*]:-<none>}"
    echo "primary     : ${first_uuid:-<none>}"
  } > "$DRIVER_LOG"

  echo "drive-opencode-interactive(live): $EXIT_REASON (sessions=$n, primary=${first_uuid:-<none>})"
  case "$EXIT_REASON" in
    ok) exit 0 ;;
    *)  exit 1 ;;
  esac
}

run_send() {
  local text="$1"
  # Optional per-step model: when the send step carries a `model` field, thread
  # `opencode run -m <provider/model>` so THIS turn runs on the named model
  # instead of the config default. This is the model-select primitive
  # (gap:model-select): a real A->B model switch across turns within one
  # --session chain — the daemon reads per-turn message.data.model.modelID from
  # its SQLite store (latest-non-empty-wins, opencode/metrics.go), so the switch
  # is observable. Omitting the field (every other cell) passes NO -m flag and
  # keeps the single-config-default behavior byte-for-byte unchanged.
  local model="${2:-}"
  local args=()
  if [[ -n "$SESSION_ID" ]]; then
    args+=(--session "$SESSION_ID")
  fi
  if [[ -n "$model" ]]; then
    args+=(-m "$model")
  fi

  local remaining
  remaining=$(remaining_seconds)
  if (( remaining <= 0 )); then
    EXIT_REASON="timeout"
    return 1
  fi

  echo "[driver] send (session=${SESSION_ID:-<new>}, model=${model:-<default>}, remaining=${remaining}s): $text" >&2
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
  #
  # `parent_id IS NULL OR parent_id = ''` excludes subagent (child) sessions,
  # which opencode keys to the SAME directory as their parent — without the
  # filter a turn that spawns a subagent could capture the child id instead of
  # the parent the export below intends.
  #
  # `.timeout 5000` lets the read wait out a transient SQLITE_BUSY rather than
  # failing the bare `SESSION_ID=$(...)` under `set -e` — opencode is a
  # concurrent WAL writer (especially under the cross-adapter shared-cwd load).
  if [[ -z "$SESSION_ID" ]]; then
    SESSION_ID=$(sqlite3 -cmd ".timeout 5000" "$OPENCODE_DB" \
      "SELECT id FROM session WHERE directory = '$RUN_CWD' AND (parent_id IS NULL OR parent_id = '') ORDER BY time_created DESC LIMIT 1;")
    if [[ -z "$SESSION_ID" ]]; then
      echo "[driver] WARN: no session row found for cwd=$RUN_CWD" >&2
    else
      echo "[driver] captured session_id=$SESSION_ID" >&2
    fi
  fi
  return 0
}

# Route: a `reset_session` (or `slash`) step needs the live opencode TUI —
# headless `opencode run` treats slash commands as ordinary prompt text. All
# other opencode cells stay on the simpler, deterministic headless path.
if [[ "$(jq -r 'any(.[]?; .type == "reset_session" or .type == "slash")' <<<"$SCRIPT_JSON")" == "true" ]]; then
  run_live   # drives the TUI under tmux and exits; never returns here.
fi

# Iterate steps.
STEP_COUNT=$(jq 'length' <<<"$SCRIPT_JSON")
for (( i = 0; i < STEP_COUNT; i++ )); do
  STEP=$(jq -c ".[$i]" <<<"$SCRIPT_JSON")
  TYPE=$(jq -r '.type' <<<"$STEP")
  case "$TYPE" in
    send)
      TEXT=$(jq -r '.text' <<<"$STEP")
      # Optional `model` field → run THIS turn on the named provider/model via
      # `opencode run -m`; absent → config default (unchanged for every other
      # cell). This is the gap:model-select unblock for model-switch-midsession.
      MODEL=$(jq -r '.model // empty' <<<"$STEP")
      run_send "$TEXT" "$MODEL" || break
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
# `_role`, `_cwd`, `_ts`, `_model`, `_error` fields the OpenCode parser
# expects. This is what the replay tool reads from transcript.jsonl in the
# committed fixture.
TRANSCRIPT_OUT="$STAGING/opencode-transcript.jsonl"
: > "$TRANSCRIPT_OUT"
if [[ -n "$SESSION_ID" ]]; then
  # Role lives inside message.data JSON (no top-level column), so extract
  # it with json_extract. modelID lives in message.data.model.modelID.
  # `_error` carries message.data.error: an aborted/errored turn (quota,
  # context-overflow, provider error) records the failure on the MESSAGE,
  # not as a step-finish reason=error part — opencode emits only a bare
  # step-start part on that message — so message.data.error is the sole
  # turn-ending signal the daemon's watcher.go isErrorMessage keys on.
  # Without it the exported fixture loses the error and the replayed turn
  # never settles working→ready. `json(...)` nests the error sub-object as
  # real JSON (SQLite JSON null when absent — isErrorMessage treats null as
  # "no error"). (#493 daemon side; this is the matching export.)
  # Concurrent reads against opencode's running DB are safe — opencode
  # writes in WAL mode and sqlite3's default open mode tolerates a
  # parallel writer. The -readonly flag fails on this DB because it
  # disables the WAL fallback path; omit it. `.timeout 5000` waits out a
  # transient SQLITE_BUSY instead of producing a truncated/empty export.
  sqlite3 "$OPENCODE_DB" <<SQL >> "$TRANSCRIPT_OUT"
.timeout 5000
.mode list
.separator ""
SELECT json_set(
  p.data,
  '\$._role',  json_extract(m.data, '\$.role'),
  '\$._cwd',   s.directory,
  '\$._ts',    p.time_updated,
  '\$._model', json_extract(m.data, '\$.model.modelID'),
  '\$._error', json(json_extract(m.data, '\$.error'))
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
