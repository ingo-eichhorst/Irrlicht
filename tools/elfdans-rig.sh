#!/usr/bin/env bash
# elfdans-rig.sh — stand up an isolated relay for the Elfdans device test, and
# run every assertion from docs/elfdans-device-test.md that does not need a
# phone in your hand.
#
#   tools/elfdans-rig.sh up [--serve]   build + start a relay on 127.0.0.1:7839
#                                      with auth on, in its own state dir;
#                                      --serve also publishes it over Tailscale
#   tools/elfdans-rig.sh check          run the phone-free assertions (exit 1 on
#                                      the first failure, with the reason)
#   tools/elfdans-rig.sh drive <name>   drive one Phase 5 policy scenario through
#                                      the relay as a synthetic daemon: waiting,
#                                      ready, flap, burst, subagent, disconnect
#   tools/elfdans-rig.sh status         what is running, and where its state is
#   tools/elfdans-rig.sh down [--wipe]  stop it; --wipe also deletes the state
#
# The state dir is .build/elfdans-rig — never $IRRLICHT_HOME, never your real
# ~/.local/share/irrlicht — so nothing here can touch a production relay's
# tokens or a paired phone's subscription.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
RIG_DIR="$REPO_ROOT/.build/elfdans-rig"
STATE_DIR="$RIG_DIR/state"
BIN="$RIG_DIR/irrlichtrelay"
DRIVE_BIN="$RIG_DIR/elfdans-drive"
DRIVE_STATE="$RIG_DIR/drive-state.json"
PID_FILE="$RIG_DIR/relay.pid"
LOG_FILE="$RIG_DIR/relay.log"
TOKEN_FILE="$RIG_DIR/client-token"
PORT="${ELFDANS_RIG_PORT:-7839}"
BASE="http://127.0.0.1:$PORT"

die() { printf '\033[31mFAIL\033[0m %s\n' "$*" >&2; exit 1; }
ok()  { printf '\033[32m  ok\033[0m %s\n' "$*"; }
info(){ printf '\033[36m==>\033[0m %s\n' "$*"; }

# Every external tool this script depends on is checked up front: a rig that
# silently skips a check reports the same "all pass" as one that ran it, which
# is the failure this whole test plan exists to stop making.
require_tools() {
    for t in "$@"; do
        command -v "$t" >/dev/null 2>&1 || die "missing required tool: $t"
    done
}

relay_running() {
    [ -f "$PID_FILE" ] && kill -0 "$(cat "$PID_FILE")" 2>/dev/null
}

relay_env() {
    env IRRLICHT_HOME="$STATE_DIR" "$@"
}

# nohup + disown so the relay outlives the invocation that started it — both
# `up` and `check`'s restart step use this. Without it, `check` leaves you with
# a passing report and no running relay, exactly when you are about to move on
# to the phases that need one.
start_relay() {
    port_free_or_die
    nohup env IRRLICHT_HOME="$STATE_DIR" "$BIN" serve \
        --addr "127.0.0.1:$PORT" --auth tokens-file >>"$LOG_FILE" 2>&1 &
    local pid=$!
    disown %% 2>/dev/null || true
    echo "$pid" > "$PID_FILE"
    wait_for_relay || die "relay did not answer $BASE/api/v1/version — see $LOG_FILE"
    # Answering is not the same as OURS answering. Without this, a relay left
    # over from a previous run (or the user's real one, since 7839 is the
    # production default) satisfies the readiness probe while the process we
    # just launched is already dead of a bind error — and the rig reports
    # "up" over somebody else's state.
    kill -0 "$pid" 2>/dev/null \
        || die "something else is serving $BASE — our relay exited (see $LOG_FILE). Free the port, or set ELFDANS_RIG_PORT."
}

# 7839 is the relay's production default, so a busy port is more likely to be
# something the user cares about than a stray of ours. Refuse; never kill it.
port_free_or_die() {
    if curl -fsS "$BASE/api/v1/version" >/dev/null 2>&1; then
        die "$BASE is already serving — refusing to start a second relay on it. Stop it, or run with ELFDANS_RIG_PORT=7859."
    fi
}

# ── up ───────────────────────────────────────────────────────────────
cmd_up() {
    local serve=0
    [ "${1:-}" = "--serve" ] && serve=1
    require_tools go curl

    relay_running && die "a rig relay is already running (pid $(cat "$PID_FILE")) — 'down' first"

    info "building irrlichtrelay"
    mkdir -p "$RIG_DIR" "$STATE_DIR"
    chmod 700 "$STATE_DIR"
    ( cd "$REPO_ROOT/core" && go build -o "$BIN" ./cmd/irrlichtrelay/ ) || die "build failed"

    if [ ! -s "$TOKEN_FILE" ]; then
        info "issuing a client token"
        relay_env "$BIN" token issue --label "elfdans-rig-client" --workspace rig \
            | awk '/^  /{print $1}' | tail -1 > "$TOKEN_FILE"
        [ -s "$TOKEN_FILE" ] || die "token issue produced no plaintext token"
        chmod 600 "$TOKEN_FILE"
    fi

    info "starting relay on $BASE (auth on, state in $STATE_DIR)"
    start_relay
    ok "relay up (pid $(cat "$PID_FILE"))"

    if [ "$serve" = "1" ]; then
        require_tools tailscale
        info "publishing over Tailscale"
        tailscale serve --bg "$PORT" || die "tailscale serve failed"
        tailscale serve status || true
    fi

    cat <<EOF

Client token (Settings → Sources, and what mints pairing codes):
  $(cat "$TOKEN_FILE")

Point a daemon at it:
  IRRLICHT_RELAY_TOKEN=$(cat "$TOKEN_FILE") IRRLICHT_RELAY_URL=ws://127.0.0.1:$PORT core/bin/irrlichd

Then: tools/elfdans-rig.sh check    # the phone-free assertions
      docs/elfdans-device-test.md   # the phases that need the phone
EOF
}

wait_for_relay() {
    for _ in $(seq 1 50); do
        curl -fsS "$BASE/api/v1/version" >/dev/null 2>&1 && return 0
        sleep 0.1
    done
    return 1
}

# ── check ────────────────────────────────────────────────────────────
# A synthetic but VALID subscription: the relay validates the p256dh as a real
# 65-byte uncompressed P-256 point, so a placeholder is rejected at the door.
# The last 65 bytes of the DER SPKI encoding are exactly that point.
make_subscription_json() {
    local endpoint="$1" key auth
    key=$(openssl ecparam -name prime256v1 -genkey -noout 2>/dev/null \
        | openssl ec -pubout -outform DER 2>/dev/null | tail -c 65 | b64url)
    auth=$(openssl rand 16 | b64url)
    printf '{"endpoint":"%s","keys":{"p256dh":"%s","auth":"%s"}}' "$endpoint" "$key" "$auth"
}

b64url() { base64 | tr -d '\n' | tr '+/' '-_' | tr -d '='; }

# Conditionals here are `if`, never `cond && args+=(…)`: under `set -e` a
# false test makes the whole list return non-zero, which aborts the function
# before curl ever runs — and the caller then compares an empty string to a
# status code and reports a misleading failure.
api() {
    local method="$1" path="$2" bearer="${3:-}" payload="${4:-}"
    local args=(-sS -o "$RIG_DIR/.body" -w '%{http_code}' -X "$method")
    if [ -n "$bearer" ]; then args+=(-H "Authorization: Bearer $bearer"); fi
    if [ -n "$payload" ]; then args+=(-H 'Content-Type: application/json' -d "$payload"); fi
    args+=("$BASE$path")
    curl "${args[@]}"
}

body() { cat "$RIG_DIR/.body"; }

cmd_check() {
    require_tools curl openssl awk
    relay_running || die "no rig relay running — 'up' first"
    local token; token=$(cat "$TOKEN_FILE")
    local failures=0
    step() { printf '\033[36m--\033[0m %s\n' "$*"; }

    step "push/info advertises push on an auth-enabled relay"
    [ "$(api GET /api/v1/push/info)" = "200" ] || die "push/info did not answer 200"
    echo "$(body)" | grep -q '"enabled":true' || die "push/info: enabled is not true — $(body)"
    echo "$(body)" | grep -q '"vapid_public_key":"[A-Za-z0-9_-]\{80,\}"' \
        || die "push/info carries no plausible VAPID public key — $(body)"
    ok "enabled:true with a VAPID key"

    step "state files are 0600"
    for f in tokens.json vapid-keys.json; do
        [ -f "$STATE_DIR/$f" ] || die "$f was never written"
        local mode; mode=$(stat -f '%Lp' "$STATE_DIR/$f" 2>/dev/null || stat -c '%a' "$STATE_DIR/$f")
        [ "$mode" = "600" ] || die "$f is mode $mode, want 600"
    done
    ok "tokens.json and vapid-keys.json are 0600"

    step "pairing: mint → redeem → subscribe"
    [ "$(api POST /api/v1/push/pairings "$token")" = "201" ] || die "mint failed: $(body)"
    local code; code=$(body | sed -n 's/.*"code":"\([^"]*\)".*/\1/p')
    [ -n "$code" ] || die "mint returned no code: $(body)"
    # Every JSON payload is built in a variable and passed as "$var". Inlining
    # it as "$(api … "{\"a\":1,\"b\":2}")" does not work: the outer quoting
    # layer consumes the backslashes, the inner command sees an unquoted
    # {a,b}, and bash brace-expands it into two arguments — so api runs twice,
    # each time with half a body, and the relay answers a truthful 401.
    local payload; payload=$(printf '{"code":"%s","label":"rig-phone"}' "$code")
    [ "$(api POST /api/v1/push/pair "" "$payload")" = "200" ] \
        || die "redeem failed: $(body)"
    local device; device=$(body | sed -n 's/.*"token":"\([^"]*\)".*/\1/p')
    local device_id; device_id=$(body | sed -n 's/.*"token_id":"\([^"]*\)".*/\1/p')
    [ -n "$device" ] || die "redeem returned no device token: $(body)"
    ok "device token $device_id issued in the code's workspace"

    step "the same code cannot be redeemed twice"
    local replay; replay=$(printf '{"code":"%s"}' "$code")
    [ "$(api POST /api/v1/push/pair "" "$replay")" = "401" ] \
        || die "a spent code was accepted again — single-use is broken"
    ok "spent code answers the uniform 401"

    step "a valid subscription registers under the authenticated token"
    local sub; sub=$(make_subscription_json "https://push.example.test/v2/rig-capability-path")
    [ "$(api POST /api/v1/push/subscriptions "$device" "$sub")" = "204" ] \
        || die "subscribe failed: $(body)"
    [ "$(api GET /api/v1/push/subscriptions "$device")" = "200" ] || die "health read failed"
    echo "$(body)" | grep -q '"registered":true' || die "health says not registered — $(body)"
    ok "registered, and readable by its own device token"

    step "the health endpoint never repeats the endpoint path (RFC 8030 §8.3)"
    echo "$(body)" | grep -q 'rig-capability-path' \
        && die "health response leaks the subscription path — $(body)"
    ok "endpoint path is not echoed"

    step "a malformed subscription is refused at the door"
    local bad; bad='{"endpoint":"https://x.test/p","keys":{"p256dh":"AAAA","auth":"AAAA"}}'
    [ "$(api POST /api/v1/push/subscriptions "$device" "$bad")" = "400" ] \
        || die "a malformed subscription was accepted: $(body)"
    ok "400, not a stored dud"

    step "VAPID identity and subscriptions survive a relay restart"
    local key_before; key_before=$(api GET /api/v1/push/info >/dev/null; body)
    cmd_down >/dev/null
    start_relay
    api GET /api/v1/push/info >/dev/null
    [ "$(body)" = "$key_before" ] || die "VAPID key changed across restart — every phone would be orphaned"
    [ "$(api GET /api/v1/push/subscriptions "$device")" = "200" ] || die "subscription lost across restart"
    ok "same VAPID key, subscription intact"

    step "revoking the device token prunes its delivery address"
    relay_env "$BIN" token revoke "$device_id" >/dev/null || die "revoke failed"
    local pruned=0
    for _ in $(seq 1 30); do
        grep -q "$device_id" "$STATE_DIR/push-subscriptions.json" 2>/dev/null || { pruned=1; break; }
        sleep 0.5
    done
    [ "$pruned" = "1" ] || die "revoked token still has a subscription after 15s — the §8.1 coupling is broken"
    ok "subscription pruned"

    relay_running || die "the checks passed but the relay is not running — 'up' before the device phases"
    printf '\n\033[32mAll phone-free checks passed.\033[0m Phases 3, 4 and the visual half of 5\n'
    printf 'in docs/elfdans-device-test.md still need a real device. The relay is still up.\n'
    return $failures
}

# ── the anonymous-relay guard, which needs its own instance ──────────
cmd_check_guard() {
    require_tools curl
    local gport=$((PORT + 10)) gdir="$RIG_DIR/guard-state" gbase
    gbase="http://127.0.0.1:$gport"
    [ -x "$BIN" ] || die "no relay binary at $BIN — 'up' first (it builds one)"
    rm -rf "$gdir"; mkdir -p "$gdir"
    info "starting a second relay with --auth off on $gbase"
    env IRRLICHT_HOME="$gdir" "$BIN" serve --addr "127.0.0.1:$gport" --auth off >"$RIG_DIR/guard.log" 2>&1 &
    local gpid=$!
    trap 'kill '"$gpid"' 2>/dev/null || true' RETURN
    local ready=""
    for _ in $(seq 1 50); do curl -fsS "$gbase/api/v1/version" >/dev/null 2>&1 && { ready=1; break; }; sleep 0.1; done
    # A guard relay that never answered has to say so. Without this the checks
    # below curl a dead port, read an empty body, and report the ASSERTION as
    # failed — "an anonymous relay advertises push as enabled" about a relay
    # that never started. That is the one diagnosis that sends the reader into
    # the wrong file, and it is what this ran into: `check` before `up` left no
    # binary to exec, and the rig blamed the §8.1 guard for it.
    [ -n "$ready" ] || die "the --auth off guard relay never answered on $gbase — $(tail -n 1 "$RIG_DIR/guard.log" 2>/dev/null || echo 'no guard.log')"

    curl -sS "$gbase/api/v1/push/info" | grep -q '"enabled":false' \
        || die "an anonymous relay advertises push as enabled"
    local status
    status=$(curl -sS -o "$RIG_DIR/.gbody" -w '%{http_code}' -X POST "$gbase/api/v1/push/pairings")
    [ "$status" = "403" ] || die "anonymous relay answered $status to a mint, want 403"
    grep -q -- '--auth tokens-file' "$RIG_DIR/.gbody" \
        || die "the 403 does not name the fix — $(cat "$RIG_DIR/.gbody")"
    [ -f "$gdir/vapid-keys.json" ] && die "an anonymous relay generated VAPID key material"
    ok "anonymous mode refuses push, names the fix, and mints no key"
}

# ── drive ────────────────────────────────────────────────────────────
# tools/elfdans-drive dials this rig's relay as a daemon and produces one
# docs/elfdans-device-test.md Phase 5 row on demand — the rows that otherwise
# need a real agent driven into a state, and, for the burst, four of them
# inside twenty seconds. It reuses the rig's relay URL, client token and state
# dir, so the tester runs one tool rather than two.
#
# The driver pairs a device of its own and prints what the relay actually
# delivered, so a scenario that decided nothing says so. That pairing is
# reused across runs via drive-state.json, which also holds the synthetic
# daemon id: the relay's watchdog roster keeps one entry per daemon it has
# ever seen (arc42 §8.6), and a fresh id per run would leave one phantom
# entry — and one "disconnected" banner per relay restart — behind each time.
cmd_drive() {
    require_tools go curl
    relay_running || die "no rig relay running — 'up' first"
    [ -s "$TOKEN_FILE" ] || die "no client token at $TOKEN_FILE — 'up' first"
    [ "$#" -ge 1 ] || die "usage: tools/elfdans-rig.sh drive <scenario> [flags]  ('drive -h' lists them)"

    info "building elfdans-drive"
    ( cd "$REPO_ROOT/tools/elfdans-drive" && go build -o "$DRIVE_BIN" . ) || die "build failed"

    "$DRIVE_BIN" -relay "ws://127.0.0.1:$PORT" -token "$(cat "$TOKEN_FILE")" \
        -state "$DRIVE_STATE" "$@"
}

# ── status / down ────────────────────────────────────────────────────
cmd_status() {
    if relay_running; then
        echo "relay: running (pid $(cat "$PID_FILE")) on $BASE"
    else
        echo "relay: not running"
    fi
    echo "state: $STATE_DIR"
    [ -f "$STATE_DIR/push-subscriptions.json" ] \
        && echo "paired: $(grep -c token_id "$STATE_DIR/push-subscriptions.json" || echo 0) subscription(s)"
    echo "log:   $LOG_FILE"
}

cmd_down() {
    if relay_running; then
        kill "$(cat "$PID_FILE")" 2>/dev/null || true
        wait "$(cat "$PID_FILE")" 2>/dev/null || true
    fi
    rm -f "$PID_FILE"
    command -v tailscale >/dev/null 2>&1 && tailscale serve --https="$PORT" off >/dev/null 2>&1 || true
    if [ "${1:-}" = "--wipe" ]; then
        rm -rf "$RIG_DIR"
        echo "rig stopped and state wiped"
    else
        echo "rig stopped (state kept in $STATE_DIR — 'down --wipe' to remove)"
    fi
}

case "${1:-}" in
    up)     shift; cmd_up "$@" ;;
    check)  shift; cmd_check_guard; cmd_check "$@" ;;
    drive)  shift; cmd_drive "$@" ;;
    status) cmd_status ;;
    down)   shift; cmd_down "$@" ;;
    *)      sed -n '2,19p' "$0" | sed 's/^# \{0,1\}//'; exit 2 ;;
esac
