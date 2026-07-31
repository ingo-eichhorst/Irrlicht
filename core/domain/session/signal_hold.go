package session

import "sync"

// SignalKind identifies one out-of-band state signal that is held for a
// session between the moment it arrives and the moment it stops describing
// reality.
//
// A hold exists because every signal above TierTranscript arrives on a
// different goroutine than the one that classifies, and — per the #1141 spike
// — usually arrives *late*: the Notification/idle_prompt hook fires ~6s after
// the turn goes idle, by which point the transcript tier has already guessed
// and the badge already says something. Holding the authoritative signal is
// what lets it correct that guess instead of racing it.
type SignalKind string

const (
	// SignalPermissionPrompt is Claude Code's / Codex's PermissionRequest
	// hook: a permission prompt is open and the user is blocked on it right
	// now (#108, #1171). The one genuinely instant signal in the system.
	SignalPermissionPrompt SignalKind = "permission_prompt"

	// SignalTurnDone is the Stop hook: the turn ended, authoritatively, at
	// true turn end (#1161, #1171).
	SignalTurnDone SignalKind = "turn_done"

	// SignalIdlePrompt is the Notification/idle_prompt hook: the agent is
	// sitting idle at the prompt waiting for the user (#1173). The signal
	// waiting_cue's prose regex exists to approximate.
	SignalIdlePrompt SignalKind = "idle_prompt"
)

// SignalPayload is the data a held signal carries beyond its own existence.
// Only SignalTurnDone populates it today; the zero value is correct for a
// signal whose whole content is "this happened".
type SignalPayload struct {
	// LastAssistantText is the turn's final assistant message as the hook
	// delivered it (display-truncated). The Stop hook carries this, which is
	// why it does double duty: IsWaitingForUserInput needs the final text,
	// and the hook has it before the transcript flush does.
	LastAssistantText string

	// WaitingCue is the adapter's waiting-cue verdict computed over the
	// hook's *full* message text, rather than the 200-rune transcript tail
	// the classifier would otherwise see (#1150).
	WaitingCue bool
}

// signalPolicy declares how one kind of held signal behaves — how long it
// outranks lower tiers, when it stops being true, and what it folds onto
// metrics. It is deliberately data rather than code: before #1288 each of
// these three was a hand-written overlay method on SessionDetector with its
// own lifecycle rule, plus a fourth copy in the replay harness, and the
// remaining phases of #1129 would have added six more. Adding a signal should
// mean adding a row here.
type signalPolicy struct {
	// tier is where this signal sits on the authority ladder.
	tier SignalTier

	// consumeOnce drops the hold as it is applied, so it affects exactly the
	// one classify pass it triggered and never bleeds into the next turn.
	// The alternative — persistent — re-applies every pass until stale
	// reports the signal no longer describes reality, so that a lower-tier
	// reclassify in between cannot revert the correction.
	consumeOnce bool

	// stale reports that the held signal has stopped describing reality and
	// must be dropped *without* being applied. Nil means "only consumeOnce
	// or an explicit Release ends this hold".
	stale func(m *SessionMetrics) bool

	// apply folds the signal onto the metrics the classifier is about to
	// read.
	apply func(m *SessionMetrics, p SignalPayload)
}

// signalPolicies is the declared behaviour of every out-of-band signal.
//
// All three are TierHook today. That is not a redundancy to factor out — it
// is the current state of a ladder whose other tiers are filed and unbuilt
// (Phases 4-7 of #1129), and the field is what lets a Phase 4 OTel signal
// land as a row here rather than as another bespoke overlay.
var signalPolicies = map[SignalKind]signalPolicy{
	SignalPermissionPrompt: {
		tier: TierHook,
		// Persistent: the prompt stays open until the agent acts on it, and
		// the hold must survive every fswatcher re-evaluation in between.
		// Cleared normally by PostToolUse/PostToolUseFailure via Release.
		//
		// Denial is the case that needs stale: Claude Code fires no
		// PostToolUseFailure when the user rejects a prompt, so the only
		// evidence the prompt closed is the transcript's own
		// "[Request interrupted by user for tool use]" marker. A lower tier
		// retiring a higher one reads backwards, but it is sound here — this
		// is not the transcript overruling the hook's verdict, it is the
		// transcript supplying the end-of-life notice the hook never sends.
		stale: func(m *SessionMetrics) bool { return m.LastWasToolDenial },
		apply: func(m *SessionMetrics, _ SignalPayload) { m.PermissionPending = true },
	},

	SignalTurnDone: {
		tier:        TierHook,
		consumeOnce: true,
		apply: func(m *SessionMetrics, p SignalPayload) {
			m.HookTurnDone = true
			if p.LastAssistantText != "" {
				m.LastAssistantText = p.LastAssistantText
			}
			// Only ever adds to the cue verdict, never clears it: the hook
			// can push a finished turn to waiting, but must not mask a cue
			// the transcript already found on its own.
			if p.WaitingCue {
				m.PendingWaitingCue = true
			}
		},
	},

	SignalIdlePrompt: {
		tier: TierHook,
		// Persistent, and deliberately so: this is the hold that makes the
		// ~6s-late correction stick. A stray lower-tier reclassify between
		// the hook landing and the user replying would otherwise route the
		// corrected waiting straight back to ready via the turn-done rule.
		//
		// The signal holds only while the finished turn is still the last
		// thing that happened. IsAgentDone going false means either the user
		// replied or a tool opened — either way the idle window is over and
		// the rules that own those cases take it from here.
		stale: func(m *SessionMetrics) bool { return !m.IsAgentDone() },
		apply: func(m *SessionMetrics, _ SignalPayload) { m.IdlePromptPending = true },
	},
}

// signalOrder fixes the sequence in which held signals are applied.
//
// THIS ORDER IS LOAD-BEARING, and a map range would silently randomise it.
// SignalIdlePrompt's staleness test calls IsAgentDone, which reads the
// HookTurnDone that SignalTurnDone.apply has just set. Applied in this order,
// a Stop hook and an idle_prompt hook arriving for the same turn agree — the
// turn is done, so the idle hold survives and correctly holds the session in
// waiting. Reverse them and idle_prompt evaluates staleness against metrics
// that do not yet know the turn ended: on any adapter whose transcript tail
// is not literally "turn_done", IsAgentDone reads false, the hold is dropped
// as stale, and the correction the hook exists to deliver is thrown away one
// instruction before it would have been applied.
var signalOrder = []SignalKind{
	SignalPermissionPrompt,
	SignalTurnDone,
	SignalIdlePrompt,
}

// SignalHolds is the per-session store of out-of-band signals awaiting a
// classify pass — one mechanism shared by the daemon's live path and the
// offline replay harness, which before #1288 each carried their own copy of
// the same overlay logic and could drift apart without any test noticing.
//
// Safe for concurrent use: hooks arrive on HTTP handler goroutines while the
// event loop classifies.
type SignalHolds struct {
	mu   sync.Mutex
	held map[string]map[SignalKind]SignalPayload
}

// NewSignalHolds returns an empty, ready-to-use store.
func NewSignalHolds() *SignalHolds {
	return &SignalHolds{held: map[string]map[SignalKind]SignalPayload{}}
}

// Hold records that kind has fired for sessionID, replacing any previous hold
// of the same kind — a second Stop hook for the same session describes the
// same turn ending, and its payload is the fresher one.
func (h *SignalHolds) Hold(sessionID string, kind SignalKind, p SignalPayload) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.held[sessionID] == nil {
		h.held[sessionID] = map[SignalKind]SignalPayload{}
	}
	h.held[sessionID][kind] = p
}

// Release drops one held signal — the explicit end-of-life path, used when
// something other than the policy's own staleness rule ends the hold (a
// PostToolUse closing a permission prompt).
func (h *SignalHolds) Release(sessionID string, kind SignalKind) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if s := h.held[sessionID]; s != nil {
		delete(s, kind)
		if len(s) == 0 {
			delete(h.held, sessionID)
		}
	}
}

// Held reports whether kind is currently held for sessionID, without applying
// or consuming it.
func (h *SignalHolds) Held(sessionID string, kind SignalKind) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	_, ok := h.held[sessionID][kind]
	return ok
}

// DropSession forgets every hold for a session, for when the session itself
// goes away.
func (h *SignalHolds) DropSession(sessionID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.held, sessionID)
}

// Overlay folds every currently-valid held signal for sessionID onto m, in
// signalOrder. Stale holds are dropped without being applied; consume-once
// holds are dropped as they are applied.
//
// Call this after the metrics have been rebuilt from the transcript — which
// zeroes the transient fields these signals set — and before classification,
// which reads them.
//
// Note that staleness is evaluated before apply on every pass, including the
// first: a signal that is already contradicted when it arrives is discarded
// rather than applied once. That matters for a late signal (the ~6s
// idle_prompt, or a retrospective OTel span) that lands after the condition it
// describes has already ended.
func (h *SignalHolds) Overlay(sessionID string, m *SessionMetrics) {
	if m == nil {
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	holds := h.held[sessionID]
	if len(holds) == 0 {
		return
	}

	for _, kind := range signalOrder {
		payload, ok := holds[kind]
		if !ok {
			continue
		}
		policy := signalPolicies[kind]

		if policy.stale != nil && policy.stale(m) {
			delete(holds, kind)
			continue
		}

		policy.apply(m, payload)
		if policy.consumeOnce {
			delete(holds, kind)
		}
	}

	if len(holds) == 0 {
		delete(h.held, sessionID)
	}
}
