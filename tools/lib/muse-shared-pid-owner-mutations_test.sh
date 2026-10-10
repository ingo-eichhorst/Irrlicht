#!/usr/bin/env bash
# muse-shared-pid-owner-mutations_test.sh — committed mutation fixture for
# issue #2084's muse SharedPIDOwner (core/adapters/inbound/agents/muse/pid.go
# OwnsSharedPID).
#
# WHY THIS FILE EXISTS. #2084 declares a per-root ownership probe so concurrent
# muse roots on one `muse serve` host's PID stop superseding each other. The
# concurrency tests in
# core/application/services/pid_manager_muse_shared_pid_test.go were seen red
# on the unfixed code. The retirement test's assignment case was GREEN there
# by construction — the exclusive same-PID policy retires every older root —
# so its red evidence is mutation 2. The subagent test is a lock (green before
# and after), so its red evidence is mutation 3:
#
#   1. the probe answers false for every root   → the three concurrency tests
#      go red (a live root is deleted on the assignment, periodic and seed
#      paths);
#   2. the probe answers true for every root    → the retirement test goes red
#      on every path (a root whose lock the host released is kept forever);
#   3. isDedupDeleteCandidate stops exempting subagents → the subagent lock
#      goes red on every path (a muse subagent on its parent's PID, holding no
#      lock, is retired), so that lock really reaches the exemption.
#
# The transcript_removed record for opt-in adapters is pinned by
# codex-shared-pid-owner-mutations_test.sh and is not repeated here.
#
# tools/mutate.sh owns the mechanics this file must not re-improvise: the
# stale/ambiguous-anchor guards, and the byte-for-byte restore that never
# touches git state. Modeled on codex-shared-pid-owner-mutations_test.sh.

set -uo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$DIR/../.." && pwd)"
MUTATE_SH="$REPO_ROOT/tools/mutate.sh"

need() {
  local tool="$1"
  if ! command -v "$tool" >/dev/null 2>&1; then
    echo "FAIL: muse-shared-pid-owner-mutations — $tool not found" >&2
    exit 1
  fi
  return 0
}
quote_output() { sed 's/^/      | /'; return 0; }
need go
need git

if [[ ! -x "$MUTATE_SH" ]]; then
  echo "FAIL: muse-shared-pid-owner-mutations — $MUTATE_SH is missing or not executable" >&2
  exit 1
fi

if [[ -n "$(git -C "$REPO_ROOT" status --porcelain)" ]]; then
  echo "muse-shared-pid-owner-mutations: CANNOT RUN — the worktree is dirty, and mutate.sh needs a" >&2
  echo "  clean tree for its post-restore check to mean anything. Commit or clean up, then:" >&2
  echo "    bash tools/lib/muse-shared-pid-owner-mutations_test.sh" >&2
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

PKG="./core/application/services/"
FILE="core/adapters/inbound/agents/muse/pid.go"
ANCHOR=$'\towner, err := DiscoverPID(cwd, transcriptPath, nil)\n\treturn err == nil && owner == pid'

# ── 1. no root ever proves ownership: concurrent live roots are deleted ──
# One run per path, each -run anchored to a single test, so every path has to
# go red on its own — a single shared run would pass if any one of the three
# failed and hide a sibling that silently stopped testing anything.
for path_test in \
  "assignment:TestHandlePIDAssigned_ConcurrentMuseRootsKeepServePID" \
  "periodic:TestCheckPIDLiveness_ConcurrentMuseRootsKeepServePID" \
  "seed:TestSeedPIDs_ConcurrentMuseRootsKeepServePID"; do
  assert_go_test_goes_red \
    "OwnsSharedPID answering false for a lock the host still holds (${path_test%%:*} path)" \
    "$FILE" \
    "$ANCHOR" \
    $'\treturn false' \
    "$PKG" \
    "^${path_test#*:}\$" \
    "removed a live muse root sharing the muse serve host's pid"
done

# ── 2. every root proves ownership: a released root is never retired ──
for path in assignment periodic seed; do
  assert_go_test_goes_red \
    "OwnsSharedPID answering true for a lock the host released ($path path)" \
    "$FILE" \
    "$ANCHOR" \
    $'\treturn true' \
    "$PKG" \
    "^TestSamePIDRetirement_ReleasedMuseRootIsRetiredAndRecorded\$/^${path}\$" \
    "$path path: a muse root whose lock the host released survived"
done

# ── 3. subagents stop being exempt: the subagent lock must go red ──
PM_FILE="core/application/services/pid_manager.go"
PM_ANCHOR=$'\tif victim.ParentSessionID != "" || strings.HasPrefix(victim.SessionID, "proc-") {'
for path in assignment periodic seed; do
  assert_go_test_goes_red \
    "isDedupDeleteCandidate making a muse subagent a same-PID victim ($path path)" \
    "$PM_FILE" \
    "$PM_ANCHOR" \
    $'\tif strings.HasPrefix(victim.SessionID, "proc-") {' \
    "$PKG" \
    "^TestSamePIDReconciliation_MuseSubagentOnServePIDIsNeverAVictim\$/^${path}\$" \
    "$path path removed a muse subagent sharing its parent's pid"
done

if [[ $fails -gt 0 ]]; then
  echo "muse-shared-pid-owner-mutations: $fails FAILED"
  exit 1
fi
echo "muse-shared-pid-owner-mutations: ALL PASS"
