#!/usr/bin/env bash
# presession-pid-inherit-mutations_test.sh — committed mutation fixture for
# issue #2042's three guards on handing a retired pre-session's PID to the
# real session that replaced it (core/application/services:
# SessionDetector.offerPreSessionPID and PIDManager.DiscoverPIDWithRetry /
# inheritPreSessionPID).
#
# The real session takes the pre-session's PID only when (1) exactly one
# pre-session was retired — with several, the match cannot say which process
# produced it — (2) that PID is still alive, and (3) the adapter's own first
# discovery attempt found nothing, because the pre-session match is by project
# or cwd, not by process. These are guards the change ADDS, so there is no
# "before the fix" to run red; each mutation below breaks one guard and
# requires its test to go red:
#   - `len(ids) == 1` → `len(ids) >= 1` offers the first of two candidates
#     (TestSessionDetector_AmbiguousPreSessionsNotInherited_Issue2042);
#   - dropping the IsPIDAlive check binds a dead PID
#     (TestSessionDetector_DeadPreSessionPIDNotInherited_Issue2042);
#   - dropping the `state.PID != 0` check binds the pre-session PID over a
#     discovered one
#     (TestSessionDetector_AdapterDiscoveryWinsOverPreSessionPID_Issue2042).
#
# tools/mutate.sh owns the mechanics this file must not re-improvise.

set -uo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$DIR/../.." && pwd)"
MUTATE_SH="$REPO_ROOT/tools/mutate.sh"

need() {
  local tool="$1"
  if ! command -v "$tool" >/dev/null 2>&1; then
    echo "FAIL: presession-pid-inherit-mutations — $tool not found" >&2
    exit 1
  fi
  return 0
}
quote_output() { sed 's/^/      | /'; return 0; }
need go
need git

if [[ ! -x "$MUTATE_SH" ]]; then
  echo "FAIL: presession-pid-inherit-mutations — $MUTATE_SH is missing or not executable" >&2
  exit 1
fi

if [[ -n "$(git -C "$REPO_ROOT" status --porcelain)" ]]; then
  echo "presession-pid-inherit-mutations: CANNOT RUN — the worktree is dirty, and mutate.sh needs a" >&2
  echo "  clean tree for its post-restore check to mean anything. Commit or clean up, then:" >&2
  echo "    bash tools/lib/presession-pid-inherit-mutations_test.sh" >&2
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
    echo "      guard's source moved and this fixture needs updating — it does NOT mean the"
    echo "      guard is fine."
    echo "$out" | quote_output
    fails=$((fails + 1))
    return
  fi
  if grep -q 'GO_TEST_RC=0' <<<"$out"; then
    echo "FAIL: $label — the test stayed GREEN under the mutation, so the guard does not reach"
    echo "      what it claims to protect."
    echo "$out" | quote_output
    fails=$((fails + 1))
    return
  fi
  if grep -qE '^# |build failed|cannot use|undefined:' <<<"$out"; then
    echo "FAIL: $label — the mutation broke the BUILD rather than the guard. A fixture that"
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

PKG="./core/application/services/..."

# ── the single-candidate rule ──
assert_go_test_goes_red \
  "offering the first of several retired pre-sessions" \
  "core/application/services/session_detector_helpers.go" \
  $'\tif len(ids) == 1 {\n\t\td.offerPreSessionPID(ids[0], newSessionID)' \
  $'\tif len(ids) >= 1 {\n\t\td.offerPreSessionPID(ids[0], newSessionID)' \
  "$PKG" \
  "TestSessionDetector_AmbiguousPreSessionsNotInherited_Issue2042" \
  "from an ambiguous pre-session match"

# ── the liveness check ──
assert_go_test_goes_red \
  "inheriting a pre-session PID whose process has already exited" \
  "core/application/services/pid_manager.go" \
  $'\tif !pm.IsPIDAlive(pid) {\n\t\treturn false\n\t}\n\tif state, _ := pm.repo.Load(sessionID); state == nil || state.PID != 0 {' \
  $'\tif state, _ := pm.repo.Load(sessionID); state == nil || state.PID != 0 {' \
  "$PKG" \
  "TestSessionDetector_DeadPreSessionPIDNotInherited_Issue2042" \
  "took dead pid"

# ── adapter discovery first ──
assert_go_test_goes_red \
  "binding the pre-session PID over an adapter-discovered one" \
  "core/application/services/pid_manager.go" \
  $'\tif state, _ := pm.repo.Load(sessionID); state == nil || state.PID != 0 {\n\t\treturn false\n\t}\n\tpm.log.LogInfo(logComponentSessionDetector, sessionID,\n\t\tfmt.Sprintf("adapter discovery found no pid; inheriting' \
  $'\tif state, _ := pm.repo.Load(sessionID); state == nil {\n\t\treturn false\n\t}\n\tpm.log.LogInfo(logComponentSessionDetector, sessionID,\n\t\tfmt.Sprintf("adapter discovery found no pid; inheriting' \
  "$PKG" \
  "TestSessionDetector_AdapterDiscoveryWinsOverPreSessionPID_Issue2042" \
  "must not win"

if [[ $fails -gt 0 ]]; then
  echo "presession-pid-inherit-mutations: $fails FAILED"
  exit 1
fi
echo "presession-pid-inherit-mutations: ALL PASS"
