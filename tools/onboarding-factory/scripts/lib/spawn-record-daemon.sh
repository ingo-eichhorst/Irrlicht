#!/usr/bin/env bash
# spawn-record-daemon.sh — the recording daemon's whole lifecycle, owned once.
#
# Two scripts record fixtures against an `irrlichd --record` daemon:
# run-cell.sh (one adapter) and run-cell-multi.sh (a cross-adapter pair). Both
# need the same five things — the same env assembly, the same spawn, the same
# socket-ready wait, the same INT→TERM→KILL shutdown ladder, and the same
# snapshot/restore of the shared agent config the daemon rewrites. Each used to
# carry its own copy, so a recorder-lifecycle fix reached only whichever script
# the author happened to be editing: #1178's hook-config snapshot landed in
# run-cell.sh alone, and the cross-adapter recorder would still have exited
# leaving the user's ~/.claude/settings.json repointed at a dead port (#1214).
#
# Usage — one call to start, one to stop:
#   source "$SCRIPT_DIR/lib/spawn-record-daemon.sh"
#   spawn_record_daemon "$DAEMON" "$STAGING" "$BIND_ADDR" "$ONBOARD_HOME" || exit 1
#   ... drive the agent ...
#   stop_record_daemon; trap - EXIT      # drain before reading the recording
#
# spawn_record_daemon installs `stop_record_daemon` as the EXIT trap itself, so
# any failure between spawn and drain still shuts the daemon down and hands the
# user's config back. Call it explicitly (then clear the trap) when you need the
# recorder flushed before continuing.
#
# Artifacts it writes under <staging>:
#   daemon.log           — the daemon's stdout+stderr
#   daemon.shutdown      — how it ended: sigint | sigterm | sigkill | unknown
#   hook-config-backup/  — the pre-run copy of the shared agent config
#   recordings/          — IRRLICHT_RECORDINGS_DIR (the caller mkdir -p's it)

# shellcheck source=lib/hook-config-snapshot.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/hook-config-snapshot.sh"

# State published for the caller (and read back by stop_record_daemon).
RECORD_DAEMON_PID=""
RECORD_DAEMON_SOCK=""
RECORD_DAEMON_STAGING=""

# Poll knobs, read at call time. The defaults ARE the production timings; they
# are overridable only so the unit tests can drive the ladder without spending
# the grace periods in real seconds.
#   RECORD_DAEMON_SOCK_TICKS  × SOCK_TICK_S  = 40 × 0.25s = 10s for the socket
#   RECORD_DAEMON_INT_TICKS   × KILL_TICK_S  = 12 × 0.5s  = 6s grace after INT,
#                                              i.e. the recorder's 5s flush
#                                              interval + 1s slack
#   RECORD_DAEMON_TERM_TICKS  × KILL_TICK_S  =  6 × 0.5s  = 3s grace after TERM

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
}

# record_daemon_env <recordings-dir> <bind-addr> [<irrlicht-home>] prints the
# daemon's environment, one NAME=VALUE per line, for `env` to apply.
#
# grant-all: the consent-first permission gate (#570) would otherwise leave a
# fresh recording daemon monitoring nothing until a wizard is answered —
# fixtures must never hang on consent.
# IRRLICHT_HOME is emitted only in coexist mode; IRRLICHT_READY_SESSION_TTL only
# when the caller set one (idle-survival cells shrink the 30-min production
# default to a recordable window), because an explicit env array would otherwise
# drop the inherited override.
record_daemon_env() {
  local recordings="$1" bind="$2" home="${3:-}"
  printf '%s\n' "IRRLICHT_RECORDINGS_DIR=$recordings"
  printf '%s\n' "IRRLICHT_BIND_ADDR=$bind"
  printf '%s\n' "IRRLICHT_PERMISSION_MODE=grant-all"
  [[ -n "$home" ]] && printf '%s\n' "IRRLICHT_HOME=$home"
  [[ -n "${IRRLICHT_READY_SESSION_TTL:-}" ]] && printf '%s\n' "IRRLICHT_READY_SESSION_TTL=$IRRLICHT_READY_SESSION_TTL"
  return 0
}

# spawn_record_daemon <daemon-bin> <staging-dir> <bind-addr> [<irrlicht-home>]
# snapshots the shared agent config, starts the daemon, arms the EXIT trap, and
# waits for its socket. Returns non-zero (after reporting to stderr) if the
# socket never appears, so the caller can add its own failure bookkeeping —
# run-cell-multi.sh writes an ERROR run-manifest — before exiting.
spawn_record_daemon() {
  local daemon_bin="$1" staging="$2" bind="$3" home="${4:-}"

  RECORD_DAEMON_STAGING="$staging"
  RECORD_DAEMON_SOCK="$(record_daemon_sock "$home")"

  # Save the shared agent config the daemon is about to rewrite (see
  # lib/hook-config-snapshot.sh); the shutdown hands it back. Refuse to start if
  # the backup dir can't be created: restore_hook_configs reads "no backup for
  # this file" as "the daemon created it", so an unwritable snapshot would end
  # the run by DELETING the user's real ~/.claude/settings.json.
  if ! mkdir -p "$staging/hook-config-backup"; then
    echo "cannot create the hook-config backup dir: $staging/hook-config-backup" >&2
    return 1
  fi
  snapshot_hook_configs "$staging/hook-config-backup"

  # Read the env into an array so a value containing spaces (e.g. an
  # IRRLICHT_HOME path with a space) stays one word — an unquoted
  # ${home:+VAR="$home"} would word-split on it.
  local daemon_env=() kv
  while IFS= read -r kv; do daemon_env+=("$kv"); done \
    < <(record_daemon_env "$staging/recordings" "$bind" "$home")

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
    sleep "${RECORD_DAEMON_SOCK_TICK_S:-0.25}"
  done
  echo "daemon socket never appeared: $RECORD_DAEMON_SOCK" >&2
  return 1
}

# kill_record_daemon escalates INT -> TERM -> KILL, giving the recorder time to
# flush between each, and records which signal ended it in <staging>/daemon.shutdown.
# A daemon that is already gone (or was never spawned) leaves "unknown" there,
# matching what the pre-extraction scripts wrote.
kill_record_daemon() {
  [[ -n "$RECORD_DAEMON_STAGING" ]] || return 0
  local shutdown_file="$RECORD_DAEMON_STAGING/daemon.shutdown"
  local reason="unknown" _
  if [[ -n "$RECORD_DAEMON_PID" ]] && kill -0 "$RECORD_DAEMON_PID" 2>/dev/null; then
    reason="sigint"
    kill -INT "$RECORD_DAEMON_PID" 2>/dev/null || true
    for _ in $(seq 1 "${RECORD_DAEMON_INT_TICKS:-12}"); do
      kill -0 "$RECORD_DAEMON_PID" 2>/dev/null || { echo "$reason" > "$shutdown_file"; return 0; }
      sleep "${RECORD_DAEMON_KILL_TICK_S:-0.5}"
    done
    reason="sigterm"
    kill -TERM "$RECORD_DAEMON_PID" 2>/dev/null || true
    for _ in $(seq 1 "${RECORD_DAEMON_TERM_TICKS:-6}"); do
      kill -0 "$RECORD_DAEMON_PID" 2>/dev/null || { echo "$reason" > "$shutdown_file"; return 0; }
      sleep "${RECORD_DAEMON_KILL_TICK_S:-0.5}"
    done
    reason="sigkill"
    kill -KILL "$RECORD_DAEMON_PID" 2>/dev/null || true
  fi
  echo "$reason" > "$shutdown_file"
}

# stop_record_daemon is the whole teardown: drain the daemon, then hand the
# user's agent config back. Armed as the EXIT trap by spawn_record_daemon, and
# safe to call explicitly before clearing that trap.
stop_record_daemon() {
  kill_record_daemon
  restore_hook_configs
}
