#!/usr/bin/env bash
# Mutation fixture for the e2e's step 4b (webview iframe request observed by
# the stub daemon). Mutates src/webviewHtml.ts so buildCsp emits
# `frame-src 'none'`, runs `npm run test:e2e`, and PASSES only if that run
# FAILS and its output names step 4b. Every other outcome exits non-zero:
# mutated run green, failure elsewhere, stale anchor, missing tools, dirty tree.
# Needs a clean git tree (tools/mutate.sh refuses a dirty one, exit 4).
set -uo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
pkg="$(cd "$here/../.." && pwd)"
repo="$(cd "$pkg" && git rev-parse --show-toplevel)" || { echo "frame-src-mutation: FAIL not inside a git repository" >&2; exit 2; }
mutate="$repo/tools/mutate.sh"
file="$pkg/src/webviewHtml.ts"

[[ -f "$mutate" ]] || { echo "frame-src-mutation: FAIL tools/mutate.sh not found at $mutate" >&2; exit 2; }
[[ -f "$file" ]] || { echo "frame-src-mutation: FAIL $file not found" >&2; exit 2; }
command -v npm >/dev/null 2>&1 || { echo "frame-src-mutation: FAIL npm not on PATH" >&2; exit 2; }

# shellcheck disable=SC2016  # literal template text, not an expansion
anchor='`frame-src ${o.daemonOrigin}`,'
# shellcheck disable=SC2016
replacement="\"frame-src 'none'\","

out="$(mktemp "${TMPDIR:-/tmp}/frame-src-mutation.XXXXXX")" || { echo "frame-src-mutation: FAIL mktemp" >&2; exit 2; }
trap 'rm -f "$out"' EXIT

cd "$pkg" || exit 2
"$mutate" "$file" "$anchor" "$replacement" npm run test:e2e >"$out" 2>&1
rc=$?
cat "$out"

if [[ $rc -ne 0 ]]; then
  echo "frame-src-mutation: FAIL mutate.sh refused or failed (exit $rc): stale anchor, dirty tree, or restore problem; the mutation did not run to completion" >&2
  exit 1
fi
if ! grep -q '=== .*test:e2e.* exit=' "$out"; then
  echo "frame-src-mutation: FAIL could not find the mutated run's exit line; cannot tell whether the e2e ran" >&2
  exit 1
fi
if grep -Eq '=== .*test:e2e.* exit=0 ===' "$out"; then
  echo "frame-src-mutation: FAIL the e2e PASSED with frame-src 'none'; step 4b does not observe the iframe request" >&2
  exit 1
fi
if ! grep -q '4b webview iframe' "$out"; then
  echo "frame-src-mutation: FAIL the mutated e2e failed, but not at step 4b (see output above)" >&2
  exit 1
fi
if ! git -C "$repo" diff --quiet -- "$file"; then
  echo "frame-src-mutation: FAIL src/webviewHtml.ts is not restored" >&2
  exit 1
fi
echo "frame-src-mutation: PASS mutated e2e failed at step 4b as required; tree restored"
