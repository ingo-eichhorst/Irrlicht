#!/usr/bin/env bash
# vm-pool-mutations_test.sh — the committed mutation fixtures for
# examples/vm-pool/pool.sh, whose rules tools/lib/vm-pool_test.sh pins. The
# kit is new, so its rules have no "before the fix" to run red; each earns its
# place by turning vm-pool_test.sh red when the rule is broken here.
#
# Same tools/mutate.sh underneath as tools/lib/workerlist-mutations_test.sh;
# the subject is a bash test rather than a Go one.

set -uo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$DIR/../.." && pwd)"
MUTATE_SH="$REPO_ROOT/tools/mutate.sh"
POOL="examples/vm-pool/pool.sh"

# A missing tool is a hard failure, not a skip — exiting 0 here would read as
# a PASS to preflight's shell_lib_tests.
for tool in go git jq; do
  command -v "$tool" >/dev/null 2>&1 || { echo "FAIL: vm-pool-mutations — $tool not found" >&2; exit 1; }
done
if [[ ! -x "$MUTATE_SH" ]]; then
  echo "FAIL: vm-pool-mutations — $MUTATE_SH is missing or not executable" >&2
  exit 1
fi

# mutate.sh refuses a dirty tree; a dirty tree must not silently pass where
# the answer is load-bearing (see cost-unattributed-mutations_test.sh).
if [[ -n "$(git -C "$REPO_ROOT" status --porcelain)" ]]; then
  echo "vm-pool-mutations: CANNOT RUN — the worktree is dirty, and mutate.sh needs a" >&2
  echo "  clean tree for its post-restore check to mean anything. Commit or clean up, then:" >&2
  echo "    bash tools/lib/vm-pool-mutations_test.sh" >&2
  if [[ -n "${CI:-}" || -n "${MUTATION_FIXTURES_STRICT:-}" ]]; then
    echo "  (failing rather than skipping: CI/strict mode)" >&2
    exit 1
  fi
  echo "  (skipped locally; set MUTATION_FIXTURES_STRICT=1 to make this a failure)" >&2
  exit 0
fi

fails=0

# assert_pool_test_goes_red <label> <anchor> <replacement> <want-in-output>
#
# Applies one mutation to pool.sh and requires vm-pool_test.sh to FAIL, with
# the named check among its failures — a suite that could not run at all
# ("not found", a syntax error) is not a catch.
assert_pool_test_goes_red() {
  local label="$1" anchor="$2" replacement="$3" want="$4" out rc
  out="$(cd "$REPO_ROOT" && "$MUTATE_SH" "$POOL" "$anchor" "$replacement" \
    bash -c 'bash tools/lib/vm-pool_test.sh 2>&1; echo POOL_TEST_RC=$?' 2>&1)"
  rc=$?
  if [[ $rc -ne 0 ]]; then
    echo "FAIL: $label — mutate.sh refused (exit $rc); a STALE anchor needs updating"
    sed 's/^/      | /' <<<"$out"
    fails=$((fails + 1))
    return
  fi
  if grep -q 'POOL_TEST_RC=0' <<<"$out"; then
    echo "FAIL: $label — vm-pool_test.sh stayed GREEN under the mutation"
    sed 's/^/      | /' <<<"$out"
    fails=$((fails + 1))
    return
  fi
  if ! grep -q 'vm-pool_test: [0-9]* FAILED' <<<"$out" || ! grep -qF -- "FAIL: $want" <<<"$out"; then
    echo "FAIL: $label — vm-pool_test.sh failed, but not at the expected check"
    echo "      wanted: FAIL: $want"
    sed 's/^/      | /' <<<"$out"
    fails=$((fails + 1))
    return
  fi
  echo "ok  $label"
}

assert_pool_test_goes_red \
  "reporting waiting on a stopped task reddens the stale-waiting-file check" \
  '(if $status == "running" and $state == "waiting" then {state: "waiting"} else {} end)' \
  '(if $state == "waiting" then {state: "waiting"} else {} end)' \
  "stopped task with a stale waiting file"

assert_pool_test_goes_red \
  "passing the heartbeat through unchecked reddens the malformed-heartbeat check" \
  '($heartbeat | ts | {last_heartbeat: .}) // {}' \
  '{last_heartbeat: $heartbeat}' \
  "malformed heartbeat"

assert_pool_test_goes_red \
  "dropping the backend fallback reddens the killed-guest check" \
  'if [[ "$status" == stopped && -z "$ended" ]]; then' \
  'if false; then' \
  "killed guest"

assert_pool_test_goes_red \
  "passing a credential by value reddens the --env check" \
  '--env) envs+=(-e "${2:?--env needs a name}"); shift 2 ;;' \
  '--env) envs+=(-e "${2}=${!2}"); shift 2 ;;' \
  "--env put the credential's VALUE on docker's argv"

assert_pool_test_goes_red \
  "mounting guest/ read-write reddens the read-only check" \
  'local cli="$DOCKER" ro=":ro"' \
  'local cli="$DOCKER" ro=""' \
  "guest/ is not mounted read-only"

assert_pool_test_goes_red \
  "reading the whole .status object of container inspect reddens the Apple container check" \
  '[[ "$(jq -r '"'"'.[0].status.state // empty'"'"' <<<"$out")" == running ]]' \
  '[[ "$(jq -r '"'"'.[0].status // empty'"'"' <<<"$out")" == running ]]' \
  "running Apple container task"

assert_pool_test_goes_red \
  "mounting guest/ read-write on the Apple container backend reddens its read-only check" \
  '[[ "$backend" == container ]] && cli="$CONTAINER"' \
  '[[ "$backend" == container ]] && { cli="$CONTAINER"; ro=""; }' \
  "guest/ is not mounted read-only on the Apple container backend"

assert_pool_test_goes_red \
  "removing any worker list in down reddens the other-writer check" \
  'if [[ -f "$OUT" ]] && [[ "$(jq -r '"'"'.writer // empty'"'"' "$OUT" 2>/dev/null)" == "$WRITER" ]]; then' \
  'if [[ -f "$OUT" ]]; then' \
  "down removed a worker list it did not write"

if [[ $fails -gt 0 ]]; then
  echo "vm-pool-mutations: $fails FAILED"
  exit 1
fi
echo "vm-pool-mutations: ALL PASS"
