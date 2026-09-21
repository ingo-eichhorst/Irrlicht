#!/usr/bin/env bash
# provapi-cache-key-account-mutations_test.sh — committed mutation fixture
# 4/5 for issue #2003's daemon-wide account-quota poller
# (core/application/services/accountpoller.go).
#
# #2003 is all new guards with no "before the fix" to run red — see
# provapi-redirect-refused-mutations_test.sh's header for the full rationale,
# shared verbatim across this ticket's five fixtures.
#
# The mutation changes cacheKeyFor — the ONE function that decides what key
# admits a Poll call into the dedup/cache map — to key by req.SessionID
# instead of the confirmed account in req.Key, reproducing #2003 §1.4's "more
# sessions ... must not multiply account API calls" as a real defect: ten
# sessions sharing one confirmed account would each get their own cache
# entry and each trigger their own outbound fetch.
# TestAccountPoller_TenSessionsOneConfirmedAccountIsOnePoll drives this
# through ten concurrent Poll calls against one AccountQuotaKey (differing
# only in SessionID) and asserts the fake transport was called exactly once.
#
# tools/mutate.sh owns the mechanics this file must not re-improvise. Modeled
# on tools/lib/cost-unattributed-mutations_test.sh (PR #1999) and
# tools/lib/pi-provider-absent-not-confirmed-mutations_test.sh (PR #2017).

set -uo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$DIR/../.." && pwd)"
MUTATE_SH="$REPO_ROOT/tools/mutate.sh"

need() {
  local tool="$1"
  if ! command -v "$tool" >/dev/null 2>&1; then
    echo "FAIL: provapi-cache-key-account-mutations — $tool not found" >&2
    exit 1
  fi
  return 0
}
quote_output() { sed 's/^/      | /'; return 0; }
need go
need git

if [[ ! -x "$MUTATE_SH" ]]; then
  echo "FAIL: provapi-cache-key-account-mutations — $MUTATE_SH is missing or not executable" >&2
  exit 1
fi

if [[ -n "$(git -C "$REPO_ROOT" status --porcelain)" ]]; then
  echo "provapi-cache-key-account-mutations: CANNOT RUN — the worktree is dirty, and mutate.sh needs a" >&2
  echo "  clean tree for its post-restore check to mean anything. Commit or clean up, then:" >&2
  echo "    bash tools/lib/provapi-cache-key-account-mutations_test.sh" >&2
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

# ── cacheKeyFor keys by session instead of by confirmed account ───────────
assert_go_test_goes_red \
  "keying the cache by SessionID instead of the confirmed account multiplies polls per session" \
  "core/application/services/accountpoller.go" \
  $'func cacheKeyFor(req PollRequest) AccountQuotaKey {\n\treturn req.Key\n}' \
  $'func cacheKeyFor(req PollRequest) AccountQuotaKey {\n\treturn AccountQuotaKey{Provider: req.Key.Provider, Account: req.SessionID, Scope: req.Key.Scope}\n}' \
  "./core/application/services/..." \
  "TestAccountPoller_TenSessionsOneConfirmedAccountIsOnePoll" \
  "want exactly 1 (ten sessions, one confirmed account)"

if [[ $fails -gt 0 ]]; then
  echo "provapi-cache-key-account-mutations: $fails FAILED"
  exit 1
fi
echo "provapi-cache-key-account-mutations: ALL PASS"
