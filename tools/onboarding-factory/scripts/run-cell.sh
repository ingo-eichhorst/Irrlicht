#!/usr/bin/env bash
# run-cell.sh — execute one (adapter, scenario) cell end-to-end.
#
# Pipeline:
#   recipe-lint  →  refuse a step type the driver lacks (#476, exit 3)
#   recipe-runtime → refuse a bare_mode/env/mock block the driver cannot honor
#                    (#1803, exit 5) — recording without it drives the REAL provider
#   precheck.sh  →  spawn isolated irrlichd --record
#                →  build + launch the cell's `mock` server, wait for it to LISTEN
#                →  drive-<adapter>.sh (runs the agent under timeout)
#                →  stop the mock, then assert the driver's env receipt
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
# Profile-aware committed recording selection. This CLI runner produces and
# compares cli-local evidence.
# shellcheck source=lib/recording-profile.sh
source "$SCRIPT_DIR/lib/recording-profile.sh"
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
# unapplied_grants — the daemon's own account of an install that was granted
# but never applied (#1362). Shared between the attach and spawn paths
# (#1754); see the lib header for why the spawn path never checked this
# before.
# shellcheck source=lib/unapplied-grants-check.sh
source "$SCRIPT_DIR/lib/unapplied-grants-check.sh"
# bare_mode / env / mock — the per-cell recipe's runtime block (#1803). Its
# header says what each field is for and why every one of its checks is a hard
# refusal rather than a warning.
# shellcheck source=lib/recipe-runtime.sh
source "$SCRIPT_DIR/lib/recipe-runtime.sh"

# "Did the driver's tmux sessions actually go away?" — the post-run counterpart
# to the completeness check, for the same reason (#1825): driver.exit-reason is
# the driver's claim about ITSELF, and every exit_clean recording for months
# left a live agent + tmux session behind while reporting `ok`.
# shellcheck source=lib/tmux-teardown-check.sh
source "$SCRIPT_DIR/lib/tmux-teardown-check.sh"

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

# --- Recipe runtime block (#1803) ---------------------------------------
# `bare_mode`, `env` and `mock` decide WHERE the agent's provider calls go. A
# recipe that asks for a mock and silently doesn't get one records the agent
# talking to the REAL provider — real credentials, real tokens, and a green
# fixture that proves nothing about the error path it was written for. So this
# is validated statically, BEFORE a daemon or a CLI exists, alongside the other
# pre-record refusals. Exit 5 = runtime_gap, distinct from 3 (driver_gap) and
# 4 (semantic_gap) so a caller routes it to the right fix.
#
# Resolved here rather than at the drive block below because the refusal is
# about the driver's declared capabilities, and both branches need the path.
if [[ -n "$SCRIPT_JSON" ]]; then
  DRIVER="$REPO_ROOT/replaydata/agents/$ADAPTER/driver-interactive.sh"
else
  DRIVER="$REPO_ROOT/replaydata/agents/$ADAPTER/driver.sh"
fi
if ! recipe_runtime_mock_check "$CELL_JSON"; then
  echo "runtime_gap: $ADAPTER/$SCENARIO declares a malformed .mock block — not recording." >&2
  exit 5
fi
MOCK_ADDR="$(recipe_runtime_mock_addr "$CELL_JSON")"
if ! DRIVER_ENV_LINES="$(recipe_runtime_env_lines "$CELL_JSON" "$MOCK_ADDR")"; then
  echo "runtime_gap: $ADAPTER/$SCENARIO declares an .env block that cannot be rendered — not recording." >&2
  exit 5
fi
RUNTIME_GAPS="$(recipe_runtime_driver_gaps "$CELL_JSON" "$DRIVER")"
if [[ -n "$RUNTIME_GAPS" ]]; then
  echo "runtime_gap: $ADAPTER/$SCENARIO needs runtime support its driver does not declare (DRIVE_SUPPORTS):" >&2
  while IFS= read -r g; do [[ -n "$g" ]] && printf '  - %s\n' "$g" >&2; done <<<"$RUNTIME_GAPS"
  echo "Teach $(basename "$DRIVER") to honor it and add it to DRIVE_SUPPORTS — recording without it would drive the REAL provider." >&2
  exit 5
fi

MOCK_PID=""

# start_recipe_mock — build and launch the cell's mock, wait for it to LISTEN,
# and compose its teardown into the EXIT trap. No-op when the cell declares no
# mock.
#
# `go build` into $STAGING rather than `go run`: `go run` puts a toolchain
# process between this script and the server, so $! is the wrong pid to kill
# and the server outlives the run. Both bespoke recorders build for the same
# reason.
start_recipe_mock() {
  [[ -n "$MOCK_ADDR" ]] || return 0
  local pkg bin port args_json
  pkg="$(jq -r '.mock.package' <<<"$CELL_JSON")"
  port="${MOCK_ADDR##*:}"
  bin="$STAGING/mock-server"

  # Refuse a port already in use rather than racing a stale mock from an
  # earlier run: a leftover server on the same port answers the agent, and the
  # recording would be of the WRONG mock's script.
  if nc -z 127.0.0.1 "$port" 2>/dev/null; then
    echo "runtime_gap: port $port is already in use — kill the stale mock before recording $ADAPTER/$SCENARIO" >&2
    exit 5
  fi

  echo "mock: building $pkg"
  ( cd "$REPO_ROOT" && go build -o "$bin" "$pkg" ) || {
    echo "runtime_gap: could not build the cell's mock package $pkg" >&2
    exit 5
  }

  local -a mock_args=(--addr "$MOCK_ADDR")
  args_json="$(jq -r '(.mock.args // [])[]' <<<"$CELL_JSON")"
  while IFS= read -r a; do [[ -n "$a" ]] && mock_args+=("$a"); done <<<"$args_json"

  "$bin" "${mock_args[@]}" > "$STAGING/mock.log" 2>&1 &
  MOCK_PID=$!
  echo "mock: pid $MOCK_PID on $MOCK_ADDR (${mock_args[*]})"

  # Compose, don't replace. lib/spawn-record-daemon.sh owns the EXIT trap and
  # its header says callers never write `trap` themselves — but the mock needs
  # a teardown too, and on the --attach path nothing arms a trap at all. So
  # read back what IS armed and assert it is the one thing this composition
  # knows how to preserve; a different value means the lib changed and this
  # block is stale, which is a stop, not something to guess through.
  local armed
  armed="$(trap -p EXIT)"
  case "$armed" in
    "") trap stop_recipe_mock EXIT ;;
    "trap -- 'stop_record_daemon' EXIT")
      trap 'stop_recipe_mock; stop_record_daemon' EXIT ;;
    *)
      echo "runtime_gap: unexpected EXIT trap [$armed] — run-cell.sh's mock teardown does not know how to compose with it" >&2
      kill "$MOCK_PID" 2>/dev/null || true
      exit 5
      ;;
  esac

  recipe_runtime_wait_listening 127.0.0.1 "$port" 15 || {
    echo "runtime_gap: the cell's mock never came up; its log follows" >&2
    cat "$STAGING/mock.log" >&2
    exit 5
  }
  # Listening is not the same as alive: a server that binds and then dies on
  # its first request would pass the probe above.
  kill -0 "$MOCK_PID" 2>/dev/null || {
    echo "runtime_gap: the cell's mock exited right after binding; its log follows" >&2
    cat "$STAGING/mock.log" >&2
    exit 5
  }
}

# stop_recipe_mock — idempotent; safe to call from the trap and explicitly.
stop_recipe_mock() {
  [[ -n "${MOCK_PID:-}" ]] || return 0
  kill "$MOCK_PID" 2>/dev/null || true
  wait "$MOCK_PID" 2>/dev/null || true
  MOCK_PID=""
  return 0
}

# write_driver_env — hand the rendered runtime block to the driver as FILES
# under $STAGING, the same path-not-blob convention settings.json uses (a
# KEY=VALUE blob on argv would be back to shell-quoting fragility, and an
# exported variable would not reach a tmux pane at all — the tmux server's
# environment is not the calling shell's).
write_driver_env() {
  rm -f "$STAGING/driver-env" "$STAGING/driver-bare" "$STAGING/driver-env.applied"
  [[ -n "$DRIVER_ENV_LINES" ]] && printf '%s\n' "$DRIVER_ENV_LINES" > "$STAGING/driver-env"
  [[ "$(recipe_runtime_bare "$CELL_JSON")" == "true" ]] && echo 1 > "$STAGING/driver-bare"
  return 0
}

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
    # Scoped to THIS cell's adapter, not the whole list, by check_unapplied_grants
    # (lib/unapplied-grants-check.sh, shared with the spawn path below, #1754):
    # unapplied_grants is daemon-wide and carries every adapter's #1362 install
    # failures and #1365 version-floor refusals — so an unrelated old codex CLI
    # would otherwise block a claude-code recording, with advice (set the
    # override) that cannot fix a version floor. Only a refusal on the adapter
    # this cell records can damage this cell's fixture.
    # $ADAPTER alone, with no partner: a cross-adapter cell never reaches this
    # point — a non-empty partner_adapter exits above, pointing at
    # run-cell-multi.sh, which has no attach path at all.
    check_unapplied_grants "$ONBOARD_BIND" "$ADAPTER" "$PERM_JSON" || exit 1 # NOSONAR (shell:S5332) — reads the loopback response fetched above
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
  # Same surface the attach path checks above, moved out where it can be
  # shared (#1754): an install that failed silently ("granted but NOT
  # applied", #1362's shape) reads "granted" in .permissions on the spawn
  # path too, and unapplied_grants is the only place that names it. Polled,
  # not sampled once: the socket comes up BEFORE the daemon's grant-all Apply
  # pass runs (see unapplied-grants-check.sh's header for the trace), so a
  # single immediate check can read "clean" only because it hasn't been
  # evaluated yet. wait_for_unapplied_grants_clear trusts a refusal instantly
  # but polls a clean reading out to a deadline before believing it.
  wait_for_unapplied_grants_clear "$ONBOARD_BIND" "$ADAPTER" || exit 1
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
  DRIVER_INPUT="$SCRIPT_JSON"
else
  DRIVER_INPUT="$PROMPT"
fi
[[ -x "$DRIVER" ]] || { echo "driver missing: $DRIVER" >&2; exit 1; }

# --- Recipe runtime: mock server + driver env (#1803) -------------------
# Launched as late as possible — after the daemon is up and its hook installs
# are graded — so the mock's lifetime is bounded by the drive itself. The
# driver reads both files from $STAGING, the same path-not-blob convention
# settings.json uses.
start_recipe_mock
write_driver_env

# The driver's pid is this run's IDENTITY for the tmux-teardown gate below
# (#1825 / AC4): every interactive driver embeds its own `$$` as a field of
# every tmux session name it creates, so the pid is what separates this run's
# leftovers from a concurrent cell's or the operator's own tmux. A foreground
# call never sets `$!`, so the pid is taken from the driver ITSELF: `bash -c`
# writes the pid it is running under, then `exec`s the driver into that same
# process — so driver.pid holds exactly the `$$` the driver will go on to see.
#
# Deliberately NOT `"$DRIVER" … & DRIVER_PID=$!; wait` (which is what
# run-cell-multi.sh:297-312 does, because it genuinely needs the concurrency).
# Measured on this bash: an async child of a NON-interactive shell gets stdin
# redirected from /dev/null and SIGINT/SIGQUIT set to SIG_IGN — so backgrounding
# would quietly change what a driver reads from stdin and stop it answering a
# Ctrl-C on the recording operator's terminal, to learn a pid the exec wrapper
# hands over for free. The wrapper keeps the call in the foreground, in the same
# process group, with the same stdin, argv and exit status as before.
# BEGIN driver_pid_capture
DRIVER_PID_FILE="$STAGING/driver.pid"
set +e
bash -c 'printf "%s\n" "$$" > "$1" || exit 1; shift; exec "$@"' \
  bash "$DRIVER_PID_FILE" \
  "$DRIVER" "$STAGING" "$UUID" "$TIMEOUT_S" "$STAGING/settings.json" "$DRIVER_INPUT"
set -e
DRIVER_REASON="$(cat "$STAGING/driver.exit-reason" 2>/dev/null || echo "unknown")"
DRIVER_PID="$(tr -d '[:space:]' < "$DRIVER_PID_FILE" 2>/dev/null || true)"
# END driver_pid_capture

# BEGIN driver_teardown_gate
# --- Did the driver's tmux sessions actually die? MEASURE (#1825 / AC4) --
# Measured HERE, one line after the driver returns and before anything else
# gets a chance to take time — the daemon flush below alone spends 6s on the
# attach path, and a look taken after it is a strictly more lenient look. The
# VERDICT is acted on IMMEDIATELY below — the only thing between them is the
# manifest plumbing the verdict needs, which runs no command that can fail. It
# used to sit ~64 lines further down, behind `stop_recipe_mock` and two
# `recipe_runtime_assert_* || exit 5` gates, so a driver that both leaked a
# session and skipped its env receipt (one abort causes both) exited 5 with no
# manifest at all: classify-failure.sh had nothing to read, graded the run
# `unknown`, and the "never retry driver_session_leaked blind" rule never
# fired — a retry then started a second live agent beside the leaked one.
#
# Scoped to interactive cells by the same $SCRIPT_JSON discriminator that chose
# the driver above, so both halves stay honest: an interactive driver cannot
# have recorded anything without tmux (so "no tmux" there is a broken lookup and
# fails loudly), while a headless `prompt` cell legitimately never starts one —
# verified: `grep -c tmux replaydata/agents/*/driver.sh` is 0 for all four.
TMUX_GATE_STATUS="skipped"
TMUX_GATE_DETAIL="headless cell — no interactive driver ran, and no driver.sh uses tmux"
if [[ -n "$SCRIPT_JSON" ]]; then
  # Before tmux is asked anything: is there a run identity to ask ABOUT? The pid
  # comes from a file the driver's own process wrote (the driver_pid_capture
  # block above), and the two ways it can be unusable have different first moves
  # for the operator, so they
  # get different sentences:
  #
  #   no file at all      → the `bash -c` wrapper exited 1 on its own `printf >`
  #                         before it could `exec` the driver — that write is
  #                         the only thing between it and the exec, and an
  #                         unwritable $STAGING is what makes it fail. So the
  #                         driver almost certainly never ran, and there is
  #                         nothing of this run's for tmux to be holding.
  #   a file with garbage → the wrapper DID run, so the driver started, and its
  #                         sessions may well be out there under a name no pid
  #                         of ours can match.
  #
  # Both are `unreadable` — "could not look" is never "it is gone" — but they
  # are not the same problem. check_tmux_teardown's own refusal names only the
  # value ("the driver pid must be a whole number, got ''"), and an operator who
  # reads that under a heading with the word tmux in it goes and looks at tmux.
  DRIVER_PID_PROBLEM=""
  if [[ ! -f "$DRIVER_PID_FILE" ]]; then
    DRIVER_PID_PROBLEM="driver.pid was never written at $DRIVER_PID_FILE — the pid wrapper exited before it could exec the driver, so the driver almost certainly never ran. This is a staging-writability problem, NOT a tmux one: tmux was not asked anything"
  elif [[ -z "$DRIVER_PID" ]]; then
    DRIVER_PID_PROBLEM="driver.pid at $DRIVER_PID_FILE is empty or unreadable — the wrapper ran, so the driver DID start and may have left sessions behind under a name this run can no longer match. Not a tmux problem: tmux was not asked anything"
  elif [[ -n "${DRIVER_PID//[0-9]/}" ]]; then
    DRIVER_PID_PROBLEM="driver.pid at $DRIVER_PID_FILE holds '$DRIVER_PID', which is not a whole number — the wrapper ran, so the driver DID start and may have left sessions behind under a name this run can no longer match. Not a tmux problem: tmux was not asked anything"
  fi

  if [[ -n "$DRIVER_PID_PROBLEM" ]]; then
    TMUX_GATE_STATUS="unreadable"
    TMUX_GATE_DETAIL="$DRIVER_PID_PROBLEM"
  else
    # The pair await_gone_bound checks (see the lib header's rule 3). The
    # lifetime is the cell's own driver timeout — the upper bound on how long the
    # session could have lived; the grace is a tenth of it, capped at 5s so a
    # 900s cell does not buy a 90s wait for a session that should already be gone,
    # and floored at 1s so the arithmetic can never produce the "look exactly
    # once" deadline await_gone_bound refuses. A cell whose timeout is under 10s
    # therefore fails the bound and is reported LOUDLY as unreadable, which is the
    # honest answer: at that ratio the check would assert nothing. (Every
    # applicable cell today declares 60s or more.)
    TEARDOWN_LIFETIME_S="$TIMEOUT_S"
    TEARDOWN_DEADLINE_S=$(( TEARDOWN_LIFETIME_S / 10 ))
    if [[ "$TEARDOWN_DEADLINE_S" -gt 5 ]]; then TEARDOWN_DEADLINE_S=5; fi
    if [[ "$TEARDOWN_DEADLINE_S" -lt 1 ]]; then TEARDOWN_DEADLINE_S=1; fi

    TMUX_GATE_RC=0
    check_tmux_teardown "$DRIVER_PID" "$TEARDOWN_DEADLINE_S" "$TEARDOWN_LIFETIME_S" \
      "the cell's own driver timeout" || TMUX_GATE_RC=$?
    case "$TMUX_GATE_RC" in
      0) TMUX_GATE_STATUS="clean"
         TMUX_GATE_DETAIL="no tmux session carries driver pid ${DRIVER_PID:-<unrecorded>} (settled after ${TMUX_TEARDOWN_ELAPSED}s)" ;;
      1) TMUX_GATE_STATUS="leaked"
         TMUX_GATE_DETAIL="$TMUX_TEARDOWN_SURVIVORS" ;;
      *) TMUX_GATE_STATUS="unreadable"
         TMUX_GATE_DETAIL="$TMUX_TEARDOWN_REASON" ;;
    esac
  fi
  echo "tmux teardown: $TMUX_GATE_STATUS — $TMUX_GATE_DETAIL"
fi

# --- Driver outputs + the ERROR-manifest writer -------------------------
# Everything the tmux VERDICT below needs, and nothing else. Placed between the
# measurement and its verdict deliberately: not one line here can exit. Both
# reads fall back rather than failing, the assignment is a string, and the two
# definitions run no command at all — so the verdict below really is the first
# thing that ACTS after the driver returns, and no gate can be inserted above it
# by accident.
#
# transcript.path and session.uuid are DRIVER outputs, written before it
# returned. They used to be read after the daemon flush, which was simply later
# than they were available.
TRANSCRIPT="$(cat "$STAGING/transcript.path" 2>/dev/null || true)"
ACTUAL_UUID="$(cat "$STAGING/session.uuid" 2>/dev/null || true)"

MANIFEST="$STAGING/run-manifest.json"

# daemon_shutdown_state — how the recorder was stopped, read at CALL time from
# the file the flush writes. Deliberately NOT snapshotted into a variable here:
# write_error_manifest can now fire BEFORE the flush (the verdict immediately
# below), and a value captured before that file exists would be baked into every
# later manifest too. Read late, it is "unknown" for the pre-flush callers —
# which is the truth, the daemon is still up — and exact for everyone after.
daemon_shutdown_state() {
  cat "$STAGING/daemon.shutdown" 2>/dev/null || echo "unknown"
}

# Write an ERROR-verdict run-manifest with the standard envelope plus
# error-specific fields supplied as a JSON object (pass '{}' for none).
#
# Defined HERE — the first line where all of its inputs exist (ACTUAL_UUID and
# DRIVER_REASON from the driver just above, the daemon's shutdown reason read
# lazily) — rather than further down next to a caller, so that EVERY hard gate
# after the driver returns can use it. It has been moved up twice for that
# reason: it sat below the recording picker, whose own `|| exit 1` then left no
# manifest at all, and it sat below `recipe_runtime_assert_* || exit 5`, which
# left the tmux gate below unreachable on exactly the runs that had leaked
# (#1825).
write_error_manifest() {
  local error_code="$1"
  local extras_json="$2"
  jq -n \
    --arg adapter "$ADAPTER" \
    --arg scenario "$FOLDER" \
    --arg session_uuid "$ACTUAL_UUID" \
    --arg error "$error_code" \
    --arg driver_exit_reason "$DRIVER_REASON" \
    --arg daemon_shutdown "$(daemon_shutdown_state)" \
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

# BEGIN tmux_teardown_verdict
# --- Did the driver's tmux sessions actually die? VERDICT (#1825 / AC4) --
# The measurement was taken the instant the driver returned; this is where it is
# acted on, and it is the FIRST thing after the driver that can exit. A hard
# gate, not advisory: unlike the completeness check (whose header documents a
# real ~7% legitimately-incomplete population), a tmux session still carrying
# THIS run's driver pid has no legitimate population — the driver said it was
# gone, and it is not. `unreadable` fails just as hard and under its own error
# code, because #1825 is precisely the bug of an unasked question and an
# answered one printing the same thing.
#
# Nothing that can `exit` may be placed between the measurement above and this
# block. A bare exit past it produces no manifest, classify-failure.sh grades
# the run `unknown`, and the record skill's "never retry driver_session_leaked
# blind" rule cannot fire — so the retry starts a second live agent beside the
# one still running. lib/tmux-teardown-check_test.sh pins the order by
# extracting this marker block and re-composing it the old way.
case "$TMUX_GATE_STATUS" in
  clean | skipped) ;;
  *)
    if [[ "$TMUX_GATE_STATUS" == "leaked" ]]; then
      TMUX_GATE_ERROR="driver_tmux_session_survived"
    else
      TMUX_GATE_ERROR="driver_tmux_teardown_unreadable"
    fi
    write_error_manifest "$TMUX_GATE_ERROR" \
      "$(jq -nc \
          --arg tmux_teardown "$TMUX_GATE_STATUS" \
          --arg tmux_teardown_detail "$TMUX_GATE_DETAIL" \
          --arg driver_pid "$DRIVER_PID" \
          '{tmux_teardown: $tmux_teardown, tmux_teardown_detail: $tmux_teardown_detail, driver_pid: $driver_pid}')"
    echo "ERROR: driver tmux teardown $TMUX_GATE_STATUS (driver pid ${DRIVER_PID:-<unrecorded>}): $TMUX_GATE_DETAIL" >&2
    if [[ "$TMUX_GATE_STATUS" == "leaked" ]]; then
      echo "  kill the survivor(s) with: tmux kill-session -t <name>" >&2
    fi
    exit 1
    ;;
esac
# END tmux_teardown_verdict

# The mock has nothing left to serve; stop it before the daemon so its log is
# complete in staging when the recorder flushes.
stop_recipe_mock
recipe_runtime_assert_env_receipt "$STAGING" "$(basename "$DRIVER")" || exit 5
recipe_runtime_assert_mock_used "$STAGING" "$CELL_JSON" "$MOCK_ADDR" || exit 5
# END driver_teardown_gate

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

# Compare the staged CLI transcript against the newest committed CLI recording.
COMMITTED_CELL="$REPO_ROOT/replaydata/agents/$ADAPTER/scenarios/$FOLDER"
# A never-recorded cell has no recordings/ dir, so the glob matches nothing
# and `ls` exits non-zero — under `set -euo pipefail` that would abort the
# whole run right after a successful capture. Tolerate the empty match; the
# `[[ -n "$NEWEST_REC" ... ]]` guard below already handles "no committed
# recording yet" (COMMITTED_PRESENT=false).
NEWEST_REC="$(newest_recording_for_profile "$COMMITTED_CELL" cli-local)"
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
  --arg daemon_shutdown "$(daemon_shutdown_state)" \
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
