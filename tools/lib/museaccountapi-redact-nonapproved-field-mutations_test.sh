#!/usr/bin/env bash
# museaccountapi-redact-nonapproved-field-mutations_test.sh — committed
# mutation fixture 2/4 for issue #2007's completion criterion "Retain a
# non-approved field from the response" (§7 mutation fixture #2).
#
# #2007 is a new guard with no "before the fix" to run red — see
# provapi-redirect-refused-mutations_test.sh's header (#2003) for the full
# rationale, shared across every ticket in this family.
#
# core/adapters/outbound/museaccountapi/redact.go's RedactSubscriptionResponse
# decodes the raw response through subscriptionResponse's own strict
# allowlist (parser.go), so a non-approved field cannot reach the redacted
# output through THAT decode by construction. The mutation reproduces the
# real-world shape of this defect anyway: a second, unconstrained decode
# (into a generic map, exactly what a hurried fix might add "just to grab
# one more field") that smuggles a non-approved value (user_email) into an
# already-approved field's text. TestRedactSubscriptionResponse_DropsNonApprovedFields
# asserts the redacted output's key set is EXACTLY the approved allowlist
# AND that none of several forbidden values appear anywhere in the output
# bytes — the second check is what this specific mutation trips, since the
# leaked value lands inside subs_tier_name's own string rather than as a new
# top-level key.
#
# tools/mutate.sh owns the mechanics this file must not re-improvise.

set -uo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$DIR/../.." && pwd)"
MUTATE_SH="$REPO_ROOT/tools/mutate.sh"

need() {
  local tool="$1"
  if ! command -v "$tool" >/dev/null 2>&1; then
    echo "FAIL: museaccountapi-redact-nonapproved-field-mutations — $tool not found" >&2
    exit 1
  fi
  return 0
}
quote_output() { sed 's/^/      | /'; return 0; }
need go
need git

if [[ ! -x "$MUTATE_SH" ]]; then
  echo "FAIL: museaccountapi-redact-nonapproved-field-mutations — $MUTATE_SH is missing or not executable" >&2
  exit 1
fi

if [[ -n "$(git -C "$REPO_ROOT" status --porcelain)" ]]; then
  echo "museaccountapi-redact-nonapproved-field-mutations: CANNOT RUN — the worktree is dirty, and mutate.sh needs a" >&2
  echo "  clean tree for its post-restore check to mean anything. Commit or clean up, then:" >&2
  echo "    bash tools/lib/museaccountapi-redact-nonapproved-field-mutations_test.sh" >&2
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

ANCHOR=$'\tredacted := redactedResponse{\n\t\tIsSubsActive: resp.IsSubsActive,\n\t\tSubsTierName: resp.SubsTierName,\n\t}'
REPLACEMENT=$'\tvar extra map[string]interface{}\n\t_ = json.Unmarshal(raw, &extra)\n\tredacted := redactedResponse{\n\t\tIsSubsActive: resp.IsSubsActive,\n\t\tSubsTierName: resp.SubsTierName,\n\t}\n\tif email, ok := extra["user_email"].(string); ok {\n\t\tredacted.SubsTierName = redacted.SubsTierName + " " + email\n\t}'

# ── a second, unconstrained decode smuggles user_email into the output ────
assert_go_test_goes_red \
  "a second unconstrained decode leaking a non-approved field into the redacted output" \
  "core/adapters/outbound/museaccountapi/redact.go" \
  "$ANCHOR" \
  "$REPLACEMENT" \
  "./core/adapters/outbound/museaccountapi/..." \
  "TestRedactSubscriptionResponse_DropsNonApprovedFields" \
  "forbidden data verbatim"

if [[ $fails -gt 0 ]]; then
  echo "museaccountapi-redact-nonapproved-field-mutations: $fails FAILED"
  exit 1
fi
echo "museaccountapi-redact-nonapproved-field-mutations: ALL PASS"
