#!/usr/bin/env bash
# fswatcher-rearm-mutations_test.sh — committed mutation fixture for issue #2098.
#
# #2098 adds the rearm queue: a directory created at runtime is registered a
# second time after rearmSettle. That repairs a registration that fsnotify's
# kqueue reader downgraded (fsnotify/fsnotify#783). The repair is new, so it
# has no "before the fix" to run red. Each mutation below removes one link of
# the chain and requires TestRearmDue_RepairsARuntimeDirWhoseWatchWasDowngraded
# to go red with that link's own message. tools/mutate.sh owns the mechanics
# (exact-once anchor, byte-for-byte restore) and this file must not
# re-improvise them.

set -uo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$DIR/../.." && pwd)"
MUTATE_SH="$REPO_ROOT/tools/mutate.sh"
NAME=fswatcher-rearm-mutations

need() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "FAIL: $NAME — $1 not found" >&2
    exit 1
  fi
  return 0
}
quote_output() { sed 's/^/      | /'; return 0; }
need go
need git

if [[ ! -x "$MUTATE_SH" ]]; then
  echo "FAIL: $NAME — $MUTATE_SH is missing or not executable" >&2
  exit 1
fi

if [[ -n "$(git -C "$REPO_ROOT" status --porcelain)" ]]; then
  echo "$NAME: CANNOT RUN — the worktree is dirty, and mutate.sh needs a clean tree" >&2
  echo "  for its post-restore check to mean anything. Commit or clean up, then:" >&2
  echo "    bash tools/lib/$NAME""_test.sh" >&2
  if [[ -n "${CI:-}" || -n "${MUTATION_FIXTURES_STRICT:-}" ]]; then
    echo "  (failing rather than skipping: CI/strict mode, where a skip is indistinguishable" >&2
    echo "   from a pass)" >&2
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
    echo "FAIL: $label — mutate.sh refused (exit $rc). A stale or ambiguous anchor means the"
    echo "      guard's source moved and this fixture needs updating — not that the guard is fine."
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
    echo "FAIL: $label — the mutation broke the BUILD rather than the guard."
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

PKG=./core/adapters/inbound/agents/fswatcher/
RUN='^TestRearmDue_RepairsARuntimeDirWhoseWatchWasDowngraded$'
FILE=core/adapters/inbound/agents/fswatcher/watcher.go

# 1. handleDirCreate stops queueing the re-arm.
assert_go_test_goes_red \
  "handleDirCreate no longer queues a re-arm" \
  "$FILE" \
  $'\t\tw.queueRearm(name)\n' \
  $'\t\t// mutation: re-arm not queued\n' \
  "$PKG" "$RUN" \
  "no re-arm was scheduled for a directory created at runtime"

# 2. The re-arm walks the subtree but skips the forced second Add (addWatch
# then short-circuits on watched, which is the pre-#2098 behaviour).
assert_go_test_goes_red \
  "rearmDue no longer forces a second Add past the watched short-circuit" \
  "$FILE" \
  $'\t\t\tif reregister {\n\t\t\t\tdelete(w.watched, p)\n\t\t\t}\n' \
  $'\t\t\t// mutation: no forced re-registration\n' \
  "$PKG" "$RUN" \
  "its NOTE_WRITE registration was not restored"

if [[ $fails -gt 0 ]]; then
  echo "$NAME: $fails FAILED"
  exit 1
fi
echo "$NAME: ALL PASS"
