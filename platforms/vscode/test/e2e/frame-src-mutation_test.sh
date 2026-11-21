#!/usr/bin/env bash
# Self-test for frame-src-mutation.sh's classify_log, fed synthetic logs.
set -uo pipefail
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=frame-src-mutation.sh
source "$here/frame-src-mutation.sh"
d="$(mktemp -d "${TMPDIR:-/tmp}/fsm-test.XXXXXX")" || exit 2
trap 'rm -rf "$d"' EXIT
fail=0
expect() { # name want(0|1) file
  classify_log "$3" >/dev/null 2>&1; local got=$?
  if [[ $got -eq $2 ]]; then echo "ok   $1"; else echo "FAIL $1 (want rc=$2 got rc=$got)"; fail=1; fi
}
printf '%s\n' '[e2e] PASS activate (+1 ms, total 1 ms)' 'e2e: FAILED: e2e step "4b webview iframe GET /?embed=vscode" timed out after 20000 ms' '=== npm run test:e2e exit=1 ===' >"$d/timeout4b"
printf '%s\n' '[e2e] PASS 4b webview iframe reached daemon (+5 ms, total 9 ms)' 'e2e: FAILED: e2e step "4d pairing e2e-agent working" timed out after 20000 ms' '=== npm run test:e2e exit=1 ===' >"$d/past4b"
printf '%s\n' '[e2e] PASS 4b webview iframe reached daemon (+5 ms, total 9 ms)' '=== npm run test:e2e exit=0 ===' >"$d/green"
printf '%s\n' 'e2e step "4b webview iframe x" timed out' >"$d/noexit"
expect "4b timeout -> PASS verdict" 0 "$d/timeout4b"
expect "PASS 4b then 4d failure -> FAIL verdict" 1 "$d/past4b"
expect "green run -> FAIL verdict" 1 "$d/green"
expect "no exit line -> FAIL verdict" 1 "$d/noexit"
exit $fail
