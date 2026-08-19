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
// Two hooks are absent. A third, HookStop, was — and the reason it was is
// worth stating, because it was measured to be wrong in exactly one step
// (#1695):
//
//   - HookStop carries a payload (the turn's final assistant text and the
//     waiting-cue verdict computed over the *full* message rather than the
//     200-rune transcript tail, #1150) which a name-keyed lookup cannot
//     supply. That much is true, and it is why HandleStopHook stays a
//     dedicated entry point — the daemon has the payload and must not lose it.
//     What does NOT follow, and is what kept this row out until #1695, is that
//     a name-only caller therefore cannot act. Read signalPolicies' turn-done
//     row: its apply sets HookTurnDone unconditionally and guards BOTH payload
//     fields (`if c.Payload.LastAssistantText != ""`, `if c.Payload.WaitingCue`),
//     so a payload-free hold delivers the signal's whole assertion — the turn
//     ended, authoritatively — and the degradation is ONE-DIRECTIONAL: it can
//     add nothing to the cue verdict, and can never clear or mask a cue the
//     transcript already found on its own. It also cannot pin, because that
//     row is consumeOnce. TestPayloadFreeTurnDoneNeverClearsATranscriptCue is
//     that property as a test rather than as this paragraph.
//     So the row is an honest floor, not a lie: a name-only caller gets a
//     correct-but-weaker verdict, and the one thing it gives up is a cue that
//     sits outside the transcript tail.
//     The cost of leaving it out was concrete. The replay harness reads this
//     table, so with no row it dropped every recorded Stop, and the
//     hook-produced `ready` in codex's 2-13_turn-end-terminal-text golden was
//     reproduced from the transcript's own turn_done two seconds later — the
//     same bytes as a transcript-produced ready, so a Stop-handling regression
//     was invisible to every golden in the catalog (#1695, filed off #1388).
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
	// The turn ended, authoritatively, at true turn end (#1161). Name-only
	// here: a caller with the hook's payload — SessionDetector.HandleStopHook,
	// which every adapter routes Stop through — holds SignalTurnDone itself
	// and supplies it, so this row is what a caller WITHOUT the payload gets
	// (the replay harness). See the paragraph above for why that is a floor
	// rather than a wrong answer.
	HookStop: {Signal: SignalTurnDone},
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
