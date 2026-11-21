#!/usr/bin/env bash
# zai-coding-plan-mutations_test.sh — the committed mutation fixtures for
# issue #2010's new guards (the Z.ai GLM Coding Plan, and the Claude Code
# settings-layer route it depends on). A guard a change ADDS has no "before
# the fix" to run red (docs/testing-philosophy.md), so each one is broken on
# purpose here and its target test must go red with its own message.
#
# Phase 1 — route from Claude Code's settings layers
#   P1 env beats settings (precedence swapped)  -> probe row B goes red
#   P2 an unreadable/opaque layer read as absent
#   P3 a --settings flag ignored
#   P4 the AccountBinder restamping another provider's snapshot
# Phase 2 — the Z.ai quota parser and the forecast (#2010 triage 1–6)
#   Z1 `usage` read as consumption
#   Z2 `remaining` derived as limit - used
#   Z3 a missing nextResetTime defaulted to now / to zero / published as 0
#   Z4 ForecastCap pairing a Z.ai 5h window with an Anthropic 5h window
#   Z5 a published allowance hardcoded for a limit the response omits
#   Z6 another product's usage presented as the plan (no tier; other type)
# Phase 3 — credential and binding (#2010 triage 7, plus three guards)
#   S1 the poll keyed provider-wide instead of by credential
#   S2 the key read before the grant is checked
#   S3 the key used after its settings file stopped naming api.z.ai
#   S4 the route matched by substring instead of exact hostname
#
# Shape follows tools/lib/endpoint-route-mutations_test.sh: tools/mutate.sh
# applies each mutation and restores byte-for-byte without touching git.

set -uo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$DIR/../.." && pwd)"
MUTATE_SH="$REPO_ROOT/tools/mutate.sh"

need() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "FAIL: zai-coding-plan-mutations — $1 not found" >&2
    exit 1
  fi
  return 0
}

quote_output() {
  sed 's/^/      | /'
  return 0
}
need go
need git

if [[ ! -x "$MUTATE_SH" ]]; then
  echo "FAIL: zai-coding-plan-mutations — $MUTATE_SH is missing or not executable" >&2
  exit 1
fi

# A dirty tree must not silently pass: mutate.sh needs a clean tree for its
# post-restore check. Hard failure in CI/strict mode, a loud skip locally.
if [[ -n "$(git -C "$REPO_ROOT" status --porcelain)" ]]; then
  echo "zai-coding-plan-mutations: CANNOT RUN — the worktree is dirty, and mutate.sh needs a" >&2
  echo "  clean tree for its post-restore check to mean anything. Commit or clean up, then:" >&2
  echo "    bash tools/lib/zai-coding-plan-mutations_test.sh" >&2
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
# The test must FAIL under the mutation, must not fail by breaking the build,
# and must print want — a red for the wrong reason is not evidence.
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
    echo "FAIL: $label — the mutation broke the BUILD rather than the guard."
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

ROUTE_GO="core/adapters/inbound/agents/processlifecycle/endpoint_route_claudecode.go"
ROUTE_PKG="./core/adapters/inbound/agents/processlifecycle/"
BINDER_GO="core/application/services/account_binder.go"
SERVICES_PKG="./core/application/services/"
PARSER_GO="core/adapters/outbound/zaiaccountapi/parser.go"
CRED_GO="core/adapters/outbound/zaiaccountapi/credential.go"
DEST_GO="core/adapters/outbound/zaiaccountapi/destination.go"
ZAI_PKG="./core/adapters/outbound/zaiaccountapi/"
RATELIMIT_GO="core/domain/session/rate_limit.go"
DOMAIN_PKG="./core/domain/session/"
KEY_GO="core/application/services/zaiaccountpoll.go"
SWEEP_GO="core/application/services/zaiaccountsweep.go"

# ── Phase 1 ─────────────────────────────────────────────────────────────────

# P1: the triage's precedence guard. Consulting the process env first is
# exactly what reported probe row B as observed on 127.0.0.1:9.
assert_go_test_goes_red \
  "P1 letting the process env outrank settings reddens probe row B" \
  "$ROUTE_GO" \
  $'\tif obs := routeFromSettings(claudesettings.Lookup(claudesettings.Sources(in), endpointSourceKey)); obs != nil {' \
  $'\tif obs := routeFromEnv(env, envErr); obs.Status == session.RouteObserved { // mutated: env first (#2010 fixture)\n\t\treturn obs\n\t}\n\tif obs := routeFromSettings(claudesettings.Lookup(claudesettings.Sources(in), endpointSourceKey)); obs != nil {' \
  "$ROUTE_PKG" \
  "TestObserveClaudeCodeRoute_SettingsBeatProcessEnv" \
  'settings.local.json must outrank the process env'

# P2: a layer that exists but cannot be parsed is unreadable, never absent.
assert_go_test_goes_red \
  "P2 reading an unparseable layer as absent reddens the unreadable-layer test" \
  "$ROUTE_GO" \
  $'\t\tobs.Status = session.RouteUnreadable' \
  $'\t\tobs.Status = session.RouteAbsent // mutated: unknown layer read as absent (#2010 fixture)' \
  "$ROUTE_PKG" \
  "TestObserveClaudeCodeRoute_UnparseableLayerIsUnreadable" \
  'want {status:unreadable'

# P3: --settings is named, not skipped — ignoring it would report the local
# file's value past a layer whose content is unknown.
assert_go_test_goes_red \
  "P3 ignoring a --settings flag reddens the settings-flag test" \
  "$ROUTE_GO" \
  $'\t\tCLIOpaque: argvErr != nil || len(argv) == 0 || claudesettings.FlagsTouchSettings(argv),' \
  $'\t\tCLIOpaque: false && (argvErr != nil || len(argv) == 0 || claudesettings.FlagsTouchSettings(argv)), // mutated: command line never consulted (#2010 fixture)' \
  "$ROUTE_PKG" \
  "TestObserveClaudeCodeRoute_SettingsFlagIsNamedNotSkipped" \
  'source:"settings:cli"}'

# P4: the binder guard (also seen red-first against main's binder).
assert_go_test_goes_red \
  "P4 letting the binder restamp another provider's snapshot reddens the binder test" \
  "$BINDER_GO" \
  $'\tif p := state.Metrics.RateLimit.Provider; p != "" && p != provider {' \
  $'\tif false { // mutated: any provider restamped (#2010 fixture)' \
  "$SERVICES_PKG" \
  "TestAccountBinder_LeavesAnotherProvidersSnapshotAlone" \
  'on a zai snapshot; the Anthropic login is not its account'

# ── Phase 2 ─────────────────────────────────────────────────────────────────

# Z1: `usage` is the limit. Swapped, the 2-of-2000 reading has remaining 1997
# above a "limit" of 2, which the parser rejects as impossible.
assert_go_test_goes_red \
  "Z1 reading usage as consumption reddens the live-probe field test" \
  "$PARSER_GO" \
  $'\t\tLimit:      *l.Usage,\n\t\tUsed:       *l.CurrentValue,' \
  $'\t\tLimit:      *l.CurrentValue, // mutated: usage read as consumption (#2010 fixture)\n\t\tUsed:       *l.Usage,' \
  "$ZAI_PKG" \
  "TestParse_LiveProbeFieldMeanings" \
  'limit CREDIT_LIMIT/5/3 reports an impossible reading'

# Z2: remaining is taken as reported; the live 1997 is not 2000-2.
assert_go_test_goes_red \
  "Z2 deriving remaining as limit minus used reddens the live-probe field test" \
  "$PARSER_GO" \
  $'\t\tRemaining:  *l.Remaining,' \
  $'\t\tRemaining:  *l.Usage - *l.CurrentValue, // mutated: remaining derived (#2010 fixture)' \
  "$ZAI_PKG" \
  "TestParse_LiveProbeFieldMeanings" \
  'want 1997 as reported'

# Z3: a missing reset stays missing — not now, not zero, not published as 0.
assert_go_test_goes_red \
  "Z3a defaulting a missing reset to now reddens the missing-reset test" \
  "$PARSER_GO" \
  $'\tif l.NextResetTime != nil {' \
  $'\tif l.NextResetTime == nil { // mutated: missing reset defaulted to now (#2010 fixture)\n\t\tnow := time.Now()\n\t\tw.ResetsAt = &now\n\t}\n\tif l.NextResetTime != nil {' \
  "$ZAI_PKG" \
  "TestParse_MissingResetStaysMissing" \
  'want nil: the response carried no nextResetTime'

assert_go_test_goes_red \
  "Z3b defaulting a missing reset to zero reddens the missing-reset test" \
  "$PARSER_GO" \
  $'\tif l.NextResetTime != nil {' \
  $'\tif l.NextResetTime == nil { // mutated: missing reset defaulted to zero (#2010 fixture)\n\t\tzero := time.Unix(0, 0)\n\t\tw.ResetsAt = &zero\n\t}\n\tif l.NextResetTime != nil {' \
  "$ZAI_PKG" \
  "TestParse_MissingResetStaysMissing" \
  'want nil: the response carried no nextResetTime'

assert_go_test_goes_red \
  "Z3c publishing a missing reset as 0 reddens the unstarted-window test" \
  "$PARSER_GO" \
  $'\tif w.ResetsAt == nil {\n\t\treturn 0, false\n\t}' \
  $'\tif w.ResetsAt == nil {\n\t\treturn 0, true // mutated: missing reset published as 0 (#2010 fixture)\n\t}' \
  "$ZAI_PKG" \
  "TestBuildSnapshot_UnstartedWindowIsNotPublished" \
  'want only the weekly window'

# Z4: #2010 §7.1 — equal duration is not the same quota.
assert_go_test_goes_red \
  "Z4 letting ForecastCap pair two providers' 5h windows reddens the forecast test" \
  "$RATELIMIT_GO" \
  $'\tif earliest.Provider != latest.Provider { // #2010: a quota needs its provider, not only its duration' \
  $'\tif false { // mutated: providers paired on duration alone (#2010 fixture)' \
  "$DOMAIN_PKG" \
  "TestForecastCap_NeverPairsTwoProvidersFiveHourWindows" \
  'ForecastCap paired an anthropic 5h window with a zai 5h window'

# Z5: #2010 §7.2 — a figure with no observation behind it is rejected.
assert_go_test_goes_red \
  "Z5 hardcoding a published allowance reddens the never-invent-a-limit test" \
  "$PARSER_GO" \
  $'\tif l.Usage == nil || *l.Usage <= 0 {\n\t\treturn Window{}, false, nil\n\t}' \
  $'\tif l.Usage == nil || *l.Usage <= 0 {\n\t\tpublished := 2000.0 // mutated: a marketing-page allowance (#2010 fixture)\n\t\tl.Usage = &published\n\t}' \
  "$ZAI_PKG" \
  "TestParse_NeverInventsALimit" \
  'want ErrNoQuotaWindows: a window with no reported limit'

# Z6: #2010 §7.3 — only the coding plan is presented as the plan.
assert_go_test_goes_red \
  "Z6a presenting a tierless response as the plan reddens the plan-only test" \
  "$PARSER_GO" \
  $'\tif q.Level == "" {' \
  $'\tif false { // mutated: no plan tier required (#2010 fixture)' \
  "$ZAI_PKG" \
  "TestBuildSnapshot_OnlyTheCodingPlanIsPresentedAsThePlan" \
  'want ErrNoCodingPlan'

assert_go_test_goes_red \
  "Z6b admitting an unverified limit type reddens the plan-only test" \
  "$PARSER_GO" \
  $'\tif l.Type != limitTypeCredit {' \
  $'\tif false { // mutated: any limit type presented (#2010 fixture)' \
  "$ZAI_PKG" \
  "TestBuildSnapshot_OnlyTheCodingPlanIsPresentedAsThePlan" \
  'an unverified limit type is dropped'

# ── Phase 3 ─────────────────────────────────────────────────────────────────

# S1: #2010 mutation 7 — two keys, two polls.
assert_go_test_goes_red \
  "S1 keying the poll provider-wide reddens the different-keys test" \
  "$KEY_GO" \
  $'\t\tAccount:  "credential:" + fingerprint,' \
  $'\t\tAccount:  "provider-wide", // mutated: one key for every credential (#2010 fixture)' \
  "$SERVICES_PKG" \
  "TestZaiAccountSweep_DifferentKeysNeverShareAReading" \
  'two keys are two polls'

# S2: the grant is checked before the key is read.
assert_go_test_goes_red \
  "S2 reading the key before checking the grant reddens the not-granted test" \
  "$SWEEP_GO" \
  $'\t\tif !granted {' \
  $'\t\t_, _ = s.deps.Credentials.Read(st.Route) // mutated: key read before the grant check (#2010 fixture)\n\t\tif !granted {' \
  "$SERVICES_PKG" \
  "TestZaiAccountSweep_NotGrantedReadsNoKeyAndClears" \
  'without the grant, want 0'

# S3: the key is used only while its file still names api.z.ai.
assert_go_test_goes_red \
  "S3 dropping the route-file host re-check reddens the still-routes-to-zai test" \
  "$CRED_GO" \
  $'\tif !IsZaiRoute(env[keyBaseURL]) {' \
  $'\tif false { // mutated: key used whatever the file now routes to (#2010 fixture)' \
  "$ZAI_PKG" \
  "TestCredentialReader_RequiresTheFileToStillRouteToZai" \
  'want settings-file-no-longer-routes-to-zai'

# S4: the binding compares the parsed hostname exactly.
assert_go_test_goes_red \
  "S4 matching the route by substring reddens the exact-host test" \
  "$DEST_GO" \
  $'\treturn strings.EqualFold(u.Hostname(), Host)' \
  $'\treturn strings.Contains(u.String(), Host) // mutated: substring match (#2010 fixture)' \
  "$ZAI_PKG" \
  "TestIsZaiRoute" \
  'IsZaiRoute("https://api.z.ai.example.com/anthropic") = true, want false'

if [[ $fails -gt 0 ]]; then
  echo "zai-coding-plan-mutations: $fails mutation(s) did not behave as required" >&2
  exit 1
fi
echo "zai-coding-plan-mutations: ok"
