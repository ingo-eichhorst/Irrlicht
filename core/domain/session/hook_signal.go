package session

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

// HookSignalEffect is what one hook event name does to a session's signal
// holds. Release distinguishes the two directions: a hook either asserts a
// signal (Release false) or retracts one (Release true).
type HookSignalEffect struct {
	Signal  SignalKind
	Release bool
}

// hookSignalEffects is the single source of truth for hook-name → SignalKind,
// covering every hook whose effect is *fully determined by its name*.
//
// It deliberately stops there. HookStop, HookNotification and HookPreCompact
// are name-plus-payload: Stop carries the turn's final assistant text into
// SignalPayload, Notification is only acted on for notification_type
// "idle_prompt", and PreCompact only for trigger "manual". Their handlers must
// read the payload, so a name-keyed lookup could not express them without
// lying about what the name alone determines — they keep their own dedicated
// entry points and are absent here on purpose. A caller that gets ok==false
// has learned "this hook needs more than its name", not "this hook is unknown".
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
