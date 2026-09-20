#!/usr/bin/env bash
# Regression tests for the DSH parent-ready observation helper.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
# shellcheck source=parent-ready.sh
source "$ROOT/replaydata/agents/deepseek-harness/parent-ready.sh"

failures=0
pass() { printf 'ok - %s\n' "$1"; }
fail() { printf 'not ok - %s\n' "$1" >&2; failures=$((failures + 1)); }

with_fake_curl() { # <response> <function name>
  local response="$1" function_name="$2" root status
  root="$(mktemp -d)"
  printf '#!/usr/bin/env bash\nprintf "%%s" "$FAKE_CURL_RESPONSE"\n' > "$root/curl"
  chmod +x "$root/curl"
  set +e
  FAKE_CURL_RESPONSE="$response" PATH="$root:$PATH" "$function_name"
  status=$?
  set -e
  rm -rf "$root"
  return "$status"
}

test_success() {
  local id='session-11111111-1111-4111-8111-111111111111'
  check() { dsh_await_parent_ready 127.0.0.1:9999 "$id" "$(date +%s)"; }
  if with_fake_curl "{\"groups\":[{\"agents\":[{\"session_id\":\"$id\",\"state\":\"ready\"}]}]}" check; then pass success; else fail success; fi
}

test_missing_parent() {
  local id='session-22222222-2222-4222-8222-222222222222' out status
  check() { dsh_await_parent_ready 127.0.0.1:9999 "$id" "$(date +%s)"; }
  set +e; out="$(with_fake_curl '{"groups":[]}' check 2>&1)"; status=$?; set -e
  [[ $status -ne 0 && "$out" == *'was absent from daemon snapshot'* ]] && pass missing-parent || fail missing-parent
}

test_timeout() {
  local id='session-33333333-3333-4333-8333-333333333333' out status
  check() { dsh_await_parent_ready 127.0.0.1:9999 "$id" "$(date +%s)"; }
  set +e; out="$(with_fake_curl "{\"groups\":[{\"agents\":[{\"session_id\":\"$id\",\"state\":\"working\"}]}]}" check 2>&1)"; status=$?; set -e
  [[ $status -ne 0 && "$out" == *'remained working'* ]] && pass timeout || fail timeout
}

test_malformed_response() {
  local id='session-44444444-4444-4444-8444-444444444444' out status
  check() { dsh_await_parent_ready 127.0.0.1:9999 "$id" "$(date +%s)"; }
  set +e; out="$(with_fake_curl '{not json}' check 2>&1)"; status=$?; set -e
  [[ $status -ne 0 && "$out" == *'snapshot was malformed'* ]] && pass malformed-api-response || fail malformed-api-response
}

test_success
test_missing_parent
test_timeout
test_malformed_response
[[ $failures -eq 0 ]]
