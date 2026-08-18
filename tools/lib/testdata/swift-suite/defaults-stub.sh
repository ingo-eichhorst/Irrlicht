#!/usr/bin/env bash
# defaults-stub.sh — a stand-in for defaults(1), for grading the domain half of
# tools/lib/swift-suite.sh's witness.
#
# It exists because the only way to drive an added key, a removed key or a
# changed value against the REAL `defaults` is to `defaults write` a domain in
# the developer's home — which is #1661's incident, not a test of it. Nothing in
# this repository writes a real preference domain, so the external command is
# swapped and everything downstream of it stays production code.
#
# Contract, deliberately narrow: it models `export <domain> -` and nothing else.
#
#   $SWIFT_SUITE_STUB_DIR/<domain>        the bytes to emit on stdout.
#   $SWIFT_SUITE_STUB_DIR/<domain>.rc     the status to exit with (default 0).
#
# An UNMODELLED domain is a loud 99 naming the call, never a quiet 0 — the same
# rule ars-badge-push_test.sh's git stub follows. A stub that answered "fine" to
# a call it does not model would make every arm pass for a reason unrelated to
# its obligation, and here it would specifically hand the control probe an empty
# answer, which is one of the states under test.

set -uo pipefail

if [[ "${1:-}" != "export" || "${3:-}" != "-" ]]; then
  echo "STUB: unmodelled call: defaults $*" >&2
  exit 99
fi

domain="${2:-}"
dir="${SWIFT_SUITE_STUB_DIR:-}"
if [[ -z "$dir" || ! -d "$dir" ]]; then
  echo "STUB: SWIFT_SUITE_STUB_DIR is not a directory: '${dir}'" >&2
  exit 99
fi
if [[ ! -f "$dir/$domain" ]]; then
  echo "STUB: unmodelled domain: $domain (no $dir/$domain)" >&2
  exit 99
fi

cat "$dir/$domain"
if [[ -f "$dir/$domain.rc" ]]; then
  exit "$(cat "$dir/$domain.rc")"
fi
exit 0
