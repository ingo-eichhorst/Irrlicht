#!/usr/bin/env bash
# museaccountapi-token-hash-account-id-mutations_test.sh — committed
# mutation fixture 1/4 for issue #2007's completion criterion "Derive the
# account ID from a token hash" (§7 mutation fixture #1).
#
# #2007 is a new guard with no "before the fix" to run red — see
# provapi-redirect-refused-mutations_test.sh's header (#2003) for the full
# rationale, shared across every ticket in this family.
#
# core/adapters/outbound/museaccountapi/parser.go's BuildSnapshot has no
# credential in scope at all (by design — the credential never crosses out
# of CredentialResolver.Resolve except through accountquota's transport), so
# the literal defect the ticket names ("hash the access token") cannot be
# written at this call site. The mutation instead derives
# ConfirmedAccountRef from the raw response BODY, the closest
# identity-shaped data actually reachable here — the same defect class as
# hashing the credential (manufacturing an account identifier instead of
# leaving it unconfirmed, issue #2007 §1.3) and the one place BuildSnapshot
# could smuggle either kind of derived value into ConfirmedAccountRef.
# TestBuildSnapshot_NeverDerivesAnAccountRef asserts ConfirmedAccountRef ==
# "" across several fixtures, independent of what identity-shaped data the
# input JSON carries.
#
# tools/mutate.sh owns the mechanics this file must not re-improvise.

set -uo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$DIR/../.." && pwd)"
MUTATE_SH="$REPO_ROOT/tools/mutate.sh"

need() {
  local tool="$1"
  if ! command -v "$tool" >/dev/null 2>&1; then
    echo "FAIL: museaccountapi-token-hash-account-id-mutations — $tool not found" >&2
    exit 1
  fi
  return 0
}
quote_output() { sed 's/^/      | /'; return 0; }
need go
need git

if [[ ! -x "$MUTATE_SH" ]]; then
  echo "FAIL: museaccountapi-token-hash-account-id-mutations — $MUTATE_SH is missing or not executable" >&2
  exit 1
fi

if [[ -n "$(git -C "$REPO_ROOT" status --porcelain)" ]]; then
  echo "museaccountapi-token-hash-account-id-mutations: CANNOT RUN — the worktree is dirty, and mutate.sh needs a" >&2
  echo "  clean tree for its post-restore check to mean anything. Commit or clean up, then:" >&2
  echo "    bash tools/lib/museaccountapi-token-hash-account-id-mutations_test.sh" >&2
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

# ── ConfirmedAccountRef derived from the response body instead of left empty ──
assert_go_test_goes_red \
  "deriving ConfirmedAccountRef from response data instead of leaving it unconfirmed" \
  "core/adapters/outbound/museaccountapi/parser.go" \
  $'ConfirmedAccountRef: accountRef(),' \
  $'ConfirmedAccountRef: fmt.Sprintf("muse-%x", body),' \
  "./core/adapters/outbound/museaccountapi/..." \
  "TestBuildSnapshot_NeverDerivesAnAccountRef" \
  "issue #2007"

if [[ $fails -gt 0 ]]; then
  echo "museaccountapi-token-hash-account-id-mutations: $fails FAILED"
  exit 1
fi
echo "museaccountapi-token-hash-account-id-mutations: ALL PASS"
