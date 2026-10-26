#!/usr/bin/env bash
# opencode-v2-hooks-mutations_test.sh — committed mutation fixture for issue
# #2108: opencode 2.x gets its own hook artifact at the same path, selected by
# the installed version, and the receiver drops each 2.x event's repeat.
#
# WHY THIS FILE EXISTS. #2108's defect tests were seen red on the base (the PR
# body carries the runs). What they cannot show is that each guard the change
# ADDS reaches what it protects — the variant selection in
# agent.ManagedUserFile.SelectVariant, the service running the SELECTED
# variant's Apply, the consent gate on the store reader, the probe rule, the
# #1372 loop verifying the selected variant, the receiver's dedupe and turn-end
# arms, the 2.x artifact's shape guards, the store reader's ordering, and the
# contract arms graded by their self-tests. Each mutation below breaks one
# guard and requires the named test to go red with the named message:
#
#   1. SelectVariant without the unknown-version refusal → opencode's version
#      contract wiring goes red (the arm grades domain code no wiring can get
#      wrong, so its mutation is here).
#   2. SelectVariant comparing variants by floor only → the 1.x artifact is
#      selected at 2.x versions.
#   3. runClosureEffect running the permission's own Apply instead of the
#      selected variant's → a 1.x file survives a grant against 2.1.4.
#   4. resolveInstalledVersion reading Observed without its permission → the
#      store is read while `database` is denied.
#   5. shouldConfirmByProbe always probing → a store answer of 1.19.4 is
#      overridden by a probe's 2.1.4 (the downward direction is held by #24).
#   6. the #1372 loop verifying a variant target with the permission's own
#      Verify → a 1.x file under 2.1.4 reads intact.
#   7. the receiver without the dedupe → one event delivered twice dispatches
#      twice.
#   8. the dedupe keyed on the event NAME → two prompts in one turn collapse.
#   9. the receiver without the 2.x turn ends → an interrupt never releases.
#  10. the dedupe set never evicting → it grows past its bound.
#  11. the 2.x plugin reading a second context member → its shape guard.
#  12. the 2.x plugin handing the context on → its shape guard.
#  13. the 2.x plugin dropping event_id → its payload guard.
#  14. the 2.x plugin exporting a third key → its export guard.
#  15. the count arm accepting a missing variant count → its self-test.
#  16. the disjointness arm disabled → its self-test.
#  17. the envelope arm disabled → its self-test.
#  18. the variant Apply/Verify check disabled → its self-test.
#  19. the disclosure arm ignoring variant bounds → its self-test.
#  20. opencode's copy stating only the 1.x count → opencode's real
#      disclosure wiring goes red.
#  21. opencode's gate without ObservedRequires → the store is read while
#      `database` is denied, through the real declaration.
#  22. the store reader ordering by time_updated → an imported 1.x session
#      touched last decides the major.
#  23. the receiver without the event-id length bound → an oversized id is
#      dispatched and remembered.
#  24. resolveInstalledVersion taking a probe that LOWERS a refused store
#      answer → the 1.x plugin is written for a 3.x / 2.0.x opencode (review
#      finding 1 of PR #2115).
#  25. the install refusing an unknown version with a current artifact on
#      disk → a working install is recorded as not applied (finding 2).
#  26. the #1372 loop reading an unknown version as damage with a current
#      artifact on disk → it repairs what nothing broke (finding 2).
#  27. the #1372 loop not removing our file above every artifact → the 2.x
#      plugin survives a 3.0.0 opencode.
#  28. the #1372 loop reporting a major change as damage → the designed
#      upgrade rewrite is logged as someone else's tampering (finding 3).
#  29. firstVariantReading (behind currentVariant and oursPresent) swallowing
#      a failed Verify → above every artifact, a plugin the loop cannot read
#      reads intact (follow-up review finding 1).
#  30. the install's keep rule swallowing a failed Verify → an unknown
#      version's refusal hides that the installed file could not be read.
#
# tools/mutate.sh owns the mechanics this file must not re-improvise: the
# stale/ambiguous-anchor guards, and the byte-for-byte restore that never
# touches git state. Modeled on opencode-v2-harmless-mutations_test.sh.

set -uo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$DIR/../.." && pwd)"
MUTATE_SH="$REPO_ROOT/tools/mutate.sh"

need() {
  local tool="$1"
  if ! command -v "$tool" >/dev/null 2>&1; then
    echo "FAIL: opencode-v2-hooks-mutations — $tool not found" >&2
    exit 1
  fi
  return 0
}
quote_output() { sed 's/^/      | /'; return 0; }
need go
need git

if [[ ! -x "$MUTATE_SH" ]]; then
  echo "FAIL: opencode-v2-hooks-mutations — $MUTATE_SH is missing or not executable" >&2
  exit 1
fi

if [[ -n "$(git -C "$REPO_ROOT" status --porcelain)" ]]; then
  echo "opencode-v2-hooks-mutations: CANNOT RUN — the worktree is dirty, and mutate.sh needs a" >&2
  echo "  clean tree for its post-restore check to mean anything. Commit or clean up, then:" >&2
  echo "    bash tools/lib/opencode-v2-hooks-mutations_test.sh" >&2
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

DOMAIN="core/domain/agent/declaration.go"
GATE="core/application/services/permission_version_gate.go"
SERVICE="core/application/services/permission_service.go"
REVERIFY="core/application/services/hook_reverify.go"
HOOKS="core/adapters/inbound/agents/opencode/hooks.go"
PLUGIN_V2="core/adapters/inbound/agents/opencode/plugin_v2.js"
AGENT="core/adapters/inbound/agents/opencode/agent.go"
STOREVER="core/adapters/inbound/agents/opencode/storeversion.go"
VERSION_ARMS="core/internal/contracttesting/hook_version.go"
DISCLOSURE_ARMS="core/internal/contracttesting/hook_disclosure.go"
OPENCODE="./core/adapters/inbound/agents/opencode/"
SERVICES="./core/application/services/"
CONTRACTS="./core/internal/contracttesting/"

# ── 1. SelectVariant without the unknown-version refusal ──
assert_go_test_goes_red \
  "SelectVariant guessing an artifact for an unreadable version" \
  "$DOMAIN" \
  'if _, ok := cliversion.Parse(installed); !ok {' \
  'if _, ok := cliversion.Parse(installed); false && !ok {' \
  "$OPENCODE" \
  '^TestHooksPermission_DeclaresVersionGate$' \
  "guessing the major installs the file the other one rejects"

# ── 2. SelectVariant comparing variants by floor only ──
assert_go_test_goes_red \
  "SelectVariant ignoring each variant's ceiling" \
  "$DOMAIN" \
  'switch bound, _ := v.gate().Check(installed); bound {' \
  'switch bound, _ := (&VersionGate{Min: v.Min}).Check(installed); bound {' \
  "$OPENCODE" \
  '^TestHooksPermission_DeclaresVersionGate$' \
  "outside its declared range"

# ── 3. the service running the permission's own Apply ──
assert_go_test_goes_red \
  "runClosureEffect ignoring the selected variant" \
  "$SERVICE" \
  'return variant.Apply, bound, nil' \
  'return e.perm.Apply, bound, nil' \
  "$SERVICES" \
  '^TestOpenCodeUpgrade_RewritesTheOneXPluginInPlace$' \
  "is not the 2.x artifact"

# ── 4. Observed read without its permission ──
assert_go_test_goes_red \
  "resolveInstalledVersion ignoring ObservedRequires" \
  "$GATE" \
  'if gate.Observed != nil && observedAllowed(granted, agentName, gate.ObservedRequires) {' \
  'if gate.Observed != nil {' \
  "$SERVICES" \
  '^TestVariantGate_ObservedIsNotConsultedWithoutItsPermission$' \
  "Observed was called 1 time(s) while the database permission is not granted"

# ── 5. the probe overriding a store answer that selects an artifact ──
assert_go_test_goes_red \
  "shouldConfirmByProbe always probing" \
  "$GATE" \
  $'_, bound, _ := w.SelectVariant(installed)\n\treturn bound != agent.WithinBounds' \
  $'_, bound, _ := w.SelectVariant(installed)\n\treturn bound != agent.WithinBounds || true' \
  "$SERVICES" \
  '^TestVariantGate_AStoreAnswerWinsOverTheProbe$' \
  "store 1.19.4 / probe 2.1.4 installed"

# ── 6. the #1372 loop verifying with the permission's own Verify ──
assert_go_test_goes_red \
  "verifyTarget ignoring the variant selection" \
  "$REVERIFY" \
  $'if t.variants == nil {\n\t\tstatus, err = t.verify()' \
  $'if true {\n\t\tstatus, err = t.verify()' \
  "$SERVICES" \
  '^TestVariantGate_ReverifyRewritesAOneXFileAfterAnUpgrade$' \
  "want one repair"

# ── 7. the receiver without the dedupe ──
assert_go_test_goes_red \
  "serveHookRequest without the event-id dedupe" \
  "$HOOKS" \
  'if payload.EventID != "" && !seen.firstSighting(payload.EventID) {' \
  'if false && payload.EventID != "" && !seen.firstSighting(payload.EventID) {' \
  "$OPENCODE" \
  '^TestHookReceiver_ARepeatedEventIDDispatchesOnce$' \
  "HandlePermissionPromptHook called 2 times for one event delivered twice"

# ── 8. the dedupe keyed on the event name ──
assert_go_test_goes_red \
  "the dedupe keyed on hook_event_name" \
  "$HOOKS" \
  'seen.firstSighting(payload.EventID)' \
  'seen.firstSighting(payload.HookEventName)' \
  "$OPENCODE" \
  '^TestHookReceiver_DistinctEventIDsAllDispatch$' \
  "two distinct permission.asked events dispatched 1 times"

# ── 9. the receiver without the 2.x turn ends ──
assert_go_test_goes_red \
  "serveHookRequest without session.execution.*" \
  "$HOOKS" \
  'case HookEventSessionIdle, HookEventExecutionSucceeded, HookEventExecutionFailed, HookEventExecutionInterrupted:' \
  'case HookEventSessionIdle:' \
  "$OPENCODE" \
  '^TestHookReceiver_TwoXTurnEndsReleaseAndReportTurnEnd$' \
  "HandleStopHook called 0 times, want 1"

# ── 10. the dedupe set never evicting ──
assert_go_test_goes_red \
  "recentEventIDs without eviction" \
  "$HOOKS" \
  $'\t\tdelete(r.ids, old)\n' \
  '' \
  "$OPENCODE" \
  '^TestRecentEventIDs_ForgetsTheOldestPastItsBound$' \
  "past its bound of 2"

# ── 11. the 2.x plugin reading a second context member ──
assert_go_test_goes_red \
  "plugin_v2.js reading ctx.permission" \
  "$PLUGIN_V2" \
  $'\t\tvoid forward(ctx);\n' \
  $'\t\tvoid forward(ctx);\n\t\tctx.permission;\n' \
  "$OPENCODE" \
  '^TestShippedV2PluginUsesOnlyTheEventBus$' \
  "want exactly [event]"

# ── 12. the 2.x plugin handing the context on ──
assert_go_test_goes_red \
  "plugin_v2.js passing the context elsewhere" \
  "$PLUGIN_V2" \
  $'\t\tvoid forward(ctx);\n' \
  $'\t\tvoid forward(ctx);\n\t\tglobalThis.irrlichtContext = ctx;\n' \
  "$OPENCODE" \
  '^TestShippedV2PluginUsesOnlyTheEventBus$' \
  "names the context outside the allowed hand-offs"

# ── 13. the 2.x plugin dropping event_id ──
assert_go_test_goes_red \
  "plugin_v2.js without event_id" \
  "$PLUGIN_V2" \
  $'\t\t\t\t\tevent_id: event.id == null ? "" : String(event.id),\n' \
  '' \
  "$OPENCODE" \
  '^TestShippedPluginPayloadMatchesTheReceiversFields$' \
  'openCodeHookPayload decodes "event_id" but plugin_v2.js never writes it'

# ── 14. the 2.x plugin exporting a third key ──
assert_go_test_goes_red \
  "plugin_v2.js exporting an event hook" \
  "$PLUGIN_V2" \
  $'\tid: "irrlicht",\n' \
  $'\tid: "irrlicht",\n\tevent: async function () {},\n' \
  "$OPENCODE" \
  '^TestShippedV2PluginExportsAnIDAndASetup$' \
  "want exactly [id setup]"

# ── 15. the count arm accepting a missing variant count ──
assert_go_test_goes_red \
  "assertStatesEveryVariantCount without the per-variant clause" \
  "$DISCLOSURE_ARMS" \
  $'\t\tif !stated[n] {' \
  $'\t\tif false && !stated[n] {' \
  "$CONTRACTS" \
  '^TestVariantArm_CopyStatingOneVariantsCount$' \
  "copy stating only the first variant's count"

# ── 16. the disjointness arm disabled ──
assert_go_test_goes_red \
  "assertVariantsDisjoint disabled" \
  "$VERSION_ARMS" \
  'if startsBeforeEnd(a.Min, b.Max) && startsBeforeEnd(b.Min, a.Max) {' \
  'if false && startsBeforeEnd(a.Min, b.Max) && startsBeforeEnd(b.Min, a.Max) {' \
  "$CONTRACTS" \
  '^TestVariantArm_OverlappingVariants$' \
  "two variants whose ranges share"

# ── 17. the envelope arm disabled ──
assert_go_test_goes_red \
  "assertGateSpansItsVariants ignoring the floor" \
  "$VERSION_ARMS" \
  'if gate.Min != lowest {' \
  'if false && gate.Min != lowest {' \
  "$CONTRACTS" \
  '^TestVariantArm_AGateWiderThanItsVariants$' \
  "an envelope floor below the lowest variant's"

# ── 18. the variant Apply/Verify check disabled ──
assert_go_test_goes_red \
  "assertVariant without the Apply/Verify check" \
  "$VERSION_ARMS" \
  'if v.Apply == nil || v.Verify == nil {' \
  'if false && (v.Apply == nil || v.Verify == nil) {' \
  "$CONTRACTS" \
  '^TestVariantArm_AVariantWithNoApply$' \
  "a variant declaring no Apply"

# ── 19. the disclosure arm ignoring variant bounds ──
assert_go_test_goes_red \
  "assertStatesVersionFloor without the variant clause" \
  "$DISCLOSURE_ARMS" \
  'if bound.value != "" && !strings.Contains(disclosure, bound.value) {' \
  'if false && bound.value != "" && !strings.Contains(disclosure, bound.value) {' \
  "$CONTRACTS" \
  '^TestVariantArm_CopyMissingAVariantsFloor$' \
  "copy that never states the second variant's floor"

# ── 20. opencode's copy stating only the 1.x count ──
assert_go_test_goes_red \
  "opencode's consent copy without the 2.x count" \
  "$AGENT" \
  'strconv.Itoa(len(installedHookEventsV2)) + " hook entries (opencode 2.x) to " +' \
  '"" +' \
  "$OPENCODE" \
  '^TestHooksPermission_DisclosureMatchesInstalled$' \
  "consent copy never states 5 hook entries"

# ── 21. opencode's gate without ObservedRequires ──
assert_go_test_goes_red \
  "opencode's gate reading the store behind no permission" \
  "$AGENT" \
  $'\t\t\t\t\t\tObservedRequires: PermissionKeyDatabase,\n' \
  '' \
  "$SERVICES" \
  '^TestVariantGate_ObservedIsNotConsultedWithoutItsPermission$' \
  "Observed was called 1 time(s) while the database permission is not granted"

# ── 22. the store reader ordering by time_updated ──
assert_go_test_goes_red \
  "newestObservedCLIVersion ordering by time_updated" \
  "$STOREVER" \
  'SELECT version FROM session_v2 ORDER BY time_created DESC, id DESC LIMIT 1' \
  'SELECT version FROM session_v2 ORDER BY time_updated DESC, id DESC LIMIT 1' \
  "$OPENCODE" \
  '^TestNewestObservedCLIVersion_CreationNotUpdateDecides$' \
  "after the imported 1.x session was updated last, want 2.1.4"

# ── 23. the receiver without the event-id length bound ──
assert_go_test_goes_red \
  "serveHookRequest without maxEventIDLen" \
  "$HOOKS" \
  'if len(payload.EventID) > maxEventIDLen {' \
  'if false && len(payload.EventID) > maxEventIDLen {' \
  "$OPENCODE" \
  '^TestHookReceiver_AnOversizedEventIDIsDropped$' \
  "event id dispatched 1 times, want 0"

# ── 24. a probe lowering a refused store answer ──
assert_go_test_goes_red \
  "resolveInstalledVersion taking a lower probe" \
  "$GATE" \
  'if len(w.Variants) > 0 && cliversion.Less(probed, installed) {' \
  'if false && len(w.Variants) > 0 && cliversion.Less(probed, installed) {' \
  "$SERVICES" \
  '^TestVariantGate_AProbeCannotLowerARefusedStoreAnswer$' \
  "the probe lowered the version to a major the store says is not running"

# ── 25. the install refusing an unknown version over a current artifact ──
assert_go_test_goes_red \
  "hookVersionRefusal without keeping a current install" \
  "$GATE" \
  'if kept != nil {' \
  'if false && kept != nil {' \
  "$SERVICES" \
  '^TestVariantGate_AnUnknownVersionAtStartKeepsACurrentInstall$' \
  "with a current install on disk, want none"

# ── 26. the loop reading an unknown version as damage ──
assert_go_test_goes_red \
  "unselectedStatus ignoring a current install" \
  "$REVERIFY" \
  'if err != nil || current != nil {' \
  'if err != nil || (current != nil && false) {' \
  "$SERVICES" \
  '^TestVariantGate_AnUnknownVersionKeepsACurrentInstall$' \
  "want no repair at all"

# ── 27. the loop not removing our file above every artifact ──
assert_go_test_goes_red \
  "unselectedStatus without the ceiling removal" \
  "$REVERIFY" \
  'if err != nil || !present {' \
  'if err != nil || !present || true {' \
  "$SERVICES" \
  '^TestVariantGate_ReverifyAtTheNextMajorRemovesOnceThenRests$' \
  "the 2.x plugin survived a pass under opencode 3.0.0"

# ── 28. a major change reported as damage ──
assert_go_test_goes_red \
  "verifyTarget without the upgrade note" \
  "$REVERIFY" \
  'if current, _ := currentVariant(t.variants); current != nil && current != variant {' \
  'if current, _ := currentVariant(t.variants); false && current != nil && current != variant {' \
  "$SERVICES" \
  '^TestVariantGate_AnUpgradeIsNotReportedAsTampering$' \
  "the 1.x → 2.x rewrite was logged as tampering"

# ── 29. oursPresent swallowing a failed Verify ──
assert_go_test_goes_red \
  "firstVariantReading reading a failed Verify as nothing there" \
  "$GATE" \
  $'\t\tif err != nil {\n\t\t\treturn nil, err' \
  $'\t\tif err != nil {\n\t\t\treturn nil, nil' \
  "$SERVICES" \
  '^TestVariantGate_ReverifyCannotReadThePluginAtTheNextMajor$' \
  "want unreadable:1"

# ── 30. the keep rule swallowing a failed Verify ──
assert_go_test_goes_red \
  "hookVersionRefusal hiding a failed read of the installed file" \
  "$GATE" \
  $'kept, err := currentVariant(p.Writes)\n\t\tif err != nil {' \
  $'kept, err := currentVariant(p.Writes)\n\t\tif false && err != nil {' \
  "$SERVICES" \
  '^TestVariantGate_AnUnknownVersionCannotReadThePlugin$' \
  "want it to carry the failed read of the installed plugin"

if [[ $fails -gt 0 ]]; then
  echo "opencode-v2-hooks-mutations: $fails FAILED"
  exit 1
fi
echo "opencode-v2-hooks-mutations: ALL PASS"
