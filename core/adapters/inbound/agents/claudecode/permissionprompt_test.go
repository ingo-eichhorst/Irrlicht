package claudecode

import (
	"net/http"
	"regexp"
	"strings"
	"testing"
)

// narrowPermissionMatcher is the pre-#1861 PermissionRequest / PostToolUse /
// PostToolUseFailure matcher: a nine-alternative TOOL ALLOWLIST.
//
// Kept as a literal for the same reason legacyMatcher is — it seeds the
// fixtures that prove an install written by the previous release is migrated
// rather than duplicated. It is deliberately not derived from hookMatcher; a
// derived copy would track the constant and stop being a fixture for the old
// value at the moment it mattered.
const narrowPermissionMatcher = "Bash|Write|Edit|MultiEdit|NotebookEdit|WebFetch|mcp__.*|AskUserQuestion|ExitPlanMode"

// matcherCharClass is the character class Claude Code's Wcr requires before it
// will treat a matcher as an exact-name list rather than a regexp, for the
// events SGe marks as matcher-bearing (which includes both the tool events and
// Notification). Verified against the 2.1.259 bundle.
var matcherCharClass = regexp.MustCompile(`^[a-zA-Z0-9_|, -]+$`)

// hookDelivers models Claude Code's own delivery predicate, bCt(query, matcher,
// …), read from the 2.1.259 bundle:
//
//	if(!n||n==="*")return!0;                    // literal short-circuit
//	let y=Wcr(n,r,o); …                         // exact-name list path
//	if(y!==void 0)return y.includes(e)||…
//	try{ let C=new RegExp(n); if(C.test(e))… }  // regexp path
//
// Modelling it, rather than just calling regexp.MatchString on the matcher, is
// the point: the three branches behave differently, and which one a matcher
// takes is decided by its SPELLING. A test that only compiled the matcher as a
// regexp would report `"*"` — the spelling this adapter actually installs — as
// matching nothing, because `regexp.Compile("*")` is an error in Go and a
// short-circuit in Claude Code.
//
// The alias/expansion arguments (zcr, JRt, e8) are not modelled: they only ever
// ADD matches, so ignoring them makes this predicate strictly conservative — it
// can report a miss Claude Code would have delivered, never a delivery Claude
// Code would have dropped. That is the safe direction for a test asserting
// coverage.
func hookDelivers(t *testing.T, matcher, query string) bool {
	t.Helper()
	if matcher == "" || matcher == "*" {
		return true
	}
	if matcherCharClass.MatchString(matcher) {
		for _, name := range splitMatcherNames(matcher) {
			if name == query {
				return true
			}
		}
		return false
	}
	re, err := regexp.Compile(matcher)
	if err != nil {
		// Claude Code logs "Invalid regex pattern in hook matcher" and returns
		// false — an uncompilable matcher silently delivers nothing.
		t.Errorf("matcher %q does not compile as a regexp: %v — Claude Code would "+
			"log 'Invalid regex pattern in hook matcher' and deliver NOTHING", matcher, err)
		return false
	}
	return re.MatchString(query)
}

// splitMatcherNames mirrors Wcr's `n.split(/[|,]/).map(trim).filter(Boolean)`.
func splitMatcherNames(matcher string) []string {
	names := []string{}
	for _, part := range strings.FieldsFunc(matcher, func(r rune) bool { return r == '|' || r == ',' }) {
		if name := strings.TrimSpace(part); name != "" {
			names = append(names, name)
		}
	}
	return names
}

// permissionGatedTools are tool names Claude Code can raise a permission modal
// for. Claude Code matches the settings.json matcher against the TOOL NAME
// (WQn returns e.tool_name for PermissionRequest / PostToolUse /
// PostToolUseFailure), so a name the matcher misses delivers no hook at all and
// the session keeps reading `working` behind an overlay the user is blocked
// on — #1861.
//
// The first group is what the pre-#1861 allowlist covered. The second is what
// it missed; `Read` is the issue's own reproduction (a `permissions.ask` rule
// of `Read(//etc/**)`).
//
// Note that three of the missed names are missed for a subtler reason than
// absence: the old matcher took the regexp path unanchored, so `BashOutput` and
// `TodoWrite` matched by CONTAINING `Bash` / `Write` while `KillShell`, `Read`
// and `Glob` did not match at all. An allowlist of tool names cannot be
// maintained against a tool set that grows every release — which is the
// argument for a match-all matcher rather than a longer list.
var permissionGatedTools = []string{
	// Covered before #1861.
	"Bash", "Write", "Edit", "MultiEdit", "NotebookEdit", "WebFetch",
	"mcp__server__tool", "AskUserQuestion", "ExitPlanMode",
	// Not covered before #1861.
	"Read", "Glob", "Grep", "LS", "WebSearch", "KillShell",
	"PowerShell", "Tmux", "Monitor", "REPL", "Skill", "Task", "SlashCommand",
}

// TestHookMatcher_DeliversForEveryPermissionGatedTool is #1861's class-1 defect
// test: the installed PermissionRequest matcher must cause delivery for every
// tool a permission modal can be raised for, because Claude Code — not the
// daemon — decides from it whether to send the hook at all.
func TestHookMatcher_DeliversForEveryPermissionGatedTool(t *testing.T) {
	for _, tool := range permissionGatedTools {
		if !hookDelivers(t, hookMatcher, tool) {
			t.Errorf("hookMatcher %q does not deliver for tool %q — a permission modal on %s "+
				"delivers no PermissionRequest hook, so the session stays working (#1861)",
				hookMatcher, tool, tool)
		}
	}
}

// TestHookMatcher_TakesClaudeCodesLiteralShortCircuit pins the spelling.
//
// Only "" and "*" short-circuit in bCt. ".*" also matches every tool, but by
// falling through to `new RegExp(".*")` — one Wcr call plus one regexp compile
// and test per hook evaluation, which since this matcher went match-all means
// per tool call. The constant takes the cheap branch, and this test is what
// stops it drifting back to a spelling that merely behaves the same.
func TestHookMatcher_TakesClaudeCodesLiteralShortCircuit(t *testing.T) {
	if hookMatcher != "" && hookMatcher != "*" {
		t.Errorf("hookMatcher = %q, want %q (or empty): those are the only two spellings "+
			"bCt short-circuits on (`if(!n||n===\"*\")return!0`). Any other spelling — %q "+
			"included — is delivered via a per-evaluation regexp compile instead (#1861)",
			hookMatcher, "*", ".*")
	}
}

// TestMatcherForEvent_ArmAndClearShareOneMatcher pins the structural property
// that makes widening the matcher safe at all, and is the reason #1861 widens
// one constant rather than giving PermissionRequest its own.
//
// PermissionRequest ARMS the SignalPermissionPrompt hold; PostToolUse and
// PostToolUseFailure RELEASE it. Widening only the arming side would let a tool
// arm a hold that nothing can clear, pinning the session at waiting until the
// 12-hour ceiling — the defect class #1088 shipped. Routing all three through
// matcherForEvent's single `default` arm makes the symmetry structural rather
// than a fact someone has to re-check.
//
// A LOCK: it passes on main by construction. It exists so a future change that
// splits the three apart has to say so out loud.
func TestMatcherForEvent_ArmAndClearShareOneMatcher(t *testing.T) {
	arm := matcherForEvent(HookPermissionRequest)
	for _, event := range []string{HookPostToolUse, HookPostToolUseFailure} {
		if got := matcherForEvent(event); got != arm {
			t.Errorf("matcherForEvent(%s) = %q, want %q (identical to PermissionRequest's): "+
				"a tool that can ARM the waiting hold but not CLEAR it pins the session at "+
				"waiting until permissionPromptHoldTimeout (#1861, #1088)", event, got, arm)
		}
	}
}

// seedHookGroups writes a settings.json whose only content is the given hook
// groups, so a migration test reads as "seed / install / assert" rather than
// carrying the fixture plumbing inline. Wraps hookport_test.go's seedSettings,
// which takes the whole settings object.
func seedHookGroups(t *testing.T, home string, hooks map[string]interface{}) string {
	t.Helper()
	return seedSettings(t, home, map[string]interface{}{"hooks": hooks})
}

// installedMatcher reads the matcher written for event from the settings at
// path. It fails the test unless the event carries exactly ONE group (via
// singleHookGroup) — which is the assertion that distinguishes "migrated in
// place" from "a second group appended beside the stale one". The caller runs
// EnsureHooksInstalled itself.
func installedMatcher(t *testing.T, path, event string) string {
	t.Helper()
	hooksMap, ok := readJSON(t, path)["hooks"].(map[string]interface{})
	if !ok {
		t.Fatalf("settings at %s has no hooks map", path)
	}
	matcher, _ := singleHookGroup(t, hooksMap, event)["matcher"].(string)
	return matcher
}

// TestEnsureHooksInstalled_WidensNarrowPermissionMatcherInPlace proves the
// widening actually reaches a user who already has the previous release's
// entry — the population the bug is reported from. hookjson.upgradeStaleMatchers
// reconciles a sentinel-bearing group's matcher in place, so the migration must
// rewrite, never append a second group.
//
// Asserted against the OUTCOME (does the installed matcher cause delivery for a
// tool the old one missed?) rather than against the constant, so it is a defect
// test rather than a tautology: every other install-side matcher assertion in
// this package compares the written value to the constant that wrote it and is
// therefore green whatever the constant says.
func TestEnsureHooksInstalled_WidensNarrowPermissionMatcherInPlace(t *testing.T) {
	narrow := []interface{}{ourHookGroup(narrowPermissionMatcher)}
	path := seedHookGroups(t, withTempHome(t), map[string]interface{}{
		HookPermissionRequest:  narrow,
		HookPostToolUse:        narrow,
		HookPostToolUseFailure: narrow,
	})

	if _, err := EnsureHooksInstalled(); err != nil {
		t.Fatal(err)
	}

	for _, event := range []string{HookPermissionRequest, HookPostToolUse, HookPostToolUseFailure} {
		written := installedMatcher(t, path, event)
		if !hookDelivers(t, written, "Read") {
			t.Errorf("%s: installed matcher = %q, which still does not deliver for %q — "+
				"an existing install was not widened, so the reported case stays broken (#1861)",
				event, written, "Read")
		}
	}

	// The migration must converge: a second install writes nothing further.
	modified, err := EnsureHooksInstalled()
	if err != nil {
		t.Fatal(err)
	}
	if modified {
		t.Error("expected modified=false on the second install (migration must converge)")
	}
}

// blockingNotificationTypes are the notification_type values Claude Code emits
// when a human is blocked on a dialog. Each is #1861's class 2: the dialog
// carries no tool name, so no PermissionRequest fires for it at any matcher
// width, and this notification is its only hook signal.
//
// Taken from the 2.1.259 bundle's dialog-descriptor table, NOT from dialog
// names: the MCP elicitation dialogs set type elicitation_dialog /
// elicitation_url_dialog explicitly, so permission_prompt (which is only the
// DEFAULT for a dialog notification that sets no type) does not cover them.
// #1861's first revision missed exactly that and shipped a matcher covering
// two of these five.
var blockingNotificationTypes = []string{
	"permission_prompt",
	"elicitation_dialog",
	"elicitation_url_dialog",
	"agent_needs_input",
}

// allDrivingNotificationTypes is every type the daemon acts on: the blocking
// dialogs plus idle_prompt. Kept separate from blockingNotificationTypes
// because the handler must route idle_prompt to a DIFFERENT method, so the
// dispatch test must not include it.
var allDrivingNotificationTypes = append([]string{"idle_prompt"}, blockingNotificationTypes...)

// TestHookMatcherNotification_DeliversForEveryBlockingType is #1861's class-2
// defect test on the install side.
func TestHookMatcherNotification_DeliversForEveryBlockingType(t *testing.T) {
	for _, ntype := range allDrivingNotificationTypes {
		if !hookDelivers(t, hookMatcherNotification, ntype) {
			t.Errorf("hookMatcherNotification %q does not deliver for notification_type %q — "+
				"Claude Code never sends it, so a blocking dialog of that kind leaves the "+
				"session reading working (#1861)", hookMatcherNotification, ntype)
		}
	}
}

// TestNotificationTypesDrivingState_MatchesTheInstalledMatcher pins the two
// halves against each other. The matcher decides what Claude Code DELIVERS; the
// map decides what the handler ACTS on. A type in one and not the other is
// either a POST that exists only to be dropped, or — the direction that costs a
// user a `waiting` — an installed delivery the handler silently ignores.
func TestNotificationTypesDrivingState_MatchesTheInstalledMatcher(t *testing.T) {
	for ntype := range notificationTypesDrivingState {
		if !hookDelivers(t, hookMatcherNotification, ntype) {
			t.Errorf("handler acts on notification_type %q but hookMatcherNotification %q "+
				"never asks Claude Code to deliver it — the branch is dead (#1861)",
				ntype, hookMatcherNotification)
		}
	}
	for _, name := range splitMatcherNames(hookMatcherNotification) {
		if _, drivesState := notificationTypesDrivingState[name]; !drivesState {
			t.Errorf("hookMatcherNotification installs %q but notificationTypesDrivingState "+
				"does not act on it — every delivery of it is a POST we exist only to drop",
				name)
		}
	}
}

// TestEnsureHooksInstalled_WidensNotificationMatcherInPlace is the install-path
// half of the class-2 fix: an existing install written with the idle_prompt-only
// matcher must be reconciled in place, not duplicated.
func TestEnsureHooksInstalled_WidensNotificationMatcherInPlace(t *testing.T) {
	path := seedHookGroups(t, withTempHome(t), map[string]interface{}{
		HookNotification: []interface{}{ourHookGroup("idle_prompt")},
	})

	if _, err := EnsureHooksInstalled(); err != nil {
		t.Fatal(err)
	}

	written := installedMatcher(t, path, HookNotification)
	for _, ntype := range allDrivingNotificationTypes {
		if !hookDelivers(t, written, ntype) {
			t.Errorf("installed Notification matcher = %q, which does not deliver for %q (#1861)",
				written, ntype)
		}
	}
}

// TestHookHandler_NotificationBlockingDialog is #1861's class-2 defect test on
// the receiving side. Widening the matcher only gets the POST delivered;
// handleNotificationHook rejected every type but idle_prompt, so the delivery
// would have been logged and dropped.
//
// It must reach HandlePermissionPromptHook and NOT HandleIdlePromptHook, and it
// must carry a DISCRIMINATED wire name: both branches record a lifecycle
// KindHookReceived and they hold different signals, so a shared bare
// "Notification" would make them indistinguishable in the trace and in every
// recording.
func TestHookHandler_NotificationBlockingDialog(t *testing.T) {
	for _, ntype := range blockingNotificationTypes {
		t.Run(ntype, func(t *testing.T) {
			target := &mockTarget{}
			handler := NewHookHandler(target, nil, nil, mockLogger{})
			promptPath := inTreeTranscript(t, "sess-permprompt")

			rec := postHook(t, handler, hookPayload{
				TranscriptPath:   promptPath,
				HookEventName:    "Notification",
				NotificationType: ntype,
			})

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
			}
			prompts := target.getPermissionPromptCalls()
			if len(prompts) != 1 {
				t.Fatalf("got %d HandlePermissionPromptHook calls, want 1 — this notification "+
					"is the only hook signal a tool-less blocking dialog emits (#1861)", len(prompts))
			}
			if prompts[0].sessionID != "sess-permprompt" {
				t.Errorf("sessionID = %q, want %q", prompts[0].sessionID, "sess-permprompt")
			}
			if prompts[0].transcriptPath != promptPath {
				t.Errorf("transcriptPath = %q, want the payload path", prompts[0].transcriptPath)
			}
			if want := "Notification/" + ntype; prompts[0].hookEventName != want {
				t.Errorf("hookEventName = %q, want %q — a bare %q would be indistinguishable "+
					"from the idle_prompt branch, which holds a different signal (#1861)",
					prompts[0].hookEventName, want, "Notification")
			}
			if n := len(target.getIdlePromptCalls()); n != 0 {
				t.Errorf("got %d HandleIdlePromptHook calls, want 0: the two hold different "+
					"signals with different staleness rules", n)
			}
			if len(target.getCalls()) != 0 || len(target.getStopCalls()) != 0 || len(target.getCompactCalls()) != 0 {
				t.Error("must not reach the permission/stop/compact paths")
			}
		})
	}
}

// TestHookHandler_NotificationBlockingDialogConsentGated confirms the new
// branch sits inside BOTH consent gates rather than beside them. The #1740
// registry tripwire is package-granular and cannot see a single branch that
// escapes a gate (docs/testing-contracts.md), so this is the assertion that
// covers it.
//
// keyedGate rather than fakeGate, deliberately: fakeGate answers false for
// every key, so it cannot tell a receiver gated on "hooks" from one gated on
// "transcripts", and would pass even if the new branch honoured only one of
// them. Both directions are asserted because admitHookRequest and
// DecodeConfined are two independent chokepoints (#1466).
func TestHookHandler_NotificationBlockingDialogConsentGated(t *testing.T) {
	cases := []struct {
		name string
		gate keyedGate
	}{
		{"hooks denied", keyedGate{PermissionKeyHooks: false, PermissionKeyTranscripts: true}},
		{"transcripts denied", keyedGate{PermissionKeyHooks: true, PermissionKeyTranscripts: false}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			target := &mockTarget{}
			handler := NewHookHandler(target, nil, tc.gate, mockLogger{})

			rec := postHook(t, handler, hookPayload{
				TranscriptPath:   inTreeTranscript(t, "sess-gated-perm"),
				HookEventName:    "Notification",
				NotificationType: "permission_prompt",
			})

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			if n := len(target.getPermissionPromptCalls()); n != 0 {
				t.Errorf("dispatched %d permission-prompt calls while the permission was not granted", n)
			}
		})
	}
}
