#!/usr/bin/env bash
# opencode-v2-driver_test.sh — the opencode-v2 API driver
# (replaydata/agents/opencode-v2/driver-interactive.sh) against a STUB service.
# Plain bash. Run directly or via scripts/smoke-test.sh.
#
# Nothing here touches a real OpenCode service: the "service" is a python3
# stub HTTP server over a scratch 2.x store built from the 2.1.4 DDL, its
# process is a `sleep` whose identity a stub `ps` first on PATH describes, and
# its log is a scratch file the stub appends to. What is under test is the
# driver's own decisions (#2109):
#
#   - it drives a turn end to end and writes the staging contract, with one
#     opencode.Parser line per session_message row in the export;
#   - the credential reaches the API (the stub checks Basic auth) and never
#     the staging dir;
#   - a stub that never goes idle ends the run as `timeout` by its deadline;
#   - it REFUSES, before creating any session, when two services run, when the
#     service's environment differs from the run's, when the plugin's beacon
#     would post to another daemon, when OPENCODE_DB is set, when service.json
#     is missing — and after creating one, when the plugin fails to load.

set -uo pipefail   # NOT -e: assertions capture non-zero return codes

DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$DIR/../../../.." && pwd)"
DRIVER="$ROOT/replaydata/agents/opencode-v2/driver-interactive.sh"
DDL="$ROOT/core/adapters/inbound/agents/opencode/testdata/v2-schema.sql"

fails=0
pass() { echo "  PASS: $1"; return 0; }
fail() { echo "  FAIL: $1 — $2"; fails=$((fails + 1)); return 0; }
assert_eq() { if [[ "$3" == "$2" ]]; then pass "$1"; else fail "$1" "expected [$2] got [$3]"; fi; }
assert_has() { case "$3" in *"$2"*) pass "$1" ;; *) fail "$1" "wanted to find [$2] in: ${3:0:600}" ;; esac; }

for tool in python3 sqlite3 jq curl; do
  command -v "$tool" >/dev/null 2>&1 || { echo "opencode-v2-driver_test: $tool is required and not on PATH — this test cannot run" >&2; exit 1; }
done
[[ -x "$DRIVER" ]] || { echo "opencode-v2-driver_test: $DRIVER is missing or not executable" >&2; exit 1; }

TMP="$(mktemp -d)"
SVC_SLEEP=""; SVC_SLEEP2=""; STUB_PID=""
cleanup() {
  local p
  for p in "$STUB_PID" "$SVC_SLEEP" "$SVC_SLEEP2"; do
    [[ -n "$p" ]] || continue
    kill "$p" 2>/dev/null; wait "$p" 2>/dev/null
  done
  rm -rf "$TMP"
  return 0
}
trap cleanup EXIT

PASSWORD="pw-2109-$RANDOM$RANDOM-secret"
SVC_HOME="$TMP/svchome"
CFG="$SVC_HOME/.config"; DATA="$SVC_HOME/.local/share"; STATE="$SVC_HOME/.local/state"
mkdir -p "$CFG/opencode/plugin" "$DATA/opencode/log" "$STATE/opencode" "$DATA/irrlicht"
PLUGIN="$CFG/opencode/plugin/irrlicht.js"; : > "$PLUGIN"
LOG="$DATA/opencode/log/opencode.log"; : > "$LOG"
DB="$DATA/opencode/opencode.db"
sqlite3 "$DB" < "$DDL" || { echo "opencode-v2-driver_test: could not build the store from $DDL" >&2; exit 1; }
DAEMON_BIND="127.0.0.1:7999"
printf '%s\n' "$DAEMON_BIND" > "$DATA/irrlicht/irrlichd.addr"

# The "service" process: alive, so kill -0 holds; the stub ps names it.
sleep 600 & SVC_SLEEP=$!
sleep 600 & SVC_SLEEP2=$!

# --- stub ps --------------------------------------------------------------
STUBS="$TMP/stubs"; mkdir -p "$STUBS"
PS_LIST="$TMP/ps.list"     # "<pid> <args>" lines for `ps -axo pid=,args=`
PS_ENV="$TMP/ps.env"       # env words the service process carries
cat > "$STUBS/ps" <<EOF
#!/bin/bash
# stub ps: -axo pid=,args= | -o args= -p PID | -E -ww -o command= -p PID
args="\$*"
pid="\${@: -1}"
case "\$args" in
  *-axo*) cat "$PS_LIST" ;;
  *-E*) line="\$(awk -v p="\$pid" '\$1 == p { \$1 = ""; sub(/^ /, ""); print }' "$PS_LIST")"; [[ -n "\$line" ]] && echo "\$line \$(cat "$PS_ENV")" ;;
  *"-o args="*) awk -v p="\$pid" '\$1 == p { \$1 = ""; sub(/^ /, ""); print }' "$PS_LIST" ;;
  *) /bin/ps "\$@" ;;
esac
EOF
chmod +x "$STUBS/ps"

one_service() { printf '%s /opt/oc2/bin/opencode serve --service\n%s /usr/bin/vim notes.txt\n' "$SVC_SLEEP" "$SVC_SLEEP2" > "$PS_LIST"; }
two_services() { printf '%s /opt/oc2/bin/opencode serve --service\n%s /opt/oc2-beta/bin/opencode2 serve --service\n' "$SVC_SLEEP" "$SVC_SLEEP2" > "$PS_LIST"; }
service_env() { printf 'HOME=%s XDG_CONFIG_HOME=%s XDG_DATA_HOME=%s XDG_STATE_HOME=%s' "$SVC_HOME" "$CFG" "${1:-$DATA}" "$STATE" > "$PS_ENV"; }

# --- stub API -------------------------------------------------------------
cat > "$TMP/stub.py" <<'PY'
import base64, json, os, re, socketserver, sqlite3, sys, time
from http.server import BaseHTTPRequestHandler
DB, LOG, PLUGIN, PW, OUT = (os.environ[k] for k in ("STUB_DB", "STUB_LOG", "STUB_PLUGIN", "STUB_PW", "STUB_OUT"))
def mode():
    try: return open(os.path.join(OUT, "mode")).read().strip()
    except OSError: return "idle"
def note(name, line):
    with open(os.path.join(OUT, name), "a") as f: f.write(line + "\n")
class H(BaseHTTPRequestHandler):
    def log_message(self, *a): pass
    def reply(self, code, obj):
        b = json.dumps(obj).encode()
        self.send_response(code); self.send_header("content-type", "application/json")
        self.send_header("content-length", str(len(b))); self.end_headers(); self.wfile.write(b)
    def authed(self):
        h = self.headers.get("authorization", "")
        ok = h == "Basic " + base64.b64encode(("opencode:" + PW).encode()).decode()
        note("auth", "ok" if ok else "BAD")
        return ok
    def body(self):
        n = int(self.headers.get("content-length") or 0)
        return json.loads(self.rfile.read(n) or b"{}")
    def do_GET(self):
        if not self.authed(): return self.reply(401, {})
        if re.fullmatch(r"/api/session/ses_\w+/permission", self.path): return self.reply(200, {"data": []})
        if self.path.startswith("/api/command?location%5Bdirectory%5D="):
            # Like the real service: the location's first scoped request boots
            # it, and the boot loads (or fails to load) the installed plugin.
            note("booted", self.path)
            line = 'msg="loading plugin" id=%s entrypoint=file://%s role=server' % (PLUGIN, PLUGIN)
            with open(LOG, "a") as f:
                f.write("timestamp=x level=INFO " + line + "\n")
                if mode() == "badplugin":
                    f.write('timestamp=x level=WARN message="failed to load plugin" target=%s ref=err_x\n' % PLUGIN)
            return self.reply(200, {"location": {}, "data": []})
        self.reply(404, {})
    def do_POST(self):
        if not self.authed(): return self.reply(401, {})
        now = int(time.time() * 1000)
        db = sqlite3.connect(DB)
        if self.path == "/api/session":
            d = self.body()["location"]["directory"]
            note("created", d)
            db.execute("INSERT INTO session_v2 (id, project_id, slug, directory, version, time_created, time_updated) VALUES ('ses_stub1','p','s',?,'2.1.4',?,?)", (d, now, now))
            db.commit()
            return self.reply(200, {"data": {"id": "ses_stub1"}})
        m = re.fullmatch(r"/api/session/(ses_\w+)/prompt", self.path)
        if m:
            text = self.body()["text"]
            if mode() == "idle":
                sid = m.group(1)
                rows = [("user", {"text": text}), ("assistant", {"content": [{"type": "text", "text": "ok"}], "tokens": {"input": 10, "output": 1}}), ("idle", {"outcome": "succeeded"})]
                for i, (typ, data) in enumerate(rows):
                    db.execute("INSERT INTO session_message (id, session_id, type, seq, time_created, time_updated, data) VALUES (?,?,?,?,?,?,?)",
                               ("msg_%d" % i, sid, typ, i, now + i, now + i, json.dumps(data)))
                db.execute("UPDATE session_v2 SET time_idle=?, idle_outcome='succeeded' WHERE id=?", (now + 3, sid))
                db.commit()
            return self.reply(200, {"data": {}})
        if re.fullmatch(r"/api/session/ses_\w+/interrupt", self.path): return self.reply(200, {"data": {}})
        self.reply(404, {})
# socketserver.TCPServer, not http.server.HTTPServer: HTTPServer.server_bind
# resolves the host with socket.getfqdn — a reverse DNS lookup that stalled
# past this test's deadline on a CI runner (#2109's first PR run: the stub
# "never published its port" four times in a row).
class S(socketserver.TCPServer):
    allow_reuse_address = True
srv = S(("127.0.0.1", 0), H)
open(os.path.join(OUT, "port.tmp"), "w").write(str(srv.server_address[1])); os.rename(os.path.join(OUT, "port.tmp"), os.path.join(OUT, "port"))
srv.serve_forever()
PY
STUB_OUT="$TMP/stubout"; mkdir -p "$STUB_OUT"
STUB_DB="$DB" STUB_LOG="$LOG" STUB_PLUGIN="$PLUGIN" STUB_PW="$PASSWORD" STUB_OUT="$STUB_OUT" \
  python3 -I "$TMP/stub.py" >"$TMP/stub.log" 2>&1 & STUB_PID=$!
waited=0
until [[ -s "$STUB_OUT/port" ]]; do
  if ! kill -0 "$STUB_PID" 2>/dev/null || (( waited >= 300 )); then
    echo "opencode-v2-driver_test: the stub API never published its port ($((waited / 10))s) — this test cannot run. Stub output:" >&2
    sed 's/^/  | /' "$TMP/stub.log" >&2
    exit 1
  fi
  sleep 0.1; waited=$((waited + 1))
done
PORT="$(cat "$STUB_OUT/port")"

write_service_json() {
  jq -nc --argjson pid "$SVC_SLEEP" --arg url "http://127.0.0.1:$PORT" --arg pw "$PASSWORD" \
    '{id:"svc", pid:$pid, url:$url, password:$pw, version:"2.1.4"}' > "$STATE/opencode/service.json"
  chmod 600 "$STATE/opencode/service.json"
}

# run_driver <name> <timeout-s> <script-json> [VAR=value ...] runs the driver
# in a child with the run's exports, a hard watchdog of timeout+8s (a driver
# that ignores its own deadline must not hang this suite), and publishes
# RUN_RC, RUN_REASON, RUN_OUT, RUN_ELAPSED and RUN_STAGING. The driver's
# readiness knobs are shortened so the stub need not be waited on in real
# seconds; the production values are the driver's defaults.
run_driver() {
  local name="$1" timeout="$2" script="$3"; shift 3
  RUN_STAGING="$TMP/staging-$name"; mkdir -p "$RUN_STAGING"
  local start; start=$(date +%s)
  env -u OPENCODE_DB -u IRRLICHT_ONBOARD_CWD PATH="$STUBS:$PATH" XDG_CONFIG_HOME="$CFG" XDG_DATA_HOME="$DATA" \
      XDG_STATE_HOME="$STATE" IRRLICHT_BIND_ADDR="$DAEMON_BIND" \
      OC2_PLUGIN_WAIT_S=3 OC2_PLUGIN_RECHECK_S=0.2 OC2_SEED_SETTLE_S=0 "$@" \
      "$DRIVER" "$RUN_STAGING" unused "$timeout" /nonexistent "$script" > "$RUN_STAGING/out" 2>&1 &
  local dpid=$! t=0
  while kill -0 "$dpid" 2>/dev/null; do
    if (( t >= (timeout + 8) * 10 )); then kill -9 "$dpid" 2>/dev/null; echo "WATCHDOG KILLED" >> "$RUN_STAGING/out"; break; fi
    sleep 0.1; t=$((t + 1))
  done
  wait "$dpid"; RUN_RC=$?
  RUN_ELAPSED=$(( $(date +%s) - start ))
  RUN_REASON="$(cat "$RUN_STAGING/driver.exit-reason" 2>/dev/null || echo missing)"
  RUN_OUT="$(cat "$RUN_STAGING/out")"
  return 0
}
reset_store() { sqlite3 "$DB" "DELETE FROM session_message; DELETE FROM session_v2;"; : > "$STUB_OUT/created"; : > "$STUB_OUT/auth"; : > "$STUB_OUT/booted"; }
created_count() { grep -c . "$STUB_OUT/created" 2>/dev/null || true; }

one_service; service_env; write_service_json

echo "== a turn end to end: send, wait_turn, export, contract =="
reset_store; echo idle > "$STUB_OUT/mode"
run_driver ok 30 '[{"type":"send","text":"Reply with exactly the word: ok"},{"type":"wait_turn"},{"type":"sleep","seconds":1}]'
assert_eq "exit reason ok" "ok" "$RUN_REASON"
assert_eq "exit 0" "0" "$RUN_RC"
assert_eq "session.uuid is the service's id" "ses_stub1" "$(cat "$RUN_STAGING/session.uuid" 2>/dev/null)"
assert_eq "the session's location was booted (GET /api/command for it)" "1" "$(grep -c . "$STUB_OUT/booted" 2>/dev/null)"
T="$(cat "$RUN_STAGING/transcript.path" 2>/dev/null)"
assert_eq "the export has one line per session_message row" "3" "$(grep -c . "$T" 2>/dev/null)"
assert_eq "every line carries type, _cwd and _ts" "3" "$(jq -c 'select(.type and ._cwd and ._ts)' "$T" 2>/dev/null | grep -c .)"
assert_eq "types in seq order" "user assistant idle" "$(jq -r .type "$T" 2>/dev/null | tr '\n' ' ' | sed 's/ $//')"
CWD_WANT="$(cd "$RUN_STAGING/cwd" 2>/dev/null && pwd -P)"
[[ -n "$CWD_WANT" ]] || fail "the run created its cwd" "no $RUN_STAGING/cwd"
assert_eq "_cwd is the run's own canonical cwd" "$CWD_WANT" "$(jq -r ._cwd "$T" 2>/dev/null | head -1)"
assert_eq "the API saw the credential on every call" "" "$(grep -v '^ok$' "$STUB_OUT/auth")"
[[ -s "$STUB_OUT/auth" ]] && pass "…and was called" || fail "…and was called" "no request reached the stub"
if grep -rqF -- "$PASSWORD" "$RUN_STAGING"; then
  fail "the password is nowhere in staging" "found in: $(grep -rlF -- "$PASSWORD" "$RUN_STAGING" | tr '\n' ' ')"
else
  pass "the password is nowhere in staging"
fi

echo "== a service that never goes idle: timeout, by the deadline =="
reset_store; echo never > "$STUB_OUT/mode"
run_driver never 6 '[{"type":"send","text":"x"},{"type":"wait_turn"}]'
assert_eq "exit reason timeout" "timeout" "$RUN_REASON"
assert_has "…saying the turn never ended" "no new idle" "$RUN_OUT"
if (( RUN_ELAPSED <= 6 + 4 )); then pass "…within the deadline (${RUN_ELAPSED}s for a 6s budget)"; else fail "…within the deadline" "${RUN_ELAPSED}s for a 6s budget"; fi
assert_eq "the watchdog never had to kill it" "" "$(grep -F 'WATCHDOG' <<<"$RUN_OUT")"

echo "== refusals, before any session exists =="
reset_store; two_services
run_driver two 20 '[{"type":"send","text":"x"}]'
assert_eq "two services: exit reason nonzero(2)" "nonzero(2)" "$RUN_REASON"
assert_has "…naming the count" "2 opencode 'serve --service' processes run" "$RUN_OUT"
assert_eq "…and no session was created" "0" "$(created_count)"
one_service

reset_store; service_env "$TMP/elsewhere"
run_driver envdiff 20 '[{"type":"send","text":"x"}]'
assert_eq "service XDG_DATA_HOME differs: refused" "nonzero(2)" "$RUN_REASON"
assert_has "…naming the variable" "runs with XDG_DATA_HOME=" "$RUN_OUT"
assert_eq "…and no session was created" "0" "$(created_count)"
service_env

reset_store; printf '127.0.0.1:7837\n' > "$DATA/irrlicht/irrlichd.addr"
run_driver beacon 20 '[{"type":"send","text":"x"}]'
assert_eq "beacon resolves another daemon: refused" "nonzero(2)" "$RUN_REASON"
assert_has "…saying where the beacon would post" "127.0.0.1:7837" "$RUN_OUT"
assert_eq "…and no session was created" "0" "$(created_count)"
printf '%s\n' "$DAEMON_BIND" > "$DATA/irrlicht/irrlichd.addr"

reset_store
run_driver db 20 '[{"type":"send","text":"x"}]' OPENCODE_DB=/x/other.db
assert_eq "OPENCODE_DB set: refused" "nonzero(2)" "$RUN_REASON"
assert_eq "…and no session was created" "0" "$(created_count)"

reset_store; mv "$STATE/opencode/service.json" "$TMP/service.json.away"
run_driver nosj 20 '[{"type":"send","text":"x"}]'
assert_eq "no service.json: refused" "nonzero(2)" "$RUN_REASON"
assert_has "…saying no service runs there" "no 2.x service runs under this XDG_STATE_HOME" "$RUN_OUT"
mv "$TMP/service.json.away" "$STATE/opencode/service.json"

reset_store; kill "$SVC_SLEEP2" 2>/dev/null; wait "$SVC_SLEEP2" 2>/dev/null
printf '%s /opt/oc2/bin/opencode serve --service\n' "$SVC_SLEEP2" > "$PS_LIST"
jq --argjson p "$SVC_SLEEP2" '.pid = $p' "$STATE/opencode/service.json" > "$TMP/sj" && mv "$TMP/sj" "$STATE/opencode/service.json"
run_driver dead 20 '[{"type":"send","text":"x"}]'
assert_eq "a dead service pid: refused" "nonzero(2)" "$RUN_REASON"
assert_has "…saying so" "is not running" "$RUN_OUT"
SVC_SLEEP2=""; one_service 2>/dev/null; write_service_json
printf '%s /opt/oc2/bin/opencode serve --service\n' "$SVC_SLEEP" > "$PS_LIST"

echo "== a plugin the service fails to load: refused before driving =="
reset_store; echo badplugin > "$STUB_OUT/mode"
run_driver badplugin 20 '[{"type":"send","text":"x"}]'
assert_eq "exit reason nonzero(2)" "nonzero(2)" "$RUN_REASON"
assert_has "…naming the failed load" "failed to load plugin" "$RUN_OUT"

echo "== the driver never traces itself =="
if grep -nE '^[^#]*set -[a-z]*x|set -o xtrace' "$DRIVER" "$ROOT/replaydata/agents/opencode-v2/service-api.sh"; then
  fail "no xtrace" "a traced driver echoes the credential's curl config"
else
  pass "no xtrace"
fi

echo ""
if (( fails == 0 )); then
  echo "opencode-v2-driver_test: ALL PASS"
  exit 0
fi
echo "opencode-v2-driver_test: $fails FAILED" >&2
exit 1
