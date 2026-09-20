#!/usr/bin/env bash
# Regression tests for native-child completion discovery used by the DSH driver.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
# shellcheck source=child-turn.sh
source "$ROOT/replaydata/agents/deepseek-harness/child-turn.sh"

failures=0
pass() { printf 'ok - %s\n' "$1"; }
fail() { printf 'not ok - %s\n' "$1" >&2; failures=$((failures + 1)); }

make_frame() { # <sessions> <directory-id> <header-parent> <completed>
  local sessions="$1" id="$2" parent="$3" completed="$4" dir
  dir="$sessions/$id"
  mkdir -p "$dir"
  {
    printf '{"type":"session","version":3,"id":"%s","origin":"subagent","parentSession":"%s"}\n' "$id" "$parent"
    if [[ "$completed" == true ]]; then
      printf '{"type":"turn/end","data":{"reason":{"kind":"completed"}}}\n'
    fi
  } | zstd -q -f -o "$dir/session.v3.jsonl.zstd"
}

make_marker() { # <sessions> <name>
  local sessions="$1" name="$2" marker
  marker="$sessions/$name"
  : > "$marker"
  printf '%s\n' "$marker"
}

test_success() {
  local root parent child marker got
  root="$(mktemp -d)"; parent='session-11111111-1111-4111-8111-111111111111'; child='22222222-2222-4222-8222-222222222222'
  marker="$(make_marker "$root" marker)"
  make_frame "$root" "$child" "$parent" true
  got="$(dsh_await_child_turn_end "$root" "$parent" "$marker" $(( $(date +%s) + 1 )))" || { fail success; rm -rf "$root"; return; }
  [[ "$got" == "$root/$child/session.v3.jsonl.zstd" ]] && pass success || fail success
  rm -rf "$root"
}

test_timeout() {
  local root parent child marker out status
  root="$(mktemp -d)"; parent='session-33333333-3333-4333-8333-333333333333'; child='44444444-4444-4444-8444-444444444444'
  marker="$(make_marker "$root" marker)"
  make_frame "$root" "$child" "$parent" false
  set +e
  out="$(dsh_await_child_turn_end "$root" "$parent" "$marker" "$(date +%s)" 2>&1)"; status=$?
  set -e
  [[ $status -ne 0 && "$out" == *'completed turn/end was not observed'* ]] && pass timeout || fail timeout
  rm -rf "$root"
}

test_wrong_parent() {
  local root parent child marker out status
  root="$(mktemp -d)"; parent='session-55555555-5555-4555-8555-555555555555'; child='66666666-6666-4666-8666-666666666666'
  marker="$(make_marker "$root" marker)"
  make_frame "$root" "$child" 'session-77777777-7777-4777-8777-777777777777' true
  set +e
  out="$(dsh_await_child_turn_end "$root" "$parent" "$marker" "$(date +%s)" 2>&1)"; status=$?
  set -e
  [[ $status -ne 0 && "$out" == *'different parent'* ]] && pass wrong-parent || fail wrong-parent
  rm -rf "$root"
}

test_stale_matching_child_is_ignored() {
  local root parent child marker out status
  root="$(mktemp -d)"; parent='session-88888888-8888-4888-8888-888888888888'; child='99999999-9999-4999-8999-999999999999'
  make_frame "$root" "$child" "$parent" true
  touch -t 202001010000 "$root/$child/session.v3.jsonl.zstd"
  marker="$(make_marker "$root" marker)"
  set +e
  out="$(dsh_await_child_turn_end "$root" "$parent" "$marker" "$(date +%s)" 2>&1)"; status=$?
  set -e
  [[ $status -ne 0 && "$out" == *'no durable native child'* ]] && pass stale-child || fail stale-child
  rm -rf "$root"
}

test_direct_calls_do_not_replace_return_trap() {
  local root parent child marker before after
  root="$(mktemp -d)"; parent='session-aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa'; child='bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb'
  marker="$(make_marker "$root" marker)"
  make_frame "$root" "$child" "$parent" true
  trap ':' RETURN
  before="$(trap -p RETURN)"
  dsh_await_child_turn_end "$root" "$parent" "$marker" $(( $(date +%s) + 1 )) >/dev/null
  dsh_await_child_turn_end "$root" "$parent" "$marker" $(( $(date +%s) + 1 )) >/dev/null
  after="$(trap -p RETURN)"
  [[ "$before" == "$after" ]] && pass direct-repeat || fail direct-repeat
  trap - RETURN
  rm -rf "$root"
}

test_success
test_timeout
test_wrong_parent
test_stale_matching_child_is_ignored
test_direct_calls_do_not_replace_return_trap
[[ $failures -eq 0 ]]
