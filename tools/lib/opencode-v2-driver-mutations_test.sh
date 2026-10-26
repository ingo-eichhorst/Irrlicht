#!/usr/bin/env bash
# opencode-v2-driver-mutations_test.sh — committed mutation fixture for #2109
# P5a's API driver (replaydata/agents/opencode-v2/). Each mutation breaks one of
# the driver's own guards and requires its stub-service test
# (tools/onboarding-factory/scripts/lib/opencode-v2-driver_test.sh) to go red
# with the named message:
#
#   1. wait_turn without its deadline → a service that never idles is no
#      longer ended as `timeout` (the test's watchdog kills the driver);
#   2. the one-service check accepting any count → the refusal no longer
#      names the count (measured: the run is still refused, by the next guard —
#      the one running service must be the one service.json names — so what
#      this guard adds is the reason an operator can act on);
#   3. no location boot before the plugin wait → the plugin never logs a load
#      and the run is refused (measured live: a created session does not boot
#      its location, #2109's first opencode-v2 run);
#   4. the plugin-load check ignoring "failed to load plugin" → a run with a
#      plugin the service rejected is driven.
#
# Mutation 1 runs until the test's watchdog (the cell deadline + 8s); the
# driver's readiness waits run on the test's short knobs. Kept apart from
# opencode-v2-rig-mutations_test.sh because it drives the stub service.

set -uo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$DIR/../.." && pwd)"
MUTATE_SH="$REPO_ROOT/tools/mutate.sh"
LOCK_TEST="tools/onboarding-factory/scripts/lib/opencode-v2-driver_test.sh"

[[ -x "$MUTATE_SH" ]] || { echo "FAIL: opencode-v2-driver-mutations — $MUTATE_SH is missing or not executable" >&2; exit 1; }

if [[ -n "$(git -C "$REPO_ROOT" status --porcelain)" ]]; then
  echo "opencode-v2-driver-mutations: CANNOT RUN — the worktree is dirty, and mutate.sh needs a" >&2
  echo "  clean tree for its post-restore check to mean anything. Commit or clean up, then:" >&2
  echo "    bash tools/lib/opencode-v2-driver-mutations_test.sh" >&2
  if [[ -n "${CI:-}" || -n "${MUTATION_FIXTURES_STRICT:-}" ]]; then
    echo "  (failing rather than skipping: CI/strict mode)" >&2
    exit 1
  fi
  echo "  (skipped locally; set MUTATION_FIXTURES_STRICT=1 to make this a failure)" >&2
  exit 0
fi

fails=0
quote_output() { sed 's/^/      | /'; return 0; }

assert_red() { # <label> <file> <anchor> <replacement> <want>
  local label="$1" out rc
  out="$(cd "$REPO_ROOT" && "$MUTATE_SH" "$2" "$3" "$4" \
    bash -c "bash $LOCK_TEST 2>&1; echo SH_TEST_RC=\$?" 2>&1)"; rc=$?
  if [[ $rc -ne 0 ]]; then
    echo "FAIL: $label — mutate.sh refused (exit $rc): a STALE or ambiguous anchor, not a clean guard."
    echo "$out" | quote_output; fails=$((fails + 1)); return
  fi
  if grep -q 'SH_TEST_RC=0' <<<"$out"; then
    echo "FAIL: $label — the driver test stayed GREEN under the mutation."
    echo "$out" | quote_output; fails=$((fails + 1)); return
  fi
  if ! grep -q 'FAIL:' <<<"$out"; then
    echo "FAIL: $label — the driver test exited non-zero WITHOUT reporting a failure: it could"
    echo "      not run under the mutation, which is not evidence about the guard."
    echo "$out" | quote_output; fails=$((fails + 1)); return
  fi
  if grep -q 'syntax error' <<<"$out"; then
    echo "FAIL: $label — the mutation broke the driver's parse, not the guard."
    echo "$out" | quote_output; fails=$((fails + 1)); return
  fi
  if ! grep -qF -- "$5" <<<"$out"; then
    echo "FAIL: $label — the driver test failed, but not with the expected message."
    echo "      wanted to find: $5"
    echo "$out" | quote_output; fails=$((fails + 1)); return
  fi
  echo "ok  $label"
}

DRIVER="replaydata/agents/opencode-v2/driver-interactive.sh"
SERVICE_API="replaydata/agents/opencode-v2/service-api.sh"

# ── 1. wait_turn without its deadline ──
assert_red "wait_turn ignoring the deadline" "$DRIVER" \
  $'    if (( $(remaining_seconds) <= 0 )); then\n      log "wait_turn: no new idle' \
  $'    if false; then\n      log "wait_turn: no new idle' \
  "FAIL: exit reason timeout"

# ── 2. the one-service check accepting any count ──
assert_red "two services accepted" "$SERVICE_API" \
  '  if [[ "$n" != "1" ]]; then' '  if [[ "$n" == "0" ]]; then' \
  "FAIL: …naming the count"

# ── 3. no location boot ──
assert_red "no location boot before the plugin wait" "$DRIVER" \
  '  if ! oc2_api GET "/api/command?location%5Bdirectory%5D=' \
  '  if false && ! oc2_api GET "/api/command?location%5Bdirectory%5D=' \
  "FAIL: exit reason ok"

# ── 4. a failed plugin load ignored ──
assert_red "a failed plugin load ignored" "$SERVICE_API" \
  $'  grep -qF "failed to load plugin\\" target=$plugin " <<<"$tail_lines" && return 2\n' "" \
  "FAIL: exit reason nonzero(2) — expected [nonzero(2)] got [ok]"

if [[ $fails -gt 0 ]]; then
  echo "opencode-v2-driver-mutations: $fails FAILED"
  exit 1
fi
echo "opencode-v2-driver-mutations: ALL PASS"
