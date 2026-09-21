#!/usr/bin/env bash
# provapi-redirect-refused-mutations_test.sh — committed mutation fixture 1/5
# for issue #2003's account-quota transport (core/adapters/outbound/accountquota).
#
# WHY THIS FILE EXISTS. #2003 is all new guards — the transport, credential
# resolver and poller it adds have no "before the fix" to run red, because
# there was no account-quota transport at all before this ticket. Per
# AGENTS.md/docs/testing-philosophy.md, a guard like this earns its place by
# having its protected behavior MUTATED and observed to fail, not by having
# existed in a broken state once.
#
# The mutation replaces HTTPTransport's redirect policy
# (`CheckRedirect: refuseAllRedirects`) with Go's default (`CheckRedirect:
# nil`, which follows up to 10 redirects) — exactly #2003 §1.2's "an explicit
# redirect policy" and §6's "a stub that answers 302 to another host: the
# redirect policy refuses it" turned inside out. Both test servers in
# TestHTTPTransport_RefusesCrossHostRedirect are loopback httptest.Server
# instances (never a real host), so even under this mutation nothing here
# reaches beyond 127.0.0.1 — the test's own dial guard asserts that directly
# rather than relying on the destination URLs alone.
#
# tools/mutate.sh owns the mechanics this file must not re-improvise: the
# stale/ambiguous-anchor guards, and the byte-for-byte restore that never
# touches git state (worktrees share the parent repo's .git dir, so `git
# checkout --` / `git restore` / `git reset --hard` are banned repo-wide).
# Modeled on tools/lib/cost-unattributed-mutations_test.sh (PR #1999) and
# tools/lib/pi-provider-absent-not-confirmed-mutations_test.sh (PR #2017).

set -uo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$DIR/../.." && pwd)"
MUTATE_SH="$REPO_ROOT/tools/mutate.sh"

need() {
  local tool="$1"
  if ! command -v "$tool" >/dev/null 2>&1; then
    echo "FAIL: provapi-redirect-refused-mutations — $tool not found" >&2
    exit 1
  fi
  return 0
}
quote_output() { sed 's/^/      | /'; return 0; }
need go
need git

if [[ ! -x "$MUTATE_SH" ]]; then
  echo "FAIL: provapi-redirect-refused-mutations — $MUTATE_SH is missing or not executable" >&2
  exit 1
fi

if [[ -n "$(git -C "$REPO_ROOT" status --porcelain)" ]]; then
  echo "provapi-redirect-refused-mutations: CANNOT RUN — the worktree is dirty, and mutate.sh needs a" >&2
  echo "  clean tree for its post-restore check to mean anything. Commit or clean up, then:" >&2
  echo "    bash tools/lib/provapi-redirect-refused-mutations_test.sh" >&2
  if [[ -n "${CI:-}" || -n "${MUTATION_FIXTURES_STRICT:-}" ]]; then
    echo "  (failing rather than skipping: CI/strict mode, where a skip is indistinguishable" >&2
    echo "   from a pass and this gate is the only thing re-running this mutation)" >&2
    exit 1
  fi
  echo "  (skipped locally; set MUTATION_FIXTURES_STRICT=1 to make this a failure)" >&2
  exit 0
fi

fails=0

# assert_go_test_goes_red <label> <file> <anchor> <replacement> <pkg> <run-regex> <want-in-output>
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

# ── HTTPTransport follows a cross-host redirect instead of refusing it ─────
assert_go_test_goes_red \
  "removing the redirect refusal lets the transport follow a redirect to a second host" \
  "core/adapters/outbound/accountquota/transport.go" \
  $'CheckRedirect: refuseAllRedirects,' \
  $'CheckRedirect: nil,' \
  "./core/adapters/outbound/accountquota/..." \
  "TestHTTPTransport_RefusesCrossHostRedirect" \
  "expected a redirect_refused QuotaError, got err=<nil>"

if [[ $fails -gt 0 ]]; then
  echo "provapi-redirect-refused-mutations: $fails FAILED"
  exit 1
fi
echo "provapi-redirect-refused-mutations: ALL PASS"
