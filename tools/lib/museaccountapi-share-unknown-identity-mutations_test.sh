#!/usr/bin/env bash
# museaccountapi-share-unknown-identity-mutations_test.sh — committed
# mutation fixture 4/4 for issue #2007's completion criterion "Share one
# account's snapshot with a session whose account identity is unknown" (§7
# mutation fixture #4).
#
# #2007 is a new guard with no "before the fix" to run red — see
# provapi-redirect-refused-mutations_test.sh's header (#2003) for the full
# rationale, shared across every ticket in this family.
#
# Muse's live probe (PR body) found no stable provider-issued account
# identifier, so core/application/services/museaccountpoll.go's
# MuseAccountQuotaKey scopes the daemon-wide AccountPoller's cache key by
# SESSION rather than by a shared/guessed account — the epic's own
# resolution for this case (#1977 line 107: "do not merge unidentified
# accounts"). The mutation collapses that back to a single shared constant,
# reproducing exactly the defect this ticket's §9.3 rejects: two different
# sessions sharing one account's cached quota reading despite neither
# session's account identity ever being confirmed as the same account.
# TestMuseAccountQuotaKey_TwoSessionsNeverCollide asserts two different
# session IDs produce two different keys AND that polling both sessions
# hits the fake transport twice (never deduped into one shared poll).
#
# tools/mutate.sh owns the mechanics this file must not re-improvise.

set -uo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$DIR/../.." && pwd)"
MUTATE_SH="$REPO_ROOT/tools/mutate.sh"

need() {
  local tool="$1"
  if ! command -v "$tool" >/dev/null 2>&1; then
    echo "FAIL: museaccountapi-share-unknown-identity-mutations — $tool not found" >&2
    exit 1
  fi
  return 0
}
quote_output() { sed 's/^/      | /'; return 0; }
need go
need git

if [[ ! -x "$MUTATE_SH" ]]; then
  echo "FAIL: museaccountapi-share-unknown-identity-mutations — $MUTATE_SH is missing or not executable" >&2
  exit 1
fi

if [[ -n "$(git -C "$REPO_ROOT" status --porcelain)" ]]; then
  echo "museaccountapi-share-unknown-identity-mutations: CANNOT RUN — the worktree is dirty, and mutate.sh needs a" >&2
  echo "  clean tree for its post-restore check to mean anything. Commit or clean up, then:" >&2
  echo "    bash tools/lib/museaccountapi-share-unknown-identity-mutations_test.sh" >&2
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
  if ! grep -qF "$want" <<<"$out"; then
    echo "FAIL: $label — the test failed, but not with the expected message."
    echo "      wanted to find: $want"
    echo "$out" | quote_output
    fails=$((fails + 1))
    return
  fi
  echo "ok  $label"
}

ANCHOR=$'func MuseAccountQuotaKey(sessionID string) AccountQuotaKey {\n\treturn AccountQuotaKey{\n\t\tProvider: session.ProviderMeta,\n\t\tAccount:  "session:" + sessionID,\n\t\tScope:    museaccountapi.DestinationKey,\n\t}\n}'
REPLACEMENT=$'func MuseAccountQuotaKey(sessionID string) AccountQuotaKey {\n\treturn AccountQuotaKey{\n\t\tProvider: session.ProviderMeta,\n\t\tAccount:  "shared",\n\t\tScope:    museaccountapi.DestinationKey,\n\t}\n}'

# ── the poll key collapses to a shared constant regardless of sessionID ───
assert_go_test_goes_red \
  "collapsing the per-session account key to a shared constant lets two unconfirmed sessions share one poll" \
  "core/application/services/museaccountpoll.go" \
  "$ANCHOR" \
  "$REPLACEMENT" \
  "./core/application/services/..." \
  "TestMuseAccountQuotaKey_TwoSessionsNeverCollide" \
  "MuseAccountQuotaKey produced the same key for two different sessions"

if [[ $fails -gt 0 ]]; then
  echo "museaccountapi-share-unknown-identity-mutations: $fails FAILED"
  exit 1
fi
echo "museaccountapi-share-unknown-identity-mutations: ALL PASS"
