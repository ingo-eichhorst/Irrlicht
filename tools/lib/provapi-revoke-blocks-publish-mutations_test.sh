#!/usr/bin/env bash
# provapi-revoke-blocks-publish-mutations_test.sh — committed mutation
# fixture 5/5 for issue #2003's daemon-wide account-quota poller
# (core/application/services/accountpoller.go).
#
# #2003 is all new guards with no "before the fix" to run red — see
# provapi-redirect-refused-mutations_test.sh's header for the full rationale,
# shared verbatim across this ticket's five fixtures.
#
# The mutation removes doFetch's final `req.Granted()` recheck before
# publishing a successful fetch, reproducing #2003 §1.1's "[revocation must]
# prevent a later publication of a result that work already produced" as a
# real defect: a fetch admitted while granted, whose response completes
# successfully AFTER the permission is revoked, would then be published
# anyway. TestAccountPoller_RevokedDuringInFlightRequestNeverPublishes is
# deliberately isolated from the SEPARATE
# TestAccountPoller_RevokeCancelsInFlightRequest test (which proves
# Revoke's context cancellation stops a request outright) — this one lets
# the fake transport answer successfully and only flips the Granted
# predicate, so it exercises the recheck in isolation from cancellation
# timing, with no sleep anywhere: every step is gated by a channel signal.
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
    echo "FAIL: provapi-revoke-blocks-publish-mutations — $tool not found" >&2
    exit 1
  fi
  return 0
}
quote_output() { sed 's/^/      | /'; return 0; }
need go
need git

if [[ ! -x "$MUTATE_SH" ]]; then
  echo "FAIL: provapi-revoke-blocks-publish-mutations — $MUTATE_SH is missing or not executable" >&2
  exit 1
fi

if [[ -n "$(git -C "$REPO_ROOT" status --porcelain)" ]]; then
  echo "provapi-revoke-blocks-publish-mutations: CANNOT RUN — the worktree is dirty, and mutate.sh needs a" >&2
  echo "  clean tree for its post-restore check to mean anything. Commit or clean up, then:" >&2
  echo "    bash tools/lib/provapi-revoke-blocks-publish-mutations_test.sh" >&2
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

# ── doFetch's pre-publish Granted() recheck is removed ─────────────────────
assert_go_test_goes_red \
  "removing the pre-publish Granted() recheck publishes a result fetched after revoke" \
  "core/application/services/accountpoller.go" \
  $'\tif req.Granted == nil || !req.Granted() {\n\t\treturn p.notPublished(key, inflight), errRevokedBeforePublish\n\t}\n\n\treturn p.publish(key, inflight, resp.Body), nil' \
  $'\treturn p.publish(key, inflight, resp.Body), nil' \
  "./core/application/services/..." \
  "TestAccountPoller_RevokedDuringInFlightRequestNeverPublishes" \
  "want errRevokedBeforePublish"

if [[ $fails -gt 0 ]]; then
  echo "provapi-revoke-blocks-publish-mutations: $fails FAILED"
  exit 1
fi
echo "provapi-revoke-blocks-publish-mutations: ALL PASS"
