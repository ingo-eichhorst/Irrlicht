#!/usr/bin/env bash
# cloud-attribution-mutations_test.sh — the six committed mutation fixtures
# for issue #2013: the four §7 requires, plus two more for guards that review
# added. Every rule this ticket adds is a NEW guard, so none
# of them has a "before the fix" to run red (docs/testing-philosophy.md:
# "Anything a change ADDS ... has no 'before the fix' to run red — mutate the
# thing it protects instead"). Each entry below flips one rule and requires
# the test that protects it to fail, and to fail for the RIGHT reason.
#
#   1. Read litellm_provider as the billing provider. #1977 §3.1 forbids
#      promoting catalog metadata to an authoritative billing field, and
#      #2013 §1.1 names core/domain/session/billing_attribution.go as where
#      the temptation is strongest.
#   2. Treat a loopback endpoint as proven local inference (hence a zero
#      charge). #1977 §3.3: a local gateway can forward to a paid upstream.
#   3. Present a throughput ceiling as a subscription allowance. #1977 §7:
#      "Do not interpret a throughput limit as a subscription allowance
#      without evidence."
#   4. Collapse "known gateway, unknown account" into a confirmed
#      attribution. #1977 §3.1 permits the partially-known result explicitly;
#      this is the mutation that destroys it.
#   5. Let an UNRECOGNISED LimitKind read as an allowance. Added after review
#      measured the loose `!= LimitKindThroughput` form surfacing a window
#      spelled "throughtput" at 92%.
#   6. Let ForecastCap pair an allowance with a throughput ceiling that
#      happens to share its reset instant.
#
# Structure follows tools/lib/endpoint-route-mutations_test.sh (#2002) exactly
# — same assert_go_test_goes_red shape, same tools/mutate.sh underneath, same
# dirty-tree policy. tools/mutate.sh owns the mechanics this file must not
# re-improvise: the stale/ambiguous-anchor guards (a mutation matching zero or
# more than one site fails loudly instead of silently doing nothing), and the
# byte-for-byte restore that never touches git state (worktrees share the
# parent repo's .git dir, so `git checkout --` / `git restore` /
# `git reset --hard` are banned repo-wide).

set -uo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$DIR/../.." && pwd)"
MUTATE_SH="$REPO_ROOT/tools/mutate.sh"

# A missing tool is a hard failure, not a skip — exiting 0 here would read as
# a PASS to preflight's shell_lib_tests, so the gate would go green having
# asserted nothing.
need() {
  local tool="$1"
  if ! command -v "$tool" >/dev/null 2>&1; then
    echo "FAIL: cloud-attribution-mutations — $tool not found" >&2
    exit 1
  fi
  return 0
}

# Indents captured output so it reads as a quoted block under its FAIL line.
quote_output() {
  sed 's/^/      | /'
  return 0
}

need go
need git

if [[ ! -x "$MUTATE_SH" ]]; then
  echo "FAIL: cloud-attribution-mutations — $MUTATE_SH is missing or not executable" >&2
  exit 1
fi

# mutate.sh refuses (exit 4) against an already-dirty tree, because its
# post-restore emptiness check could prove nothing then.
#
# A DIRTY TREE MUST NOT SILENTLY PASS: this suite's runner (shell-lib-suite.sh)
# judges a script by its EXIT STATUS and has no self-skip protocol, so an
# `exit 0` here would make "the guards were verified" and "the guards could not
# be checked at all" produce byte-identical results at the gate. So it is a
# HARD FAILURE wherever the answer is load-bearing (CI, and any caller that
# sets MUTATION_FIXTURES_STRICT=1), and a loud, non-silent skip on a
# developer's dirty worktree, where failing would only train people to delete
# the fixture.
if [[ -n "$(git -C "$REPO_ROOT" status --porcelain)" ]]; then
  echo "cloud-attribution-mutations: CANNOT RUN — the worktree is dirty, and mutate.sh needs a" >&2
  echo "  clean tree for its post-restore check to mean anything. Commit or clean up, then:" >&2
  echo "    bash tools/lib/cloud-attribution-mutations_test.sh" >&2
  if [[ -n "${CI:-}" || -n "${MUTATION_FIXTURES_STRICT:-}" ]]; then
    echo "  (failing rather than skipping: CI/strict mode, where a skip is indistinguishable" >&2
    echo "   from a pass and this gate is the only thing re-running these mutations)" >&2
    exit 1
  fi
  echo "  (skipped locally; set MUTATION_FIXTURES_STRICT=1 to make this a failure)" >&2
  exit 0
fi

fails=0

DOMAIN_PKG="./core/domain/session/..."
ATTRIBUTION_GO="core/domain/session/billing_attribution.go"
RATELIMIT_GO="core/domain/session/rate_limit.go"

# assert_go_test_goes_red <label> <file> <anchor> <replacement> <pkg> <run-regex> <want-in-output>
#
# Applies one mutation and requires the named Go test to FAIL under it, AND
# to fail for the right reason. Both halves matter: a mutation that leaves
# the test green means the guard does not reach what it claims to protect,
# and a mutation that goes red because the package no longer COMPILES would
# otherwise read as success.
assert_go_test_goes_red() {
  local label="$1" file="$2" anchor="$3" replacement="$4" pkg="$5" run="$6" want="$7"
  local out rc

  out="$(cd "$REPO_ROOT" && "$MUTATE_SH" "$file" "$anchor" "$replacement" \
    bash -c "go test $pkg -run '$run' -count=1 2>&1; echo GO_TEST_RC=\$?" 2>&1)"
  rc=$?

  if [[ $rc -ne 0 ]]; then
    echo "FAIL: $label — mutate.sh refused (exit $rc). A STALE anchor means the surrounding text"
    echo "      moved and this fixture needs its anchor updated; it does NOT mean the guard is fine."
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

# ── 1. litellm_provider read as the billing provider ────────────────────────
# The catalog hint is retained as supporting evidence and stops there. Letting
# it reach Provider is exactly the promotion #1977 §3.1 forbids: the same
# Claude model is sold by Anthropic, by AWS Bedrock and by Google Vertex AI,
# so "where this model can be served" is not "who billed this request".
assert_go_test_goes_red \
  "promoting the catalog hint to the billing provider reddens the hint test" \
  "$ATTRIBUTION_GO" \
  'a.CatalogHint = catalogHint' \
  'a.CatalogHint, a.Provider = catalogHint, catalogHint // mutated: catalog hint promoted to biller (#2013 fixture)' \
  "$DOMAIN_PKG" \
  "TestResolveBillingAttribution_CatalogHintNeverBecomesTheProvider" \
  'a catalog hint is not a biller'

# ── 2. loopback read as proven local inference ──────────────────────────────
# A loopback or private-network address means the endpoint is local. Deciding
# from it that inference ran here — and therefore that nothing was billed — is
# the inference #1977 §3.3 and #2013 §1.2 both forbid.
assert_go_test_goes_red \
  "reading a loopback endpoint as a proven local runtime reddens the loopback test" \
  "$ATTRIBUTION_GO" \
  'a.Outcome = OutcomeLocalOrPrivateEndpoint' \
  'a.Outcome = OutcomeLocalRuntime // mutated: loopback treated as proven local inference (#2013 fixture)' \
  "$DOMAIN_PKG" \
  "TestResolveBillingAttribution_LoopbackIsNeverARuntimeOrAZeroCharge" \
  'a loopback address is an endpoint finding'

# ── 3. throughput ceiling rendered as a subscription allowance ──────────────
# Bedrock, Vertex and Azure meter requests per minute. ImminentWindow is what
# a quota chip and ForecastCap both read, so admitting a service ceiling here
# is what would put a throughput limit on a plan-allowance display.
#
# NOTE ON THE ANCHOR: `if !w.IsSubscriptionAllowance() {` occurs TWICE in
# rate_limit.go — once here and once in ForecastCap's prev-window match below
# — so it is ambiguous on its own and mutate.sh refuses it with exit 8. Each
# site therefore carries a distinguishing trailing comment, and the anchors
# below include it. Verified by running this file, not by reading it: the
# ambiguous form was refused before the comments were added.
assert_go_test_goes_red \
  "admitting throughput windows to ImminentWindow reddens the allowance test" \
  "$RATELIMIT_GO" \
  'if !w.IsSubscriptionAllowance() { // #2013: a ceiling is not the chip to show' \
  'if false { // mutated: throughput ceilings admitted as allowances (#2013 fixture)' \
  "$DOMAIN_PKG" \
  "TestImminentWindow_SkipsThroughputWindows" \
  'ImminentWindow returned a throughput ceiling'

# ── 4. "known gateway, unknown account" collapsed into confirmed ────────────
# The partially-known result is the honest one (#1977 §3.1). Promoting it to
# confirmed is how a Bedrock- or gateway-routed session silently acquires the
# adapter's usual provider — the misattribution #1994 removed once already.
assert_go_test_goes_red \
  "collapsing gateway-known-account-unknown into confirmed reddens the gateway test" \
  "$ATTRIBUTION_GO" \
  'a.Quality = BillingQualityGatewayKnownAccountUnknown' \
  'a.Quality = BillingQualityConfirmed // mutated: partially-known attribution collapsed (#2013 fixture)' \
  "$DOMAIN_PKG" \
  "TestResolveBillingAttribution_GatewayKeepsTheAccountUnknown" \
  'the partially-known result must survive'

# ── 5. an unrecognised LimitKind reading as an allowance ────────────────────
# The loose form below shipped in this ticket's first commit and review caught
# it: `!= LimitKindThroughput` lets EVERY value but the exact string
# "throughput" through, so a window spelled "throughtput" — outside LimitKinds,
# recognised by nothing — was surfaced by ImminentWindow at 92%. The fix fails
# closed; this mutation restores the loose form and requires the guard to
# notice. AGENTS.md: a validator that cannot read its input checks MORE.
assert_go_test_goes_red \
  "letting an unrecognised LimitKind read as an allowance reddens the fail-closed test" \
  "$RATELIMIT_GO" \
  'return w.LimitKind == "" || w.LimitKind == LimitKindAllowance' \
  'return w.LimitKind != LimitKindThroughput // mutated: unrecognised kinds read as allowances (#2013 fixture)' \
  "$DOMAIN_PKG" \
  "TestIsSubscriptionAllowance_FailsClosedOnAnUnrecognisedKind" \
  'an unrecognised kind must fail closed'

# ── 6. ForecastCap pairing an allowance with a throughput ceiling ───────────
# ImminentWindow guarantees the LATEST sample is an allowance, but ForecastCap
# matches the earlier one on duration and reset instant alone. Without this
# skip a ceiling sharing a reset time pairs with an allowance and contributes
# its slope to a plan-cap forecast.
assert_go_test_goes_red \
  "dropping ForecastCap's allowance check reddens the quota-pairing test" \
  "$RATELIMIT_GO" \
  'if !w.IsSubscriptionAllowance() { // #2013: pair like quota with like' \
  'if false { // mutated: any window may pair with an allowance (#2013 fixture)' \
  "$DOMAIN_PKG" \
  "TestForecastCap_WillNotPairAnAllowanceWithAThroughputWindow" \
  'the earlier sample was a throughput ceiling'

if [[ $fails -gt 0 ]]; then
  echo "cloud-attribution-mutations: $fails mutation(s) did not behave as required" >&2
  exit 1
fi
echo "cloud-attribution-mutations: ok"
