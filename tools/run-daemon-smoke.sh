#!/usr/bin/env bash
# run-daemon-smoke.sh — boot a real irrlichd in full isolation and verify the
# socket protocol end-to-end, without touching the production daemon.
#
# Mirrors the Go startup smoke test (core/cmd/irrlichd/daemon_smoke_test.go)
# for manual / pre-release use. It:
#   1. builds the dev daemon binary,
#   2. starts it with HOME + IRRLICHT_HOME pointed at throwaway temp dirs and
#      IRRLICHT_BIND_ADDR=127.0.0.1:0 (an OS-assigned port) and
#      IRRLICHT_DEMO_MODE=1 (no file/process watchers),
#   3. reads the resolved port from IRRLICHT_HOME/irrlichd.addr,
#   4. GETs /api/v1/agents over TCP and over the unix socket,
#   5. sends SIGTERM and confirms a clean exit + the addr file is gone.
#
# Because HOME and IRRLICHT_HOME are temp dirs, the production install at
# ~/.local/share/irrlicht/ and the user's ~/.claude config are never touched —
# run this while the production daemon/app keeps running.
#
# Usage: tools/run-daemon-smoke.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

WORK="$(mktemp -d)"
HOME_DIR="$WORK/home"
STATE_DIR="$WORK/state"
mkdir -p "$HOME_DIR" "$STATE_DIR"

BIN="$WORK/irrlichd"
DAEMON_PID=""

cleanup() {
  if [[ -n "$DAEMON_PID" ]] && kill -0 "$DAEMON_PID" 2>/dev/null; then
    kill "$DAEMON_PID" 2>/dev/null || true
    wait "$DAEMON_PID" 2>/dev/null || true
  fi
  rm -rf "$WORK"
}
trap cleanup EXIT

fail() {
  echo "FAIL: $*" >&2
  if [[ -f "$HOME_DIR/Library/Application Support/Irrlicht/logs/events.log" ]]; then
    echo "--- daemon events.log ---" >&2
    cat "$HOME_DIR/Library/Application Support/Irrlicht/logs/events.log" >&2
  fi
  exit 1
}

echo "building dev daemon…"
( cd "$ROOT_DIR/core" && go build -o "$BIN" ./cmd/irrlichd )

echo "starting isolated daemon…"
HOME="$HOME_DIR" \
IRRLICHT_HOME="$STATE_DIR" \
IRRLICHT_BIND_ADDR=127.0.0.1:0 \
IRRLICHT_DEMO_MODE=1 \
  "$BIN" >"$WORK/daemon.out" 2>&1 &
DAEMON_PID=$!

# Wait for the addr file (resolved host:port), up to 5s.
ADDR_FILE="$STATE_DIR/irrlichd.addr"
ADDR=""
for _ in $(seq 1 250); do
  if [[ -s "$ADDR_FILE" ]]; then
    ADDR="$(tr -d '[:space:]' < "$ADDR_FILE")"
    break
  fi
  if ! kill -0 "$DAEMON_PID" 2>/dev/null; then
    fail "daemon exited before writing $ADDR_FILE"
  fi
  sleep 0.02
done
[[ -n "$ADDR" ]] || fail "daemon never wrote $ADDR_FILE within 5s"
echo "daemon listening on $ADDR"

# 1. TCP round-trip.
echo -n "GET /api/v1/agents over TCP… "
TCP_BODY="$(curl -fsS "http://$ADDR/api/v1/agents")" || fail "TCP request failed"
echo "$TCP_BODY" | grep -q '"name"' || fail "TCP response has no agents: $TCP_BODY"
echo "ok"

# 2. Unix socket round-trip.
echo -n "GET /api/v1/agents over unix socket… "
SOCK="$STATE_DIR/irrlichd.sock"
UNIX_BODY="$(curl -fsS --unix-socket "$SOCK" "http://unix/api/v1/agents")" || fail "unix socket request failed"
echo "$UNIX_BODY" | grep -q '"name"' || fail "unix response has no agents: $UNIX_BODY"
echo "ok"

# 3. Clean shutdown on SIGTERM.
echo -n "SIGTERM clean shutdown… "
kill -TERM "$DAEMON_PID"
for _ in $(seq 1 150); do
  kill -0 "$DAEMON_PID" 2>/dev/null || break
  sleep 0.02
done
if kill -0 "$DAEMON_PID" 2>/dev/null; then
  fail "daemon did not exit within 3s of SIGTERM"
fi
DAEMON_PID=""
[[ ! -e "$ADDR_FILE" ]] || fail "addr file $ADDR_FILE should be removed after shutdown"
echo "ok"

echo "PASS: daemon startup smoke clean"
