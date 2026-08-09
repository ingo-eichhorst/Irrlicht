package session

import "time"

// Hook event names, as the agent CLIs put them on the wire. Claude Code fires
// all seven; Codex fires the PermissionRequest / PostToolUse / Stop subset.
//
// These live in the domain rather than in the claudecode adapter that owns the
// HTTP handler because three separate layers need to agree on them and two of
// them cannot import that adapter:
//
//   - application/services (SessionDetector) — barred by core/architecture_test.go's
//     "application/services must reach adapters through ports" rule, which is
//     exactly why the detector used to restate every name as a bare literal;
//   - tools/onboarding-factory's replay harness, which mirrors the detector's
//     hook handling against frozen recordings.
//
// The claudecode adapter re-exports these as its own HookXxx constants, so
// adapter-side call sites are unchanged.
const (
	HookPermissionRequest  = "PermissionRequest"
	HookPreToolUse         = "PreToolUse"
	HookPostToolUse        = "PostToolUse"
	HookPostToolUseFailure = "PostToolUseFailure"
	HookPreCompact         = "PreCompact"
	// HookStop fires once at true turn end, carrying last_assistant_message.
	// It is the authoritative turn-done signal (issue #1161).
	HookStop = "Stop"
	// HookNotification fires for agent UI notifications, carrying a
	// notification_type discriminator; the daemon acts only on idle_prompt
	// (issue #1173).
	HookNotification = "Notification"
)

// AllHookEvents is every hook event name the constants above define, in
// declaration order. It is the universe an adapter's consent copy is checked
// against by contracttesting.AssertHookDisclosureMatchesInstalled (issue
// #1356): a name in here that the adapter's disclosure text mentions but does
// not install is a promise it doesn't keep, in the opposite direction from the
// #1173 drift that motivated the contract.
//
// Every constant above must appear here. That obligation is not left to
// discipline — it is exactly the kind of hand-maintained restatement #1356 was
// about — so TestAllHookEvents_CoversEveryConstant reads this file's own source
// and fails on a constant that was never added.
var AllHookEvents = []string{
	HookPermissionRequest,
	HookPreToolUse,
	HookPostToolUse,
	HookPostToolUseFailure,
	HookPreCompact,
	HookStop,
	HookNotification,
}

// HookSignalEffect is what one hook event name does to a session's signal
// holds. Release distinguishes the two directions: a hook either asserts a
// signal (Release false) or retracts one (Release true).
type HookSignalEffect struct {
	Signal  SignalKind
	Release bool
}

// hookSignalEffects is the single source of truth for hook-name → SignalKind
// for the hooks a caller may act on knowing only the name.
//
// Three hooks are absent, for two different reasons — worth keeping apart,
// because they imply different things about whether a future row belongs here:
//
//   - HookStop genuinely cannot be a row. Its effect carries a payload (the
//     turn's final assistant text and the waiting-cue verdict computed from it)
//     into SignalPayload, and a name-keyed lookup has no way to supply that.
//     HandleStopHook stays a dedicated entry point.
//   - HookNotification and HookPreCompact are excluded only because their
//     *gate* is adapter-side: the claudecode HTTP handler forwards Notification
//     solely for notification_type "idle_prompt" and PreCompact solely for
//     trigger "manual", so the decision is already made before the detector is
//     called. Both holds themselves are payload-free. A consumer reading
//     post-gate events (the replay harness — dispatchHookActivity is the only
//     writer of lifecycle.KindHookReceived, and it runs downstream of those
//     gates, so a recorded PreCompact means manual by construction) could act
//     on the name alone. They are absent because no recording fires one yet;
//     when one does, a row here is the right fix, not a bespoke branch.
//
// A caller that gets ok==false has learned "I may not act on this name alone",
// not "this hook is unknown".
//
// The rows below were previously written twice — once as a switch in
// SessionDetector.HandlePermissionHook and once as a switch in the replay
// harness's applyHookEvent — and had drifted: the harness silently ignored
// PreToolUse, which the daemon holds on (issue #1320). Adding a name-determined
// signal is now one row here rather than a row plus a detector case plus a
// replay case.
var hookSignalEffects = map[string]HookSignalEffect{
	// A permission prompt is open and the user is blocked on it (#108, #1171).
	HookPermissionRequest: {Signal: SignalPermissionPrompt},
	// PreToolUse fires synchronously when the model emits a tool_use block,
	// before the assistant message reaches the JSONL. For AskUserQuestion and
	// ExitPlanMode (the installed matcher) that flips working → waiting without
	// waiting on transcript flush latency (#307).
	HookPreToolUse: {Signal: SignalPermissionPrompt},
	// The tool ran, so whatever was blocking on it no longer is.
	HookPostToolUse:        {Signal: SignalPermissionPrompt, Release: true},
	HookPostToolUseFailure: {Signal: SignalPermissionPrompt, Release: true},
}

// HookSignal reports the signal effect of a hook event name. ok is false for a
// name this table does not determine on its own — either an unrecognized hook,
// or one of the payload-gated hooks documented on hookSignalEffects.
func HookSignal(hookName string) (HookSignalEffect, bool) {
	effect, ok := hookSignalEffects[hookName]
	return effect, ok
}

// ApplyHook applies a hook's effect to sessionID's holds — the assert/retract
// branch that the detector and the replay harness would otherwise each write
// out. Keeping it here means the two consumers of HookSignal share not just
// the mapping but what they do with it, so a new row cannot be honoured by one
// and half-honoured by the other (issue #1320).
//
// at is the observation time — wall-clock in the daemon, virtual time in
// replay. Only the hold direction reads it; Release is time-independent.
func (h *SignalHolds) ApplyHook(sessionID string, effect HookSignalEffect, at time.Time) {
	if effect.Release {
		h.Release(sessionID, effect.Signal)
		return
	}
	h.Hold(sessionID, effect.Signal, SignalPayload{}, at)
}
