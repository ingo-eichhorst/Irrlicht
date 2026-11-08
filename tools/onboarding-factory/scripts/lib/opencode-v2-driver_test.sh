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
#     is missing — and after creating one, when the plugin fails to load;
#   - it answers a permission ask only when the API's id is a permission id,
#     and writes a seed_instruction only under RUN_CWD (#2109 review F2, F3);
#   - P5b-1's session steps: slots (start_session, {"session": N}, a bare
#     session step), reset_session and archive/unarchive (PATCH), model,
#     revert and await_child — each id or name it puts in a URL path or body
#     is checked first, and an unknown slot is refused;
#   - the service window's exit_clean/sigkill: refused without
#     OC2_SERVICE_WINDOW=1, refused when the pid stopped being the service,
#     and otherwise the service is signalled with every slot exported first.

set -uo pipefail   # NOT -e: assertions capture non-zero return codes

DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$DIR/../../../.." && pwd)"
DRIVER="$ROOT/replaydata/agents/opencode-v2/driver-interactive.sh"
DDL="$ROOT/core/adapters/inbound/agents/opencode/testdata/v2-schema.sql"

fails=0
# Section filter: OC2_DRIVER_TEST_ONLY=<key> runs one section's driver runs and
# assertions. Every section's setup (store resets, stub modes, service
# respawns) still runs — it is cheap, and later sections lean on it. The
# mutation fixture (tools/lib/opencode-v2-driver-mutations_test.sh) uses it so
# each mutation pays for its own section only. A key no section has fails the
# run: a typo must not read as a pass.
ONLY="${OC2_DRIVER_TEST_ONLY:-}"
SKIPPING=0
SECTIONS_RUN=0
section() { # <key> <title>
  if [[ -n "$ONLY" && "$1" != "$ONLY" ]]; then SKIPPING=1; return 0; fi
  SKIPPING=0; SECTIONS_RUN=$((SECTIONS_RUN + 1))
  echo "== $2 =="
}
pass() { (( SKIPPING )) && return 0; echo "  PASS: $1"; return 0; }
fail() { (( SKIPPING )) && return 0; echo "  FAIL: $1 — $2"; fails=$((fails + 1)); return 0; }
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
  *"-o args="*)
    # ps.drift: from the second look on, the pid is no longer the service — a
    # pid that changed identity between preflight and a service-window step.
    n=\$(( \$(cat "$TMP/ps.argscount" 2>/dev/null || echo 0) + 1 )); echo "\$n" > "$TMP/ps.argscount"
    if [[ -f "$TMP/ps.drift" && "\$n" -gt 1 ]]; then echo "/usr/bin/vim notes.txt"
    else awk -v p="\$pid" '\$1 == p { \$1 = ""; sub(/^ /, ""); print }' "$PS_LIST"; fi ;;
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
    # One shared log in arrival order, so a test can check the ORDER of calls
    # that land in different files (reset_session: create, then archive).
    with open(os.path.join(OUT, "seq"), "a") as f: f.write(name + " " + line + "\n")
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
        if re.fullmatch(r"/api/session/ses_\w+/permission", self.path):
            # PERM_ID is the id shape the sandbox 2.1.4 service answered this
            # very GET with (#2109 triage probe, obs-apiperm/pending.json).
            ask = {"perm": "per_1730b57c2001jmLgKMBeVTW1uA", "badperm": "per_x/../../interrupt"}.get(mode())
            return self.reply(200, {"data": [{"id": ask}] if ask else []})
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
    def do_PATCH(self):
        if not self.authed(): return self.reply(401, {})
        m = re.fullmatch(r"/api/session/(ses_\w+)", self.path)
        if not m: return self.reply(404, {})
        body = self.body()
        note("patched", "%s %s" % (m.group(1), json.dumps(body, separators=(",", ":"))))
        db = sqlite3.connect(DB)
        db.execute("UPDATE session_v2 SET time_archived=? WHERE id=?", (int(time.time() * 1000) if body.get("archived") else None, m.group(1)))
        db.commit()
        return self.reply(200, {"data": {}})
    def do_POST(self):
        if not self.authed(): return self.reply(401, {})
        now = int(time.time() * 1000)
        note("posted", self.path)
        db = sqlite3.connect(DB)
        if self.path == "/api/session":
            d = self.body()["location"]["directory"]
            note("created", d)
            sid = "ses_stub%d" % (db.execute("SELECT count(*) FROM session_v2").fetchone()[0] + 1)
            db.execute("INSERT INTO session_v2 (id, project_id, slug, directory, version, time_created, time_updated) VALUES (?,'p','s',?,'2.1.4',?,?)", (sid, d, now, now))
            db.commit()
            return self.reply(200, {"data": {"id": sid}})
        m = re.fullmatch(r"/api/session/(ses_\w+)/prompt", self.path)
        if m:
            body = self.body()
            text = body["text"]
            sid = m.group(1)
            note("bodies", "%s %s" % (self.path, json.dumps(body, separators=(",", ":"))))
            if mode() == "queue":
                # A turn that stays in flight until a queued prompt arrives:
                # the first prompt writes only its user row; the queued one
                # completes the turn (one idle, as the 2.1.4 service does).
                seq = db.execute("SELECT coalesce(max(seq), -1) + 1 FROM session_message WHERE session_id=?", (sid,)).fetchone()[0]
                rows = [("user", {"text": text})] if "delivery" not in body else [("user", {"text": text}), ("assistant", {"content": [{"type": "text", "text": "ok"}]}), ("idle", {"outcome": "succeeded"})]
                for i, (typ, data) in enumerate(rows):
                    db.execute("INSERT INTO session_message (id, session_id, type, seq, time_created, time_updated, data) VALUES (?,?,?,?,?,?,?)",
                               ("msg_%s%d" % (sid.replace("_", ""), seq + i), sid, typ, seq + i, now + i, now + i, json.dumps(data)))
                if "delivery" in body:
                    db.execute("UPDATE session_v2 SET time_idle=?, idle_outcome='succeeded' WHERE id=?", (now + 3, sid))
                db.commit()
                return self.reply(200, {"data": {}})
            if mode() in ("idle", "perm", "badperm", "badmsg"):
                seq = db.execute("SELECT coalesce(max(seq), -1) + 1 FROM session_message WHERE session_id=?", (sid,)).fetchone()[0]
                rows = [("user", {"text": text}), ("assistant", {"content": [{"type": "text", "text": "ok"}], "tokens": {"input": 10, "output": 1}}), ("idle", {"outcome": "succeeded"})]
                for i, (typ, data) in enumerate(rows):
                    mid = "msg_%s%d" % (sid.replace("_", ""), seq + i)
                    if typ == "user" and mode() == "badmsg": mid = "msg_x/../%d" % (seq + i)
                    db.execute("INSERT INTO session_message (id, session_id, type, seq, time_created, time_updated, data) VALUES (?,?,?,?,?,?,?)",
                               (mid, sid, typ, seq + i, now + i, now + i, json.dumps(data)))
                db.execute("UPDATE session_v2 SET time_idle=?, idle_outcome='succeeded' WHERE id=?", (now + 3, sid))
                db.commit()
            if mode() == "tool":
                db.execute("INSERT INTO session_message (id, session_id, type, seq, time_created, time_updated, data) VALUES (?,?,?,?,?,?,?)",
                           ("msg_tool1", sid, "assistant", 0, now, now, json.dumps({"content": [{"type": "tool", "name": "shell", "state": {"status": "running"}}]})))
                db.commit()
            if mode() in ("child", "badchild"):
                # The task tool's shape: a child session_v2 row with parent_id,
                # whose latest step runs a shell tool.
                child = "ses_child1" if mode() == "child" else "ses_bad'x"
                db.execute("INSERT INTO session_v2 (id, project_id, parent_id, slug, directory, version, time_created, time_updated) VALUES (?,'p',?,'s','/c','2.1.4',?,?)", (child, sid, now, now))
                db.execute("INSERT INTO session_message (id, session_id, type, seq, time_created, time_updated, data) VALUES (?,?,?,?,?,?,?)",
                           ("msg_child1", child, "assistant", 0, now, now, json.dumps({"content": [{"type": "tool", "name": "shell", "state": {"status": "running"}}]})))
                db.commit()
            return self.reply(200, {"data": {}})
        m = re.fullmatch(r"/api/session/(ses_\w+)/compact", self.path)
        if m:
            note("bodies", "%s %s" % (self.path, json.dumps(self.body(), separators=(",", ":"))))
            if mode() == "nocompact": return self.reply(500, {"error": "no"})
            sid = m.group(1)
            seq = db.execute("SELECT coalesce(max(seq), -1) + 1 FROM session_message WHERE session_id=?", (sid,)).fetchone()[0]
            for i, (typ, data) in enumerate([("compaction", {"status": "completed", "reason": "manual", "summary": "s"}), ("idle", {"outcome": "succeeded"})]):
                db.execute("INSERT INTO session_message (id, session_id, type, seq, time_created, time_updated, data) VALUES (?,?,?,?,?,?,?)",
                           ("msg_c%s%d" % (sid.replace("_", ""), seq + i), sid, typ, seq + i, now + 10 + i, now + 10 + i, json.dumps(data)))
            db.execute("UPDATE session_v2 SET time_idle=?, idle_outcome='succeeded' WHERE id=?", (now + 13, sid))
            db.commit()
            return self.reply(200, {"data": {}})
        m = re.fullmatch(r"/api/session/(ses_\w+)/shell", self.path)
        if m:
            body = self.body()
            note("bodies", "%s %s" % (self.path, json.dumps(body, separators=(",", ":"))))
            sid = m.group(1)
            seq = db.execute("SELECT coalesce(max(seq), -1) + 1 FROM session_message WHERE session_id=?", (sid,)).fetchone()[0]
            status = "running" if mode() == "shellstuck" else "exited"
            db.execute("INSERT INTO session_message (id, session_id, type, seq, time_created, time_updated, data) VALUES (?,?,?,?,?,?,?)",
                       ("msg_s%s%d" % (sid.replace("_", ""), seq), sid, "shell", seq, now, now, json.dumps({"command": body.get("command"), "status": status})))
            db.commit()
            return self.reply(200, {"data": {}})
        m = re.fullmatch(r"/api/session/ses_\w+/model", self.path)
        if m:
            note("model", json.dumps(self.body(), separators=(",", ":")))
            return self.reply(200, {"data": {}})
        if re.fullmatch(r"/api/session/ses_\w+/revert/stage", self.path):
            note("revert", "stage " + json.dumps(self.body(), separators=(",", ":")))
            return self.reply(200, {"data": {}})
        if re.fullmatch(r"/api/session/ses_\w+/revert/commit", self.path):
            note("revert", "commit")
            return self.reply(200, {"data": {}})
        if re.fullmatch(r"/api/session/ses_\w+/interrupt", self.path): return self.reply(200, {"data": {}})
        if re.fullmatch(r"/api/session/ses_\w+/background", self.path): return self.reply(200, {"data": {}})
        if re.fullmatch(r"/api/session/ses_\w+/permission/[^/]+/reply", self.path): return self.reply(200, {"data": {}})
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
  if (( SKIPPING )); then
    RUN_RC=0; RUN_REASON="skipped"; RUN_OUT=""; RUN_ELAPSED=0; RUN_STAGING="$TMP/skipped"
    return 0
  fi
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
reset_store() { sqlite3 "$DB" "DELETE FROM session_message; DELETE FROM session_v2;"; local f; for f in created auth booted posted patched model revert seq bodies; do : > "$STUB_OUT/$f"; done; rm -f "$TMP/ps.drift" "$TMP/ps.argscount"; }
prompts() { grep '/prompt$' "$STUB_OUT/posted" | tr '\n' ' ' | sed 's/ $//'; }
created_count() { grep -c . "$STUB_OUT/created" 2>/dev/null || true; }

one_service; service_env; write_service_json

section turn "a turn end to end: send, wait_turn, export, contract"
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

section never "a service that never goes idle: timeout, by the deadline"
reset_store; echo never > "$STUB_OUT/mode"
run_driver never 6 '[{"type":"send","text":"x"},{"type":"wait_turn"}]'
assert_eq "exit reason timeout" "timeout" "$RUN_REASON"
assert_has "…saying the turn never ended" "no new idle" "$RUN_OUT"
if (( RUN_ELAPSED <= 6 + 4 )); then pass "…within the deadline (${RUN_ELAPSED}s for a 6s budget)"; else fail "…within the deadline" "${RUN_ELAPSED}s for a 6s budget"; fi
assert_eq "the watchdog never had to kill it" "" "$(grep -F 'WATCHDOG' <<<"$RUN_OUT")"

section refusals "refusals, before any session exists"
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

section badplugin "a plugin the service fails to load: refused before driving"
reset_store; echo badplugin > "$STUB_OUT/mode"
run_driver badplugin 20 '[{"type":"send","text":"x"}]'
assert_eq "exit reason nonzero(2)" "nonzero(2)" "$RUN_REASON"
assert_has "…naming the failed load" "failed to load plugin" "$RUN_OUT"

section permission "a permission ask is answered only when its id is a permission id (#2109 review F2)"
# The id comes from the API and goes into the reply URL's path. A real one
# (the shape the sandbox service answered with) is answered at exactly that
# path — a lock on the pattern being loose enough; one carrying path segments
# is refused before any reply is POSTed.
reset_store; echo perm > "$STUB_OUT/mode"
run_driver perm 20 '[{"type":"send","text":"x"},{"type":"permission_reply","decision":"once","hold_seconds":0},{"type":"wait_turn"}]'
assert_eq "a per_… id: exit reason ok" "ok" "$RUN_REASON"
assert_eq "…answered at exactly its own reply path" "/api/session/ses_stub1/permission/per_1730b57c2001jmLgKMBeVTW1uA/reply" "$(grep -F '/permission/' "$STUB_OUT/posted")"
reset_store; echo badperm > "$STUB_OUT/mode"
run_driver badperm 20 '[{"type":"send","text":"x"},{"type":"permission_reply","decision":"once","hold_seconds":0},{"type":"wait_turn"}]'
assert_eq "an id carrying path segments: refused" "nonzero(2)" "$RUN_REASON"
assert_has "…naming why" "not a permission id" "$RUN_OUT"
assert_eq "…and nothing but the session and its prompt was POSTed" "/api/session /api/session/ses_stub1/prompt" "$(tr '\n' ' ' < "$STUB_OUT/posted" | sed 's/ $//')"
assert_eq "…but the session is still exported (the epilogue ran)" "3" "$(grep -c . "$(cat "$RUN_STAGING/transcript.path" 2>/dev/null)" 2>/dev/null)"

section seed "a seed_instruction writes only under RUN_CWD (#2109 review F3)"
reset_store; echo idle > "$STUB_OUT/mode"
run_driver seedup 20 '[{"type":"seed_instruction","path":"../escape.md","text":"x"},{"type":"send","text":"x"},{"type":"wait_turn"}]'
assert_eq "a leading '..' path: refused" "nonzero(2)" "$RUN_REASON"
assert_has "…naming why" "must be relative with no '..' component" "$RUN_OUT"
[[ -e "$RUN_STAGING/escape.md" ]] && got=written || got=absent
assert_eq "…and nothing was written outside RUN_CWD" "absent" "$got"
assert_eq "…and no session was created" "0" "$(created_count)"
reset_store
run_driver seedabs 20 "$(jq -nc --arg p "$TMP/abs-escape.md" '[{"type":"seed_instruction","path":$p,"text":"x"},{"type":"send","text":"x"}]')"
assert_eq "an absolute path: refused" "nonzero(2)" "$RUN_REASON"
assert_has "…naming why" "must be relative with no '..' component" "$RUN_OUT"
[[ -e "$TMP/abs-escape.md" ]] && got=written || got=absent
assert_eq "…and nothing was written there" "absent" "$got"
assert_eq "…and no session was created" "0" "$(created_count)"
reset_store
run_driver seedmid 20 '[{"type":"send","text":"x"},{"type":"wait_turn"},{"type":"seed_instruction","path":"../mid.md","text":"x"}]'
assert_eq "a mid-script path with a '..' component: refused" "nonzero(2)" "$RUN_REASON"
[[ -e "$RUN_STAGING/mid.md" ]] && got=written || got=absent
assert_eq "…and nothing was written outside RUN_CWD" "absent" "$got"
assert_eq "…but the session is still exported (the epilogue ran)" "3" "$(grep -c . "$(cat "$RUN_STAGING/transcript.path" 2>/dev/null)" 2>/dev/null)"
reset_store
run_driver seedok 20 '[{"type":"seed_instruction","path":"AGENTS.md","text":"be brief"},{"type":"send","text":"x"},{"type":"wait_turn"}]'
assert_eq "a plain relative path: exit reason ok" "ok" "$RUN_REASON"
assert_eq "…written under RUN_CWD" "be brief" "$(cat "$RUN_STAGING/cwd/AGENTS.md" 2>/dev/null)"
reset_store
# A path that passes the guard but cannot be written — its directory does not
# exist — must fail the run, not log "wrote N bytes" and record without the
# instruction (#2109 review, PR #2117 second review finding 1: both call sites
# sit in `||` lists, where errexit is off inside the function).
run_driver seednodir 20 '[{"type":"seed_instruction","path":"missing/dir/AGENTS.md","text":"x"},{"type":"send","text":"x"},{"type":"wait_turn"}]'
assert_eq "a leading write that fails: refused" "nonzero(2)" "$RUN_REASON"
assert_has "…naming why" "could not write" "$RUN_OUT"
assert_eq "…and no session was created" "0" "$(created_count)"
reset_store
run_driver seednodirmid 20 '[{"type":"send","text":"x"},{"type":"wait_turn"},{"type":"seed_instruction","path":"missing/dir/AGENTS.md","text":"x"}]'
assert_eq "a mid-script write that fails: refused" "nonzero(2)" "$RUN_REASON"
assert_has "…naming why" "could not write" "$RUN_OUT"

section slots "sessions as slots: start_session, {\"session\": N} (#2109 P5b-1)"
reset_store; echo idle > "$STUB_OUT/mode"
run_driver slots 30 '[{"type":"send","text":"a"},{"type":"wait_turn"},{"type":"start_session"},{"type":"send","text":"b"},{"type":"wait_turn"},{"type":"send","text":"c","session":1},{"type":"wait_turn"},{"type":"session","session":2},{"type":"send","text":"d"},{"type":"wait_turn"}]'
assert_eq "exit reason ok" "ok" "$RUN_REASON"
assert_eq "session.uuids lists both slots in order" "ses_stub1 ses_stub2" "$(tr '\n' ' ' < "$RUN_STAGING/session.uuids" 2>/dev/null | sed 's/ $//')"
assert_eq "session.uuid is slot 1" "ses_stub1" "$(cat "$RUN_STAGING/session.uuid" 2>/dev/null)"
assert_eq "each prompt went to the slot the recipe named" "/api/session/ses_stub1/prompt /api/session/ses_stub2/prompt /api/session/ses_stub1/prompt /api/session/ses_stub2/prompt" "$(prompts)"
assert_eq "both sessions were created at the one location" "2" "$(created_count)"
assert_eq "…which was booted once" "1" "$(grep -c . "$STUB_OUT/booted" 2>/dev/null)"
assert_eq "transcript.paths has one export per slot" "2" "$(grep -c . "$RUN_STAGING/transcript.paths" 2>/dev/null)"
assert_eq "…slot 1's holds its two turns" "6" "$(grep -c . "$(sed -n 1p "$RUN_STAGING/transcript.paths" 2>/dev/null)" 2>/dev/null)"
assert_eq "…slot 2's its two" "6" "$(grep -c . "$(sed -n 2p "$RUN_STAGING/transcript.paths" 2>/dev/null)" 2>/dev/null)"
reset_store
# A slot's idle mark survives a switch away and back: slot 2's second turn is
# sent, focus moves to slot 1, and the wait on slot 2 must compare against the
# mark its send took — not the 0 a fresh slot starts with, which against a
# real service would end the wait on the previous turn's idle.
run_driver slotmark 30 '[{"type":"send","text":"a"},{"type":"wait_turn"},{"type":"start_session"},{"type":"send","text":"b"},{"type":"wait_turn"},{"type":"send","text":"c"},{"type":"session","session":1},{"type":"wait_turn","session":2}]'
assert_eq "exit reason ok" "ok" "$RUN_REASON"
mark_line="$(grep 'turn over on ses_stub2' <<<"$RUN_OUT" | tail -1)"
case "$mark_line" in
  *"> mark 0)"*|"") fail "slot 2's idle mark survived the switch to slot 1" "a non-zero mark" "${mark_line:-no turn-over line}" ;;
  *) pass "slot 2's idle mark survived the switch to slot 1" ;;
esac
reset_store
run_driver badslot 20 '[{"type":"send","text":"a"},{"type":"wait_turn"},{"type":"send","text":"b","session":3}]'
assert_eq "an unknown slot: refused" "nonzero(2)" "$RUN_REASON"
assert_has "…naming it" "invalid session slot '3' (have 1)" "$RUN_OUT"
assert_eq "…and nothing was sent past it" "/api/session/ses_stub1/prompt" "$(prompts)"
reset_store
run_driver noslot 20 '[{"type":"send","text":"a"},{"type":"wait_turn"},{"type":"session"}]'
assert_eq "a session step with no slot: refused" "nonzero(2)" "$RUN_REASON"

section reset "reset_session, archive and unarchive (PATCH)"
reset_store; echo idle > "$STUB_OUT/mode"
run_driver reset 30 '[{"type":"send","text":"a"},{"type":"wait_turn"},{"type":"reset_session"},{"type":"send","text":"b"},{"type":"wait_turn"}]'
assert_eq "exit reason ok" "ok" "$RUN_REASON"
assert_eq "the old session was archived" 'ses_stub1 {"archived":true}' "$(cat "$STUB_OUT/patched")"
new_at="$(grep -n '^created ' "$STUB_OUT/seq" | sed -n 2p | cut -d: -f1)"
archived_at="$(grep -n '^patched ses_stub1 ' "$STUB_OUT/seq" | head -1 | cut -d: -f1)"
if [[ -n "$new_at" && -n "$archived_at" ]] && (( new_at < archived_at )); then
  pass "…after the new one existed"
else
  fail "…after the new one existed" "the second create before the PATCH" "create line '${new_at}', patch line '${archived_at}'"
fi
assert_eq "…and the next prompt went to the new one" "/api/session/ses_stub1/prompt /api/session/ses_stub2/prompt" "$(prompts)"
assert_eq "…both are in the staging contract" "ses_stub1 ses_stub2" "$(tr '\n' ' ' < "$RUN_STAGING/session.uuids" 2>/dev/null | sed 's/ $//')"
reset_store
run_driver archive 30 '[{"type":"send","text":"a"},{"type":"wait_turn"},{"type":"archive"},{"type":"unarchive"},{"type":"send","text":"b"},{"type":"wait_turn"}]'
assert_eq "exit reason ok" "ok" "$RUN_REASON"
assert_eq "archive then unarchive, on the active session" $'ses_stub1 {"archived":true}\nses_stub1 {"archived":false}' "$(cat "$STUB_OUT/patched")"

section model "model: the provider/model is checked before it is sent"
reset_store
run_driver model 20 '[{"type":"model","provider":"kolibri","model":"kolibri-1-4bit-b"},{"type":"send","text":"a"},{"type":"wait_turn"}]'
assert_eq "exit reason ok" "ok" "$RUN_REASON"
assert_eq "the switch carried exactly the recipe's ids" '{"model":{"id":"kolibri-1-4bit-b","providerID":"kolibri"}}' "$(cat "$STUB_OUT/model")"
reset_store
run_driver badmodel 20 '[{"type":"model","provider":"kolibri","model":"../x y"},{"type":"send","text":"a"},{"type":"wait_turn"}]'
assert_eq "a malformed model id: refused" "nonzero(2)" "$RUN_REASON"
assert_has "…naming why" "not a provider/model id" "$RUN_OUT"
assert_eq "…and no switch was sent" "" "$(cat "$STUB_OUT/model")"

section revert "revert: the target is the Nth-last user message, and must be a message id"
reset_store; echo idle > "$STUB_OUT/mode"
run_driver revert 30 '[{"type":"send","text":"a"},{"type":"wait_turn"},{"type":"send","text":"b"},{"type":"wait_turn"},{"type":"revert"}]'
assert_eq "exit reason ok" "ok" "$RUN_REASON"
assert_eq "stage at the latest user message (turn 2's), then commit" $'stage {"messageID":"msg_sesstub13","files":true}\ncommit' "$(cat "$STUB_OUT/revert")"
reset_store; echo badmsg > "$STUB_OUT/mode"
run_driver badrevert 30 '[{"type":"send","text":"a"},{"type":"wait_turn"},{"type":"revert"}]'
assert_eq "a revert target that is not a message id: refused" "nonzero(2)" "$RUN_REASON"
assert_has "…naming why" "not a message id" "$RUN_OUT"
assert_eq "…and nothing was staged" "" "$(cat "$STUB_OUT/revert")"
reset_store; echo idle > "$STUB_OUT/mode"
run_driver revertshort 20 '[{"type":"send","text":"a"},{"type":"wait_turn"},{"type":"revert","turns":2}]'
assert_eq "fewer user turns than asked: refused" "nonzero(2)" "$RUN_REASON"
assert_has "…saying there are fewer" "has fewer than 2 user message(s)" "$RUN_OUT"
assert_eq "…and nothing was staged" "" "$(cat "$STUB_OUT/revert")"
reset_store
run_driver revertturns 20 '[{"type":"send","text":"a"},{"type":"wait_turn"},{"type":"revert","turns":"x"}]'
assert_eq "a non-numeric turn count: refused" "nonzero(2)" "$RUN_REASON"
assert_has "…naming why" "turns must be a positive integer" "$RUN_OUT"

section await_tool "await_tool: the session's own running tool"
reset_store; echo tool > "$STUB_OUT/mode"
run_driver tool 20 '[{"type":"send","text":"a"},{"type":"await_tool","tool":"shell"}]'
assert_eq "exit reason ok" "ok" "$RUN_REASON"
assert_has "…once its shell tool runs" "ses_stub1 is running its shell tool" "$RUN_OUT"
reset_store; echo idle > "$STUB_OUT/mode"
run_driver notool 3 '[{"type":"send","text":"a"},{"type":"wait_turn"},{"type":"await_tool","tool":"shell"}]'
assert_eq "no such tool by the deadline: timeout" "timeout" "$RUN_REASON"
assert_has "…from the await itself" "ses_stub1 ran no shell tool by the deadline" "$RUN_OUT"
reset_store
run_driver toolname 20 '[{"type":"send","text":"a"},{"type":"wait_turn"},{"type":"await_tool"}]'
assert_eq "await_tool without a tool name: refused" "nonzero(2)" "$RUN_REASON"

section background "background"
reset_store; echo idle > "$STUB_OUT/mode"
run_driver background 20 '[{"type":"send","text":"a"},{"type":"background"},{"type":"wait_turn"}]'
assert_eq "exit reason ok" "ok" "$RUN_REASON"
assert_eq "…POSTed to the active session's background endpoint" "1" "$(grep -cx '/api/session/ses_stub1/background' "$STUB_OUT/posted")"

section await_child "await_child: an observed child, not a sleep"
reset_store; echo child > "$STUB_OUT/mode"
run_driver child 20 '[{"type":"send","text":"a"},{"type":"await_child","tool":"shell"}]'
assert_eq "exit reason ok" "ok" "$RUN_REASON"
assert_has "…once the child's shell tool is running" "child session ses_child1 of ses_stub1 is running its shell tool" "$RUN_OUT"
reset_store; echo badchild > "$STUB_OUT/mode"
run_driver badchild 20 '[{"type":"send","text":"a"},{"type":"await_child","tool":"shell"}]'
assert_eq "a child id that is not a session id: refused" "nonzero(2)" "$RUN_REASON"
assert_has "…naming why" "not a session id" "$RUN_OUT"
reset_store; echo idle > "$STUB_OUT/mode"
run_driver nochild 3 '[{"type":"send","text":"a"},{"type":"wait_turn"},{"type":"await_child"}]'
assert_eq "no child by the deadline: timeout" "timeout" "$RUN_REASON"
assert_has "…from the await itself" "await_child: no child of ses_stub1 by the deadline" "$RUN_OUT"

section compact "compact: a manual compaction, then its own idle (#2109 P5b-2)"
reset_store; echo idle > "$STUB_OUT/mode"
run_driver compact 20 '[{"type":"send","text":"a"},{"type":"wait_turn"},{"type":"compact"},{"type":"wait_turn"}]'
assert_eq "exit reason ok" "ok" "$RUN_REASON"
assert_eq "…POSTed to the active session's compact endpoint" "1" "$(grep -cx '/api/session/ses_stub1/compact' "$STUB_OUT/posted")"
assert_has "…and waited for the compaction's own idle" "idle_outcome=succeeded (time_idle" "$(grep -A1 'compact sent' <<<"$RUN_OUT")"
assert_eq "…whose rows are in the export" "user assistant idle compaction idle" "$(jq -r .type "$(cat "$RUN_STAGING/transcript.path" 2>/dev/null)" 2>/dev/null | tr '\n' ' ' | sed 's/ $//')"
reset_store; echo nocompact > "$STUB_OUT/mode"
run_driver nocompact 5 '[{"type":"send","text":"a"},{"type":"compact"},{"type":"wait_turn"}]'
assert_eq "a compaction the service refuses: not recorded as ok" "nonzero(2)" "$RUN_REASON"
assert_has "…naming why" "compaction of ses_stub1 failed" "$RUN_OUT"

section shell "shell: a harmless user shell escape, waited on in the store (#2109 P5b-2)"
reset_store; echo idle > "$STUB_OUT/mode"
run_driver shell 20 '[{"type":"send","text":"a"},{"type":"wait_turn"},{"type":"shell","command":"echo hello"},{"type":"send","text":"b"},{"type":"wait_turn"}]'
assert_eq "exit reason ok" "ok" "$RUN_REASON"
assert_eq "…the command went in the body" '/api/session/ses_stub1/shell {"command":"echo hello"}' "$(grep '/shell ' "$STUB_OUT/bodies")"
assert_has "…and the step waited for its row to leave running" "shell escape 'echo hello' on ses_stub1 finished (1 shell row(s) done)" "$RUN_OUT"
for bad in 'rm -rf x' 'echo ../x' 'echo ..' 'echo a;b' 'echo $(id)' 'cat /etc/passwd' 'echo a/b' 'echo  x' ''; do
  reset_store
  run_driver "badshell" 20 "$(jq -nc --arg c "$bad" '[{"type":"send","text":"a"},{"type":"wait_turn"},{"type":"shell","command":$c}]')"
  assert_eq "shell '$bad': refused" "nonzero(2)" "$RUN_REASON"
  assert_eq "…and nothing reached the shell endpoint" "0" "$(grep -c '/shell$' "$STUB_OUT/posted")"
done
reset_store; echo shellstuck > "$STUB_OUT/mode"
run_driver shellstuck 4 '[{"type":"shell","command":"echo hello"}]'
assert_eq "a shell row that never leaves running: timeout" "timeout" "$RUN_REASON"
assert_has "…from the shell step itself" "never left running by the deadline" "$RUN_OUT"

section midturn "mid_turn_send: only while a turn is in flight, with the API's delivery (#2109 P5b-2)"
reset_store; echo queue > "$STUB_OUT/mode"
run_driver midturn 20 '[{"type":"send","text":"long"},{"type":"mid_turn_send","text":"also this"},{"type":"wait_turn"}]'
assert_eq "exit reason ok" "ok" "$RUN_REASON"
assert_eq "…the second prompt went with delivery queue" '/api/session/ses_stub1/prompt {"text":"also this","delivery":"queue"}' "$(sed -n 2p "$STUB_OUT/bodies")"
assert_eq "…and the turn's one idle ended it" "user user assistant idle" "$(jq -r .type "$(cat "$RUN_STAGING/transcript.path" 2>/dev/null)" 2>/dev/null | tr '\n' ' ' | sed 's/ $//')"
reset_store; echo queue > "$STUB_OUT/mode"
run_driver steer 20 '[{"type":"send","text":"long"},{"type":"mid_turn_send","text":"also this","delivery":"steer"},{"type":"wait_turn"}]'
assert_eq "delivery steer is passed through" '/api/session/ses_stub1/prompt {"text":"also this","delivery":"steer"}' "$(sed -n 2p "$STUB_OUT/bodies")"
reset_store; echo queue > "$STUB_OUT/mode"
run_driver baddelivery 20 '[{"type":"send","text":"long"},{"type":"mid_turn_send","text":"x","delivery":"later"}]'
assert_eq "a delivery the API does not take: refused" "nonzero(2)" "$RUN_REASON"
assert_eq "…and only the first prompt was sent" "1" "$(grep -c '/prompt ' "$STUB_OUT/bodies")"
reset_store; echo idle > "$STUB_OUT/mode"
run_driver notinflight 20 '[{"type":"send","text":"a"},{"type":"wait_turn"},{"type":"mid_turn_send","text":"late"}]'
assert_eq "a mid-turn send after the turn ended: refused" "nonzero(2)" "$RUN_REASON"
assert_has "…naming why" "no turn of ses_stub1 is in flight" "$RUN_OUT"
assert_eq "…and it was not sent" "1" "$(grep -c '/prompt ' "$STUB_OUT/bodies")"

section window "the service window: exit_clean / sigkill"
reset_store; echo idle > "$STUB_OUT/mode"
run_driver nowindow 20 '[{"type":"send","text":"a"},{"type":"wait_turn"},{"type":"sigkill"}]'
assert_eq "sigkill without a service window: refused" "nonzero(2)" "$RUN_REASON"
assert_has "…naming the opt-in" "OC2_SERVICE_WINDOW=1" "$RUN_OUT"
assert_eq "…before any session or turn" "0" "$(created_count)"
kill -0 "$SVC_SLEEP" 2>/dev/null && got=alive || got=dead
assert_eq "…and the service was not signalled" "alive" "$got"
reset_store; touch "$TMP/ps.drift"
run_driver drift 20 '[{"type":"send","text":"a"},{"type":"wait_turn"},{"type":"sigkill"}]' OC2_SERVICE_WINDOW=1
assert_eq "a pid that stopped being the service: refused" "nonzero(2)" "$RUN_REASON"
assert_has "…naming why" "is no longer an opencode 'serve --service'" "$RUN_OUT"
kill -0 "$SVC_SLEEP" 2>/dev/null && got=alive || got=dead
assert_eq "…and it was not signalled" "alive" "$got"
reset_store
run_driver window 20 '[{"type":"send","text":"a"},{"type":"wait_turn"},{"type":"start_session"},{"type":"sigkill"},{"type":"sleep","seconds":1}]' OC2_SERVICE_WINDOW=1
assert_eq "with the window: exit reason ok" "ok" "$RUN_REASON"
kill -0 "$SVC_SLEEP" 2>/dev/null && got=alive || got=dead
assert_eq "…the service is gone" "dead" "$got"
assert_has "…every slot was exported before the signal" "exported before SIGKILL" "$RUN_OUT"
assert_eq "…slot 1's export holds its turn" "3" "$(grep -c . "$(sed -n 1p "$RUN_STAGING/transcript.paths" 2>/dev/null)" 2>/dev/null)"
assert_eq "…and both slots are in the contract" "2" "$(grep -c . "$RUN_STAGING/session.uuids" 2>/dev/null)"
# A new "service" for the cases after this one.
# A driver that did not signal it must not hang this suite on `wait`.
kill "$SVC_SLEEP" 2>/dev/null; wait "$SVC_SLEEP" 2>/dev/null
sleep 600 & SVC_SLEEP=$!
one_service; write_service_json
reset_store
run_driver term 20 '[{"type":"send","text":"a"},{"type":"wait_turn"},{"type":"exit_clean"}]' OC2_SERVICE_WINDOW=1
assert_eq "exit_clean with the window: exit reason ok" "ok" "$RUN_REASON"
kill -0 "$SVC_SLEEP" 2>/dev/null && got=alive || got=dead
assert_eq "…the service got SIGTERM and is gone" "dead" "$got"
assert_has "…after the export" "exported before SIGTERM" "$RUN_OUT"
# A driver that did not signal it must not hang this suite on `wait`.
kill "$SVC_SLEEP" 2>/dev/null; wait "$SVC_SLEEP" 2>/dev/null
sleep 600 & SVC_SLEEP=$!
one_service; write_service_json

section xtrace "the driver never traces itself"
if grep -nE '^[^#]*set -[a-z]*x|set -o xtrace' "$DRIVER" "$ROOT/replaydata/agents/opencode-v2/service-api.sh"; then
  fail "no xtrace" "a traced driver echoes the credential's curl config"
else
  pass "no xtrace"
fi

echo ""
if [[ -n "$ONLY" ]] && (( SECTIONS_RUN == 0 )); then
  echo "opencode-v2-driver_test: no section named '$ONLY' (OC2_DRIVER_TEST_ONLY) — nothing was checked" >&2
  exit 1
fi
if (( fails == 0 )); then
  echo "opencode-v2-driver_test: ALL PASS"
  exit 0
fi
echo "opencode-v2-driver_test: $fails FAILED" >&2
exit 1
