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

# Recording/transcript files this script scans for are always *.jsonl; named
# once so the glob literal isn't repeated across find invocations (S1192).
JSONL_GLOB='*.jsonl'

# shellcheck source=lib/shard-lib.sh
source "$SCRIPT_DIR/lib/shard-lib.sh"   # per-scenario shard reader (#511)
# Recording-daemon lifecycle — env, spawn, socket wait, shutdown ladder, and the
# shared agent config snapshot/restore (#1178) — owned once and shared with
# run-cell-multi.sh so a recorder fix can't reach only one of them (#1214).
# shellcheck source=lib/spawn-record-daemon.sh
source "$SCRIPT_DIR/lib/spawn-record-daemon.sh"
# Recording-file selection. Attached mode must prove the file came from THIS run
# (#1333 / B6) — the old inline fallback curated whatever ran last.
# shellcheck source=lib/pick-recording.sh
source "$SCRIPT_DIR/lib/pick-recording.sh"
# "Did this run actually finish?" — shared with run-cell-multi.sh for the same
# reason the daemon lifecycle is (#1214).
# shellcheck source=lib/completeness-check.sh
source "$SCRIPT_DIR/lib/completeness-check.sh"
# Per-adapter agent-home isolation — the declaration of which adapters have a
# relocatable home, and which of them default to staging, shared with
# run-cell-multi.sh for the same reason (#1214).
# shellcheck source=lib/agent-home.sh
source "$SCRIPT_DIR/lib/agent-home.sh"
# Bounded wait for the daemon's hook install to land before the CLI reads its
# hook config — see the lib header for the recording it was written against.
# shellcheck source=lib/hook-install-wait.sh
source "$SCRIPT_DIR/lib/hook-install-wait.sh"

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
# PRECHECK_JSON_OUT captures the CLI version precheck detects, so
# promote-recording.sh can stamp it instead of re-deriving it (#1333 / B3). It
# has to be a temp path because $STAGING doesn't exist yet; it moves in below.
# Cleanup is explicit rather than an EXIT trap: spawn_record_daemon arms its own
# below, and a second `trap ... EXIT` here would replace it. precheck is the most
# likely thing on this path to fail (CLI missing, version floor, port busy), so
# the failure branch is the one that matters.
PRECHECK_JSON_TMP="$(mktemp -t irr-precheck.XXXXXX)"
if ! ATTACH="$ATTACH" PRECHECK_JSON_OUT="$PRECHECK_JSON_TMP" "$SCRIPT_DIR/precheck.sh" "$ADAPTER"; then
  rm -f "$PRECHECK_JSON_TMP"
  exit 1
fi

# --- Staging -------------------------------------------------------------
# Stage under FOLDER (the on-disk recording dir), which equals COVERAGE_ID for
# all but the 2 variant-folder cells — so a re-record lands on the same dir the
# committed recording uses and promote-recording can diff it.
TS="$(date -u +%Y%m%dT%H%M%S)"
STAGING="$REPO_ROOT/.build/refresh/$ADAPTER/$FOLDER-$TS"
# shellcheck source=lib/assert-staging-path.sh
. "$REPO_ROOT/replaydata/_lib/assert-staging-path.sh"
mkdir -p "$STAGING/recordings" "$STAGING/replaydata/agents/$ADAPTER/scenarios/$FOLDER" "$STAGING/reports"

# precheck's machine-readable output now has somewhere to live (#1333 / B3).
if [[ -s "$PRECHECK_JSON_TMP" ]]; then
  mv "$PRECHECK_JSON_TMP" "$STAGING/precheck.json"
fi
rm -f "$PRECHECK_JSON_TMP"

# Repo provenance (#1333 / B7). Serialized recording assumes exclusive use of
# the worktree; nothing enforced it, so a concurrent session's commits could
# leave a recording whose daemon_version names a build that never produced it.
# Capture HEAD now and again after the driver returns — promote-recording.sh
# stamps both and warns when they differ, making the smear visible not silent.
GIT_HEAD_START="$(git -C "$REPO_ROOT" rev-parse --short HEAD 2>/dev/null || echo unknown)"

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
# ~/.codex/sessions) regardless of IRRLICHT_HOME. Hook-driven observation
# works too, but for TWO different reasons depending on how the adapter's
# installed entry reaches the daemon, and only the first of them is #1178's:
#
#   URL delivery (claudecode, codex, copilot) — the entry the installer writes
#   CARRIES the address, resolved from IRRLICHT_BIND_ADDR at install time, so
#   the bytes in the user's config already name this daemon (#1178).
#
#   Beacon delivery (gemini-cli, kiro-cli, mistral-vibe — every adapter that
#   imports core/pkg/hookbeacon) — the entry carries NO address at all. It
#   names `irrlichd hook-post <adapter>`, and the beacon resolves where to POST
#   at FIRE time, from ITS OWN process environment: IRRLICHT_BIND_ADDR first,
#   then the addr file under IRRLICHT_HOME (core/pkg/daemonaddr resolveClient).
#   That process is a child of the agent CLI, which the driver launches under
#   tmux — so in coexist mode a pane carrying neither variable resolves the
#   PRODUCTION addr file and posts every hook to the daemon on 7837. The
#   recording then contains no hook_received event and reads as an adapter that
#   cannot report state. See the IRRLICHT_BIND_ADDR export below.
ONBOARD_HOME="${IRRLICHT_ONBOARD_HOME:-}"
if [[ -n "$ONBOARD_HOME" ]]; then
  # Coexist mode: default to an alternate port so we don't clash with a
  # production daemon on 7837 (precheck refuses 7837 in coexist mode). A
  # 7837 default here would make the one-knob `IRRLICHT_ONBOARD_HOME=…`
  # path abort at precheck.
  ONBOARD_BIND="${IRRLICHT_ONBOARD_BIND_ADDR:-127.0.0.1:7838}"
else
  ONBOARD_BIND="${IRRLICHT_ONBOARD_BIND_ADDR:-127.0.0.1:7837}"
fi

# The address a beacon-delivered hook must POST to, exported so the driver
# inherits it and can forward it into the tmux pane. This is the same
# half-wiring agent-home.sh's header describes for the *_HOME variables and the
# same fix: exporting it reaches the daemon (a direct child) but NOT the agent
# CLI, whose pane is spawned by the tmux SERVER. Each beacon adapter's driver
# therefore carries an explicit `env IRRLICHT_BIND_ADDR=…` prefix on its
# `tmux new-session`, and TestEveryBeaconAdapterDriverPassesTheDaemonAddress
# (tools/onboarding-factory/internal/righome) fails if one stops doing so.
#
# Exported unconditionally, including in the non-coexist path where the value
# is the default port the beacon would have picked anyway: a variable set only
# on the branch that needs it is a variable whose passthrough is exercised only
# on that branch.
export IRRLICHT_BIND_ADDR="$ONBOARD_BIND"

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
# Agent-home isolation. Which adapters have a relocatable home, which env var
# each reads, and which of them default to staging rather than being opt-in, is
# ONE declaration in lib/agent-home.sh — a per-adapter `if` here is how codex's
# isolation came to exist in this script and not in run-cell-multi.sh (the same
# split that made #1178's config snapshot reach one recorder and not the other,
# #1214). It runs BEFORE spawn_record_daemon so the daemon inherits the value
# and `--print-managed-files` — which is what the snapshot protects — resolves
# under the isolated home rather than the operator's real one.
#
# The staging default is spelled "<adapter>-home" so copilot — the one adapter
# on the `default` policy — keeps resolving to exactly the "$STAGING/copilot-home"
# its own driver falls back to when run standalone. Two spellings of one default
# is how the daemon and the driver end up on different stores.
agent_home_isolate "$ADAPTER" "$STAGING/$ADAPTER-home" || exit 1

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
  # Cheap pre-flight so an obviously-idle daemon fails BEFORE we spend credits:
  # the dir must contain at least one .jsonl. (A daemon not in --record mode has
  # an empty dir.) This proves PRESENCE only — a daemon that stopped recording
  # yesterday still passes it. The real freshness check is after the driver
  # returns, in pick_attached_recording (#1333 / B6); do not treat this as the
  # guard.
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
  # $ONBOARD_BIND is a loopback host:port for a daemon this rig starts itself,
  # which shell:S5332 would exempt if it could resolve the variable; it can't, and
  # it carries the finding into every reader of the response, hence both
  # annotations. The daemon's local API is plain HTTP by design — there is no TLS
  # listener to point at instead.
  PERM_JSON="$(curl -fsS --max-time 2 "http://$ONBOARD_BIND/api/v1/permissions" 2>/dev/null || true)" # NOSONAR (shell:S5332) — loopback-only daemon API
  if [[ -n "$PERM_JSON" ]]; then
    PERM_OK="$(jq -r '(.mode == "grant-all") or ([.agents[].permissions[].state] | all(. == "granted"))' <<<"$PERM_JSON" 2>/dev/null || echo false)" # NOSONAR (shell:S5332) — reads the loopback response above
    if [[ "$PERM_OK" != "true" ]]; then
      echo "attach: daemon at $ONBOARD_BIND has unanswered/denied permissions — it would monitor nothing and record an empty fixture" >&2
      echo "        restart it with IRRLICHT_PERMISSION_MODE=grant-all (or grant every permission via the wizard)" >&2
      exit 1
    fi
    # Granted is not applied (#1385, #1449). A grant-all daemon started by hand
    # now REFUSES to install hooks into the user's real config unless the
    # operator took responsibility for those files, and the refusal leaves the
    # permission reading "granted" — so the check above passes while every
    # hook-delivered observation is missing. That records a fixture in which the
    # adapter looks incapable of reporting state, which is worse than not
    # recording at all. unapplied_grants is the daemon's own list of exactly
    # this (an older daemon omits the field → empty → skipped).
    # Scoped to THIS cell's adapter, not the whole list. unapplied_grants is
    # daemon-wide and carries every adapter's #1362 install failures and #1365
    # version-floor refusals — so an unrelated old codex CLI would otherwise
    # block a claude-code recording, with advice (set the override) that cannot
    # fix a version floor. Only a refusal on the adapter this cell records can
    # damage this cell's fixture.
    # $ADAPTER alone, with no partner: a cross-adapter cell never reaches this
    # point — a non-empty partner_adapter exits above, pointing at
    # run-cell-multi.sh, which has no attach path at all.
    # The filter is a variable so the jq call stays one physical line: a
    # NOSONAR annotation only suppresses the line it sits on, and Sonar's taint
    # tracking carries S5332 from the curl above into every reader of $PERM_JSON.
    UNAPPLIED_FILTER='[.unapplied_grants // [] | .[] | select(.agent == $a) | "\(.agent)/\(.key): \(.reason)"] | join("; ")'
    UNAPPLIED="$(jq -r --arg a "$ADAPTER" "$UNAPPLIED_FILTER" <<<"$PERM_JSON" 2>/dev/null || echo "")" # NOSONAR (shell:S5332) — reads the loopback response above
    if [[ -n "$UNAPPLIED" ]]; then
      echo "attach: daemon at $ONBOARD_BIND has grants that were NOT applied — it would record a fixture missing those signals" >&2 # NOSONAR (shell:S5332) — names the loopback daemon above
      echo "        $UNAPPLIED" >&2 # NOSONAR (shell:S5332) — echoes the loopback response above
      echo "        if this is the #1449 shared-config refusal: back up the files that irrlichd --print-managed-files names," >&2
      echo "        then restart the daemon with IRRLICHT_ALLOW_SHARED_CONFIG_WRITES=1 (or record without --attach, which snapshots them for you)" >&2
      exit 1
    fi
  fi
  echo "attach: using running daemon's recordings at $ATTACHED_RECORDINGS_DIR"
else
  # The lib snapshots the shared agent config, spawns the daemon, arms
  # stop_record_daemon as the EXIT trap, and waits for its socket. A non-zero
  # return means the socket never appeared (it has already said so on stderr);
  # the trap still drains the daemon and restores the config on the way out.
  # $ADAPTER narrows grant-all's auto-grant to this run's own adapter, so a
  # cell recording e.g. mistral-vibe no longer auto-grants — and never Applies
  # — every OTHER adapter's hook installer too (claudecode chief among them:
  # #1769).
  spawn_record_daemon "$DAEMON" "$STAGING" "$ONBOARD_BIND" "$ONBOARD_HOME" "$ADAPTER" || exit 1
fi

# The daemon's socket is bound BEFORE its grant-all Apply closures have written
# the adapter's hook config, and every agent CLI here reads that config once, at
# startup. Driving now would record a complete, healthy-looking fixture with no
# hook events in it (#1735). Skipped on the attach path, where the daemon has
# been up since long before this script started and run-cell's own
# unapplied_grants check above already graded its installs.
if [[ "$ATTACH" != "1" ]]; then
  wait_for_hook_install "$ADAPTER" "$STAGING" "$ONBOARD_BIND" || exit 1
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
  echo "attached" > "$STAGING/daemon.shutdown"
  echo "attach: waiting 6s for recorder flush..."
  sleep 6
else
  stop_record_daemon
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
  # `|| true`, never `|| echo 0`: grep -c ALREADY prints 0 when it matches
  # nothing, and exits 1 for the same reason — so the fallback appended a
  # SECOND zero and the variable became the two-line string "0\n0", which
  # `[[ … -gt 1 ]]` rejects with a syntax error. It fires only when
  # session.uuids exists but is empty, i.e. when the driver resolved no
  # session at all — so the bug corrupted the diagnostics of exactly the
  # runs that had already gone wrong (#1388).
  uuid_count=$(grep -c . "$STAGING/session.uuids" || true)
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
    #
    # Assigned then exported, not `export X="$(cat …)"` (#1684): `export`
    # always succeeds, so the combined form discards `cat`'s status and an
    # unreadable paths file would export an empty list rather than fail here.
    IRRLICHT_EXTRA_TRANSCRIPTS="$(cat "$STAGING/transcript.paths")"
    export IRRLICHT_EXTRA_TRANSCRIPTS
    echo "multi-session: primary=$ACTUAL_UUID, extras=$EXTRA_IDS"
  fi
fi

# --- Locate the recording file ------------------------------------------
# Isolated mode: one .jsonl in $STAGING/recordings/.
# Attached mode: the file the daemon was writing to during THIS run, identified
# by mtime against the marker dropped before the driver ran. There is
# deliberately no stale fallback — see lib/pick-recording.sh (#1333 / B6).
if [[ "$ATTACH" == "1" ]]; then
  RECORDING="$(pick_attached_recording "$ATTACHED_RECORDINGS_DIR" "$JSONL_GLOB" "$ATTACH_MARKER")" || exit 1
else
  RECORDING="$(pick_isolated_recording "$STAGING/recordings" "$JSONL_GLOB")" || true
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
        find "$SUBAGENT_DIR" -maxdepth 1 -name "$JSONL_GLOB" -type f 2>/dev/null | wc -l | tr -d ' '
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

GIT_HEAD_END="$(git -C "$REPO_ROOT" rev-parse --short HEAD 2>/dev/null || echo unknown)"
if [[ "$GIT_HEAD_START" != "$GIT_HEAD_END" ]]; then
  echo "WARNING: HEAD moved during this run ($GIT_HEAD_START -> $GIT_HEAD_END) —" >&2
  echo "         another session committed in this worktree while it recorded." >&2
fi

# --- Completeness (#1333 / A3) ------------------------------------------
# Runs on EVERY outcome, including `ok`. driver.exit-reason is the driver's
# claim about itself; this is the daemon's account of whether the run actually
# finished. Two of three broken copilot runs reported `ok` with a silently
# truncated recording, and `ok` is what invites promotion. Advisory by design —
# see the header of completeness-check.sh for why it is not a hard gate.
COMPLETENESS="$(completeness_json "$STAGING" --scenario "$FOLDER")"
report_completeness "$COMPLETENESS"

# --- Manifest -----------------------------------------------------------
jq -n \
  --arg adapter "$ADAPTER" \
  --arg scenario "$FOLDER" \
  --arg session_uuid "$ACTUAL_UUID" \
  --argjson completeness "$COMPLETENESS" \
  --arg git_head_start "$GIT_HEAD_START" \
  --arg git_head_end "$GIT_HEAD_END" \
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
    completeness: $completeness,
    git_head_start: $git_head_start,
    git_head_end: $git_head_end,
    daemon_shutdown: $daemon_shutdown,
    timeout_seconds: $timeout_seconds}' \
  > "$MANIFEST"

echo "staged: $STAGED_TRANSCRIPT"
echo "manifest: $MANIFEST"
