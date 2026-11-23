#!/usr/bin/env bash
# Self-test for frame-src-mutation.sh's classify_log, fed synthetic logs. Every
# FAIL branch of classify_log has a case with a needle for its own message, so
# deleting a branch turns a case red instead of falling through to another one.
set -uo pipefail
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=frame-src-mutation.sh
source "$here/frame-src-mutation.sh"
# shellcheck source=../../../../tools/lib/checker-assert.sh
source "$here/../../../../tools/lib/checker-assert.sh"
d="$(mktemp -d "${TMPDIR:-/tmp}/fsm-test.XXXXXX")" || exit 2
trap 'rm -rf "$d"' EXIT
rc=0
printf '%s\n' '[e2e] PASS activate (+1 ms, total 1 ms)' 'e2e: FAILED: e2e step "4b webview iframe GET /?embed=vscode" timed out after 20000 ms' '=== npm run test:e2e exit=1 ===' >"$d/timeout4b"
printf '%s\n' '[e2e] PASS 4b webview iframe reached daemon (+5 ms, total 9 ms)' 'e2e: FAILED: e2e step "4d pairing e2e-agent working" timed out after 20000 ms' '=== npm run test:e2e exit=1 ===' >"$d/past4b"
printf '%s\n' '[e2e] PASS 4b webview iframe reached daemon (+5 ms, total 9 ms)' '=== npm run test:e2e exit=0 ===' >"$d/green"
printf '%s\n' 'e2e step "4b webview iframe x" timed out' >"$d/noexit"
printf '%s\n' '[e2e] PASS activate (+1 ms, total 1 ms)' 'e2e: FAILED: e2e step "4a connection online" timed out after 20000 ms' '=== npm run test:e2e exit=1 ===' >"$d/before4b"
assert_checker_rc classify_log "4b timeout -> PASS verdict" 0 '' "$d/timeout4b"
assert_checker_rc classify_log "PASS 4b then 4d failure -> FAIL verdict" 1 'step 4b passed under frame-src' "$d/past4b"
assert_checker_rc classify_log "green run -> FAIL verdict" 1 'e2e PASSED' "$d/green"
assert_checker_rc classify_log "no exit line -> FAIL verdict" 1 "could not find the mutated run's exit line" "$d/noexit"
assert_checker_rc classify_log "failure before 4b -> FAIL verdict" 1 'not at step 4b' "$d/before4b"
exit "$rc"
