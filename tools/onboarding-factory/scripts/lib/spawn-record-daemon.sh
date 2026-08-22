#!/usr/bin/env bash
# spawn-record-daemon.sh — the recording daemon's whole lifecycle, owned once.
#
# Two scripts record fixtures against an `irrlichd --record` daemon:
# run-cell.sh (one adapter) and run-cell-multi.sh (a cross-adapter pair). Both
# need the same five things — the same env assembly, the same spawn, the same
# socket-ready wait, the same INT→TERM→KILL shutdown ladder, and the same
# snapshot/restore of the shared agent config the daemon rewrites. Each used to
# carry its own copy, so a recorder-lifecycle fix reached only whichever script
# the author happened to be editing: #1178's config snapshot landed in
# run-cell.sh alone, and the cross-adapter recorder would still have exited
# leaving the user's ~/.claude/settings.json repointed at a dead port (#1214).
#
# Usage — one call to start, one to stop:
#   source "$SCRIPT_DIR/lib/spawn-record-daemon.sh"
#   spawn_record_daemon "$DAEMON" "$STAGING" "$BIND_ADDR" "$ONBOARD_HOME" "$ADAPTERS" || exit 1
#   ... drive the agent ...
#   stop_record_daemon                   # drain before reading the recording
#
# The EXIT trap is the lib's, both halves: spawn_record_daemon arms
# stop_record_daemon (so any failure between spawn and drain still shuts the
# daemon down and hands the user's config back), and stop_record_daemon disarms
# it. Callers never write `trap` themselves.
#
# Artifacts it writes under <staging>:
#   daemon.log           — the daemon's stdout+stderr
#   daemon.shutdown      — how it ended: sigint | sigterm | sigkill | unknown
#   managed-file-backup/  — the pre-run copy of the shared agent config
#   recordings/          — IRRLICHT_RECORDINGS_DIR (the caller mkdir -p's it)

# shellcheck source-path=SCRIPTDIR
# shellcheck source=managed-file-snapshot.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/managed-file-snapshot.sh"

# State published for the caller (and read back by stop_record_daemon).
RECORD_DAEMON_PID=""
RECORD_DAEMON_SOCK=""
RECORD_DAEMON_STAGING=""

# Poll knobs, read at call time. The defaults ARE the production timings; they
# are overridable only so the unit tests can drive the ladder without spending
# the grace periods in real seconds. Each loop keeps its own tick DEFAULT;
# RECORD_DAEMON_POLL_TICK_S overrides them together.
#   RECORD_DAEMON_SOCK_TICKS = 40 × 0.25s = 10s for the socket to appear
#   RECORD_DAEMON_INT_TICKS  = 12 × 0.5s  = 6s grace after INT, i.e. the
#                                           recorder's 5s flush interval + 1s slack
#   RECORD_DAEMON_TERM_TICKS =  6 × 0.5s  = 3s grace after TERM

# record_daemon_sock <irrlicht-home> prints the unix socket the daemon will
# listen on. A non-empty home is coexist mode (the daemon keeps its socket,
# addr file and state under there); empty means the production layout.
record_daemon_sock() {
  local home="${1:-}"
  if [[ -n "$home" ]]; then
    printf '%s\n' "$home/irrlichd.sock"
  else
    printf '%s\n' "$HOME/.local/share/irrlicht/irrlichd.sock"
  fi
  return 0
}

# record_daemon_env <recordings-dir> <bind-addr> [<irrlicht-home>] prints the
# daemon's environment, one NAME=VALUE per line, for `env` to apply. One per
# line (rather than an array) so it can be asserted directly by the unit tests;
# spaces in a value survive, a literal newline in one would not — no caller can
# produce one (a bind address, and paths under the repo root).
#
# grant-all: the consent-first permission gate (#570) would otherwise leave a
# fresh recording daemon monitoring nothing until a wizard is answered —
# fixtures must never hang on consent.
# IRRLICHT_ALLOW_SHARED_CONFIG_WRITES: since #1449 a grant-all daemon refuses,
# by default, to run an Apply that writes a shared user config outside its own
# isolated home — because a hand-started dev daemon doing that is what left the
# reporter's ~/.claude/settings.json pointing at three dead ports. This rig is
# the one caller entitled to lift it, and the entitlement is earned two lines
# down in spawn_record_daemon: snapshot_managed_files backs up every one of
# those files first and the EXIT trap hands them back. Keep the two together —
# if the snapshot ever stops running, this must stop being set.
# adapter_slug_to_daemon_name <slug> prints the daemon's registered
# agent.Identity.Name for an onboarding-factory adapter SLUG ($ADAPTER, the
# replaydata/agents/<slug> directory name). Every adapter's slug matches its
# daemon identity verbatim EXCEPT claudecode: the pre-#319 registry spelling
# "claude-code" survives (mirrors
# tools/onboarding-factory/internal/shard/shard.go's SlugForAdapter, which
# maps the same divergence in the OPPOSITE direction — identity to slug).
#
# This is not cosmetic: record_adapter_names feeds straight into
# PermissionService.Start's EXACT-STRING recordAdapters lookup
# (permission_service.go, scopedOutByRecordAdapters), so a mismatched name
# doesn't fail loudly — it silently scopes OUT the very adapter the run is
# recording. A first cut of #1769 shipped IRRLICHT_RECORD_ADAPTERS=claudecode
# unmodified and was caught only by a review subagent driving the real
# PermissionService end to end: recording a claudecode cell would have
# withheld claudecode's OWN hooks/statusline/instructions grants, breaking
# every hook-delivered signal in that adapter's recordings — the one adapter
# this issue exists to protect.
adapter_slug_to_daemon_name() {
  case "$1" in
    claudecode) printf '%s' "claude-code" ;;
    *) printf '%s' "$1" ;;
  esac
}

# record_adapter_names <comma-separated-slugs> translates each onboarding-
# factory adapter slug to its daemon identity name (adapter_slug_to_daemon_name
# above) and rejoins with commas, for IRRLICHT_RECORD_ADAPTERS. Empty input
# prints nothing.
record_adapter_names() {
  local slugs="$1" out="" slug
  [[ -z "$slugs" ]] && return 0
  local IFS=','
  # shellcheck disable=SC2206  # word-splitting on IFS=',' is the point: each
  # element is a bare adapter slug (no spaces, no glob metacharacters).
  local parts=($slugs)
  for slug in "${parts[@]}"; do
    [[ -z "$slug" ]] && continue
    if [[ -z "$out" ]]; then out="$(adapter_slug_to_daemon_name "$slug")"
    else out="$out,$(adapter_slug_to_daemon_name "$slug")"; fi
  done
  printf '%s' "$out"
}

# IRRLICHT_RECORD_ADAPTERS is emitted only when the caller names adapters
# (comma-separated, translated to daemon identity names by
# record_adapter_names). Since #1449's ALLOW_SHARED_CONFIG_WRITES lifts the
# shared-config guard for EVERY adapter, not just the one this call is
# recording, grant-all used to auto-grant and Apply every OTHER adapter's
# hook installer too — including claudecode's, which is one of
# righome.Unisolatable's structurally unrelocatable adapters and so
# repointed the operator's REAL ~/.claude/settings.json at this daemon for
# the run's whole duration, whatever adapter was actually being recorded
# (#1769). Naming the adapter(s) here narrows PermissionService's grant-all
# auto-grant to them, closing that window; run-cell.sh and run-cell-multi.sh
# are the callers, threading their own $ADAPTER / $ADAPTERS through.
# IRRLICHT_HOME is emitted only in coexist mode; IRRLICHT_READY_SESSION_TTL only
# when the caller set one (idle-survival cells shrink the 30-min production
# default to a recordable window), because an explicit env array would otherwise
# drop the inherited override.
record_daemon_env() {
  local recordings="$1" bind="$2" home="${3:-}" adapters="${4:-}"
  printf '%s\n' "IRRLICHT_RECORDINGS_DIR=$recordings"
  printf '%s\n' "IRRLICHT_BIND_ADDR=$bind"
  printf '%s\n' "IRRLICHT_PERMISSION_MODE=grant-all"
  printf '%s\n' "IRRLICHT_ALLOW_SHARED_CONFIG_WRITES=1"
  [[ -n "$home" ]] && printf '%s\n' "IRRLICHT_HOME=$home"
  if [[ -n "$adapters" ]]; then
    printf '%s\n' "IRRLICHT_RECORD_ADAPTERS=$(record_adapter_names "$adapters")"
  fi
  [[ -n "${IRRLICHT_READY_SESSION_TTL:-}" ]] && printf '%s\n' "IRRLICHT_READY_SESSION_TTL=$IRRLICHT_READY_SESSION_TTL"
  return 0
}

# spawn_record_daemon <daemon-bin> <staging-dir> <bind-addr> [<irrlicht-home>]
# [<adapters>] snapshots the shared agent config, starts the daemon, arms the
# EXIT trap, and waits for its socket. adapters (comma-separated) narrows
# grant-all's auto-grant to those adapters (#1769) — see record_daemon_env.
# Returns non-zero (after reporting to stderr) if the socket never appears,
# so the caller can add its own failure bookkeeping — run-cell-multi.sh
# writes an ERROR run-manifest — before exiting.
spawn_record_daemon() {
  local daemon_bin="$1" staging="$2" bind="$3" home="${4:-}" adapters="${5:-}"

  RECORD_DAEMON_STAGING="$staging"
  RECORD_DAEMON_SOCK="$(record_daemon_sock "$home")"

  # Save the shared agent config the daemon is about to rewrite (see
  # lib/managed-file-snapshot.sh); the shutdown hands it back. WHICH files those
  # are is asked of the daemon binary itself, so the set follows the adapters
  # that actually install hooks (#1357). Refuse to start if the backup dir can't
  # be created. The snapshot owns that directory — it creates it and reports its
  # own failure — so there is one gate here, not two: a recording daemon we
  # cannot undo must never be spawned in the first place.
  if ! snapshot_managed_files "$staging/managed-file-backup" "$daemon_bin"; then
    echo "cannot snapshot the shared agent config; refusing to spawn the recording daemon" >&2
    return 1
  fi

  # Read the env into an array so a value containing spaces (e.g. an
  # IRRLICHT_HOME path with a space) stays one word — an unquoted
  # ${home:+VAR="$home"} would word-split on it.
  local daemon_env=() kv
  while IFS= read -r kv; do daemon_env+=("$kv"); done \
    < <(record_daemon_env "$staging/recordings" "$bind" "$home" "$adapters")

  env "${daemon_env[@]}" "$daemon_bin" --record >"$staging/daemon.log" 2>&1 &
  RECORD_DAEMON_PID=$!
  echo "daemon started (pid $RECORD_DAEMON_PID, bind=$bind${home:+, home=$home})"

  trap stop_record_daemon EXIT

  wait_for_record_daemon
}

# wait_for_record_daemon polls for the unix socket, which signals the daemon is
# ready to accept connections.
wait_for_record_daemon() {
  local _
  for _ in $(seq 1 "${RECORD_DAEMON_SOCK_TICKS:-40}"); do
    [[ -S "$RECORD_DAEMON_SOCK" ]] && return 0
    sleep "${RECORD_DAEMON_POLL_TICK_S:-0.25}"
  done
  echo "daemon socket never appeared: $RECORD_DAEMON_SOCK" >&2
  return 1
}

# signal_record_daemon <signal-name> <grace-ticks> sends the signal and polls for
# the daemon to go. 0 if it died within the grace, 1 if it outlived it.
signal_record_daemon() {
  local sig="$1" ticks="$2" _
  kill -"$sig" "$RECORD_DAEMON_PID" 2>/dev/null || true
  for _ in $(seq 1 "$ticks"); do
    kill -0 "$RECORD_DAEMON_PID" 2>/dev/null || return 0
    sleep "${RECORD_DAEMON_POLL_TICK_S:-0.5}"
  done
  return 1
}

# kill_record_daemon escalates INT -> TERM -> KILL, giving the recorder time to
# flush between each, and records which signal ended it in <staging>/daemon.shutdown.
# A daemon that is already gone (or was never spawned) leaves "unknown" there,
# matching what the pre-extraction scripts wrote.
kill_record_daemon() {
  [[ -n "$RECORD_DAEMON_STAGING" ]] || return 0
  local reason="unknown"
  if [[ -n "$RECORD_DAEMON_PID" ]] && kill -0 "$RECORD_DAEMON_PID" 2>/dev/null; then
    if signal_record_daemon INT "${RECORD_DAEMON_INT_TICKS:-12}"; then
      reason="sigint"
    elif signal_record_daemon TERM "${RECORD_DAEMON_TERM_TICKS:-6}"; then
      reason="sigterm"
    else
      reason="sigkill"
      kill -KILL "$RECORD_DAEMON_PID" 2>/dev/null || true
    fi
  fi
  echo "$reason" > "$RECORD_DAEMON_STAGING/daemon.shutdown"
}

# stop_record_daemon is the whole teardown: drain the daemon, hand the user's
# agent config back, and disarm the EXIT trap spawn_record_daemon armed. Owning
# both halves of that trap is the point — a caller that had to remember its own
# `trap - EXIT` would be back to a contract split across two files, which is the
# shape of duplication this lib exists to remove. Callers just call it; running
# as the trap itself, the disarm is a harmless no-op.
stop_record_daemon() {
  kill_record_daemon
  restore_managed_files
  trap - EXIT
  return 0
}
