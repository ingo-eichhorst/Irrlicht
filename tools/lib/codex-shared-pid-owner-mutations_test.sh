#!/usr/bin/env bash
# codex-shared-pid-owner-mutations_test.sh — committed mutation fixture for
# issue #2077's codex SharedPIDOwner (core/adapters/inbound/agents/codex/pid.go
# OwnsSharedPID).
#
# WHY THIS FILE EXISTS. #2077 adds a per-root ownership probe so concurrent
# codex roots on the shared managed app-server daemon's PID stop superseding
# each other. The concurrency tests in
# core/application/services/pid_manager_codex_shared_pid_test.go were seen red
# on the unfixed code; the retirement test
# (TestCheckPIDLiveness_ReleasedCodexRootIsRetired) was GREEN there by
# construction — the exclusive same-PID policy retires every older root — so
# its only red evidence is the mutation below. Both directions are pinned:
#
#   1. the probe answers false for every root  → the three concurrency tests go
#      red (a live root is deleted on the assignment, periodic and seed paths);
#   2. the probe answers true for every root   → the retirement test goes red
#      (a root whose rollout the daemon released is kept forever).
#
# tools/mutate.sh owns the mechanics this file must not re-improvise: the
# stale/ambiguous-anchor guards, and the byte-for-byte restore that never
# touches git state. Modeled on
# tools/lib/museaccountapi-zero-quota-on-auth-failure-mutations_test.sh.

set -uo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$DIR/../.." && pwd)"
MUTATE_SH="$REPO_ROOT/tools/mutate.sh"

need() {
  local tool="$1"
  if ! command -v "$tool" >/dev/null 2>&1; then
    echo "FAIL: codex-shared-pid-owner-mutations — $tool not found" >&2
    exit 1
  fi
  return 0
}
quote_output() { sed 's/^/      | /'; return 0; }
need go
need git

if [[ ! -x "$MUTATE_SH" ]]; then
  echo "FAIL: codex-shared-pid-owner-mutations — $MUTATE_SH is missing or not executable" >&2
  exit 1
fi

if [[ -n "$(git -C "$REPO_ROOT" status --porcelain)" ]]; then
  echo "codex-shared-pid-owner-mutations: CANNOT RUN — the worktree is dirty, and mutate.sh needs a" >&2
  echo "  clean tree for its post-restore check to mean anything. Commit or clean up, then:" >&2
  echo "    bash tools/lib/codex-shared-pid-owner-mutations_test.sh" >&2
  if [[ -n "${CI:-}" || -n "${MUTATION_FIXTURES_STRICT:-}" ]]; then
    echo "  (failing rather than skipping: CI/strict mode, where a skip is indistinguishable" >&2
    echo "   from a pass and this gate is the only thing re-running this mutation)" >&2
    exit 1
  fi
  echo "  (skipped locally; set MUTATION_FIXTURES_STRICT=1 to make this a failure)" >&2
  exit 0
fi

fails=0

assert_go_test_goes_red() {
  local label="$1" file="$2" anchor="$3" replacement="$4" pkg="$5" run="$6" want="$7"
  local out rc

  out="$(cd "$REPO_ROOT" && "$MUTATE_SH" "$file" "$anchor" "$replacement" \
    bash -c "go test $pkg -run '$run' -race -count=1 -v 2>&1; echo GO_TEST_RC=\$?" 2>&1)"
  rc=$?

  if [[ $rc -ne 0 ]]; then
    echo "FAIL: $label — mutate.sh refused (exit $rc). A STALE or ambiguous anchor means the"
    echo "      probe's source moved and this fixture needs updating — it does NOT mean the"
    echo "      probe is fine."
    echo "$out" | quote_output
    fails=$((fails + 1))
    return
  fi
  if grep -q 'GO_TEST_RC=0' <<<"$out"; then
    echo "FAIL: $label — the test stayed GREEN under the mutation, so it does not reach"
    echo "      the probe it claims to protect."
    echo "$out" | quote_output
    fails=$((fails + 1))
    return
  fi
  if grep -qE '^# |build failed|cannot use|undefined:' <<<"$out"; then
    echo "FAIL: $label — the mutation broke the BUILD rather than the probe. A fixture that"
    echo "      cannot compile proves nothing about the behavior it is meant to exercise."
    echo "$out" | quote_output
    fails=$((fails + 1))
    return
  fi
  if ! grep -qF -- "$want" <<<"$out"; then
    echo "FAIL: $label — the test failed, but not with the expected message."
    echo "      wanted to find: $want"
    echo "$out" | quote_output
    fails=$((fails + 1))
    return
  fi
  echo "ok  $label"
}

FILE="core/adapters/inbound/agents/codex/pid.go"
ANCHOR=$'\towner, err := DiscoverPID(cwd, transcriptPath, nil)\n\treturn err == nil && owner == pid'

# ── 1. no root ever proves ownership: concurrent live roots are deleted ──
assert_go_test_goes_red \
  "OwnsSharedPID answering false for a rollout the daemon still holds" \
  "$FILE" \
  "$ANCHOR" \
  $'\treturn false' \
  "./core/application/services/" \
  "ConcurrentCodexRootsKeepDaemonPID" \
  "removed a live codex root sharing the app-server daemon's pid"

# ── 2. every root proves ownership: a released root is never retired ──
assert_go_test_goes_red \
  "OwnsSharedPID answering true for a rollout the daemon released" \
  "$FILE" \
  "$ANCHOR" \
  $'\treturn true' \
  "./core/application/services/" \
  "TestCheckPIDLiveness_ReleasedCodexRootIsRetired" \
  "a codex root whose rollout the daemon released survived the same-PID sweep"

if [[ $fails -gt 0 ]]; then
  echo "codex-shared-pid-owner-mutations: $fails FAILED"
  exit 1
fi
echo "codex-shared-pid-owner-mutations: ALL PASS"
