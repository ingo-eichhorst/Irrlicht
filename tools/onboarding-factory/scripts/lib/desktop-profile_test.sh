#!/usr/bin/env bash
set -uo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
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
for mutation in \
  'codex|0|{"prompt":"ok"}' \
  'claudecode|1|{"prompt":"ok"}' \
  'claudecode|0|{"script":[{"type":"send-text"}]}' \
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

echo "== loopback allocation is dynamic and checked =="
address="$(desktop_choose_loopback_address)"
desktop_require_free_loopback_address "$address"
assert_eq "selected address is free loopback" 0 "$?"

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
