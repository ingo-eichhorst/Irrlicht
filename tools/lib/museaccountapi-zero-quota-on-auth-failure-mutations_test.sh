#!/usr/bin/env bash
# museaccountapi-zero-quota-on-auth-failure-mutations_test.sh — committed
# mutation fixture 3/4 for issue #2007's completion criterion "Publish a
# zero quota on an authentication failure" (§7 mutation fixture #3).
#
# #2007 is a new guard with no "before the fix" to run red — see
# provapi-redirect-refused-mutations_test.sh's header (#2003) for the full
# rationale, shared across every ticket in this family.
#
# #2003's own AccountPoller.recordFailure already guards the LOWER layer
# (never sets Observation.HasValue on a failure —
# provapi-no-zero-on-401-mutations_test.sh protects that). This fixture
# protects the layer #2007 adds on top:
# core/application/services/museaccountpoll.go's MuseAccountRefresh, the
# caller-facing composition that turns an Observation into a
# session.RateLimitSnapshot. The mutation makes its !obs.HasValue branch
# manufacture an empty (zero-percent) snapshot instead of returning an error
# — reproducing "0% used" for a session whose credential was actually
# rejected, exactly what issue #2003 §1.4 and the pinned herdr-agent-quota
# source ("never '0% used'") both warn against.
# TestMuseAccountRefresh_NeverPublishesZeroOnFailure drives an
# auth-rejected fetch through MuseAccountRefresh and asserts it returns
# (nil, non-nil error).
#
# tools/mutate.sh owns the mechanics this file must not re-improvise.

set -uo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$DIR/../.." && pwd)"
MUTATE_SH="$REPO_ROOT/tools/mutate.sh"

need() {
  local tool="$1"
  if ! command -v "$tool" >/dev/null 2>&1; then
    echo "FAIL: museaccountapi-zero-quota-on-auth-failure-mutations — $tool not found" >&2
    exit 1
  fi
  return 0
}
quote_output() { sed 's/^/      | /'; return 0; }
need go
need git

if [[ ! -x "$MUTATE_SH" ]]; then
  echo "FAIL: museaccountapi-zero-quota-on-auth-failure-mutations — $MUTATE_SH is missing or not executable" >&2
  exit 1
fi

if [[ -n "$(git -C "$REPO_ROOT" status --porcelain)" ]]; then
  echo "museaccountapi-zero-quota-on-auth-failure-mutations: CANNOT RUN — the worktree is dirty, and mutate.sh needs a" >&2
  echo "  clean tree for its post-restore check to mean anything. Commit or clean up, then:" >&2
  echo "    bash tools/lib/museaccountapi-zero-quota-on-auth-failure-mutations_test.sh" >&2
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

ANCHOR=$'\tif !obs.HasValue {\n\t\tif obs.FailureReason != "" {\n\t\t\treturn nil, fmt.Errorf("services: Muse account quota not available: %s", obs.FailureReason)\n\t\t}\n\t\treturn nil, errors.New("services: Muse account quota not available yet")\n\t}'
REPLACEMENT=$'\tif !obs.HasValue {\n\t\tzero := session.RateLimitSnapshot{}\n\t\treturn &zero, nil\n\t}'

# ── no cached value yet manufactures an empty snapshot instead of erroring ──
assert_go_test_goes_red \
  "manufacturing a zero-value RateLimitSnapshot instead of returning an error on failure" \
  "core/application/services/museaccountpoll.go" \
  "$ANCHOR" \
  "$REPLACEMENT" \
  "./core/application/services/..." \
  "TestMuseAccountRefresh_NeverPublishesZeroOnFailure" \
  "expected an error on the immediate retry"

if [[ $fails -gt 0 ]]; then
  echo "museaccountapi-zero-quota-on-auth-failure-mutations: $fails FAILED"
  exit 1
fi
echo "museaccountapi-zero-quota-on-auth-failure-mutations: ALL PASS"
