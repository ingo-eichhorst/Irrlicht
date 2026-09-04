#!/usr/bin/env bash
set -uo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
SCRIPTS_DIR="$(cd "$DIR/.." && pwd)"
# shellcheck source=desktop-profile.sh
source "$DIR/desktop-profile.sh"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
fails=0
pass() { echo "  PASS: $1"; }
fail() { echo "  FAIL: $1 — $2"; fails=$((fails + 1)); }
assert_eq() { [[ "$2" == "$3" ]] && pass "$1" || fail "$1" "expected [$2], got [$3]"; }

echo "== Desktop cells stay inside the basic prompt boundary =="
desktop_profile_validate_cell desktop-local claudecode 0 '{"prompt":"ok","settings":{}}'
assert_eq "basic Claude Code cell passes" 0 "$?"
desktop_profile_validate_cell desktop-local claudecode 0 '{"script":[{"type":"send","text":"ok"}],"settings":{}}'
assert_eq "a recipe cell reaches the recipe lint (#1888)" 0 "$?"
for mutation in \
  'codex|0|{"prompt":"ok"}' \
  'claudecode|1|{"prompt":"ok"}' \
  'claudecode|0|{"prompt":"ok","settings":{"model":"x"}}' \
  'claudecode|0|{"prompt":"ok","env":{"TOKEN":"x"}}' \
  'claudecode|0|{"prompt":"ok","mock":{"package":"./x"}}'; do
  IFS='|' read -r adapter attach json <<<"$mutation"
  if desktop_profile_validate_cell desktop-local "$adapter" "$attach" "$json" 2>/dev/null; then
    fail "unsafe cell is refused" "$mutation passed"
  else
    pass "unsafe cell is refused ($adapter)"
  fi
done

echo "== recipe → Desktop control lint (#1888) =="
# The lint reads the cell's recipe through shard-lib and the driver's scraped
# declaration. Point both at fixtures so this suite never depends on what the
# real claudecode recipes happen to say today.
export IR_SCENARIOS_FILE="$TMP/replaydata/scenarios.json"
export IR_AGENTS_DIR="$TMP/replaydata/agents"
mkdir -p "$(dirname "$IR_SCENARIOS_FILE")"
printf '{"meta":{},"scenarios":[]}\n' > "$IR_SCENARIOS_FILE"
desktop_shard() {
  local name="$1" recipe="$2"
  local cell="$IR_AGENTS_DIR/claudecode/scenarios/$name"
  mkdir -p "$cell"
  printf '{"scenario_id":"%s","details":{"recipe":%s}}\n' "$name" "$recipe" > "$cell/metadata.json"
}
desktop_shard runnable-cell  '{"script":[{"type":"send","text":"hi"},{"type":"wait_turn"},{"type":"sleep","seconds":2}]}'
desktop_shard gap-cell       '{"script":[{"type":"send","text":"hi"},{"type":"reset_session"},{"type":"session","session":1}]}'
desktop_shard prompt-cell    '{"prompt":"hi"}'
desktop_shard two-session    '{"script":[{"type":"send","text":"hi"},{"type":"start_session"}]}'

REAL_DESKTOP_DRIVER="$(cd "$DIR/../../../.." && pwd)/replaydata/agents/claudecode/driver-desktop.sh"
[[ -f "$REAL_DESKTOP_DRIVER" ]] ||
  fail "the real driver-desktop.sh is readable" "not found at $REAL_DESKTOP_DRIVER"

desktop_recipe_gaps "$REAL_DESKTOP_DRIVER" runnable-cell claudecode >/dev/null
assert_eq "a recipe inside the Desktop grammar passes" 0 "$?"
desktop_recipe_gaps "$REAL_DESKTOP_DRIVER" prompt-cell claudecode >/dev/null
assert_eq "a prompt cell has nothing to lint" 0 "$?"

GAPS="$(desktop_recipe_gaps "$REAL_DESKTOP_DRIVER" gap-cell claudecode)"
assert_eq "an undrivable recipe is refused" 1 "$?"
case "$GAPS" in
  *"reset_session"*"session-reset"*) pass "the refusal names reset_session's missing control" ;;
  *) fail "the refusal names reset_session's missing control" "got [$GAPS]" ;;
esac
case "$GAPS" in
  *"session"*"session-list-row"*) pass "the refusal names session's missing control" ;;
  *) fail "the refusal names session's missing control" "got [$GAPS]" ;;
esac

REPORT="$(desktop_report_recipe_gaps "$REAL_DESKTOP_DRIVER" gap-cell claudecode gap-cell 2>&1)"
assert_eq "the reported verdict exits 4" 4 "$?"
case "$REPORT" in
  *"not runnable through Desktop"*) pass "the verdict carries the #1888 phrase" ;;
  *) fail "the verdict carries the #1888 phrase" "got [$REPORT]" ;;
esac

# Mutation: a driver that declares no DRIVE_ELICITS scrapes to an empty set.
# "I could not look" must never read as "nothing disagreed".
printf '#!/usr/bin/env bash\necho hi\n' > "$TMP/no-declaration.sh"
desktop_recipe_gaps "$TMP/no-declaration.sh" runnable-cell claudecode >/dev/null 2>&1
assert_eq "a driver with no declaration refuses loudly" 2 "$?"
desktop_recipe_gaps "$TMP/does-not-exist.sh" runnable-cell claudecode >/dev/null 2>&1
assert_eq "a missing driver refuses loudly" 2 "$?"

# Mutation: DRIVE_ELICITS widened to accept a step the Go driver cannot drive.
# The lint would then wave a slash recipe through to a live Desktop run.
sed 's/^DRIVE_ELICITS=.*/DRIVE_ELICITS="archive interrupt keys mode model send session sleep slash start_session wait_turn reset_session"/' \
  "$REAL_DESKTOP_DRIVER" > "$TMP/widened-driver.sh"
desktop_recipe_gaps "$TMP/widened-driver.sh" gap-cell claudecode >/dev/null 2>&1
assert_eq "a widened declaration stops refusing (mutation is detected by contract_test.go)" 0 "$?"

# The rig stages one session's evidence; a recipe asking for two is refused
# with its own reason rather than half-recorded.
assert_eq "session count includes the implicit first session" 2 \
  "$(desktop_recipe_sessions two-session claudecode)"
desktop_recipe_rig_gaps two-session claudecode 2>/dev/null
assert_eq "a two-session recipe exceeds this rig" 1 "$?"
desktop_recipe_rig_gaps runnable-cell claudecode
assert_eq "a one-session recipe fits this rig" 0 "$?"
desktop_report_recipe_gaps "$REAL_DESKTOP_DRIVER" two-session claudecode two-session >/dev/null 2>&1
assert_eq "the two-session verdict exits 4" 4 "$?"

unset IR_SCENARIOS_FILE IR_AGENTS_DIR

echo "== loopback allocation is dynamic and checked =="
address="$(desktop_choose_loopback_address)"
desktop_require_free_loopback_address "$address"
assert_eq "selected address is free loopback" 0 "$?"

# A BUSY port must be refused. The check had no such fixture, so deleting it
# entirely left this suite green.
busy_port=""
python3 - "$TMP/busy.port" <<'PYBUSY' &
import socket, sys, time
s = socket.socket(); s.bind(("127.0.0.1", 0)); s.listen(1)
open(sys.argv[1], "w").write(str(s.getsockname()[1]))
time.sleep(30)
PYBUSY
BUSY_PID=$!
for _ in $(seq 1 200); do
  [[ -s "$TMP/busy.port" ]] && break
  sleep 0.01
done
busy_port="$(cat "$TMP/busy.port" 2>/dev/null || true)"
if [[ "$busy_port" =~ ^[0-9]+$ ]]; then
  desktop_require_free_loopback_address "127.0.0.1:$busy_port" 2>/dev/null
  assert_eq "a busy loopback port is refused" 1 "$?"
else
  # Could not stand the listener up. That is a failure, not a pass.
  fail "a busy loopback port is refused" "a bound port" "no listener came up"
fi
kill "$BUSY_PID" 2>/dev/null || true
wait "$BUSY_PID" 2>/dev/null || true

# A probe that cannot look must refuse, not report the port free. Shadow lsof
# with a stub that answers nothing, exactly as a denied or missing lsof does.
lsof() { return 1; }
desktop_require_free_loopback_address "$address" 2>/dev/null
assert_eq "a blind port probe refuses" 1 "$?"
unset -f lsof
desktop_require_free_loopback_address "$address"
assert_eq "the real probe still accepts a free port" 0 "$?"

echo "== clone-wide lock covers setup in linked worktrees =="
lock_repo="$TMP/lock-repo"
linked_repo="$TMP/lock-linked"
mkdir "$lock_repo"
git -C "$lock_repo" init -b main >/dev/null
git -C "$lock_repo" config user.email desktop-driver-test@example.invalid
git -C "$lock_repo" config user.name "Desktop Driver Test"
printf 'seed\n' > "$lock_repo/seed"
git -C "$lock_repo" add seed
git -C "$lock_repo" commit -m seed >/dev/null
git -C "$lock_repo" worktree add --detach "$linked_repo" >/dev/null
first_ready="$TMP/first-lock-ready"
release_first="$TMP/release-first-lock"
(
  desktop_acquire_clone_lock "$lock_repo" || exit 1
  : > "$first_ready"
  for _ in $(seq 1 100); do
    [[ -e "$release_first" ]] && exit 0
    sleep 0.02
  done
  exit 2
) &
first_pid=$!
for _ in $(seq 1 100); do
  [[ -e "$first_ready" ]] && break
  sleep 0.02
done
if [[ -e "$first_ready" ]]; then
  pass "first worktree holds the lock"
else
  fail "first worktree holds the lock" "acquisition deadline expired"
fi
(
  desktop_acquire_clone_lock "$linked_repo" || exit 1
  : > "$TMP/second.snapshot"
  : > "$TMP/second.daemon"
) 2>"$TMP/second-lock.err"
second_rc=$?
assert_eq "second worktree is refused" "1" "$second_rc"
[[ -e "$TMP/second.snapshot" || -e "$TMP/second.daemon" ]] && got="reached-setup" || got="blocked-before-setup"
assert_eq "second worktree cannot reach snapshot or daemon" "blocked-before-setup" "$got"
: > "$release_first"
wait "$first_pid"
assert_eq "first lock holder exits after release" "0" "$?"
run_cell="$DIR/../run-cell.sh"
lock_line="$(awk '/desktop_acquire_clone_lock/{print NR; exit}' "$run_cell")"
spawn_line="$(awk '/spawn_record_daemon .*DAEMON/{print NR; exit}' "$run_cell")"
if [[ -n "$lock_line" && -n "$spawn_line" && "$lock_line" -lt "$spawn_line" ]]; then
  got="before-setup"
else
  got="missing-or-late"
fi
assert_eq "run-cell acquires the lock before daemon setup" "before-setup" "$got"

echo "== identity-field and full-session evidence is staged and joined =="
source_dir="$TMP/source"
destination="$TMP/destination"
mkdir -p "$source_dir"
sid="cli-1"
cwd="/repo/.build/cell/cwd"
printf '%s\n' "{\"sessionId\":\"local_1\",\"cliSessionId\":\"$sid\",\"cwd\":\"$cwd\"}" > "$source_dir/desktop-registry.json"
printf '%s\n' "{\"selected_environment\":\"Local\",\"requested_workspace\":\"$cwd\"}" > "$source_dir/desktop-environment.json"
printf '%s\n' '{"pid":123,"command":"/Applications/Claude.app/claude"}' > "$source_dir/process.json"
printf '%s\n' "{\"session_id\":\"$sid\",\"cwd\":\"$cwd\",\"pid\":123,\"launcher\":{\"host_bundle_id\":\"com.anthropic.claudefordesktop\"}}" > "$source_dir/irrlicht-session.json"
printf '%s\n' "{\"sessionId\":\"$sid\",\"cwd\":\"$cwd\",\"entrypoint\":\"claude-desktop\"}" > "$source_dir/transcript.jsonl"
recording="$TMP/recording.jsonl"
hook_line="{ \"kind\": \"hook_received\", \"session_id\": \"$sid\", \"hook_name\": \"Stop\", \"seq\": 7 }"
printf '%s\n' "$hook_line" > "$recording"
desktop_stage_evidence "$source_dir" "$recording" "$sid" "$cwd" "$destination"
assert_eq "valid evidence stages" 0 "$?"
assert_eq "hook lifecycle row is preserved byte-for-byte" "$hook_line" "$(cat "$destination/hooks.jsonl")"

echo "== evidence mutations fail loudly =="
cp "$source_dir/transcript.jsonl" "$TMP/transcript.good"
printf '%s\n' "{\"sessionId\":\"$sid\",\"cwd\":\"$cwd\",\"entrypoint\":\"sdk-cli\"}" > "$source_dir/transcript.jsonl"
if desktop_stage_evidence "$source_dir" "$recording" "$sid" "$cwd" "$TMP/mutated" 2>/dev/null; then
  fail "sdk-cli mutation is refused" "invalid entrypoint passed"
else
  pass "sdk-cli mutation is refused"
fi
cp "$TMP/transcript.good" "$source_dir/transcript.jsonl"
printf '%s\n' "{\"kind\":\"hook_received\",\"session_id\":\"$sid\",\"hook_name\":\"\"}" > "$recording"
if desktop_stage_evidence "$source_dir" "$recording" "$sid" "$cwd" "$TMP/no-hook-name" 2>/dev/null; then
  fail "blank hook-name mutation is refused" "invalid receipt passed"
else
  pass "blank hook-name mutation is refused"
fi
printf '%s\n' "$hook_line" > "$recording"
ln -sf "$TMP/transcript.good" "$source_dir/transcript.jsonl"
if desktop_stage_evidence "$source_dir" "$recording" "$sid" "$cwd" "$TMP/symlink" 2>/dev/null; then
  fail "symlink mutation is refused" "symlink evidence passed"
else
  pass "symlink mutation is refused"
fi
rm -f "$source_dir/transcript.jsonl"
cp "$TMP/transcript.good" "$source_dir/transcript.jsonl"
jq '.envScopeId = "builtin_local"' "$source_dir/desktop-registry.json" > "$TMP/scoped.json"
mv "$TMP/scoped.json" "$source_dir/desktop-registry.json"
if desktop_stage_evidence "$source_dir" "$recording" "$sid" "$cwd" "$TMP/scoped" 2>/dev/null; then
  fail "non-Local scope mutation is refused" "builtin_local passed"
else
  pass "non-Local scope mutation is refused"
fi
jq 'del(.envScopeId)' "$source_dir/desktop-registry.json" > "$TMP/unscoped.json"
mv "$TMP/unscoped.json" "$source_dir/desktop-registry.json"
outside="$TMP/outside"
mkdir "$outside"
ln -s "$outside" "$TMP/escaped-destination"
if desktop_stage_evidence "$source_dir" "$recording" "$sid" "$cwd" "$TMP/escaped-destination" 2>/dev/null; then
  fail "destination symlink mutation is refused" "evidence escaped"
else
  pass "destination symlink mutation is refused"
fi
mkdir "$TMP/hook-target"
printf 'outside bytes\n' > "$outside/hooks.jsonl"
ln -s "$outside/hooks.jsonl" "$TMP/hook-target/hooks.jsonl"
if desktop_stage_evidence "$source_dir" "$recording" "$sid" "$cwd" "$TMP/hook-target" 2>/dev/null; then
  fail "hook target symlink is refused" "hooks.jsonl symlink passed"
else
  pass "hook target symlink is refused"
fi
assert_eq "hook target bytes stay unchanged" "outside bytes" "$(cat "$outside/hooks.jsonl")"

echo "== the Desktop driver gets a scaled timeout, the manifest keeps the scenario's =="
# A scenario's timeout_seconds was budgeted for a headless CLI turn, and the Go
# driver gives each step a third of it. 2-1_basic-turn declares 60s, so the
# composer step got 20s — and a measured idle composer on 1.46388.4 needs ~8s,
# more under a recording daemon. The scenario's own number must NOT change: it
# is what cli-local is measured against and what the manifest records.
scale_block="$(awk '/^# BEGIN desktop_driver_timeout$/{f=1;next} /^# END desktop_driver_timeout$/{f=0} f' \
  "$SCRIPTS_DIR/run-cell.sh")"
if [[ -z "$scale_block" ]]; then
  fail "the desktop timeout scaling was found" "a DESKTOP_TIMEOUT_FACTOR block" "nothing matched"
else
  pass "the desktop timeout scaling was found"
  for profile in cli-local desktop-local; do
    got="$(TIMEOUT_S=60 EXECUTION_PROFILE="$profile" bash -c \
      'set -u; '"$scale_block"'; printf "%s" "$DRIVER_TIMEOUT_S"')"
    case "$profile" in
      cli-local)     assert_eq "cli-local keeps the scenario timeout" "60" "$got" ;;
      desktop-local) assert_eq "desktop-local scales it" "240" "$got" ;;
    esac
  done
fi
# The driver must receive the SCALED value and the manifest the scenario's own.
if grep -Fq '"$DRIVER" "$STAGING" "$UUID" "$DRIVER_TIMEOUT_S"' "$SCRIPTS_DIR/run-cell.sh"; then
  got=scaled
else
  got=unscaled
fi
assert_eq "the driver is invoked with the scaled timeout" "scaled" "$got"
if grep -Fq -- '--argjson timeout_seconds "$TIMEOUT_S"' "$SCRIPTS_DIR/run-cell.sh"; then
  got=scenario
else
  got=drifted
fi
assert_eq "the manifest still records the scenario's own timeout" "scenario" "$got"

echo "== execution-results uses the fixed #1886 schema =="
desktop_write_execution_results "$TMP/execution-results.json" basic-turn rec-1 observed-passing
got="$(jq -c . "$TMP/execution-results.json")"
want='{"schema_version":1,"results":[{"scenario_id":"basic-turn","execution_profile":"desktop-local","outcome":"observed-passing","recording":"rec-1","evidence":{"desktop_registry":"desktop-registry.json","transcript":"transcript.jsonl","hooks":"hooks.jsonl","process":"process.json","irrlicht_session":"irrlicht-session.json","environment":"desktop-environment.json"}}]}'
assert_eq "execution-results schema" "$want" "$got"
if desktop_write_execution_results "$TMP/failure.json" basic-turn rec-1 observed-failure ""; then
  fail "observed failure requires a reason" "blank reason passed"
else
  pass "observed failure requires a reason"
fi

echo ""
if [[ "$fails" -eq 0 ]]; then
  echo "desktop-profile_test: ALL PASS"
else
  echo "desktop-profile_test: $fails FAILURE(S)" >&2
  exit 1
fi
