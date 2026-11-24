#!/usr/bin/env bash
# vm-pool_test.sh — tests examples/vm-pool/pool.sh without a container
# runtime. A fake `docker` answers from a table, and the worker list pool.sh
# writes is decoded by the daemon's own worker-list parser — through the
# replay harness (WORKERLIST_FIXTURES_DIR) — instead of by a second copy of
# the schema kept here.
#
# What it pins:
#   1. a running task whose guest says waiting is busy + "waiting";
#   2. a task that stopped while its state file still says waiting is NOT
#      waiting — waiting on a free slot would make the whole file unreadable;
#   3. a killed guest (no `ended`) falls back to the backend's exit code and
#      finish time, cut to whole seconds;
#   4. a malformed heartbeat is dropped rather than passed on;
#   5. `--env NAME` passes only the name — the value never reaches argv — and
#      guest/ is mounted read-only, on both backends;
#   6. `down` removes only a worker list this script wrote;
#   7. the Apple `container` backend reads the run state from where
#      `container inspect` puts it (`.[0].status.state`), so a running task
#      is busy and a stopped one is free with its last run.
# tools/lib/vm-pool-mutations_test.sh holds the mutations each rule was seen
# red under.

set -uo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$DIR/../.." && pwd)"
POOL="$REPO_ROOT/examples/vm-pool/pool.sh"

need() { command -v "$1" >/dev/null 2>&1 || { echo "FAIL: vm-pool_test — $1 not found" >&2; exit 1; }; }
need jq
need go

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
fails=0
ok() { echo "ok  $1"; }
bad() { echo "FAIL: $1"; fails=$((fails + 1)); }

# ── fake docker ──────────────────────────────────────────────────────────────
# inspect answers from $FAKE_DOCKER_TABLE/<name>.running|.finished (a missing
# file = no such container); run records its argv, one argument per line.
cat > "$tmp/docker" <<'EOF'
#!/usr/bin/env bash
case "$1" in
  inspect)
    case "$3" in
      '{{.State.Running}}') f="$FAKE_DOCKER_TABLE/$4.running" ;;
      '{{.State.FinishedAt}} {{.State.ExitCode}}') f="$FAKE_DOCKER_TABLE/$4.finished" ;;
      *) echo "fake docker: unsupported format $3" >&2; exit 2 ;;
    esac
    [ -f "$f" ] || exit 1
    cat "$f"
    ;;
  run) printf '%s\n' "$@" > "$FAKE_DOCKER_TABLE/run.argv" ;;
  rm) ;;
  *) echo "fake docker: unsupported $1" >&2; exit 2 ;;
esac
EOF
chmod +x "$tmp/docker"

# ── fake Apple container CLI ─────────────────────────────────────────────────
# inspect prints the shape `container inspect` printed in container 1.7.1
# (recorded on macOS 27.0.1, 2026-11-24): an array whose run state is
# .status.state ("running" or "stopped"), with no exit code or end time. A
# missing name exits 1, as the real CLI does. run records its argv.
cat > "$tmp/container" <<'EOF'
#!/usr/bin/env bash
case "$1" in
  inspect)
    f="$FAKE_DOCKER_TABLE/$2.ct-state"
    [ -f "$f" ] || { echo "Error: container not found: $2" >&2; exit 1; }
    printf '[{"id":"%s","configuration":{},"status":{"networks":[],"startedDate":"2026-11-24T18:42:50Z","state":"%s"}}]\n' "$2" "$(cat "$f")"
    ;;
  run) printf '%s\n' "$@" > "$FAKE_DOCKER_TABLE/run-container.argv" ;;
  stop | delete) ;;
  *) echo "fake container: unsupported $1" >&2; exit 2 ;;
esac
EOF
chmod +x "$tmp/container"
table="$tmp/table"
mkdir -p "$table"

export VM_POOL_DIR="$tmp/pool" VM_POOL_DOCKER="$tmp/docker" FAKE_DOCKER_TABLE="$table"
export VM_POOL_CONTAINER_CLI="$tmp/container"
export IRRLICHT_WORKERLIST_FILE="$tmp/out/workerlist.json" VM_POOL_NAME="Test pool"
now="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

# task <team> <task> <docker-running: true|false|missing> [file=value...]
task() {
  local team="$1" name="$2" running="$3" kv
  shift 3
  mkdir -p "$VM_POOL_DIR/$team/$name"
  printf 'docker\n' > "$VM_POOL_DIR/$team/$name/backend"
  [ "$running" = missing ] || printf '%s\n' "$running" > "$table/irrlicht-pool-$team-$name.running"
  for kv in "$@"; do printf '%s\n' "${kv#*=}" > "$VM_POOL_DIR/$team/$name/${kv%%=*}"; done
}
task lead-A t1 true state=waiting "heartbeat=$now"
task lead-A t2 true state=busy "heartbeat=$now"
task lead-A t3 false "state=done" "ended=$now" exit=2
task lead-A t7 false state=waiting "heartbeat=$now"
task lead-B t4 false state=busy
printf '2026-11-21T10:00:05.123456789Z 137\n' > "$table/irrlicht-pool-lead-B-t4.finished"
task lead-B t5 true state=busy heartbeat=garbage
task lead-B t6 missing state=busy

# ctask <team> <task> <container-state: running|stopped|missing> [file=value...]
ctask() {
  local team="$1" name="$2" state="$3" kv
  shift 3
  mkdir -p "$VM_POOL_DIR/$team/$name"
  printf 'container\n' > "$VM_POOL_DIR/$team/$name/backend"
  [ "$state" = missing ] || printf '%s\n' "$state" > "$table/irrlicht-pool-$team-$name.ct-state"
  for kv in "$@"; do printf '%s\n' "${kv#*=}" > "$VM_POOL_DIR/$team/$name/${kv%%=*}"; done
}
ctask lead-D d1 running state=waiting "heartbeat=$now"
ctask lead-D d2 stopped state=done "ended=$now" exit=3

doc="$("$POOL" status)" || { bad "pool.sh status failed"; doc='{}'; }
w() { jq -c --arg n "$1" '[.teams[].workers[] | select(.name == $n)][0]' <<<"$doc"; }

[ "$(w t1 | jq -c '{busy, state}')" = '{"busy":true,"state":"waiting"}' ] \
  && ok "a running task whose guest says waiting is busy and waiting" \
  || bad "running+waiting task: got $(w t1)"
[ "$(w t7 | jq -c '{busy, state}')" = '{"busy":false,"state":null}' ] \
  && ok "a stopped task whose state file still says waiting is free and not waiting" \
  || bad "stopped task with a stale waiting file: got $(w t7)"
[ "$(w t4 | jq -c '.last_run')" = '{"end":"2026-11-21T10:00:05Z","exit":137}' ] \
  && ok "a killed guest falls back to the backend's exit code and finish time" \
  || bad "killed guest: got $(w t4)"
[ "$(w t5 | jq -c 'has("last_heartbeat")')" = false ] \
  && ok "a malformed heartbeat is dropped" \
  || bad "malformed heartbeat: got $(w t5)"
[ "$(w t6 | jq -c '{busy, last_run}')" = '{"busy":false,"last_run":null}' ] \
  && ok "a task with no container is free with no last run" \
  || bad "missing container: got $(w t6)"
[ "$(w d1 | jq -c '{busy, state}')" = '{"busy":true,"state":"waiting"}' ] \
  && ok "a running Apple container task is busy and waiting" \
  || bad "running Apple container task: got $(w d1)"
[ "$(w d2 | jq -c '{busy, last_run}')" = "$(jq -nc --arg now "$now" '{busy: false, last_run: {end: $now, exit: 3}}')" ] \
  && ok "a stopped Apple container task is free with its last run" \
  || bad "stopped Apple container task: got $(w d2)"

# ── the daemon's own parser accepts it and reads what we meant ───────────────
scen="$tmp/scenarios/pool-status"
mkdir -p "$scen/input/ticks/tick-001"
printf '%s\n' "$doc" > "$scen/input/ticks/tick-001/status.json"
jq -n --arg now "$now" '{
  description: "pool.sh status, decoded by the worker-list parser",
  ticks: [{now: $now}],
  verify: {
    name: "Test pool",
    teams: ["lead-A", "lead-B", "lead-D"],
    worker_states: ["waiting", "busy", "idle", "idle", "idle", "busy", "idle", "waiting", "idle"],
    last_exit: {t3: 2, t4: 137, d2: 3},
    stale_workers: []
  }}' > "$scen/scenario.json"
out="$(cd "$REPO_ROOT/core" && WORKERLIST_FIXTURES_DIR="$tmp/scenarios" \
  go test ./adapters/inbound/orchestrators/workerlist/ -run 'TestWorkerlistReplay$' -update-goldens -count=1 -v 2>&1)"
if grep -q -- '--- PASS: TestWorkerlistReplay/pool-status' <<<"$out"; then
  ok "the daemon's worker-list parser decodes the file and reads every slot as meant"
else
  bad "the daemon's parser rejected or misread pool.sh's worker list:"
  sed 's/^/      | /' <<<"$out"
fi

# ── run: credentials by name only, guest read-only ───────────────────────────
SECRET_FOR_TEST="s3cr3t-value" "$POOL" run lead-C t8 --env SECRET_FOR_TEST -- true >/dev/null 2>&1 \
  || bad "pool.sh run failed against the fake docker"
argv="$(cat "$table/run.argv" 2>/dev/null)"
if [ -z "$argv" ]; then
  bad "pool.sh run never called docker run"
elif grep -q 's3cr3t-value' <<<"$argv"; then
  bad "--env put the credential's VALUE on docker's argv"
elif ! grep -qx 'SECRET_FOR_TEST' <<<"$argv"; then
  bad "--env did not pass the name: $(tr '\n' ' ' <<<"$argv")"
else
  ok "--env passes a credential by name, never its value"
fi
grep -qx -- "$REPO_ROOT/examples/vm-pool/guest:/opt/irrlicht-guest:ro" <<<"$argv" \
  && ok "guest/ is mounted read-only" \
  || bad "guest/ is not mounted read-only: $(tr '\n' ' ' <<<"$argv")"
"$POOL" run lead-E t9 --backend container -- true >/dev/null 2>&1 \
  || bad "pool.sh run failed against the fake container CLI"
cargv="$(cat "$table/run-container.argv" 2>/dev/null)"
if [ -z "$cargv" ]; then
  bad "pool.sh run --backend container never called container run"
else
  grep -qx -- "$REPO_ROOT/examples/vm-pool/guest:/opt/irrlicht-guest:ro" <<<"$cargv" \
    && ok "guest/ is mounted read-only on the Apple container backend" \
    || bad "guest/ is not mounted read-only on the Apple container backend: $(tr '\n' ' ' <<<"$cargv")"
fi

# ── down: only our own worker list ───────────────────────────────────────────
mkdir -p "$tmp/out"
printf '{"schema": 1, "teams": []}\n' > "$IRRLICHT_WORKERLIST_FILE"
"$POOL" down >/dev/null 2>&1
[ -f "$IRRLICHT_WORKERLIST_FILE" ] \
  && ok "down keeps a worker list another writer owns" \
  || bad "down removed a worker list it did not write"
"$POOL" supervise --once && "$POOL" down >/dev/null 2>&1
[ ! -f "$IRRLICHT_WORKERLIST_FILE" ] \
  && ok "down removes the worker list it wrote" \
  || bad "down left its own worker list behind"

if [ "$fails" -gt 0 ]; then
  echo "vm-pool_test: $fails FAILED"
  exit 1
fi
echo "vm-pool_test: ALL PASS"
