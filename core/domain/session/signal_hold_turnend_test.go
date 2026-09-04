package session

import "testing"

// TestSignalHolds_PermissionPromptClearsOnHookTurnEnd is #1861's clearing-edge
// test, and the reason the SignalPermissionPrompt row may be armed by something
// other than a tool-keyed hook at all.
//
// #1861 adds a second asserter for this signal: claudecode's
// Notification/permission_prompt, which fires for the blocking dialogs that
// carry no tool name (sandbox_network_access, auto_mode_*, mcp_elicitation,
// managed_settings_security). Those dialogs produce no PostToolUse, which is
// the release edge every existing asserter relies on — so without a second
// clearing edge the hold would ride to permissionPromptHoldTimeout's 12-hour
// ceiling. A waiting hold with no clearing edge is the defect class #1088
// shipped; a ceiling alone is a backstop, not an edge.
//
// The edge is the Stop hook: Claude Code cannot end a turn while a blocking
// dialog is on screen, so an authoritative turn boundary is proof no dialog is
// open. Reading HookTurnDone (hook-tier evidence) rather than IsAgentDone
// (which would also accept the transcript-tail heuristic) is deliberate — see
// the row's own comment for why the heuristic half is not safe here.
//
// The transcript tail is deliberately NOT "turn_done": that is what makes the
// hook half observable on its own, the same construction
// TestSignalHolds_ApplicationOrderIsLoadBearing uses.
func TestSignalHolds_PermissionPromptClearsOnHookTurnEnd(t *testing.T) {
	h := NewSignalHolds()
	h.Hold(holdSID, SignalPermissionPrompt, SignalPayload{}, holdT0)
	h.Hold(holdSID, SignalTurnDone, SignalPayload{}, holdT0)

	m := &SessionMetrics{LastEventType: "assistant_message"}
	h.Overlay(holdSID, m, holdT0)

	// No explicit row-order assertion here on purpose. Asserting `m.HookTurnDone`
	// would be INERT — SignalTurnDone declares no stale and no ripe, so its apply
	// runs and sets the flag in either table order — and a hand-rolled index
	// comparison would only restate what two other things already cover: the two
	// assertions below fail directly if the rows are swapped (the permission row
	// would evaluate its staleness before turn-done wrote HookTurnDone, so the
	// hold would survive), and TestSignalPolicies_OrderIsPinned pins the exact
	// full sequence with the reason.
	if m.PermissionPending {
		t.Error("PermissionPending must not be set: the Stop hook landed with no tool open, " +
			"so no blocking dialog can still be on screen (#1861)")
	}
	if h.Held(holdSID, SignalPermissionPrompt) {
		t.Error("the permission hold must be dropped, not merely left unapplied — " +
			"an unapplied hold would re-apply on the next pass and pin the session at waiting")
	}
}

// TestSignalHolds_PermissionPromptSurvivesTurnEndWhileAToolIsOpen is a LOCK on
// the guard half of the clearing edge above: it pins behaviour that must NOT
// change, and passes on main by construction.
//
// Claude Code fires Stop at true turn end, but IsAgentDone's own doc records
// that a turn-done signal can arrive while a tool call is still outstanding (a
// sub-agent spawned via the Agent tool fires turn_done before its tool result
// comes back). An open tool call is exactly the shape a tool permission prompt
// has — #307's AskUserQuestion / ExitPlanMode fast path most of all — so an
// unguarded turn-end release would drop the very holds the fast path exists to
// place. The !HasOpenToolCall half of the rule is what keeps that from
// happening, and this test is what keeps the half from being deleted as
// redundant.
func TestSignalHolds_PermissionPromptSurvivesTurnEndWhileAToolIsOpen(t *testing.T) {
	h := NewSignalHolds()
	h.Hold(holdSID, SignalPermissionPrompt, SignalPayload{}, holdT0)
	h.Hold(holdSID, SignalTurnDone, SignalPayload{}, holdT0)

	m := &SessionMetrics{
		LastEventType:     "assistant_message",
		HasOpenToolCall:   true,
		LastOpenToolNames: []string{"ExitPlanMode"},
	}
	h.Overlay(holdSID, m, holdT0)

	if !m.PermissionPending {
		t.Error("PermissionPending must survive a Stop hook that arrives with a tool still open: " +
			"that is #307's ExitPlanMode / AskUserQuestion fast path")
	}
	if !h.Held(holdSID, SignalPermissionPrompt) {
		t.Error("the hold itself must survive too, or the next pass loses the correction")
	}
}
