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
#      resets that make a re-grant the Keychain retry;
#   D. the remaining guards: the no-dial scan, expiry, the key cache,
#      redaction, the vibe-only binding, header ordering, the profile-name
#      refusal, and the consent contract over the real effects;
#   E. QA round 1: grant-all never grants an explicit-grant permission (the
#      service, the declaration, a real boot), the daemon's grant wiring, the
#      refresh's and the sweep's revoke re-checks, and clearing every reading
#      the sweep cannot serve for the current Chrome profile.
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
need() {
  local tool="$1"
  command -v "$tool" >/dev/null 2>&1 || { echo "FAIL: mistralvibe-mutations — $tool not found" >&2; exit 1; }
}
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

# The Go packages and the one source file the mutations below name most.
PKG_CHROMECOOKIES="./core/adapters/outbound/chromecookies/"
PKG_MISTRALVIBE="./core/adapters/outbound/mistralvibe/"
PKG_SERVICES="./core/application/services/"
PKG_IRRLICHD="./core/cmd/irrlichd/"
SWEEP_GO="core/application/services/mistralvibesweep.go"

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
  "$PKG_MISTRALVIBE" \
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
  "$PKG_CHROMECOOKIES" \
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
  "$PKG_CHROMECOOKIES" \
  'TestRead_NeverReadsTheKeychainBeforeItHasTo' \
  "before every required cookie was known present"

# ── A4. A cookie value that could smuggle a cookie or header is forwarded ────
assert_go_test_goes_red \
  "forwarding a cookie value containing ; , CR, LF or space" \
  "core/adapters/outbound/mistralvibe/cookies.go" \
  'if !isToken(c.Name) || !isCookieValue(c.Value()) {' \
  'if !isToken(c.Name) {' \
  "$PKG_MISTRALVIBE" \
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
  $'\tif !obs.HasValue {\n\t\tif obs.FailureReason != "" {\n\t\t\treturn nil, fmt.Errorf("services: Mistral Vibe usage not available: %s", obs.FailureReason)\n\t\t}\n\t\treturn nil, errors.New("services: Mistral Vibe usage not available yet")\n\t}' \
  $'\tif !obs.HasValue {\n\t\tzero := session.RateLimitSnapshot{Provider: session.ProviderMistral}\n\t\t_ = errors.New\n\t\treturn &zero, nil\n\t}' \
  "$PKG_SERVICES" \
  'TestMistralVibeRefresh_NeverPublishesZeroOnAuthFailure' \
  "a reading was published after a 401"

# ── B2. Item 5: usage_percentage is read as a fraction and rescaled ──────────
# As run: 0.081567 (the 2026-11-21 live reading, 0.08 %) becomes 8.1567 %.
assert_go_test_goes_red \
  "reading usage_percentage as a fraction (multiplying by 100)" \
  "core/adapters/outbound/mistralvibe/parser.go" \
  'out := Usage{UsedPercent: pct}' \
  'out := Usage{UsedPercent: pct * 100}' \
  "$PKG_MISTRALVIBE" \
  'TestBuildSnapshot_ReadsUsagePercentageAsAPercentage' \
  "read as a fraction and rescaled"

# ── B3. An out-of-range percentage is accepted instead of refused ────────────
assert_go_test_goes_red \
  "accepting a usage_percentage outside [0, 100]" \
  "core/adapters/outbound/mistralvibe/parser.go" \
  'return !math.IsNaN(p) && p >= 0 && p <= 100' \
  'return !math.IsNaN(p)' \
  "$PKG_MISTRALVIBE" \
  'TestParseUsage_RefusesWhatItCannotTrust' \
  "want mistralvibe: usage_percentage is outside [0, 100]"

# ── B4. An absent usage_percentage is read as zero ───────────────────────────
assert_go_test_goes_red \
  "reading an absent usage_percentage as 0 %" \
  "core/adapters/outbound/mistralvibe/parser.go" \
  $'\tif u.UsagePercentage == nil {\n\t\treturn Usage{}, ErrNoUsage\n\t}' \
  $'\tif u.UsagePercentage == nil {\n\t\tzero := 0.0\n\t\tu.UsagePercentage = &zero\n\t}' \
  "$PKG_MISTRALVIBE" \
  'TestParseUsage_RefusesWhatItCannotTrust' \
  "want mistralvibe: response carries no usage_percentage"

# ═══ C. Consent ══════════════════════════════════════════════════════════

# ── C1. §7.3: the sweep keeps going after the permission is revoked ──────────
# As run: the not-granted branch is skipped. AccountPoller's admission check
# still refuses the fetch, so no request is sent; what goes red is the read
# of the Chrome profile the sweep now makes on every revoked tick. The anchor
# carries the comment line under the check, because bind's pre-write re-check
# (E6) opens with the same `if !s.deps.Granted() {` and mutate.sh refuses an
# anchor that is not unique.
assert_go_test_goes_red \
  "§7.3 the sweep reading the browser after the permission is revoked" \
  "$SWEEP_GO" \
  $'\tif !s.deps.Granted() {\n\t\t// Not granted: unbind.' \
  $'\tif !s.deps.Granted() && false {\n\t\t// Not granted: unbind.' \
  "$PKG_SERVICES" \
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
  "$PKG_IRRLICHD" \
  'TestMistralVibeAPI_RevokeCancelsTheFetchInFlight' \
  "the revoke left the request in flight running"

# ── C3. §7.4 (re-scoped): a vibe session stays bound without the grant ───────
# As run: unbind() no longer clears, so the not-granted tick leaves every
# vibe session's reading in place.
assert_go_test_goes_red \
  "§7.4 binding a vibe session while the user-asserted permission is not granted" \
  "$SWEEP_GO" \
  $'\t\t\ts.deps.Writer.SetProviderRateLimit(st.SessionID, session.ProviderMistral, nil)\n' \
  $'\t\t\t_ = st\n' \
  "$PKG_SERVICES" \
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
  "$PKG_MISTRALVIBE" \
  'TestSession_AccountIsTheProfileNeverACookie' \
  "account context changed when only a cookie value changed"

# ── C5. §7.3: the poller is never told about a revoke ───────────────────────
# Review of #2012, finding 3. As run: MistralVibeRefresh hands the poller an
# always-true grant, so AccountPoller's admission check passes after a revoke
# and the cookies are read and the request is sent. (The refresh's own
# post-poll re-check, E5, still keeps the result off the sessions.)
assert_go_test_goes_red \
  "§7.3 the poller polling and publishing past a revoke" \
  "core/application/services/mistralvibepoll.go" \
  $'\t\tGranted:        req.Granted,' \
  $'\t\tGranted:        func() bool { return true },' \
  "$PKG_SERVICES" \
  'TestMistralVibeRefresh_PassesTheGrantToThePoller' \
  "a revoked refresh read the cookies"

# ── C6. A new grant does not retry a failed Keychain read ───────────────────
# Review of #2012, finding 4: chromecookies.KeyCache keeps a failed read so an
# unanswered dialog is not raised on every poll, and granting again is the
# documented retry.
assert_go_test_goes_red \
  "a new grant not retrying a failed Keychain read" \
  "core/cmd/irrlichd/mistralvibe_effects.go" \
  $'\tstart := func() error {\n\t\tkeys.Reset()\n' \
  $'\tstart := func() error {\n' \
  "$PKG_IRRLICHD" \
  'TestMistralVibeAPI_GrantRetriesAFailedKeychainRead' \
  "granting again did not retry the failed read"

# ── C7. A revoke keeps the derived cookie key in memory ─────────────────────
assert_go_test_goes_red \
  "a revoke keeping the derived cookie key cached" \
  "core/cmd/irrlichd/mistralvibe_effects.go" \
  $'\t\tpoller.Revoke(session.ProviderMistral)\n\t\tkeys.Reset()\n' \
  $'\t\tpoller.Revoke(session.ProviderMistral)\n' \
  "$PKG_IRRLICHD" \
  'TestMistralVibeAPI_RevokeDropsTheKey' \
  "the revoke kept the derived key cached"

# ═══ D. The remaining guards ══════════════════════════════════════════════

# ── D1. A file in mistralvibe names a transport (the no-dial tripwire) ─────
# As run: destination.go gains a string naming NewHTTPTransport(, which is
# what a package building its own transport would contain.
assert_go_test_goes_red \
  "the no-dial scan missing a transport named in a production file" \
  "core/adapters/outbound/mistralvibe/destination.go" \
  'const CSRFHeader = "X-CSRFToken"' \
  $'const CSRFHeader = "X-CSRFToken"\n\nvar _ = "NewHTTPTransport("' \
  "$PKG_MISTRALVIBE" \
  'TestPackage_NeverBuildsATransport' \
  "nothing in this package may build a transport or dial"

# ── D2. An expired cookie is still selected ────────────────────────────────
assert_go_test_goes_red \
  "an expired cookie being returned and forwarded" \
  "core/adapters/outbound/chromecookies/store.go" \
  $'\treturn time.UnixMicro(r.expiresUTC - windowsEpochOffset).Before(now)' \
  $'\treturn false && time.UnixMicro(r.expiresUTC - windowsEpochOffset).Before(now)' \
  "$PKG_CHROMECOOKIES" \
  'TestRead_SelectsOnlyTheSelectedCookies' \
  "want exactly"

# ── D3. A failed Keychain read is retried on the next poll ─────────────────
# As run: the cache drops a failed read straight away, so an unanswered
# dialog would be raised again on every poll.
assert_go_test_goes_red \
  "the key cache not keeping a failed Keychain read" \
  "core/adapters/outbound/chromecookies/keycache.go" \
  $'\t\t\tr.key, r.err = c.source(ctx)\n\t\t}()\n\t\treturn r.key, r.err' \
  $'\t\t\tr.key, r.err = c.source(ctx)\n\t\t}()\n\t\tif r.err != nil {\n\t\t\tc.Reset()\n\t\t}\n\t\treturn r.key, r.err' \
  "$PKG_CHROMECOOKIES" \
  'TestKeyCache_OneReadPerResetAndFailureIsKept' \
  "a failed Keychain read was retried"

# ── D4. Concurrent callers each start their own Keychain read ──────────────
assert_go_test_goes_red \
  "the key cache not sharing a read in flight" \
  "core/adapters/outbound/chromecookies/keycache.go" \
  $'\tif r == nil {' \
  $'\tif true {' \
  "$PKG_CHROMECOOKIES" \
  'TestKeyCache_ConcurrentCallersShareOneRead' \
  "concurrent callers made"

# ── D5. Formatting a Cookie prints its value ───────────────────────────────
assert_go_test_goes_red \
  "a formatted Cookie leaking its value" \
  "core/adapters/outbound/chromecookies/store.go" \
  'func (c Cookie) String() string   { return c.Name + "=[redacted]" }' \
  'func (c Cookie) String() string   { return c.Name + "=" + c.value }' \
  "$PKG_CHROMECOOKIES" \
  'TestCookieAndKey_FormatRedacted' \
  "formatting leaked a cookie value"

# ── D6. The reading is bound to every session, not only top-level vibe ones ─
assert_go_test_goes_red \
  "the sweep binding non-vibe sessions and subagents" \
  "$SWEEP_GO" \
  'return st != nil && st.Adapter == s.deps.Adapter && st.ParentSessionID == ""' \
  'return st != nil' \
  "$PKG_SERVICES" \
  'TestMistralVibeSweep_OnePollBindsEveryVibeSession' \
  "want exactly v1, v2, v3"

# ── D7. The companion header is set after the auth header ──────────────────
# As run: the companion is set a second time after the credential, so a
# companion naming the auth header replaces the credential itself.
assert_go_test_goes_red \
  "a companion header able to replace the credential" \
  "core/adapters/outbound/accountquota/transport.go" \
  $'\thttpReq.Header.Set(header, req.Auth.Prefix+req.Credential.Reveal())' \
  $'\thttpReq.Header.Set(header, req.Auth.Prefix+req.Credential.Reveal())\n\tif companion != "" {\n\t\thttpReq.Header.Set(req.Auth.CompanionHeader, companion)\n\t}' \
  "./core/adapters/outbound/accountquota/" \
  'TestHTTPTransport_CompanionCannotReplaceTheCredential' \
  'want exactly ["a=1"]'

# ── D8. Local State can point outside Chrome's user data dir ────────────────
assert_go_test_goes_red \
  "a traversal-shaped profile name being followed" \
  "core/adapters/outbound/chromecookies/profile.go" \
  $'\tif slices.Contains([]string{".", ".."}, name) || strings.ContainsAny(name, `/\\`) {' \
  $'\tif false && (slices.Contains([]string{".", ".."}, name) || strings.ContainsAny(name, `/\\`)) {' \
  "$PKG_CHROMECOOKIES" \
  'TestLastUsedProfile' \
  "want chromecookies: Chrome profile not found"

# ── D9. The consent contract over the real effects ──────────────────────────
# As run: unbind no longer clears, so the reading outlives a revoke.
assert_go_test_goes_red \
  "the browser-session permission's contract: a revoke leaving the reading" \
  "$SWEEP_GO" \
  $'\t\t\ts.deps.Writer.SetProviderRateLimit(st.SessionID, session.ProviderMistral, nil)\n' \
  $'\t\t\t_ = st\n' \
  "$PKG_IRRLICHD" \
  'TestMistralVibePermission_GateContract' \
  "effect still observed after revoking a previously granted permission"

# ═══ E. QA round 1 ═══════════════════════════════════════════════════════

# ── E1. grant-all auto-grants a permission that requires an explicit grant ─
assert_go_test_goes_red \
  "grant-all granting a RequiresExplicitGrant permission" \
  "core/application/services/permission_service.go" \
  $'\t\t\t\tif p.RequiresExplicitGrant {' \
  $'\t\t\t\tif false {' \
  "$PKG_SERVICES" \
  'TestGrantAll_LeavesAnExplicitGrantPermissionUnanswered' \
  "grant-all auto-granted a permission that requires an explicit grant"

# ── E2. The browser-session declaration drops RequiresExplicitGrant ─────────
assert_go_test_goes_red \
  "the browser-session permission no longer requiring an explicit grant" \
  "core/adapters/outbound/mistralvibe/permission.go" \
  $'\t\t\tRequiresExplicitGrant: true,' \
  $'\t\t\tRequiresExplicitGrant: false,' \
  "$PKG_IRRLICHD" \
  'TestConsentCatalog_ExplicitGrantPermissionsSurviveGrantAll' \
  "permissions requiring an explicit grant ="

# ── E3. A grant-all boot reads the browser (the boot half of E1) ───────────
assert_go_test_goes_red \
  "a grant-all daemon boot granting the browser-session permission" \
  "core/application/services/permission_service.go" \
  $'\t\t\t\tif p.RequiresExplicitGrant {' \
  $'\t\t\t\tif false {' \
  "$PKG_IRRLICHD" \
  'TestMistralVibeAPI_GrantAllBootReadsNothing' \
  "a grant-all boot granted the browser-session permission"

# ── E4. The daemon wires the sweep to a grant that is always true ──────────
assert_go_test_goes_red \
  "the sweep's daemon grant wiring reading always-true" \
  "core/cmd/irrlichd/mistralvibe_effects.go" \
  $'\t\t\treturn perms.Granted(mistralvibe.Name, mistralvibe.PermissionKey)' \
  $'\t\t\treturn true' \
  "$PKG_IRRLICHD" \
  'TestMistralVibeAPI_SweeperIsGatedOnTheDaemonsGrant' \
  "pending: the sweep read the Chrome profile"

# ── E5. A refresh revoked mid-fetch returns the poller's held stale reading ─
assert_go_test_goes_red \
  "MistralVibeRefresh not re-checking the grant after the poll" \
  "core/application/services/mistralvibepoll.go" \
  $'\tif !req.Granted() {\n\t\treturn nil, errors.New("services: Mistral Vibe usage: the browser-session permission is not granted")\n\t}' \
  $'\t// post-poll grant re-check removed' \
  "$PKG_SERVICES" \
  'TestMistralVibeRefresh_PassesTheGrantToThePoller' \
  "a refresh revoked mid-fetch returned the held stale reading"

# ── E6. The sweep writes after a revoke that landed during the refresh ──────
assert_go_test_goes_red \
  "the sweep not re-checking the grant just before it writes" \
  "$SWEEP_GO" \
  $'\tif !s.deps.Granted() {\n\t\treturn\n\t}' \
  $'\t// pre-write grant re-check removed' \
  "$PKG_SERVICES" \
  'TestMistralVibeSweep_RevokeBeforeTheWriteWritesNothing' \
  "a reading was written after the permission was revoked mid-tick"

# ── E7. With no reading for the current profile, a listed one stays (3a) ───
# As run: the no-reading branch returns without clearing, so after a restart
# profile A's persisted reading stays on the session while profile B is the
# one Chrome uses.
assert_go_test_goes_red \
  "the no-reading tick leaving another profile's persisted reading" \
  "$SWEEP_GO" \
  $'\t\t// one a writer saved over an earlier clear) would only look current.\n\t\ts.unbind(vibe)\n\t\treturn' \
  $'\t\t// one a writer saved over an earlier clear) would only look current.\n\t\treturn' \
  "$PKG_SERVICES" \
  'TestMistralVibeSweep_RestartNeverShowsAnotherProfilesReading' \
  "still shown for profile B"

# ── E8. The clear made edge-triggered: once per profile (3b) ───────────────
# As run: the no-reading branch clears only the first time it runs for a
# profile (latched in the existing seen map), so the profile switch still
# clears profile A's reading, but a writer that saves the older copy back over
# that clear leaves it on the session for good.
assert_go_test_goes_red \
  "a clear saved over by an older copy not being cleared again" \
  "$SWEEP_GO" \
  $'\t\t// one a writer saved over an earlier clear) would only look current.\n\t\ts.unbind(vibe)\n\t\treturn' \
  $'\t\t// one a writer saved over an earlier clear) would only look current.\n\t\tif !s.seen["cleared:"+browser.Account()] {\n\t\t\ts.seen["cleared:"+browser.Account()] = true\n\t\t\ts.unbind(vibe)\n\t\t}\n\t\treturn' \
  "$PKG_SERVICES" \
  'TestMistralVibeSweep_LostClearIsClearedAgain' \
  "a resurrected profile-A reading survived the next tick under profile B"

# ── E9. No Chrome profile to resolve, and the listed reading stays ──────────
assert_go_test_goes_red \
  "an unresolvable Chrome profile leaving the listed reading" \
  "$SWEEP_GO" \
  $'\t\ts.logFailure("no Chrome profile to read: " + err.Error())\n\t\ts.unbind(vibe)\n' \
  $'\t\ts.logFailure("no Chrome profile to read: " + err.Error())\n' \
  "$PKG_SERVICES" \
  'TestMistralVibeSweep_UnconfirmedReadingIsCleared' \
  "an unconfirmed reading was left looking current"

if [[ $fails -gt 0 ]]; then
  echo "mistralvibe-mutations: $fails FAILED"
  exit 1
fi
echo "mistralvibe-mutations: ALL PASS"
