#!/usr/bin/env bash
# replaced-session-revival-mutations_test.sh — committed mutation fixture for
# issue #2059's gates on reviving a session that the same-pid cleanup deleted
# (core/application/services/session_detector_replaced_revival.go).
#
# A hook revives such a session only when (1) the activity is a hook, (2) the
# adapter's PID discovery for the session still names the pid it lost, (3) no
# other root session holding that pid has a fresh transcript, and (4) the
# replacement record still belongs to the deletion that made it. These are
# guards the change ADDS, so there is no "before the fix" to run red; each
# mutation below removes one and requires its test to go red:
#   - dropping the Synthetic check
#     (TestSessionDetector_ReplacedNotRevivedByTranscriptEvent_Issue2059);
#   - dropping the discovery check
#     (TestSessionDetector_ReplacedNotRevivedWhenDiscoveryDeclines_Issue2059);
#   - dropping the active-holder check
#     (TestSessionDetector_ReplacedNotRevivedWhileHolderActive_Issue2059);
#   - never dropping an armed record
#     (TestSessionDetector_LaterDeletionDropsReplacementRecord_Issue2059).
#
# tools/mutate.sh owns the mechanics this file must not re-improvise.

set -uo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$DIR/../.." && pwd)"
MUTATE_SH="$REPO_ROOT/tools/mutate.sh"

need() {
  local tool="$1"
  if ! command -v "$tool" >/dev/null 2>&1; then
    echo "FAIL: replaced-session-revival-mutations — $tool not found" >&2
    exit 1
  fi
  return 0
}
quote_output() { sed 's/^/      | /'; return 0; }
need go
need git

if [[ ! -x "$MUTATE_SH" ]]; then
  echo "FAIL: replaced-session-revival-mutations — $MUTATE_SH is missing or not executable" >&2
  exit 1
fi

if [[ -n "$(git -C "$REPO_ROOT" status --porcelain)" ]]; then
  echo "replaced-session-revival-mutations: CANNOT RUN — the worktree is dirty, and mutate.sh needs a" >&2
  echo "  clean tree for its post-restore check to mean anything. Commit or clean up, then:" >&2
  echo "    bash tools/lib/replaced-session-revival-mutations_test.sh" >&2
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

FILE="core/application/services/session_detector_replaced_revival.go"
PKG="./core/application/services/..."

# ── only hook activity counts ──
assert_go_test_goes_red \
  "reviving on a non-hook transcript event" \
  "$FILE" \
  $'\tif !ev.Synthetic {\n\t\treturn notRevivedNotHookMsg\n\t}\n' \
  $'\n' \
  "$PKG" \
  "TestSessionDetector_ReplacedNotRevivedByTranscriptEvent_Issue2059" \
  "was revived by a stale-transcript watcher event"

# ── adapter discovery must name the lost pid ──
assert_go_test_goes_red \
  "reviving when adapter discovery does not name the lost pid" \
  "$FILE" \
  $'\tif d.pidMgr.DiscoverPIDOnly(ev.SessionID, rec.adapter, rec.cwd, ev.TranscriptPath) != rec.pid {\n\t\treturn notRevivedNoPIDMatchMsg\n\t}\n' \
  $'\n' \
  "$PKG" \
  "TestSessionDetector_ReplacedNotRevivedWhenDiscoveryDeclines_Issue2059" \
  "adapter discovery does not name its pid"

# ── an active holder keeps the pid ──
assert_go_test_goes_red \
  "reviving over an active holder of the pid" \
  "$FILE" \
  $'\tif d.pidHolderActive(ev.SessionID, rec.pid) {\n\t\treturn notRevivedHolderActiveMsg\n\t}\n' \
  $'\n' \
  "$PKG" \
  "TestSessionDetector_ReplacedNotRevivedWhileHolderActive_Issue2059" \
  "over an active holder of its pid"

# ── a later deletion drops the record ──
assert_go_test_goes_red \
  "keeping a replacement record across a later deletion" \
  "$FILE" \
  $'\tif rec.armed {\n\t\tdelete(d.replacedSessions, sessionID)\n\t\treturn\n\t}' \
  $'\tif false {\n\t\tdelete(d.replacedSessions, sessionID)\n\t\treturn\n\t}' \
  "$PKG" \
  "TestSessionDetector_LaterDeletionDropsReplacementRecord_Issue2059" \
  "left from an earlier deletion"

if [[ $fails -gt 0 ]]; then
  echo "replaced-session-revival-mutations: $fails FAILED"
  exit 1
fi
echo "replaced-session-revival-mutations: ALL PASS"
