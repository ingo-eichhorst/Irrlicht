#!/usr/bin/env bash
# run-cell.sh — execute one (adapter, scenario) cell end-to-end.
#
# Pipeline:
#   recipe-lint  →  refuse a step type the driver lacks (#476, exit 3)
#   precheck.sh  →  spawn isolated irrlichd --record
#                →  drive-<adapter>.sh (runs the agent under timeout)
#                →  SIGINT → 6s grace → SIGTERM → SIGKILL the daemon
#                →  resolve transcript path from session UUID
#                →  tools/curate-lifecycle-fixture.sh -d <staging>/replaydata/agents
#                →  replay against staged + committed fixtures
#                →  write run-manifest.json
#
# After this script returns, the caller (skill.md, driven by Claude) reads
# the manifest + two replay reports and summarizes material changes.
#
# Usage:
#   run-cell.sh <adapter> <scenario-name>
#
# Outputs under ./.build/refresh/<adapter>/<scenario>-<UTC-ts>/:
#   recordings/            — isolated daemon recording (raw)
#   replaydata/agents/<adapter>/scenarios/<scenario>/{transcript,events}.jsonl  — staged fixture
#   reports/staged.json    — replay report over staged fixture
#   reports/committed.json — replay report over committed fixture (if any)
#   driver.log, driver.exit-reason, daemon.log
#   settings.json          — scenario's settings blob, written here for driver
#   run-manifest.json      — summary for the summarizer step

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(git rev-parse --show-toplevel 2>/dev/null || true)"
[[ -n "$REPO_ROOT" ]] || { echo "not in a git repo" >&2; exit 1; }

# shellcheck source=lib/shard-lib.sh
source "$SCRIPT_DIR/lib/shard-lib.sh"   # per-scenario shard reader (#511)

RECORDER="off"
ATTACH=0
positional=()
while [[ $# -gt 0 ]]; do
  case "$1" in
    --recorder=on)  RECORDER="on"; shift ;;
    --recorder=off) RECORDER="off"; shift ;;
    --recorder)
      echo "use --recorder=on or --recorder=off (no separate value)" >&2; exit 2 ;;
    --attach|-a) ATTACH=1; shift ;;
    -h|--help)
      echo "usage: run-cell.sh [--recorder=on|off] [--attach] <adapter> <scenario-name>" >&2; exit 0 ;;
    --) shift; positional+=("$@"); break ;;
    -*) echo "unknown flag: $1" >&2; exit 2 ;;
    *)  positional+=("$1"); shift ;;
  esac
done
if [[ ${#positional[@]} -ne 2 ]]; then
  echo "usage: run-cell.sh [--recorder=on|off] [--attach] <adapter> <scenario-name>" >&2
  exit 2
fi
ADAPTER="${positional[0]}"
SCENARIO="${positional[1]}"

# Resolve the shard that owns this cell. SCENARIO may be given as the coverage_id
# (shard name) OR a variant recording-folder name; both resolve to the same
# (coverage_id, folder) pair via the shard catalog (#511). The recipe is read
# under COVERAGE_ID; the on-disk recording folder is FOLDER (the bash twin of
# Go's resolveScenarioFolderForAgent — they differ for the 2 variant-folder cells).
COVERAGE_ID="$(shard_coverage_for_dir "$SCENARIO" "$ADAPTER")"
FOLDER="$(shard_folder "$COVERAGE_ID" "$ADAPTER")"

# --recorder=on is a deprecated no-op flag. Mode B's sensor recorder
# (signals.jsonl + frames + ground_truth) has been retired in favor of
# expected.jsonl as the single source of behavioral truth. Accept the
# flag for compatibility with older callers but emit nothing.
if [[ "$RECORDER" == "on" ]]; then
  echo "note: --recorder=on is deprecated (Mode B retired); flag has no effect." >&2
fi

# Look up the cell from its shard (#511). Absent cell → refuse.
# A cell carries either `prompt` (single-shot, headless driver) or `script`
# (array of step objects, interactive driver). Both can't be set.
CELL_JSON="$(shard_cell "$COVERAGE_ID" "$ADAPTER")"
if [[ -z "$CELL_JSON" || "$CELL_JSON" == "null" ]]; then
  echo "cell not found: scenario=$SCENARIO adapter=$ADAPTER (either unknown or missing-prompt)" >&2
  exit 1
fi

# An applicable:false cell carries a scope_note (or `notes`) explaining why;
# refuse with a clear message rather than the generic 'no prompt' error.
# Accept either key: the implement/recipe SKILL prescribes `notes`, while some
# older cells (and aider/pi) use `scope_note` — read both so the rationale is
# never silently dropped.
# Note: `jq -r '.applicable // empty'` collapses to empty because jq's //
# treats `false` as a falsy default — use `if … then … else …` instead.
APPLICABLE="$(jq -r 'if .applicable == false then "false" elif .applicable == true then "true" else "" end' <<<"$CELL_JSON")"
if [[ "$APPLICABLE" == "false" ]]; then
  SCOPE_NOTE="$(jq -r '.scope_note // .notes // "no scope_note provided"' <<<"$CELL_JSON")"
  echo "cell is not applicable for this adapter: scenario=$SCENARIO adapter=$ADAPTER" >&2
  echo "scope_note: $SCOPE_NOTE" >&2
  exit 2
fi

# Cross-adapter cells (a `partner_adapter` is declared) need a SECOND,
# different adapter live in the same cwd — the single-cell pipeline can't
# elicit that. Refuse here and point at the orchestrator.
PARTNER_ADAPTER="$(jq -r '.partner_adapter // empty' <<<"$CELL_JSON")"
if [[ -n "$PARTNER_ADAPTER" ]]; then
  echo "cell is cross-adapter (partner_adapter=$PARTNER_ADAPTER): scenario=$SCENARIO adapter=$ADAPTER" >&2
  echo "record it with: scripts/run-cell-multi.sh $COVERAGE_ID $ADAPTER" >&2
  exit 2
fi

TIMEOUT_S="$(jq -r '.timeout_seconds // 120' <<<"$CELL_JSON")"   # default when a recipe omits it (else drivers get the literal "null")
PROMPT="$(jq -r '.prompt // ""' <<<"$CELL_JSON")"
SCRIPT_JSON="$(jq -c '.script // empty' <<<"$CELL_JSON")"
if [[ -z "$PROMPT" && -z "$SCRIPT_JSON" ]]; then
  echo "cell has neither prompt nor script: scenario=$SCENARIO adapter=$ADAPTER" >&2
  exit 1
fi

# --- Recipe ↔ driver lint (#476) ----------------------------------------
# Static backstop: refuse a recipe that needs a step type the agent's
# interactive driver doesn't implement, BEFORE spinning up a daemon + CLI
# and burning tokens only to hit the runtime `unknown step type` arm.
# Headless `prompt` cells have no script and skip this. Exit 3 = driver_gap
# (distinct from exit 2 = applicable:false / cross-adapter), so the caller
# routes it to a driver-extension task rather than degrading the cell.
if [[ -n "$SCRIPT_JSON" ]]; then
  # shellcheck source=lib/recipe-lint.sh
  . "$SCRIPT_DIR/lib/recipe-lint.sh"
  LINT_DRIVER="$REPO_ROOT/replaydata/agents/$ADAPTER/driver-interactive.sh"
  if LINT_GAPS="$(recipe_lint_gaps "$LINT_DRIVER" "$COVERAGE_ID" "$ADAPTER")"; then :; else
    echo "driver_gap: $ADAPTER/$SCENARIO needs step type(s) driver-interactive.sh doesn't implement:" >&2
    printf '  - gap:%s\n' $LINT_GAPS >&2
    echo "Queue extend-driver $ADAPTER <primitive> (ports the step type), then implement — not recording yet." >&2
    exit 3
  fi
  # Semantic backstop (#496 RC3): a step the driver ACCEPTS but doesn't ELICIT
  # (or a slash command in send-text on a slash-requires adapter) would record
  # a no-op. The driver declares what it elicits via DRIVE_ELICITS (#508 #4).
  # Refuse before burning a daemon + CLI.
  if SEM_GAPS="$(recipe_semantic_gaps "$LINT_DRIVER" "$COVERAGE_ID" "$ADAPTER")"; then :; else
    echo "semantic_gap: $ADAPTER/$SCENARIO uses step(s) the driver accepts but doesn't elicit (per its DRIVE_ELICITS):" >&2
    # Quote + read-loop: a slash-in-send gap carries the full send-text, which
    # can contain spaces/glob chars — never word-split or pathname-expand it.
    while IFS= read -r p; do [[ -n "$p" ]] && printf '  - %s\n' "$p" >&2; done <<< "$SEM_GAPS"
    echo "Fix the recipe (use a dedicated slash/reset_session step) or extend the driver to truly elicit it — not recording." >&2
    exit 4
  fi
fi

# --- Precheck ------------------------------------------------------------
ATTACH="$ATTACH" "$SCRIPT_DIR/precheck.sh" "$ADAPTER"

# --- Staging -------------------------------------------------------------
# Stage under FOLDER (the on-disk recording dir), which equals COVERAGE_ID for
# all but the 2 variant-folder cells — so a re-record lands on the same dir the
# committed recording uses and promote-recording can diff it.
TS="$(date -u +%Y%m%dT%H%M%S)"
STAGING="$REPO_ROOT/.build/refresh/$ADAPTER/$FOLDER-$TS"
# shellcheck source=lib/assert-staging-path.sh
. "$REPO_ROOT/replaydata/_lib/assert-staging-path.sh"
mkdir -p "$STAGING/recordings" "$STAGING/replaydata/agents/$ADAPTER/scenarios/$FOLDER" "$STAGING/reports"

# Scenario's settings blob → staging file, passed to driver as a path.
# This avoids --settings <json-blob> shell-quoting fragility.
jq '.settings' <<<"$CELL_JSON" > "$STAGING/settings.json"

UUID="$(uuidgen | tr '[:upper:]' '[:lower:]')"

DAEMON="$REPO_ROOT/.build/refresh/bin/irrlichd"
REPLAY_BIN="$REPO_ROOT/.build/refresh/bin/replay"

# Isolation knobs (default = production layout). Set IRRLICHT_ONBOARD_HOME
# to a scratch dir to spawn the recording daemon with its OWN
# IRRLICHT_HOME (socket / addr file / state under there) on an alternate
# bind port, so it coexists with a running production irrlichd instead of
# clashing on 7837. Filesystem-observed adapters (codex/pi/aider/opencode)
# record fine this way because they watch the real $HOME (e.g.
# ~/.codex/sessions) regardless of IRRLICHT_HOME. claudecode is the one
# exception — its hooks POST to a hardcoded :7837 — so precheck refuses
# claudecode in coexist mode.
ONBOARD_HOME="${IRRLICHT_ONBOARD_HOME:-}"
if [[ -n "$ONBOARD_HOME" ]]; then
  # Coexist mode: default to an alternate port so we don't clash with a
  # production daemon on 7837 (precheck refuses 7837 in coexist mode). A
  # 7837 default here would make the one-knob `IRRLICHT_ONBOARD_HOME=…`
  # path abort at precheck.
  ONBOARD_BIND="${IRRLICHT_ONBOARD_BIND_ADDR:-127.0.0.1:7838}"
  ONBOARD_SOCK="$ONBOARD_HOME/irrlichd.sock"
else
  ONBOARD_BIND="${IRRLICHT_ONBOARD_BIND_ADDR:-127.0.0.1:7837}"
  ONBOARD_SOCK="$HOME/.local/share/irrlicht/irrlichd.sock"
fi

# --- Daemon source ------------------------------------------------------
# Two modes:
#  - isolated (default): spawn a dedicated `irrlichd --record` on
#    $ONBOARD_BIND (7837 unless overridden) with $STAGING/recordings as
#    its recordings dir. Killed after the driver returns so the recorder
#    flushes cleanly.
#  - attached ($ATTACH=1): use the user's already-running irrlichd.
#    Dashboard stays connected for the whole recording. We don't spawn
#    or kill anything; instead we capture the start timestamp now, sleep
#    long enough after the driver returns for the recorder's 5s
#    periodic flush, and pick the recording file from whatever the
#    daemon is writing to (env override > default
#    ~/.local/share/irrlicht/recordings/).
DAEMON_PID=""
if [[ "$ATTACH" == "1" ]]; then
  ATTACHED_RECORDINGS_DIR="${IRRLICHT_RECORDINGS_DIR:-$HOME/.local/share/irrlicht/recordings}"
  if [[ ! -d "$ATTACHED_RECORDINGS_DIR" ]]; then
    echo "attach: recordings dir not found: $ATTACHED_RECORDINGS_DIR" >&2
    echo "        set IRRLICHT_RECORDINGS_DIR or ensure the daemon is running with --record" >&2
    exit 1
  fi
  # Marker file pre-dating the driver run; used later to pick out
  # recording files touched while the driver ran.
  ATTACH_MARKER="$STAGING/attach.start"
  : > "$ATTACH_MARKER"
  # Validate the daemon is actually recording — the dir must contain at
  # least one .jsonl. (A daemon not in --record mode has an empty dir.)
  if ! ls "$ATTACHED_RECORDINGS_DIR"/*.jsonl >/dev/null 2>&1; then
    echo "attach: $ATTACHED_RECORDINGS_DIR contains no .jsonl files" >&2
    echo "        is the running irrlichd in --record mode?" >&2
    exit 1
  fi
  # Consent gate (#570): an attached ask-mode daemon with unanswered/denied
  # permissions monitors nothing and would record an EMPTY fixture that the
  # .jsonl check above can't catch (prior recordings satisfy it). Accept
  # grant-all mode, a fully-granted daemon, or a pre-#570 daemon (no
  # /api/v1/permissions endpoint → empty response → skip the check).
  PERM_JSON="$(curl -fsS --max-time 2 "http://$ONBOARD_BIND/api/v1/permissions" 2>/dev/null || true)"
  if [[ -n "$PERM_JSON" ]]; then
    PERM_OK="$(jq -r '(.mode == "grant-all") or ([.agents[].permissions[].state] | all(. == "granted"))' <<<"$PERM_JSON" 2>/dev/null || echo false)"
    if [[ "$PERM_OK" != "true" ]]; then
      echo "attach: daemon at $ONBOARD_BIND has unanswered/denied permissions — it would monitor nothing and record an empty fixture" >&2
      echo "        restart it with IRRLICHT_PERMISSION_MODE=grant-all (or grant every permission via the wizard)" >&2
      exit 1
    fi
  fi
  echo "attach: using running daemon's recordings at $ATTACHED_RECORDINGS_DIR"
else
  DAEMON_LOG="$STAGING/daemon.log"
  # Build the env assignments as an array so a value containing spaces
  # (e.g. an IRRLICHT_HOME path with a space) stays one word — an
  # unquoted ${ONBOARD_HOME:+VAR="$ONBOARD_HOME"} would word-split on it.
  # IRRLICHT_HOME is only added when ONBOARD_HOME is non-empty.
  # grant-all: the consent-first permission gate (#570) would otherwise
  # leave a fresh recording daemon monitoring nothing until a wizard is
  # answered — fixtures must never hang on consent.
  DAEMON_ENV=(IRRLICHT_RECORDINGS_DIR="$STAGING/recordings"
              IRRLICHT_BIND_ADDR="$ONBOARD_BIND"
              IRRLICHT_PERMISSION_MODE=grant-all)
  [[ -n "$ONBOARD_HOME" ]] && DAEMON_ENV+=(IRRLICHT_HOME="$ONBOARD_HOME")
  # Forward a caller-set ready-session TTL so idle-survival cells (1.3) can
  # shrink the 30-min production default to a recordable window. The daemon's
  # explicit env array would otherwise drop the inherited override.
  [[ -n "${IRRLICHT_READY_SESSION_TTL:-}" ]] && DAEMON_ENV+=(IRRLICHT_READY_SESSION_TTL="$IRRLICHT_READY_SESSION_TTL")
  env "${DAEMON_ENV[@]}" "$DAEMON" --record >"$DAEMON_LOG" 2>&1 &
  DAEMON_PID=$!
  echo "daemon started (pid $DAEMON_PID, bind=$ONBOARD_BIND${ONBOARD_HOME:+, home=$ONBOARD_HOME})"

  # Cleanup: graceful shutdown. Runs once: either via explicit call
  # before transcript resolution (we must drain before continuing), or
  # as the EXIT trap if we fail before reaching that point. `trap - EXIT`
  # after the explicit call prevents double-invocation.
  SHUTDOWN_REASON="unknown"
  cleanup() {
    if kill -0 "$DAEMON_PID" 2>/dev/null; then
      SHUTDOWN_REASON="sigint"
      kill -INT "$DAEMON_PID" 2>/dev/null || true
      # 6s grace = 5s recorder flush interval + 1s slack.
      for _ in $(seq 1 12); do
        kill -0 "$DAEMON_PID" 2>/dev/null || { echo "$SHUTDOWN_REASON" > "$STAGING/daemon.shutdown"; return; }
        sleep 0.5
      done
      SHUTDOWN_REASON="sigterm"
      kill -TERM "$DAEMON_PID" 2>/dev/null || true
      for _ in $(seq 1 6); do
        kill -0 "$DAEMON_PID" 2>/dev/null || { echo "$SHUTDOWN_REASON" > "$STAGING/daemon.shutdown"; return; }
        sleep 0.5
      done
      SHUTDOWN_REASON="sigkill"
      kill -KILL "$DAEMON_PID" 2>/dev/null || true
    fi
    echo "$SHUTDOWN_REASON" > "$STAGING/daemon.shutdown"
  }
  trap cleanup EXIT

  # Wait up to 10s for the unix socket to appear — signals the daemon is
  # ready to accept connections.
  SOCK="$ONBOARD_SOCK"
  for _ in $(seq 1 40); do
    [[ -S "$SOCK" ]] && break
    sleep 0.25
  done
  [[ -S "$SOCK" ]] || { echo "daemon socket never appeared: $SOCK" >&2; exit 1; }
fi

# --- Drive the agent ----------------------------------------------------
# Drivers are responsible for resolving the transcript path and writing
# session.uuid + transcript.path back to staging. UUID arg $2 is a
# "preferred" UUID — drive-claudecode.sh honors it via --session-id;
# codex/pi drivers ignore it and surface the agent-assigned UUID.
# Cells with a `script` block route through the interactive driver (REPL +
# step-script). Plain `prompt` cells use the headless driver.
if [[ -n "$SCRIPT_JSON" ]]; then
  DRIVER="$REPO_ROOT/replaydata/agents/$ADAPTER/driver-interactive.sh"
  DRIVER_INPUT="$SCRIPT_JSON"
else
  DRIVER="$REPO_ROOT/replaydata/agents/$ADAPTER/driver.sh"
  DRIVER_INPUT="$PROMPT"
fi
[[ -x "$DRIVER" ]] || { echo "driver missing: $DRIVER" >&2; exit 1; }
set +e
"$DRIVER" "$STAGING" "$UUID" "$TIMEOUT_S" "$STAGING/settings.json" "$DRIVER_INPUT"
set -e
DRIVER_REASON="$(cat "$STAGING/driver.exit-reason" 2>/dev/null || echo "unknown")"

# Flush the daemon's recorder before we curate.
#  - isolated: SIGINT and wait for graceful shutdown (flushes on Close).
#  - attached: just wait 6s — the recorder's 5s periodic flush + 1s
#    slack is enough to land all writes from this run on disk. The
#    user's daemon keeps running and the dashboard stays connected.
if [[ "$ATTACH" == "1" ]]; then
  SHUTDOWN_REASON="attached"
  echo "$SHUTDOWN_REASON" > "$STAGING/daemon.shutdown"
  echo "attach: waiting 6s for recorder flush..."
  sleep 6
else
  cleanup
  trap - EXIT
fi

# --- Read driver-resolved transcript + actual UUID ----------------------
TRANSCRIPT="$(cat "$STAGING/transcript.path" 2>/dev/null || true)"
ACTUAL_UUID="$(cat "$STAGING/session.uuid" 2>/dev/null || true)"

# Multi-session: drivers that chain `restart` steps (e.g. claudecode's
# session-end scenario) write the full UUID + transcript lists to
# session.uuids / transcript.paths. Curate picks them up via env so
# the fixture's events.jsonl filter includes all sessions and the
# transcript output concatenates them in order.
if [[ -f "$STAGING/session.uuids" ]]; then
  uuid_count=$(grep -c . "$STAGING/session.uuids" || echo 0)
  if [[ "$uuid_count" -gt 1 ]]; then
    EXTRA_IDS=""
    while IFS= read -r u; do
      [[ -z "$u" ]] && continue
      [[ "$u" == "$ACTUAL_UUID" ]] && continue
      EXTRA_IDS+="${EXTRA_IDS:+,}${u}"
    done < "$STAGING/session.uuids"
    export IRRLICHT_EXTRA_SESSION_IDS="$EXTRA_IDS"
    # Concatenate all transcript paths (newline-separated) so curate
    # can build a single transcript.jsonl in chronological order.
    export IRRLICHT_EXTRA_TRANSCRIPTS="$(cat "$STAGING/transcript.paths")"
    echo "multi-session: primary=$ACTUAL_UUID, extras=$EXTRA_IDS"
  fi
fi

# --- Locate the recording file ------------------------------------------
# Isolated mode: one .jsonl in $STAGING/recordings/.
# Attached mode: pick the file(s) in the running daemon's recordings
# dir that the daemon was writing to during this run (i.e. mtime newer
# than the attach marker we dropped before the driver ran). The daemon
# rotates by start-time so a recording in progress always has the
# newest mtime; multiple may match if the daemon rotated mid-run.
if [[ "$ATTACH" == "1" ]]; then
  RECORDING="$(find "$ATTACHED_RECORDINGS_DIR" -maxdepth 1 -name '*.jsonl' -type f -newer "$ATTACH_MARKER" 2>/dev/null | sort | tail -n1)"
  if [[ -z "$RECORDING" ]]; then
    # Fall back to the most-recent file regardless of mtime, in case
    # the daemon's writes haven't bumped the mtime past our marker.
    RECORDING="$(find "$ATTACHED_RECORDINGS_DIR" -maxdepth 1 -name '*.jsonl' -type f 2>/dev/null | xargs ls -1t 2>/dev/null | head -n1)"
  fi
else
  RECORDING="$(find "$STAGING/recordings" -maxdepth 1 -name '*.jsonl' -type f 2>/dev/null | head -n1)"
fi

# --- Daemon-recorded session-id mapping ---------------------------------
# The daemon's session_id often differs from the agent's native UUID:
#   - aider: daemon synthesizes proc-<pid>; agent has no native id
#   - codex: daemon strips ".jsonl" from "rollout-<ts>-<uuid>.jsonl"
#   - pi:    daemon strips ".jsonl" from "<ts>_<uuid>.jsonl"
#   - claudecode: filename IS the UUID, so daemon and driver agree
# Drivers write the agent's preferred UUID to session.uuid for fixture-
# naming parity. Look up the actual session_id the daemon recorded for
# this transcript so curate-lifecycle-fixture.sh can filter the recording
# against real events. The lookup keys on transcript_path and is a no-op
# when the IDs already agree (claudecode). When multiple PIDs share one
# transcript (e.g. aider's Python wrapper + worker), we pick the earliest
# by sequence number; curate's existing pid_discovered scan picks up the
# other PIDs from there.
# The adapter field in transcript_new uses the daemon's canonical name,
# which matches $ADAPTER for aider/codex/pi but is "claude-code" for
# claudecode — for that adapter the lookup naturally finds nothing and
# the original UUID is preserved.
if [[ -n "$RECORDING" && -n "$TRANSCRIPT" ]]; then
  RECORDED_SID="$(jq -r --arg path "$TRANSCRIPT" --arg ad "$ADAPTER" '
    select(.adapter==$ad and .kind=="transcript_new" and .transcript_path==$path)
    | [.seq, .session_id] | @tsv' "$RECORDING" | sort -n | head -n1 | cut -f2)"
  if [[ -n "$RECORDED_SID" ]]; then
    ACTUAL_UUID="$RECORDED_SID"
    echo "$ACTUAL_UUID" > "$STAGING/session.uuid"
  fi
fi

MANIFEST="$STAGING/run-manifest.json"
DAEMON_SHUTDOWN="$(cat "$STAGING/daemon.shutdown" 2>/dev/null || echo "unknown")"

# Write an ERROR-verdict run-manifest with the standard envelope plus
# error-specific fields supplied as a JSON object (pass '{}' for none).
write_error_manifest() {
  local error_code="$1"
  local extras_json="$2"
  jq -n \
    --arg adapter "$ADAPTER" \
    --arg scenario "$FOLDER" \
    --arg session_uuid "$ACTUAL_UUID" \
    --arg error "$error_code" \
    --arg driver_exit_reason "$DRIVER_REASON" \
    --arg daemon_shutdown "$DAEMON_SHUTDOWN" \
    --arg staging "$STAGING" \
    --argjson extras "$extras_json" \
    '{adapter: $adapter,
      scenario: $scenario,
      session_uuid: $session_uuid,
      verdict: "ERROR",
      error: $error,
      driver_exit_reason: $driver_exit_reason,
      daemon_shutdown: $daemon_shutdown,
      staging: $staging} + $extras' \
    > "$MANIFEST"
}

if [[ -z "$TRANSCRIPT" || -z "$RECORDING" || -z "$ACTUAL_UUID" ]]; then
  write_error_manifest "transcript_recording_or_uuid_missing" \
    "$(jq -nc \
        --argjson transcript_found "$([[ -n "$TRANSCRIPT" ]] && echo true || echo false)" \
        --argjson recording_found "$([[ -n "$RECORDING" ]] && echo true || echo false)" \
        --argjson uuid_resolved "$([[ -n "$ACTUAL_UUID" ]] && echo true || echo false)" \
        '{transcript_found: $transcript_found, recording_found: $recording_found, uuid_resolved: $uuid_resolved}')"
  echo "ERROR: transcript=${TRANSCRIPT:-missing} recording=${RECORDING:-missing} uuid=${ACTUAL_UUID:-missing}" >&2
  exit 1
fi

# --- Subagent probe -----------------------------------------------------
# If the scenario requires the `subagents` capability, the run is only
# meaningful if the parent actually emitted Agent tool calls and the daemon
# saw the resulting parent_linked events. Fail cleanly here so the manifest
# carries a structured reason instead of producing an empty .subagents/ dir
# downstream.
# "subagents" matches agents.CapSubagents in core/adapters/inbound/agents/config.go.
REQUIRES_SUBAGENTS="$(jq -r '.requires | index("subagents") // empty' <<<"$CELL_JSON")"
if [[ -n "$REQUIRES_SUBAGENTS" ]]; then
  PARENT_LINKED_COUNT="$(jq -c --arg sid "$ACTUAL_UUID" \
    'select(.kind=="parent_linked" and .parent_session_id==$sid)' \
    "$RECORDING" | wc -l | tr -d ' ')"
  # File-based subagent transcript probe — applies only to adapters that
  # write child sessions as separate .jsonl files (claudecode's
  # <parent>/subagents/agent-<uuid>.jsonl convention). For adapters whose
  # children live in a shared store (opencode = SQLite rows on the same
  # DB), the parent_linked count alone is the spawn proof.
  case "$ADAPTER" in
    opencode)
      SUBAGENT_FILES="$PARENT_LINKED_COUNT"
      ;;
    *)
      SUBAGENT_DIR="$(dirname "$TRANSCRIPT")/$ACTUAL_UUID/subagents"
      count_subagent_files() {
        find "$SUBAGENT_DIR" -maxdepth 1 -name '*.jsonl' -type f 2>/dev/null | wc -l | tr -d ' '
      }
      SUBAGENT_FILES="$(count_subagent_files)"
      # If the daemon saw parent_linked events but the child transcripts
      # haven't been flushed to disk yet (race against the parent
      # transcript's appearance), poll briefly. We only poll when we
      # already know children exist — otherwise there's nothing to wait
      # for.
      if [[ "$PARENT_LINKED_COUNT" -gt 0 && "$SUBAGENT_FILES" -eq 0 ]]; then
        for _ in $(seq 1 20); do
          sleep 0.5
          SUBAGENT_FILES="$(count_subagent_files)"
          [[ "$SUBAGENT_FILES" -gt 0 ]] && break
        done
      fi
      ;;
  esac
  if [[ "$PARENT_LINKED_COUNT" -eq 0 || "$SUBAGENT_FILES" -eq 0 ]]; then
    write_error_manifest "no_subagents_spawned" \
      "$(jq -nc \
          --argjson parent_linked_count "$PARENT_LINKED_COUNT" \
          --argjson subagent_transcript_count "$SUBAGENT_FILES" \
          '{parent_linked_count: $parent_linked_count, subagent_transcript_count: $subagent_transcript_count}')"
    echo "ERROR: scenario requires subagents but none spawned (parent_linked=$PARENT_LINKED_COUNT, files=$SUBAGENT_FILES)" >&2
    exit 1
  fi
fi

# --- Curate the staged fixture ------------------------------------------
# The committed-to-replaydata location of the curated artifacts is:
#   <staging>/replaydata/agents/<adapter>/scenarios/<scenario>/{transcript,events}.jsonl
"$REPO_ROOT/tools/curate-lifecycle-fixture.sh" \
  -d "$STAGING/replaydata/agents" \
  "$RECORDING" "$ACTUAL_UUID" "$TRANSCRIPT" "$ADAPTER" "$FOLDER"

# Adapter declares its curated transcript extension in _meta.json (#511; was
# the per-adapter capabilities.json). Default "jsonl".
TRANSCRIPT_EXT="$(meta_transcript_ext "$ADAPTER")"
STAGED_TRANSCRIPT="$STAGING/replaydata/agents/$ADAPTER/scenarios/$FOLDER/transcript.$TRANSCRIPT_EXT"

# --- Build replay reports -----------------------------------------------
# precheck.sh pre-built the replay binary under .build/refresh/bin/replay
# so we avoid `go run` recompile on each cell invocation.
# The replay CLI exits non-zero when extended-check finds daemon-vs-simulator
# transition mismatches. The report is still written and is the authoritative
# artifact — extended-check is informational. Treat nonzero as "report OK,
# warnings present"; only a missing report file counts as a real failure.
replay_one() {
  local transcript="$1" out="$2"
  (cd "$REPO_ROOT" && "$REPLAY_BIN" --quiet --out "$out" "$transcript") || true
  [[ -s "$out" ]] || { echo "replay failed (no report written) for $transcript" >&2; return 1; }
}

replay_one "$STAGED_TRANSCRIPT" "$STAGING/reports/staged.json" || exit 1

# The committed recording lives under recordings/<newest>/ (no "latest" at the
# cell root). Compare the staged transcript against the newest committed one.
COMMITTED_CELL="$REPO_ROOT/replaydata/agents/$ADAPTER/scenarios/$FOLDER"
# A never-recorded cell has no recordings/ dir, so the glob matches nothing
# and `ls` exits non-zero — under `set -euo pipefail` that would abort the
# whole run right after a successful capture. Tolerate the empty match; the
# `[[ -n "$NEWEST_REC" ... ]]` guard below already handles "no committed
# recording yet" (COMMITTED_PRESENT=false).
NEWEST_REC="$(ls -1d "$COMMITTED_CELL"/recordings/*/ 2>/dev/null | sort | tail -n1 || true)"
COMMITTED_TRANSCRIPT="${NEWEST_REC%/}/transcript.$TRANSCRIPT_EXT"
if [[ -n "$NEWEST_REC" && -f "$COMMITTED_TRANSCRIPT" ]]; then
  replay_one "$COMMITTED_TRANSCRIPT" "$STAGING/reports/committed.json" || exit 1
  COMMITTED_PRESENT=true
else
  COMMITTED_PRESENT=false
fi

# --- Manifest -----------------------------------------------------------
jq -n \
  --arg adapter "$ADAPTER" \
  --arg scenario "$FOLDER" \
  --arg session_uuid "$ACTUAL_UUID" \
  --arg staging "$STAGING" \
  --arg raw_recording "$RECORDING" \
  --arg source_transcript "$TRANSCRIPT" \
  --arg staged_fixture_transcript "$STAGED_TRANSCRIPT" \
  --arg staged_fixture_events "$STAGING/replaydata/agents/$ADAPTER/scenarios/$FOLDER/events.jsonl" \
  --arg staged_report "$STAGING/reports/staged.json" \
  --argjson committed_fixture_present "$COMMITTED_PRESENT" \
  --arg committed_report "$STAGING/reports/committed.json" \
  --arg driver_exit_reason "$DRIVER_REASON" \
  --arg daemon_shutdown "$DAEMON_SHUTDOWN" \
  --argjson timeout_seconds "$TIMEOUT_S" \
  '{adapter: $adapter,
    scenario: $scenario,
    session_uuid: $session_uuid,
    verdict: "STAGED",
    staging: $staging,
    raw_recording: $raw_recording,
    source_transcript: $source_transcript,
    staged_fixture_transcript: $staged_fixture_transcript,
    staged_fixture_events: $staged_fixture_events,
    staged_report: $staged_report,
    committed_fixture_present: $committed_fixture_present,
    committed_report: $committed_report,
    driver_exit_reason: $driver_exit_reason,
    daemon_shutdown: $daemon_shutdown,
    timeout_seconds: $timeout_seconds}' \
  > "$MANIFEST"

echo "staged: $STAGED_TRANSCRIPT"
echo "manifest: $MANIFEST"
