#!/usr/bin/env bash
# run-cell-multi.sh — record a CROSS-ADAPTER cell: two (or more) different
# agents live in the SAME cwd at once, observed by ONE --record daemon.
# This is the recording rig for scenarios whose by_adapter cells declare a
# `partner_adapter` (e.g. multiple-agents-same-workspace), which the
# single-adapter run-cell.sh refuses.
#
# Pipeline:
#   precheck.sh (per adapter, coexist+multi)
#     → spawn ONE isolated `irrlichd --record` (own IRRLICHT_HOME + bind
#       port, so it COEXISTS with production — multi mode never stops it)
#     → launch each adapter's interactive driver CONCURRENTLY, all pointed
#       at one shared cwd via IRRLICHT_ONBOARD_CWD
#     → SIGINT → grace → drain the daemon
#     → for EACH adapter: curate a per-adapter fixture — that adapter's OWN
#       transcript, but an events.jsonl spanning the WHOLE workspace (the
#       other adapters' sessions unioned in via IRRLICHT_EXTRA_SESSION_IDS,
#       NOT their transcripts) so each fixture proves "two agents, one
#       workspace, labeled independently"
#     → replay each staged transcript
#     → write run-manifest.json
#
# claudecode observation: the isolated daemon is NOT on :7837, but since
# #1178 the hook endpoint follows IRRLICHT_BIND_ADDR, so claudecode's hooks
# reach us rather than production. The transcript fswatcher (~/.claude/projects,
# keyed off the real $HOME) covers this scenario's working->ready arcs either
# way. This script snapshots and restores the shared agent config the daemon
# rewrites (lib/managed-file-snapshot.sh), so production's hooks are put back
# when the recording ends.
#
# Coexist is MANDATORY here: IRRLICHT_ONBOARD_HOME must be set (defaulting
# the bind port to 7838) so we never touch the running production daemon.
#
# Usage:
#   IRRLICHT_ONBOARD_HOME=/tmp/irrlicht-onboard-dev \
#     run-cell-multi.sh [--dry-run] <scenario-name> <primary-adapter>
#
# The adapter PAIR is the primary plus the `partner_adapter` its cell's recipe
# names — e.g. `run-cell-multi.sh multiple-agents-same-workspace kiro-cli` runs
# kiro-cli + claudecode. --dry-run resolves + prints the plan, then exits 0.
#
# Outputs under ./.build/refresh/_multi/<scenario>-<UTC-ts>/:
#   recordings/                     — the single isolated daemon recording
#   cwd/                            — the shared workspace both agents run in
#   <adapter>/                      — per-adapter driver staging
#   replaydata/agents/<adapter>/scenarios/<folder>/{transcript,events}.jsonl
#   reports/<adapter>.staged.json
#   daemon.log, run-manifest.json

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(git rev-parse --show-toplevel 2>/dev/null || true)"
[[ -n "$REPO_ROOT" ]] || { echo "not in a git repo" >&2; exit 1; }
# shellcheck source=lib/shard-lib.sh
source "$SCRIPT_DIR/lib/shard-lib.sh"   # per-scenario shard reader (#511)
# Recording-daemon lifecycle — env, spawn, socket wait, shutdown ladder, and the
# shared agent config snapshot/restore (#1178) — shared with run-cell.sh, which
# this script does NOT delegate to otherwise, so a recorder fix reaches both
# recording paths rather than whichever one the author was editing (#1214).
# shellcheck source=lib/spawn-record-daemon.sh
source "$SCRIPT_DIR/lib/spawn-record-daemon.sh"

# Session-id reconciliation helpers (daemon_sid_for_transcript,
# sid_in_recording, reconcile_slot_csv) — shared + unit-tested in lib/.
# shellcheck source=lib/reconcile.sh
source "$SCRIPT_DIR/lib/reconcile.sh"
# Recipe ↔ driver lint (#476) — same backstop run-cell.sh applies, so the
# cross-adapter path also refuses a missing driver step BEFORE recording.
# shellcheck source=lib/recipe-lint.sh
source "$SCRIPT_DIR/lib/recipe-lint.sh"
# bare_mode / env / mock — this rig does not implement them; it refuses a cell
# that declares one (#1803). See recipe_runtime_unsupported.
# shellcheck source=lib/recipe-runtime.sh
source "$SCRIPT_DIR/lib/recipe-runtime.sh"
# Recording-file selection + "did this run finish?", both shared with
# run-cell.sh so a fix can't reach only one rig (#1214).
# shellcheck source=lib/pick-recording.sh
source "$SCRIPT_DIR/lib/pick-recording.sh"
# shellcheck source=lib/completeness-check.sh
source "$SCRIPT_DIR/lib/completeness-check.sh"
# Per-adapter agent-home isolation, shared with run-cell.sh — before this file
# existed only COPILOT_HOME was handled here while run-cell.sh also handled
# CODEX_HOME, i.e. exactly the one-rig-only drift #1214 is about.
# shellcheck source=lib/agent-home.sh
source "$SCRIPT_DIR/lib/agent-home.sh"
# "Did the driver's tmux sessions actually die?" — the same library run-cell.sh
# sources, wired here for the same reason #1214 made the daemon lifecycle
# shared: a gate that lands in one rig and not the other is the DEFAULT outcome,
# not the unlucky one. This rig needs it more, not less: it is coexist-only by
# construction, so a leaked agent keeps running next to the operator's
# production daemon, in a shared workspace two adapters were both writing to.
# shellcheck source=lib/tmux-teardown-check.sh
source "$SCRIPT_DIR/lib/tmux-teardown-check.sh"

DRY_RUN=0
positional=()
while [[ $# -gt 0 ]]; do
  case "$1" in
    --dry-run) DRY_RUN=1; shift ;;
    -h|--help) echo "usage: run-cell-multi.sh [--dry-run] <scenario-name> <primary-adapter>" >&2; exit 0 ;;
    -*) echo "unknown flag: $1" >&2; exit 2 ;;
    *)  positional+=("$1"); shift ;;
  esac
done
[[ "${#positional[@]}" -eq 2 ]] || { echo "usage: run-cell-multi.sh [--dry-run] <scenario-name> <primary-adapter>" >&2; exit 2; }
SCENARIO="${positional[0]}"
PRIMARY="${positional[1]}"

# --- Coexist isolation is mandatory -------------------------------------
ONBOARD_HOME="${IRRLICHT_ONBOARD_HOME:-}"
if [[ -z "$ONBOARD_HOME" ]]; then
  echo "run-cell-multi requires IRRLICHT_ONBOARD_HOME (isolated daemon home) so it never disturbs production" >&2
  exit 2
fi
ONBOARD_BIND="${IRRLICHT_ONBOARD_BIND_ADDR:-127.0.0.1:7838}"
export IRRLICHT_ONBOARD_HOME="$ONBOARD_HOME"
export IRRLICHT_ONBOARD_BIND_ADDR="$ONBOARD_BIND"
# Where a beacon-delivered hook must POST. Exported for the same reason and with
# the same limits as run-cell.sh's copy — see the long comment there. This rig is
# coexist-ONLY, so a beacon adapter recorded here without it would post every
# hook to the production daemon 100% of the time rather than only in coexist
# mode. #1214's lesson is exactly that a recorder-lifecycle fix landing in one of
# these two scripts and not the other is the default outcome, not the unlucky
# one.
export IRRLICHT_BIND_ADDR="$ONBOARD_BIND"

# --- Resolve the cross-adapter cell -------------------------------------
# Post-consolidation (#511): there is no `cross_adapter[]` list. The cell lives
# at the PRIMARY agent's metadata.json (.details.recipe), whose `partner_adapter`
# names the second agent. SCENARIO may be the coverage_id or a variant
# recording-folder name; both resolve to the same coverage_id (the recipe key)
# via shard_coverage_for_dir. The adapter pair is (primary, partner), read
# through shard-lib so the recipe-hash form has one owner.
COVERAGE_ID="$(shard_coverage_for_dir "$SCENARIO" "$PRIMARY")"
PRIMARY_RECIPE="$(shard_recipe "$COVERAGE_ID" "$PRIMARY")"
[[ -n "$PRIMARY_RECIPE" ]] \
  || { echo "scenario not found: no $PRIMARY cell for $SCENARIO in the catalog" >&2; exit 1; }
PARTNER="$(jq -r '.partner_adapter // empty' <<<"$PRIMARY_RECIPE")"
[[ -n "$PARTNER" ]] \
  || { echo "$PRIMARY/$COVERAGE_ID has no partner_adapter — not a cross-adapter cell" >&2; exit 1; }
ADAPTERS=("$PRIMARY" "$PARTNER")
echo "cross-adapter cell: $COVERAGE_ID  adapters=[${ADAPTERS[*]}]"

# Each adapter must be applicable + carry a script (recipe from its metadata.json,
# read under COVERAGE_ID — the recipe key, not the on-disk folder name).
for a in "${ADAPTERS[@]}"; do
  recipe="$(shard_recipe "$COVERAGE_ID" "$a")"
  [[ -n "$recipe" ]] || { echo "adapter $a has no recipe for $SCENARIO" >&2; exit 1; }
  applic="$(jq -r 'if .applicable==true then "true" else "false" end' <<<"$recipe")"
  [[ "$applic" == "true" ]] || { echo "adapter $a is not applicable:true for $SCENARIO" >&2; exit 1; }
  has_script="$(jq -r '.script | if (.|type)=="array" then "yes" else "no" end' <<<"$recipe")"
  [[ "$has_script" == "yes" ]] || { echo "adapter $a has no script for $SCENARIO" >&2; exit 1; }
  # Driver-gap backstop (#476): refuse a step type this adapter's driver lacks
  # before launching any daemon/CLI, mirroring run-cell.sh's exit 3.
  if gaps="$(recipe_lint_gaps "$REPO_ROOT/replaydata/agents/$a/driver-interactive.sh" "$COVERAGE_ID" "$a")"; then :; else
    echo "driver_gap: $a/$SCENARIO needs step type(s) driver-interactive.sh doesn't implement:" >&2
    printf '  - gap:%s\n' $gaps >&2
    exit 3
  fi
  # Semantic backstop (#496 RC3): mirror run-cell.sh — a step the driver accepts
  # but doesn't elicit (or a slash command in send-text on a slash-requires
  # adapter) would record a no-op on the cross-adapter path too.
  if sem="$(recipe_semantic_gaps "$REPO_ROOT/replaydata/agents/$a/driver-interactive.sh" "$COVERAGE_ID" "$a")"; then :; else
    echo "semantic_gap: $a/$SCENARIO uses step(s) the driver accepts but doesn't elicit (per its DRIVE_ELICITS):" >&2
    while IFS= read -r p; do [[ -n "$p" ]] && printf '  - %s\n' "$p" >&2; done <<< "$sem"
    exit 4
  fi
  # Runtime-block backstop (#1803): mirror run-cell.sh's exit 5. THIS RIG DOES
  # NOT IMPLEMENT the runtime block — no mock is launched, no --bare, no env
  # reaches the pane — so a cell carrying one would be driven against the REAL
  # provider and record a healthy-looking fixture, which is precisely what the
  # block exists to prevent. Refusing is the whole point; implementing it here
  # is not, because no cross-adapter cell needs a mock today (every mock in
  # this tree overrides ONE provider's base URL, and a cross-adapter cell has
  # two adapters with two providers).
  #
  # A refusal rather than a silent skip, and stated here rather than assumed
  # unreachable: run-cell.sh and this script have diverged before — the #1178
  # config snapshot reached one and not the other (#1214).
  if runtime_gaps="$(recipe_runtime_unsupported "$COVERAGE_ID" "$a")" && [[ -n "$runtime_gaps" ]]; then
    echo "runtime_gap: $a/$SCENARIO declares a recipe runtime block that the CROSS-ADAPTER rig does not implement:" >&2
    while IFS= read -r p; do [[ -n "$p" ]] && printf '  - %s\n' "$p" >&2; done <<< "$runtime_gaps"
    echo "run-cell-multi.sh launches no mock and passes no env to the pane, so this cell would drive the REAL provider." >&2
    echo "Record it with: scripts/run-cell.sh $a $COVERAGE_ID" >&2
    exit 5
  fi
done

# --dry-run: resolution + recipe validation is done — print the plan and stop
# before any precheck, daemon, or live driver. (The on-disk recording folder is
# per-adapter via shard_folder, equal to COVERAGE_ID for all but variant cells.)
if [[ "$DRY_RUN" -eq 1 ]]; then
  echo "dry-run plan:"
  echo "  scenario (coverage_id): $COVERAGE_ID"
  for a in "${ADAPTERS[@]}"; do
    echo "  adapter: $a  folder: $(shard_folder "$COVERAGE_ID" "$a")  driver: $REPO_ROOT/replaydata/agents/$a/driver-interactive.sh"
  done
  exit 0
fi

# --- Precheck each adapter (builds bins, checks port, CLI versions) ------
# Each adapter's detected CLI version is staged so promote-recording.sh can
# stamp it rather than re-derive it (#1333 / B3); they move into $STAGING below,
# one file per adapter, since a multi-agent cell promotes per adapter.
#
# One temp DIR keyed by filename, not an associative array: macOS ships bash 3.2,
# where `declare -A` is a hard runtime error (and this script already says so at
# its slot bookkeeping). Cleanup is explicit rather than an EXIT trap because
# spawn_record_daemon arms its own and a second `trap ... EXIT` would replace it.
PRECHECK_TMPDIR="$(mktemp -d -t irr-precheck)"
for a in "${ADAPTERS[@]}"; do
  if ! ATTACH=0 PRECHECK_JSON_OUT="$PRECHECK_TMPDIR/$a.json" "$SCRIPT_DIR/precheck.sh" "$a"; then
    rm -rf "$PRECHECK_TMPDIR"
    exit 1
  fi
done

DAEMON="$REPO_ROOT/.build/refresh/bin/irrlichd"
REPLAY_BIN="$REPO_ROOT/.build/refresh/bin/replay"

# --- Staging ------------------------------------------------------------
# Append the PID so two runs of the same scenario within one UTC second don't
# collide on the same staging dir (and thus the same SHARED_CWD passed as
# IRRLICHT_ONBOARD_CWD), which would otherwise put two opencode sessions in one
# directory and let the driver's session lookup pick the wrong one.
TS="$(date -u +%Y%m%dT%H%M%S)-$$"
STAGING="$REPO_ROOT/.build/refresh/_multi/$SCENARIO-$TS"
SHARED_CWD="$STAGING/cwd"
mkdir -p "$STAGING/recordings" "$STAGING/reports" "$SHARED_CWD"

# Park each adapter's precheck output next to its per-adapter staging subdir
# (#1333 / B3), and capture the repo HEAD for provenance (#1333 / B7). The two
# rigs share a staging contract, so a field that exists in only one of them is
# how 4-2 came to record the user's own sessions (#1214 unified the daemon
# lifecycle but not the per-rig env).
for a in "${ADAPTERS[@]}"; do
  mkdir -p "$STAGING/$a"
  if [[ -s "$PRECHECK_TMPDIR/$a.json" ]]; then
    mv "$PRECHECK_TMPDIR/$a.json" "$STAGING/$a/precheck.json"
  fi
done
rm -rf "$PRECHECK_TMPDIR"
GIT_HEAD_START="$(git -C "$REPO_ROOT" rev-parse --short HEAD 2>/dev/null || echo unknown)"

MANIFEST="$STAGING/run-manifest.json"
DAEMON_SHUTDOWN="unknown"

# write_error_manifest <error-code> [<extras-json>] — emit an ERROR-verdict
# run-manifest.json on a failure path so the implement skill's "read
# run-manifest.json → classify" step gets a verdict instead of finding no
# manifest (mirrors run-cell.sh's write_error_manifest contract). Includes
# each adapter driver's exit-reason (from its per-adapter staging subdir).
write_error_manifest() {
  local error_code="$1" a r
  local extras_json="${2:-}"
  [[ -n "$extras_json" ]] || extras_json="{}"
  local reasons="{}"
  for a in "${ADAPTERS[@]}"; do
    r="$(cat "$STAGING/$a/driver.exit-reason" 2>/dev/null || echo missing)"
    reasons="$(jq -n --argjson o "$reasons" --arg k "$a" --arg v "$r" '$o + {($k): $v}')"
  done
  jq -n \
    --arg scenario "$SCENARIO" \
    --argjson adapters "$(printf '%s\n' "${ADAPTERS[@]}" | jq -R . | jq -s .)" \
    --arg error "$error_code" \
    --arg staging "$STAGING" \
    --arg daemon_shutdown "$DAEMON_SHUTDOWN" \
    --argjson driver_exit_reasons "$reasons" \
    --argjson extras "$extras_json" \
    '{scenario: $scenario, verdict: "ERROR", cross_adapter: $adapters,
      error: $error, staging: $staging, daemon_shutdown: $daemon_shutdown,
      driver_exit_reasons: $driver_exit_reasons} + $extras' \
    > "$MANIFEST"
}

# Agent-home isolation, one call per adapter in this cell, from the SAME
# declaration run-cell.sh reads (lib/agent-home.sh). It has to run BEFORE the
# spawn below: the DAEMON reads these variables eagerly when it builds each
# adapter's watcher, and `--print-managed-files` — the list the snapshot
# protects — resolves under them too.
#
# The copilot case this replaces is the one that showed what a missed export
# costs: unset, the daemon watches the real ~/.copilot while the driver writes
# to staging, so it observes every one of the user's own copilot sessions and
# none of the driver's, and curation fails with "primary session does not
# appear in the recording". Its "<staging subdir>/copilot-home" default is
# preserved exactly by the per-adapter spelling below.
#
# It also closes the drift that motivated the shared lib: this loop handled
# COPILOT_HOME and nothing else, while run-cell.sh handled CODEX_HOME too — so
# a codex cross-adapter recording could not be isolated at all, whatever the
# operator exported.
for _a in "${ADAPTERS[@]}"; do
  agent_home_isolate "$_a" "$STAGING/$_a/$_a-home" || exit 1
done

# --- Spawn ONE isolated --record daemon ---------------------------------
# The lib snapshots the shared agent config, spawns the daemon, arms
# stop_record_daemon as the EXIT trap, and waits for its socket. A non-zero
# return means the socket never appeared (it has already said so on stderr); the
# trap still drains the daemon and restores the config on the way out, so all
# that is left here is the ERROR manifest the implement skill reads.
# ADAPTERS_CSV narrows grant-all's auto-grant to this run's own pair, so a
# cross-adapter cell no longer auto-grants — and never Applies — every OTHER
# adapter's hook installer too (claudecode chief among them: #1769).
ADAPTERS_CSV="$(IFS=,; echo "${ADAPTERS[*]}")"
spawn_record_daemon "$DAEMON" "$STAGING" "$ONBOARD_BIND" "$ONBOARD_HOME" "$ADAPTERS_CSV" \
  || { write_error_manifest "daemon_socket_missing"; exit 1; }

# --- Launch every adapter's interactive driver CONCURRENTLY -------------
# All share $SHARED_CWD (the same workspace). Each gets its own staging
# subdir, fresh preferred-UUID, settings.json, and the cell's script.
# DRV_TIMEOUTS is the third parallel array (bash 3.2 — no associative arrays):
# each driver's own `timeout_seconds`, which the teardown gate below needs as
# the upper bound on how long the session it asks about could have lived. It is
# per-ADAPTER, not per-run — the two agents in a cross-adapter cell routinely
# declare different timeouts — so it cannot be re-derived after this loop.
declare -a DRV_PIDS=() DRV_ADAPTERS=() DRV_TIMEOUTS=()
for a in "${ADAPTERS[@]}"; do
  sub="$STAGING/$a"
  mkdir -p "$sub"
  recipe="$(shard_recipe "$COVERAGE_ID" "$a")"
  jq '.settings // {}' <<<"$recipe" > "$sub/settings.json"
  script_json="$(jq -c '.script' <<<"$recipe")"
  timeout_s="$(jq -r '.timeout_seconds // 240' <<<"$recipe")"
  uuid="$(uuidgen | tr '[:upper:]' '[:lower:]')"
  driver="$REPO_ROOT/replaydata/agents/$a/driver-interactive.sh"
  [[ -x "$driver" ]] || { echo "driver missing: $driver" >&2; exit 1; }
  echo "launching $a driver (shared cwd=$SHARED_CWD, timeout=${timeout_s}s)"
  IRRLICHT_ONBOARD_CWD="$SHARED_CWD" \
    "$driver" "$sub" "$uuid" "$timeout_s" "$sub/settings.json" "$script_json" \
    >"$sub/driver.out" 2>&1 &
  # `$!` is the RUN IDENTITY the teardown gate matches session names against,
  # and this rig needs no `driver.pid` file to learn it (run-cell.sh does, since
  # its call is in the foreground): the command backgrounded above is a simple
  # command, so bash forks once and execs the driver in that same child — `$!`
  # and the `$$` the driver goes on to embed in its tmux session names are one
  # number. Measured on this bash rather than assumed:
  #     $ FOO=bar ./drv.sh a b c >out 2>&1 & ; echo "parent \$! = $!"; wait; cat out
  #     parent $! = 81518
  #     driver sees $$ = 81518
  # (bash 3.2.57(1)-release, arm64-apple-darwin25 — the recording host.)
  DRV_PIDS+=($!)
  DRV_ADAPTERS+=("$a")
  DRV_TIMEOUTS+=("$timeout_s")
done

# --- Did each driver's tmux sessions actually die? (#1825 / AC4) --------
# Same two-part shape as run-cell.sh:441-489 — MEASURE the instant a driver
# returns, act on the VERDICT further down — for the same reason: everything
# after the driver returns (the daemon drain, the recording pick) takes time,
# and a look taken after it is strictly more lenient than the one this gate is
# supposed to be taking.
#
# Per driver, not per run: each adapter's sessions carry its OWN pid, so the
# check is scoped to one driver at a time and a survivor can be attributed. The
# measurement runs inside the wait loop, so driver 0's sessions are inspected
# while driver 1 may still be live — which is fine and deliberate, because pid
# scoping is what separates them.
TMUX_TEARDOWN_JSON="{}"   # adapter -> {status, detail, driver_pid}
TMUX_LEAKED=""            # space-joined adapters whose sessions outlived them
TMUX_UNREADABLE=""        # space-joined adapters whose check could not be made

# BEGIN record_driver_teardown — extracted verbatim and executed against a
# shadowed tmux by lib/run-cell-multi-teardown_test.sh. Keep the markers.
record_driver_teardown() {
  local a="$1" pid="$2" lifetime="$3"
  local deadline status detail rc=0
  # The deadline/lifetime pair await_gone_bound checks (rule 3 in
  # lib/tmux-teardown-check.sh's header). Computed by tmux_teardown_deadline_for
  # — shared with run-cell.sh (#1828) so the two rigs cannot drift apart on the
  # arithmetic; see that function's header for why a tenth, capped at 5, floored
  # at 1.
  deadline="$(tmux_teardown_deadline_for "$lifetime")"

  check_tmux_teardown "$pid" "$deadline" "$lifetime" "the cell's own driver timeout" || rc=$?
  case "$rc" in
    0) status="clean"
       detail="no tmux session carries driver pid ${pid:-<unrecorded>} (settled after ${TMUX_TEARDOWN_ELAPSED}s)" ;;
    1) status="leaked"
       detail="$TMUX_TEARDOWN_SURVIVORS"
       TMUX_LEAKED="${TMUX_LEAKED:+$TMUX_LEAKED }$a" ;;
    *) status="unreadable"
       detail="$TMUX_TEARDOWN_REASON"
       TMUX_UNREADABLE="${TMUX_UNREADABLE:+$TMUX_UNREADABLE }$a" ;;
  esac
  TMUX_TEARDOWN_JSON="$(jq -n \
    --argjson o "$TMUX_TEARDOWN_JSON" \
    --arg k "$a" --arg status "$status" --arg detail "$detail" --arg pid "$pid" \
    '$o + {($k): {status: $status, detail: $detail, driver_pid: $pid}}')"
  echo "tmux teardown [$a]: $status — $detail"
  return 0
}
# END record_driver_teardown

# BEGIN tmux_teardown_verdict — same extraction contract as above.
#
# ONE LEAKING DRIVER FAILS THE WHOLE RUN. Not "only that adapter's cell", for
# two reasons that are specific to this rig rather than tidiness:
#
#   1. There is no per-adapter verdict to degrade. This rig writes ONE
#      run-manifest.json for the run and promote-recording.sh takes the staging
#      dir as a unit; "adapter A's fixture is fine, adapter B's is not" is not a
#      state it can express.
#   2. The leak is not confined to the leaker. Every adapter's events.jsonl is
#      curated over the WHOLE workspace — ALL_SIDS unions every session of every
#      adapter in — and all of them share one cwd and one daemon. A still-live
#      agent is therefore inside every fixture this run would stage, not just
#      its own, so promoting the "unaffected" ones would promote fixtures whose
#      workspace was still changing while they were cut.
#
# When both a leak and an unreadable check happened, the LEAK names the manifest:
# it is the finding with an action attached (kill the session), and the
# unreadable adapters are still listed in the per-adapter detail so neither is
# lost. They stay distinct codes for the same reason run-cell.sh keeps them
# distinct — "it leaked" and "nothing was checked" must not print the same thing.
tmux_teardown_verdict() {
  [[ -n "$TMUX_LEAKED" || -n "$TMUX_UNREADABLE" ]] || return 0

  local TMUX_MULTI_ERROR
  if [[ -n "$TMUX_LEAKED" ]]; then
    TMUX_MULTI_ERROR="driver_tmux_session_survived"
  else
    TMUX_MULTI_ERROR="driver_tmux_teardown_unreadable"
  fi
  write_error_manifest "$TMUX_MULTI_ERROR" \
    "$(jq -nc \
        --argjson tmux_teardown "$TMUX_TEARDOWN_JSON" \
        --arg tmux_teardown_leaked "$TMUX_LEAKED" \
        --arg tmux_teardown_unreadable "$TMUX_UNREADABLE" \
        --arg tmux_teardown_detail "leaked=[${TMUX_LEAKED:-none}] unreadable=[${TMUX_UNREADABLE:-none}]" \
        '{tmux_teardown: $tmux_teardown,
          tmux_teardown_leaked: $tmux_teardown_leaked,
          tmux_teardown_unreadable: $tmux_teardown_unreadable,
          tmux_teardown_detail: $tmux_teardown_detail}')"
  if [[ -n "$TMUX_LEAKED" ]]; then
    echo "ERROR: tmux sessions outlived their driver — adapter(s): $TMUX_LEAKED" >&2
    echo "  kill the survivor(s) with: tmux kill-session -t <name> (names are in run-manifest.json .tmux_teardown)" >&2
  fi
  if [[ -n "$TMUX_UNREADABLE" ]]; then
    echo "ERROR: the tmux-teardown check could not be made — adapter(s): $TMUX_UNREADABLE" >&2
  fi
  return 1
}
# END tmux_teardown_verdict

# Wait for all drivers; record each exit status.
DRV_FAIL=0
for i in "${!DRV_PIDS[@]}"; do
  if wait "${DRV_PIDS[$i]}"; then
    echo "driver ${DRV_ADAPTERS[$i]}: ok"
  else
    rc=$?
    echo "driver ${DRV_ADAPTERS[$i]}: FAILED (exit $rc)" >&2
    DRV_FAIL=1
  fi
  # Measured HERE, one statement after this driver returned — see the block
  # comment above. A driver that FAILED is measured too: a crashed driver is
  # the likeliest one to have skipped its own teardown.
  record_driver_teardown "${DRV_ADAPTERS[$i]}" "${DRV_PIDS[$i]}" "${DRV_TIMEOUTS[$i]}"
done

# --- Drain the daemon ---------------------------------------------------
stop_record_daemon   # also disarms the EXIT trap it armed
DAEMON_SHUTDOWN="$(cat "$STAGING/daemon.shutdown" 2>/dev/null || echo unknown)"

# The teardown VERDICT, taken here rather than in the wait loop: this is the
# first line where DAEMON_SHUTDOWN is known, so the ERROR manifest carries the
# same envelope as every other failure path instead of a placeholder — and the
# daemon is drained and the operator's agent config restored before we exit.
tmux_teardown_verdict || exit 1

# --- Locate the single recording ----------------------------------------
RECORDING="$(pick_isolated_recording "$STAGING/recordings" '*.jsonl')" || true
[[ -n "$RECORDING" ]] || { echo "no recording produced under $STAGING/recordings" >&2; write_error_manifest "no_recording"; exit 1; }

# --- Collect each adapter's daemon session_id(s) + transcript(s) --------
# Drivers write session.uuid/transcript.path (the slot-1 PRIMARY, already
# the DAEMON-side session_id: rollout-stem for codex, UUID for claudecode)
# AND session.uuids/transcript.paths (ALL slots, in order) when a script
# chains start_session/reset_session/fork. We curate each adapter's fixture
# from its PRIMARY transcript but union EVERY session (all adapters, all
# slots) into the workspace events.jsonl. (bash 3.2 — parallel indexed
# arrays keyed by ADAPTERS position.)
# Reconciliation helpers (daemon_sid_for_transcript, sid_in_recording,
# reconcile_slot_csv) live in lib/reconcile.sh, sourced above and unit-tested
# by lib/reconcile_test.sh. They map each driver-written id to the
# daemon-recorded session_id and verify it actually appears in the recording.

PRIMARY_SID=()
PRIMARY_TRANSCRIPT=()
OWN_TRANSCRIPTS=()   # this adapter's own slot transcripts, newline-joined
ALL_SIDS=()          # flat union of every adapter's every slot sid
for idx in "${!ADAPTERS[@]}"; do
  a="${ADAPTERS[$idx]}"
  sub="$STAGING/$a"
  PRIMARY_TRANSCRIPT[$idx]="$(head -n1 "$sub/transcript.path" 2>/dev/null || true)"
  raw_primary_sid="$(head -n1 "$sub/session.uuid" 2>/dev/null || true)"
  # Reconcile the driver's preferred id to the daemon's recorded session_id.
  PRIMARY_SID[$idx]="$(daemon_sid_for_transcript "${PRIMARY_TRANSCRIPT[$idx]}" "$a" "$raw_primary_sid")"
  if [[ -z "${PRIMARY_SID[$idx]}" || -z "${PRIMARY_TRANSCRIPT[$idx]}" || ! -f "${PRIMARY_TRANSCRIPT[$idx]}" ]]; then
    echo "ERROR: $a driver did not resolve a session (sid=${PRIMARY_SID[$idx]:-missing}, transcript=${PRIMARY_TRANSCRIPT[$idx]:-missing})" >&2
    DRV_FAIL=1
    continue
  fi
  # The reconciled primary MUST be an id the daemon actually recorded. If
  # reconcile fell back to the driver-written id (transcript_path mismatch /
  # transcript unobserved) and that id never appears in the recording,
  # curating against it yields a silently-empty per-adapter arc — fail loudly
  # instead of staging a fixture that doesn't support its own assertions.
  if ! sid_in_recording "${PRIMARY_SID[$idx]}"; then
    echo "ERROR: $a primary session '${PRIMARY_SID[$idx]}' does not appear in the recording — reconcile fell back to a driver id the daemon never recorded; the per-adapter arc would be empty. Not staging." >&2
    DRV_FAIL=1
    continue
  fi
  # Full per-slot lists (fall back to the single-file primaries). Reconcile
  # each slot's id against its matching transcript path so every chained
  # session is filtered by its daemon-recorded id, not its preferred id.
  uuids_file="$sub/session.uuids"; [[ -f "$uuids_file" ]] || uuids_file="$sub/session.uuid"
  paths_file="$sub/transcript.paths"; [[ -f "$paths_file" ]] || paths_file="$sub/transcript.path"
  # Reconcile every slot's id against its matching transcript path (kept in
  # lockstep by reconcile_slot_csv) so each chained session is filtered by its
  # daemon-recorded id, not its driver-preferred id. The while loop runs in
  # the current shell (process substitution, not a pipe) so ALL_SIDS persists.
  csv=""
  while IFS= read -r sid; do
    csv+="${csv:+,}$sid"
    ALL_SIDS+=("$sid")
  done < <(reconcile_slot_csv "$uuids_file" "$paths_file" "$a")
  OWN_TRANSCRIPTS[$idx]="$(cat "$paths_file" 2>/dev/null || true)"
  echo "$a: primary=${PRIMARY_SID[$idx]} sids=[$csv]"
done
[[ "$DRV_FAIL" -eq 0 ]] || { echo "one or more drivers failed; not curating" >&2; write_error_manifest "driver_failed"; exit 1; }

# --- Curate one per-adapter fixture each --------------------------------
# events.jsonl spans the WHOLE workspace: every session of every adapter
# (ALL_SIDS) is unioned in via IRRLICHT_EXTRA_SESSION_IDS. transcript.jsonl
# stays THIS adapter's own — IRRLICHT_EXTRA_TRANSCRIPTS carries only this
# adapter's slot transcripts (concatenated if it chained sessions), never
# another adapter's (different format); for a single-slot adapter it's left
# empty so curate does a plain copy.
for idx in "${!ADAPTERS[@]}"; do
  a="${ADAPTERS[$idx]}"
  # extras = every sid except this adapter's primary (curate adds the
  # primary itself; its sort -u dedups any overlap with this adapter's
  # own extra slots).
  extra=""
  for s in ${ALL_SIDS[@]+"${ALL_SIDS[@]}"}; do
    [[ "$s" == "${PRIMARY_SID[$idx]}" ]] && continue
    extra+="${extra:+,}$s"
  done
  # Only concatenate this adapter's transcripts when it has more than one
  # slot; otherwise leave empty so curate copies the single primary.
  own_t=""
  if [[ "$(printf '%s\n' "${OWN_TRANSCRIPTS[$idx]}" | grep -c .)" -gt 1 ]]; then
    own_t="${OWN_TRANSCRIPTS[$idx]}"
  fi
  # Stage under the cell's on-disk recording FOLDER (per-adapter; equals
  # COVERAGE_ID for all but variant-folder cells), mirroring run-cell.sh.
  folder="$(shard_folder "$COVERAGE_ID" "$a")"
  echo "curating $a (primary=${PRIMARY_SID[$idx]}, workspace extras: $extra)"
  IRRLICHT_EXTRA_SESSION_IDS="$extra" \
  IRRLICHT_EXTRA_TRANSCRIPTS="$own_t" \
    "$REPO_ROOT/tools/curate-lifecycle-fixture.sh" \
      -d "$STAGING/replaydata/agents" \
      "$RECORDING" "${PRIMARY_SID[$idx]}" "${PRIMARY_TRANSCRIPT[$idx]}" "$a" "$folder"

  ext="$(meta_transcript_ext "$a")"
  staged_t="$STAGING/replaydata/agents/$a/scenarios/$folder/transcript.$ext"
  (cd "$REPO_ROOT" && "$REPLAY_BIN" --quiet --out "$STAGING/reports/$a.staged.json" "$staged_t") || true
  [[ -s "$STAGING/reports/$a.staged.json" ]] || { echo "replay failed for $a ($staged_t)" >&2; write_error_manifest "replay_failed" "$(jq -nc --arg a "$a" '{failed_adapter:$a}')"; exit 1; }
done

# --- Manifest -----------------------------------------------------------
GIT_HEAD_END="$(git -C "$REPO_ROOT" rev-parse --short HEAD 2>/dev/null || echo unknown)"
if [[ "$GIT_HEAD_START" != "$GIT_HEAD_END" ]]; then
  echo "WARNING: HEAD moved during this run ($GIT_HEAD_START -> $GIT_HEAD_END) —" >&2
  echo "         another session committed in this worktree while it recorded." >&2
fi

# Completeness runs here too (#1333 / A3): a cross-adapter cell is torn down the
# same way a single-adapter one is, and `ok` is just as misleading.
COMPLETENESS="$(completeness_json "$STAGING" --scenario "$COVERAGE_ID")"
report_completeness "$COMPLETENESS"

jq -n \
  --arg scenario "$COVERAGE_ID" \
  --argjson adapters "$(printf '%s\n' "${ADAPTERS[@]}" | jq -R . | jq -s .)" \
  --argjson sids "$(printf '%s\n' "${ALL_SIDS[@]}" | jq -R . | jq -s .)" \
  --arg staging "$STAGING" \
  --arg raw_recording "$RECORDING" \
  --arg daemon_shutdown "$DAEMON_SHUTDOWN" \
  --argjson completeness "$COMPLETENESS" \
  --arg git_head_start "$GIT_HEAD_START" \
  --arg git_head_end "$GIT_HEAD_END" \
  '{scenario: $scenario,
    verdict: "STAGED",
    cross_adapter: $adapters,
    session_ids: $sids,
    staging: $staging,
    raw_recording: $raw_recording,
    completeness: $completeness,
    git_head_start: $git_head_start,
    git_head_end: $git_head_end,
    daemon_shutdown: $daemon_shutdown}' \
  > "$MANIFEST"

echo "manifest: $MANIFEST"
echo "staged fixtures:"
for a in "${ADAPTERS[@]}"; do
  echo "  $STAGING/replaydata/agents/$a/scenarios/$(shard_folder "$COVERAGE_ID" "$a")/"
done
