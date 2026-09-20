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
  printf '%s\n' '#!/usr/bin/env bash' '[[ -z "${FAKE_CURL_MARKER:-}" ]] || : > "$FAKE_CURL_MARKER"' 'printf "%s" "$FAKE_CURL_RESPONSE"' > "$root/curl"
  chmod +x "$root/curl"
  set +e
  FAKE_CURL_RESPONSE="$response" FAKE_CURL_MARKER="${FAKE_CURL_MARKER:-}" PATH="$root:$PATH" "$function_name"
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

test_duplicate_parent_uses_first_match() {
  local id='session-55555555-5555-4555-8555-555555555555' out status
  check() { dsh_await_parent_ready 127.0.0.1:9999 "$id" "$(date +%s)"; }
  set +e; out="$(with_fake_curl "{\"groups\":[{\"agents\":[{\"session_id\":\"$id\",\"state\":\"working\",\"children\":[{\"session_id\":\"$id\",\"state\":\"ready\"}]}]}]}" check 2>&1)"; status=$?; set -e
  [[ $status -ne 0 && "$out" == *'remained working'* ]] && pass duplicate-parent || fail duplicate-parent
}

test_invalid_parent_state_refuses() {
  local id='session-66666666-6666-4666-8666-666666666666' out status
  check() { dsh_await_parent_ready 127.0.0.1:9999 "$id" "$(date +%s)"; }
  set +e; out="$(with_fake_curl "{\"groups\":[{\"agents\":[{\"session_id\":\"$id\",\"state\":17}]}]}" check 2>&1)"; status=$?; set -e
  [[ $status -ne 0 && "$out" == *'had malformed state 17'* ]] && pass invalid-parent-state || fail invalid-parent-state
}

test_malformed_response() {
  local id='session-44444444-4444-4444-8444-444444444444' out status
  check() { dsh_await_parent_ready 127.0.0.1:9999 "$id" "$(date +%s)"; }
  set +e; out="$(with_fake_curl '{not json}' check 2>&1)"; status=$?; set -e
  [[ $status -ne 0 && "$out" == *'snapshot was malformed'* ]] && pass malformed-api-response || fail malformed-api-response
}

rejects_before_curl() { # <bind-address> <expected-error>
  local bind_addr="$1" expected_error="$2" id='session-77777777-7777-4777-8777-777777777777'
  local marker out status
  marker="$(mktemp)"
  rm -f "$marker"
  check() { dsh_await_parent_ready "$bind_addr" "$id" "$(date +%s)"; }
  set +e; out="$(FAKE_CURL_MARKER="$marker" with_fake_curl '{"groups":[]}' check 2>&1)"; status=$?; set -e
  [[ $status -ne 0 && "$out" == *"$expected_error"* && ! -e "$marker" ]]
  status=$?
  rm -f "$marker"
  return "$status"
}

test_rejects_non_loopback_address() {
  rejects_before_curl '192.0.2.1:9999' 'must be 127.0.0.1 with a numeric unprivileged port' && pass rejects-non-loopback-address || fail rejects-non-loopback-address
}

test_rejects_url_injection() {
  rejects_before_curl '127.0.0.1:9999@evil.example' 'must be 127.0.0.1 with a numeric unprivileged port' && pass rejects-url-injection || fail rejects-url-injection
}

test_rejects_privileged_port() {
  rejects_before_curl '127.0.0.1:1023' 'must use an unprivileged port' && pass rejects-privileged-port || fail rejects-privileged-port
}

test_rejects_out_of_range_port() {
  rejects_before_curl '127.0.0.1:65536' 'must use an unprivileged port' && pass rejects-out-of-range-port || fail rejects-out-of-range-port
}

test_rejects_huge_port() {
  local port
  printf -v port '%*s' 1000 ''
  port="${port// /9}"
  rejects_before_curl "127.0.0.1:$port" 'must be 127.0.0.1 with a numeric unprivileged port' && pass rejects-huge-port || fail rejects-huge-port
}

test_success
test_missing_parent
test_timeout
test_duplicate_parent_uses_first_match
test_invalid_parent_state_refuses
test_malformed_response
test_rejects_non_loopback_address
test_rejects_url_injection
test_rejects_privileged_port
test_rejects_out_of_range_port
test_rejects_huge_port
[[ $failures -eq 0 ]]
