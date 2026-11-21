#!/usr/bin/env bash
# mistralvibe-mutations_test.sh — the committed mutation fixture for issue
# #2012 (Mistral Vibe, browser-session route). Every guard #2012 adds is
# broken below, one mutation at a time, and the test that protects it must go
# red with its own message:
#
#   A. cookie confinement — §7.1 ("forward the whole cookie jar instead of
#      the selected cookies") and the guards around what is read from Chrome,
#      decrypted, and put on the wire;
#   B. the quota reading — §7.2 ("publish a zero quota when the browser
#      session has expired") and testing-strategy item 5 ("read
#      usage_percentage as a fraction"), plus the parser's refusals;
#   C. consent — §7.3 ("keep polling after the permission is revoked") and
#      §7.4 as the triage re-scoped it ("bind a vibe session while the
#      user-asserted permission is not granted") plus its original form
#      ("derive an account reference from a cookie value"), and the key-cache
#      resets that make a re-grant the Keychain retry.
#
# These are new guards with no "before the fix" to run red (docs/testing-
# philosophy.md). The grant is checked at two layers (the sweep, and
# AccountPoller's admission and publish checks), so each consent mutation was
# chosen from what actually goes red when that one layer breaks.
# tools/mutate.sh owns the mechanics (stale/ambiguous-anchor refusal and the
# byte-for-byte restore).

set -uo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$DIR/../.." && pwd)"
MUTATE_SH="$REPO_ROOT/tools/mutate.sh"

# A missing tool is a hard failure, not a skip: exiting 0 here would read as
# a PASS to preflight's shell_lib_tests, so the gate would go green having
# asserted nothing.
need() { command -v "$1" >/dev/null 2>&1 || { echo "FAIL: mistralvibe-mutations — $1 not found" >&2; exit 1; }; }
need go
need git

if [[ ! -x "$MUTATE_SH" ]]; then
  echo "FAIL: mistralvibe-mutations — $MUTATE_SH is missing or not executable" >&2
  exit 1
fi

# mutate.sh refuses a dirty tree, because its post-restore check could prove
# nothing then. A dirty tree must not silently pass: it is a hard failure in
# CI and under MUTATION_FIXTURES_STRICT=1, and a loud skip on a developer's
# worktree (the same protocol as muse-account-sweep-mutations_test.sh).
if [[ -n "$(git -C "$REPO_ROOT" status --porcelain)" ]]; then
  echo "mistralvibe-mutations: CANNOT RUN — the worktree is dirty, and mutate.sh needs a" >&2
  echo "  clean tree for its post-restore check to mean anything. Commit or clean up, then:" >&2
  echo "    bash tools/lib/mistralvibe-mutations_test.sh" >&2
  if [[ -n "${CI:-}" || -n "${MUTATION_FIXTURES_STRICT:-}" ]]; then
    echo "  (failing rather than skipping: CI/strict mode)" >&2
    exit 1
  fi
  echo "  (skipped locally; set MUTATION_FIXTURES_STRICT=1 to make this a failure)" >&2
  exit 0
fi

fails=0

# assert_go_test_goes_red <label> <file> <anchor> <replacement> <pkg> <run-regex> <want-in-output>
#
# Applies one mutation and requires the named Go tests to FAIL under it, for
# the right reason: an assertion naming the expected text, not a build error.
assert_go_test_goes_red() {
  local label="$1" file="$2" anchor="$3" replacement="$4" pkg="$5" run="$6" want="$7"
  local out rc

  out="$(cd "$REPO_ROOT" && "$MUTATE_SH" "$file" "$anchor" "$replacement" \
    bash -c "go test $pkg -run '$run' -race -count=1 -v 2>&1; echo GO_TEST_RC=\$?" 2>&1)"
  rc=$?

  if [[ $rc -ne 0 ]]; then
    echo "FAIL: $label — mutate.sh refused (exit $rc). A STALE or ambiguous anchor means the"
    echo "      guard's source moved and this fixture needs updating; it does NOT mean the"
    echo "      guard is fine."
    echo "$out" | sed 's/^/      | /'
    fails=$((fails + 1))
    return
  fi
  if grep -q 'GO_TEST_RC=0' <<<"$out"; then
    echo "FAIL: $label — the tests stayed GREEN under the mutation, so the guard does not reach"
    echo "      what it claims to protect."
    echo "$out" | sed 's/^/      | /'
    fails=$((fails + 1))
    return
  fi
  if grep -qE '^# |build failed|cannot use|undefined:|declared and not used|imported and not used' <<<"$out"; then
    echo "FAIL: $label — the mutation broke the BUILD rather than the guard."
    echo "$out" | sed 's/^/      | /'
    fails=$((fails + 1))
    return
  fi
  if ! grep -qF -- "$want" <<<"$out"; then
    echo "FAIL: $label — the tests failed, but not with the expected message."
    echo "      wanted to find: $want"
    echo "$out" | sed 's/^/      | /'
    fails=$((fails + 1))
    return
  fi
  echo "ok  $label"
}

# ═══ A. Cookie confinement ═══════════════════════════════════════════════

# ── A1. §7.1: the selection is widened to the whole cookie jar ───────────────
# As run: the WHERE clause becomes `1 = 1 OR ...`, so every unexpired row in
# the committed fixture is decrypted and forwarded. The end-to-end credential
# test then sees cookies outside the selection in the Cookie header.
assert_go_test_goes_red \
  "§7.1 forwarding the whole cookie jar instead of the selected cookies" \
  "core/adapters/outbound/chromecookies/store.go" \
  '` WHERE host_key IN (` + strings.Join(hosts, ", ") + `)` +' \
  '` WHERE 1 = 1 OR host_key IN (` + strings.Join(hosts, ", ") + `)` +' \
  "./core/adapters/outbound/mistralvibe/" \
  'TestSession_ResolveSendsOnlyTheSelectedCookies' \
  "a cookie outside the selection would leave the machine"

# ── A2. A decrypted value is accepted without its host hash ──────────────────
# As run: the 32 bytes are stripped when there are 32 to strip, unchecked, so
# a v23 store relabelled v24 decrypts to a value instead of a refusal.
assert_go_test_goes_red \
  "accepting a v24 value whose SHA-256(host_key) prefix does not match" \
  "core/adapters/outbound/chromecookies/crypto.go" \
  $'\t\tif !bytes.HasPrefix(out, sum[:]) {\n\t\t\treturn "", fmt.Errorf("%w: host hash does not match %q", ErrUnknownEncryption, hostKey)\n\t\t}\n\t\tout = out[len(sum):]' \
  $'\t\tif len(out) >= len(sum) {\n\t\t\tout = out[len(sum):]\n\t\t}' \
  "./core/adapters/outbound/chromecookies/" \
  'TestRead_RefusesAMissingHostHash' \
  "want ErrUnknownEncryption naming the host hash"

# ── A3. The Keychain is read before the required cookies are known present ──
# As run: the key is fetched first, so a signed-out browser (or a test
# daemon's temporary $HOME with a store) raises the Keychain dialog for
# nothing.
assert_go_test_goes_red \
  "reading the Keychain before every required cookie is known present" \
  "core/adapters/outbound/chromecookies/store.go" \
  $'\tif missing := sel.firstMissing(matched); missing != "" {\n\t\treturn nil, fmt.Errorf("%w: %s", ErrCookieAbsent, missing)' \
  $'\tif _, kerr := key(ctx); kerr == nil && sel.firstMissing(matched) != "" {\n\t\treturn nil, fmt.Errorf("%w: %s", ErrCookieAbsent, sel.firstMissing(matched))' \
  "./core/adapters/outbound/chromecookies/" \
  'TestRead_NeverReadsTheKeychainBeforeItHasTo' \
  "before every required cookie was known present"

# ── A4. A cookie value that could smuggle a cookie or header is forwarded ────
assert_go_test_goes_red \
  "forwarding a cookie value containing ; , CR, LF or space" \
  "core/adapters/outbound/mistralvibe/cookies.go" \
  'if !isToken(c.Name) || !isCookieValue(c.Value()) {' \
  'if !isToken(c.Name) {' \
  "./core/adapters/outbound/mistralvibe/" \
  'TestBuildCredential_RefusesValuesThatCouldAddACookieOrHeader' \
  "want ErrInvalidCookie"

# ── A5. A second place in the transport unwraps the companion secret ────────
assert_go_test_goes_red \
  "a second RevealCompanion() call site in the accountquota transport" \
  "core/adapters/outbound/accountquota/transport.go" \
  $'\t// The ONE call site: Reveal() is invoked here' \
  $'\t_ = req.Credential.RevealCompanion()\n\t// The ONE call site: Reveal() is invoked here' \
  "./core/adapters/outbound/accountquota/" \
  'TestCredential_RevealCompanionHasOneCallSite' \
  "found 2 call(s) to .RevealCompanion()"

# ── A6. The transport drops the companion (X-CSRFToken) header ───────────────
assert_go_test_goes_red \
  "the transport not sending the credential's companion header" \
  "core/adapters/outbound/accountquota/transport.go" \
  $'\t\thttpReq.Header.Set(req.Auth.CompanionHeader, companion)\n' \
  $'\t\t_ = companion\n' \
  "./core/adapters/outbound/accountquota/" \
  'TestHTTPTransport_SendsExactlyTheCredentialHeaders' \
  "the companion header was not sent"

# ── A7. A companion the auth method does not declare is sent anyway ─────────
# As run: the mismatch check is skipped, so a credential's companion with no
# declared header (or a declared header with no companion) reaches the dial.
assert_go_test_goes_red \
  "the transport not refusing a companion/auth-method mismatch" \
  "core/adapters/outbound/accountquota/transport.go" \
  $'\tif (companion == "") != (req.Auth.CompanionHeader == "") {' \
  $'\tif false {' \
  "./core/adapters/outbound/accountquota/" \
  'TestHTTPTransport_RefusesACompanionMismatch' \
  "a companion mismatch was sent"

# ═══ B. The quota reading ════════════════════════════════════════════════

# ── B1. §7.2: a 401 with no earlier reading publishes a zero snapshot ────────
# As run: the no-value branch manufactures an empty Mistral snapshot. The
# first poll still errors (the poller's own error), so what goes red is the
# retry the backoff serves from cache, which now returns 0 % instead of the
# classified failure.
assert_go_test_goes_red \
  "§7.2 publishing a zero reading after a 401 from a signed-out browser" \
  "core/application/services/mistralvibepoll.go" \
  $'\tif !obs.HasValue {\n\t\tif obs.FailureReason != "" {\n\t\t\treturn nil, fmt.Errorf("services: Mistral Vibe usage not available: %w", vibeFailure(obs.FailureReason))\n\t\t}\n\t\treturn nil, errors.New("services: Mistral Vibe usage not available yet")\n\t}' \
  $'\tif !obs.HasValue {\n\t\tzero := session.RateLimitSnapshot{Provider: session.ProviderMistral}\n\t\t_ = errors.New\n\t\treturn &zero, nil\n\t}' \
  "./core/application/services/" \
  'TestMistralVibeRefresh_NeverPublishesZeroOnAuthFailure' \
  "a reading was published after a 401"

# ── B2. Item 5: usage_percentage is read as a fraction and rescaled ──────────
# As run: 0.081567 (the 2026-11-21 live reading, 0.08 %) becomes 8.1567 %.
assert_go_test_goes_red \
  "reading usage_percentage as a fraction (multiplying by 100)" \
  "core/adapters/outbound/mistralvibe/parser.go" \
  'out := Usage{UsedPercent: pct}' \
  'out := Usage{UsedPercent: pct * 100}' \
  "./core/adapters/outbound/mistralvibe/" \
  'TestBuildSnapshot_ReadsUsagePercentageAsAPercentage' \
  "read as a fraction and rescaled"

# ── B3. An out-of-range percentage is accepted instead of refused ────────────
assert_go_test_goes_red \
  "accepting a usage_percentage outside [0, 100]" \
  "core/adapters/outbound/mistralvibe/parser.go" \
  'if math.IsNaN(pct) || pct < 0 || pct > 100 {' \
  'if math.IsNaN(pct) {' \
  "./core/adapters/outbound/mistralvibe/" \
  'TestParseUsage_RefusesWhatItCannotTrust' \
  "want mistralvibe: usage_percentage is outside [0, 100]"

# ── B4. An absent usage_percentage is read as zero ───────────────────────────
assert_go_test_goes_red \
  "reading an absent usage_percentage as 0 %" \
  "core/adapters/outbound/mistralvibe/parser.go" \
  $'\tif u.UsagePercentage == nil {\n\t\treturn Usage{}, ErrNoUsage\n\t}' \
  $'\tif u.UsagePercentage == nil {\n\t\tzero := 0.0\n\t\tu.UsagePercentage = &zero\n\t}' \
  "./core/adapters/outbound/mistralvibe/" \
  'TestParseUsage_RefusesWhatItCannotTrust' \
  "want mistralvibe: response carries no usage_percentage"

# ═══ C. Consent ══════════════════════════════════════════════════════════

# ── C1. §7.3: the sweep keeps going after the permission is revoked ──────────
# As run: the not-granted branch is skipped. AccountPoller's admission check
# still refuses the fetch, so no request is sent; what goes red is the read
# of the Chrome profile the sweep now makes on every revoked tick.
assert_go_test_goes_red \
  "§7.3 the sweep reading the browser after the permission is revoked" \
  "core/application/services/mistralvibesweep.go" \
  $'\tif !s.deps.Granted() {' \
  $'\tif !s.deps.Granted() && false {' \
  "./core/application/services/" \
  'TestMistralVibeSweep_RevokedReadsAndSendsNothing' \
  "time(s) after the permission was revoked"

# ── C2. §7.3: a revoke leaves the request in flight running ──────────────────
# As run: Stop no longer revokes the poller, so the fetch it started keeps
# its context; the test waits 5s for the cancellation that never comes.
assert_go_test_goes_red \
  "§7.3 a revoke not cancelling the request already in flight" \
  "core/cmd/irrlichd/mistralvibe_effects.go" \
  $'\t\tpoller.Revoke(session.ProviderMistral)\n' \
  $'\t\t_ = session.ProviderMistral\n' \
  "./core/cmd/irrlichd/" \
  'TestMistralVibeAPI_RevokeCancelsTheFetchInFlight' \
  "the revoke left the request in flight running"

# ── C3. §7.4 (re-scoped): a vibe session stays bound without the grant ───────
# As run: unbind() no longer clears, so the not-granted tick leaves every
# vibe session's reading in place.
assert_go_test_goes_red \
  "§7.4 binding a vibe session while the user-asserted permission is not granted" \
  "core/application/services/mistralvibesweep.go" \
  $'\t\t\ts.deps.Writer.SetProviderRateLimit(st.SessionID, session.ProviderMistral, nil)\n' \
  $'\t\t\t_ = st\n' \
  "./core/application/services/" \
  'TestMistralVibeSweep_NotGrantedUnbindsEverySession' \
  "a vibe session stayed bound to the Mistral reading while the permission was not granted"

# ── C4. §7.4 (original form): the account reference comes from a cookie ──────
# As run: Account() reads the profile's cookies and returns the last one's
# value. A cookie rotation then moves the account, and computing it reads the
# Keychain.
assert_go_test_goes_red \
  "§7.4 deriving the account reference from a cookie value" \
  "core/adapters/outbound/mistralvibe/resolver.go" \
  $'\treturn "chrome-profile:" + s.profile.Dir\n' \
  $'\tcookies, err := chromecookies.Read(context.Background(), s.profile.CookiesPath(), CookieSelector, s.now(), s.browser.Key)\n\tif err != nil || len(cookies) == 0 {\n\t\treturn "chrome-profile:" + s.profile.Dir\n\t}\n\treturn "cookie:" + cookies[len(cookies)-1].Value()\n' \
  "./core/adapters/outbound/mistralvibe/" \
  'TestSession_AccountIsTheProfileNeverACookie' \
  "account context changed when only a cookie value changed"

# ── C5. §7.3: the poller is never told about a revoke ───────────────────────
# Review of #2012, finding 3. As run: MistralVibeRefresh hands the poller an
# always-true grant, so AccountPoller's admission and publish checks pass
# after a revoke: the cookies are read, the request is sent and the result is
# published.
assert_go_test_goes_red \
  "§7.3 the poller polling and publishing past a revoke" \
  "core/application/services/mistralvibepoll.go" \
  $'\t\tGranted:        granted,' \
  $'\t\tGranted:        func() bool { return true },' \
  "./core/application/services/" \
  'TestMistralVibeRefresh_PassesTheGrantToThePoller' \
  "a reading fetched across a revoke was published"

# ── C6. A new grant does not retry a failed Keychain read ───────────────────
# Review of #2012, finding 4: chromecookies.KeyCache keeps a failed read so an
# unanswered dialog is not raised on every poll, and granting again is the
# documented retry.
assert_go_test_goes_red \
  "a new grant not retrying a failed Keychain read" \
  "core/cmd/irrlichd/mistralvibe_effects.go" \
  $'\tstart := func() error {\n\t\tkeys.Reset()\n' \
  $'\tstart := func() error {\n' \
  "./core/cmd/irrlichd/" \
  'TestMistralVibeAPI_GrantRetriesAFailedKeychainRead' \
  "granting again did not retry the failed read"

# ── C7. A revoke keeps the derived cookie key in memory ─────────────────────
assert_go_test_goes_red \
  "a revoke keeping the derived cookie key cached" \
  "core/cmd/irrlichd/mistralvibe_effects.go" \
  $'\t\tpoller.Revoke(session.ProviderMistral)\n\t\tkeys.Reset()\n' \
  $'\t\tpoller.Revoke(session.ProviderMistral)\n' \
  "./core/cmd/irrlichd/" \
  'TestMistralVibeAPI_RevokeDropsTheKey' \
  "the revoke kept the derived key cached"

if [[ $fails -gt 0 ]]; then
  echo "mistralvibe-mutations: $fails FAILED"
  exit 1
fi
echo "mistralvibe-mutations: ALL PASS"
