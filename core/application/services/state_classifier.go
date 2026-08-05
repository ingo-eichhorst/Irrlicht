// StateClassifier provides pure functions for session state classification.
// These functions encapsulate the decision tree used to determine whether a
// session is working, waiting, or ready based on transcript metrics.
package services

import (
	"fmt"

	"irrlicht/core/domain/session"
)

// StateVerdict is the full result of a classification pass: the state to move
// to, the human-readable reason, and — the part #1288 adds — which rule
// decided and on what tier of the authority ladder its evidence arrived.
//
// The tier is provenance, not prose. It is recorded as structured data on the
// lifecycle event rather than folded into Reason, because Reason strings are
// pinned byte-for-byte by 338 committed replay goldens: putting the tier there
// would rewrite every one of them and destroy their value as a regression net
// for this very refactor.
type StateVerdict struct {
	State  string
	Reason string
	// Tier is where the deciding evidence came from. TierNone when no rule
	// fired at all.
	Tier session.SignalTier
	// Rule is the deciding rule's stable id, for logs and traces.
	Rule string
}

// stateRule is one rung of the classifier's authority ladder.
//
// Before #1288 these were bare if-statements in a fixed sequence, and the fact
// that a hook's verdict outranked a transcript guess was encoded as nothing
// more than which if-statement was written first. Declaring each rule's tier
// makes that arbitration checkable — see TestStateRules_LadderIsTierConsistent,
// which fails if a future edit reorders the ladder so a lower tier preempts a
// higher one for signals that can be true at the same time.
type stateRule struct {
	// id is a stable identifier for logs and traces. For a rule driven by a
	// held signal it is that signal's kind, so one vocabulary spans the policy
	// table, the classifier and the recorded trace.
	id string

	// signal names the held signal this rule reads, when there is one. It is
	// how the rule resolves its tier (via session.TierOf), so a signal's
	// authority is declared once in its policy row rather than restated here.
	signal session.SignalKind

	// tier is the authority of a rule that reads no held signal — the
	// transcript-native rules. Ignored when signal is set.
	tier session.SignalTier

	// tierOf overrides both for the one rule whose tier is genuinely dynamic:
	// agent-done is hook-tier when a Stop hook delivered it and
	// transcript-tier when it was inferred from the transcript tail.
	tierOf func(m *session.SessionMetrics) session.SignalTier

	// when reports whether this rule claims the decision. Nil means "always"
	// — the ladder's total default.
	when func(currentState string, m *session.SessionMetrics) bool

	// decide returns the state to move to and the reason. An empty reason is
	// normal and meaningful: the rule owns the outcome but the session is
	// already in the target state, so no transition is emitted and no lower
	// rule may run.
	decide func(currentState string, m *session.SessionMetrics) (string, string)
}

// tierFor resolves the rule's authority for this pass, preferring the dynamic
// override, then the held signal's declared tier, then the rule's own.
func (r stateRule) tierFor(m *session.SessionMetrics) session.SignalTier {
	if r.tierOf != nil {
		return r.tierOf(m)
	}
	if r.signal != "" {
		return session.TierOf(r.signal)
	}
	return r.tier
}

// toState builds the decide func shared by every rule whose outcome is a fixed
// target state and a fixed reason.
func toState(target, reason string) func(string, *session.SessionMetrics) (string, string) {
	return func(cur string, _ *session.SessionMetrics) (string, string) {
		return transitionTo(cur, target, reason)
	}
}

// stateRules is the classifier's decision ladder, most authoritative first.
//
// Order is still significant — the first rule to fire decides — but it is no
// longer the *only* record of the intended arbitration: each rule now resolves
// the tier its evidence arrives on, so the ordering can be asserted rather
// than merely commented.
//
// Two transcript-tier rules (user_blocking_tool, open_tool_stalled) sit above
// the hook-tier idle_prompt rule, which looks like an inversion and is not:
// both require an open tool call, an open tool call makes IsAgentDone false,
// and the idle_prompt hold is dropped as stale the moment IsAgentDone goes
// false. The three can never contend for the same pass. That claim is not left
// to inspection — TestStateRules_LadderIsTierConsistent enforces it by finding
// metrics where a pair does contend, and would fail if a future change made
// these reachable together.
var stateRules = []stateRule{
	{
		// Permission prompt is open (hook-based signal) → waiting.
		// The most authoritative signal available, and the only instant one.
		// Checked before NeedsUserAttention because it doesn't depend on
		// HasOpenToolCall (avoids the race where the hook fires before
		// fswatcher processes the tool_use JSONL event).
		id:     string(session.SignalPermissionPrompt),
		signal: session.SignalPermissionPrompt,
		when:   func(_ string, m *session.SessionMetrics) bool { return m.PermissionPending },
		decide: toState(session.StateWaiting, "permission prompt open → waiting"),
	},
	{
		// Manual /compact in progress (PreCompact hook) → working, regardless
		// of the stale pre-compact turn_done that IsAgentDone() would
		// otherwise read as ready. Compaction writes nothing to the transcript
		// for tens of seconds to minutes; this overlay holds the session busy
		// for that window (#657). The hold's own policy releases it the pass
		// the manual compact_boundary lands — which then routes to ready via
		// agent_done (#656) — or drops it once compactHoldTimeout elapses with
		// no boundary ever written.
		//
		// Reads a held signal like the permission rule above, rather than
		// declaring a bare tier: the hold became a signalPolicies row in #1297.
		id:     string(session.SignalCompactInProgress),
		signal: session.SignalCompactInProgress,
		when:   func(_ string, m *session.SessionMetrics) bool { return m.CompactInProgress },
		decide: toState(session.StateWorking, "manual /compact in progress → working"),
	},
	{
		// The transcript itself reports an unanswered permission prompt →
		// waiting. The agent wrote both the request and (on answer) its
		// resolution, so no hook is involved: GitHub Copilot emits
		// permission.requested / permission.completed as ordinary events,
		// paired on requestId (#1256).
		//
		// Placed below both hook-tier rules and above every other
		// transcript-tier rule, which is deliberate on both sides. Below,
		// because a hook-pushed verdict is more authoritative than a derived
		// one and must stay attributable as such in a recorded trace — and
		// because sitting above the hook rules would let a transcript-tier
		// rule preempt a hook-tier one, which is exactly what
		// TestStateRules_LadderIsTierConsistent forbids. Above, because an
		// open prompt outranks agent_done: the agent may well have written a
		// turn-ending event before blocking, and routing that to ready would
		// report a session as finished while it sits waiting for the user.
		id:     "transcript_permission_prompt",
		tier:   session.TierTranscript,
		when:   func(_ string, m *session.SessionMetrics) bool { return m.TranscriptPermissionPending },
		decide: toState(session.StateWaiting, "transcript permission prompt open → waiting"),
	},
	{
		// User-blocking tool open → waiting.
		id:     "user_blocking_tool",
		tier:   session.TierTranscript,
		when:   func(_ string, m *session.SessionMetrics) bool { return m.NeedsUserAttention() },
		decide: toState(session.StateWaiting, "user-blocking tool open → waiting"),
	},
	{
		// A permission-gated file-edit tool has been open and idle long enough
		// that the agent is almost certainly blocked on a permission prompt →
		// waiting. Transcript-based fallback for when the curl-delivered
		// PermissionRequest hook can't reach the daemon (#488). The detector
		// sets OpenToolStalled only after the open tool has lingered with no
		// transcript progress, so this never fires on a tool that is actively
		// executing.
		id:     "open_tool_stalled",
		tier:   session.TierTranscript,
		when:   func(_ string, m *session.SessionMetrics) bool { return m.OpenToolStalled },
		decide: toState(session.StateWaiting, "stalled edit tool → likely permission prompt → waiting"),
	},
	{
		// Claude Code's Notification/idle_prompt hook reported the agent is
		// idle at the prompt waiting for the user (#1173) → waiting.
		// Authoritative tier above the turn-done → ready verdict below: it
		// corrects the case where the turn ended on a plain statement with no
		// trailing question/cue, which agent_done would otherwise route to
		// ready. Live-only — the hold is only ever placed while the finished
		// turn is still idle (and never under replay), so this rule is inert
		// for every non-hook path.
		id:     string(session.SignalIdlePrompt),
		signal: session.SignalIdlePrompt,
		when:   func(_ string, m *session.SessionMetrics) bool { return m.IdlePromptPending },
		decide: toState(session.StateWaiting, "idle prompt hook → waiting"),
	},
	{
		// Agent finished turn — check if waiting for user input first. A
		// hook-delivered Stop (#1161, #1171) is authoritative here: IsAgentDone
		// consults metrics.HookTurnDone ahead of the transcript-tail heuristic,
		// so a Stop routes through the same classifyAgentDone path (a turn that
		// ended on a question/cue still lands in waiting, not ready).
		//
		// This is the one rule whose tier is genuinely dynamic — the same
		// verdict is authoritative when a hook delivered it and inferred when
		// the transcript tail did.
		id:     "agent_done",
		tierOf: tierAgentDone,
		when:   func(_ string, m *session.SessionMetrics) bool { return m.IsAgentDone() },
		decide: classifyAgentDone,
	},
	{
		// User interruption: ESC or tool-permission denial while
		// working/waiting with no open tool calls → ready.
		//
		// ESC signal: "[Request interrupted by user]" (LastWasUserInterrupt).
		// Denial signal: "[Request interrupted by user for tool use]"
		// (LastWasToolDenial). After denial, Claude Code typically returns to
		// the prompt — the agent's turn is over. If the agent does continue
		// (writes a new assistant message), the next activity will transition
		// back to working.
		id:   "user_interrupt",
		tier: session.TierTranscript,
		when: isUserInterruptReady,
		decide: func(cur string, m *session.SessionMetrics) (string, string) {
			reason := "user ESC interrupt"
			if m.LastWasToolDenial {
				reason = "tool permission denied"
			}
			return session.StateReady, fmt.Sprintf("%s while %s → ready", reason, cur)
		},
	},
	{
		// Default: transcript activity → working. A nil `when` means it always
		// fires, so the ladder is total and every pass has a decision.
		id:   "transcript_activity",
		tier: session.TierTranscript,
		decide: func(cur string, _ *session.SessionMetrics) (string, string) {
			return transitionTo(cur, session.StateWorking,
				fmt.Sprintf("transcript activity (%s → working)", cur))
		},
	},
}

// tierAgentDone reports the tier the agent-done verdict rests on: TierHook
// when a Stop hook delivered it, TierTranscript when it was inferred from the
// transcript tail. This is the distinction that makes a hook-delivered ready
// distinguishable from a guessed one in a recorded trace.
func tierAgentDone(m *session.SessionMetrics) session.SignalTier {
	if m.HookTurnDone {
		return session.TierOf(session.SignalTurnDone)
	}
	return session.TierTranscript
}

// ClassifyState applies the decision ladder to determine what state a session
// should be in based on its current state and latest metrics.
//
// Returns (newState, reason). An empty reason means no transition occurred.
// This is the ubiquitous entry point; use ClassifyStateTiered where the
// deciding tier is wanted too.
func ClassifyState(currentState string, metrics *session.SessionMetrics) (string, string) {
	v := ClassifyStateTiered(currentState, metrics)
	return v.State, v.Reason
}

// ClassifyStateTiered is ClassifyState with provenance: it reports which rule
// decided and which tier of the authority ladder its evidence arrived on.
func ClassifyStateTiered(currentState string, metrics *session.SessionMetrics) StateVerdict {
	if metrics == nil {
		return StateVerdict{State: currentState}
	}
	for _, rule := range stateRules {
		if rule.when != nil && !rule.when(currentState, metrics) {
			continue
		}
		state, reason := rule.decide(currentState, metrics)
		return StateVerdict{State: state, Reason: reason, Tier: rule.tierFor(metrics), Rule: rule.id}
	}
	// Unreachable while the ladder ends in an always-firing rule; kept so a
	// future edit that removes it degrades to "no transition" rather than to
	// an out-of-range panic.
	return StateVerdict{State: currentState}
}

// transitionTo returns (target, reason) when currentState differs from
// target, or (currentState, "") as a no-op when it's already there — the
// repeated "transition or no-op" shape used by every ClassifyState rule.
func transitionTo(currentState, target, reason string) (string, string) {
	if currentState != target {
		return target, reason
	}
	return currentState, ""
}

// classifyAgentDone handles the agent_done rule of ClassifyState: the agent has finished
// its turn. It routes to waiting first when the turn ended with a question
// or imperative cue (issue #381), otherwise to ready.
func classifyAgentDone(currentState string, metrics *session.SessionMetrics) (string, string) {
	if metrics.IsWaitingForUserInput() {
		return transitionTo(currentState, session.StateWaiting, "turn ended with question or cue → waiting")
	}
	if currentState == session.StateWorking || currentState == session.StateWaiting {
		return session.StateReady, "agent finished turn → ready"
	}
	return currentState, ""
}

// isUserInterruptReady reports whether the user_interrupt rule of ClassifyState applies: the
// session was working/waiting with no open tool call, the last transcript
// event was from the user, and that event was an ESC interrupt or
// tool-permission denial — meaning the agent's turn is effectively over.
func isUserInterruptReady(currentState string, metrics *session.SessionMetrics) bool {
	if currentState != session.StateWorking && currentState != session.StateWaiting {
		return false
	}
	return !metrics.HasOpenToolCall && metrics.LastEventType == "user" &&
		(metrics.LastWasUserInterrupt || metrics.LastWasToolDenial)
}

// SyntheticWaitingReason is the reason string used for the working→waiting
// transition synthesised when a user-blocking tool's tool_use and
// tool_result are processed in the same tailer pass (issue #150).
const SyntheticWaitingReason = "user-blocking tool opened and closed in one pass → synthetic waiting"

// ForceReadyToWorkingReason is the reason string used when a ready session's
// metrics show fresh activity — the classifier forces the working transition
// so the next step can emit the eventual working→ready in the same pass.
const ForceReadyToWorkingReason = "force ready→working on first activity"

// ShouldSynthesizeCollapsedWaiting reports whether the caller should emit
// a synthetic working→waiting transition before applying the classifier's
// result. This recovers the brief waiting episode that fswatcher collapsed
// when it coalesced the tool_use and tool_result writes of a user-blocking
// tool (AskUserQuestion / ExitPlanMode) into one event.
//
// Fires only when the session was already working (so the classifier has
// no natural way to route through waiting) and the classifier is NOT
// already transitioning to waiting on its own. Two concrete same-pass
// collapse variants reach this:
//
//   - Case A: tool_result carries is_error=true AND a trailing user text
//     "[Request interrupted by user for tool use]" sets LastWasToolDenial.
//     Classifier returns ready via the user_interrupt rule.
//   - Case B: the denial user text is followed by another user event in
//     the same pass, which clears LastWasToolDenial. Classifier then
//     returns working via the transcript_activity default. Without this helper the user
//     never sees the waiting episode at all.
//
// Callers (SessionDetector.processActivity and the replay harness) should,
// on true: emit working→waiting with SyntheticWaitingReason, set the
// effective current state to waiting, and re-run ClassifyState so the
// next transition carries the correct "while waiting" phrasing.
func ShouldSynthesizeCollapsedWaiting(currentState, newState string, metrics *session.SessionMetrics) bool {
	if currentState != session.StateWorking || newState == session.StateWaiting {
		return false
	}
	if metrics == nil {
		return false
	}
	return metrics.SawUserBlockingToolClosedThisPass
}

// SyntheticTurnSettleReason is the reason string for the synthetic
// working→ready transition emitted when a tailer pass collapses a
// genuinely distinct queued turn boundary (issue #988): a turn completed
// and a follow-up turn began in the same pass, with no observable ready
// gap between them (e.g. mistral-vibe's in-memory message queue, which
// drains a follow-up prompt the instant the prior turn clears).
const SyntheticTurnSettleReason = "turn completed, queued follow-up began in the same pass → synthetic settle"

// SyntheticQueuedTurnStartReason is the reason string for the synthetic
// ready→working transition immediately following SyntheticTurnSettleReason,
// representing the queued follow-up turn's own start.
const SyntheticQueuedTurnStartReason = "queued follow-up turn began → synthetic re-open"

// ShouldSynthesizeCollapsedTurnBoundary reports whether the caller should
// emit a synthetic working→ready→working pair before applying the
// classifier's result. This recovers the turn boundary that the tailer's
// batch scan collapsed when a queued follow-up turn began (and possibly
// completed) in the same pass as the prior turn's own turn_done — the
// batch-scan analog of ShouldSynthesizeCollapsedWaiting (issue #150), but
// for a turn_done boundary instead of a user-blocking tool.
//
// Fires only when the session was already working (a settle→re-open pair
// only makes sense mid-turn) and no overlay signal says the session is
// actually blocked on something else this pass (a real pending permission
// prompt or an in-progress manual compaction) — those forced states are
// not consistent with "a turn genuinely completed and restarted".
//
// Callers (SessionDetector.classifyAndTransition and the replay engine)
// should, on true: emit working→ready with SyntheticTurnSettleReason, then
// ready→working with SyntheticQueuedTurnStartReason, and set the effective
// current state to working so the classifier's already-computed verdict
// (typically ready, since LastEventType is the queued turn's own
// turn_done) applies as the real final transition.
func ShouldSynthesizeCollapsedTurnBoundary(currentState string, metrics *session.SessionMetrics) bool {
	if currentState != session.StateWorking {
		return false
	}
	if metrics == nil {
		return false
	}
	if metrics.PermissionPending || metrics.CompactInProgress {
		return false
	}
	return metrics.SawMidPassTurnBoundary
}

// SyntheticCatchUpTurnStartReason is the reason string for the synthetic
// ready→working transition emitted when a brand-new session's very first
// observation already shows a completed turn — discovery was delayed long
// enough that the turn finished before the daemon ever looked (issue #996).
// Paired with SyntheticCatchUpTurnDoneReason; see ShouldSynthesizeCatchUpTurn
// for when this fires.
const SyntheticCatchUpTurnStartReason = "new session created (turn already in progress at first discovery)"

// SyntheticCatchUpTurnDoneReason is the reason string for the working→ready
// half of the same synthetic pair (see SyntheticCatchUpTurnStartReason).
const SyntheticCatchUpTurnDoneReason = "turn already complete at first discovery → synthetic catch-up"

// ShouldSynthesizeCatchUpTurn reports whether a brand-new session's initial
// lifecycle record should be a synthetic ready→working→ready pair instead of
// a single flat "new session created" transition — for when discovery was
// delayed long enough that the first turn already completed (metrics.IsAgentDone())
// before the daemon ever looked, which would otherwise silently swallow it
// and mislead downstream turn-boundary consumers about which turn was first
// (issue #996, extended to child/subagent sessions by issue #999).
//
// hasLiveOrigin is the load-bearing half of the gate — proof of a genuinely
// live precursor, not an ordinary cold-start rediscovery of a large backlog
// of old, already-finished sessions (which must never get a spurious
// bounce). Its meaning depends on the caller: for a top-level session it's
// true only when this session is superseding a pre-session (proc-<pid>) the
// daemon was already live-tracking for the same project/cwd (see
// cleanupPreSessionsForProject's doc comment); for a child/subagent session,
// which never gets a pre-session of its own, it's true when the parent
// session's own OS process is still alive right now (see
// PIDManager.parentProcessLive).
func ShouldSynthesizeCatchUpTurn(hasLiveOrigin bool, metrics *session.SessionMetrics) bool {
	return hasLiveOrigin && metrics.IsAgentDone()
}
