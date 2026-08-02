package session

import (
	"sync"
	"time"
)

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

	// SignalCompactInProgress is Claude Code's PreCompact hook for a manual
	// /compact (#657): compaction is running and writing nothing to the
	// transcript, so the session must be held working through that silent
	// window. Named for the condition rather than the hook, so the string
	// matches the classifier rule id that reads it.
	//
	// The fourth hook signal, and the one that could not migrate in #1288 —
	// see holdContext for why its half-wall-clock rule needed a clock first.
	SignalCompactInProgress SignalKind = "compact_in_progress"

	// SignalOpenToolStalled is the transcript-based fallback for a held
	// permission prompt (#488): a permission-gated file-edit tool has been open
	// and idle long enough that the agent is almost certainly blocked on a
	// prompt the curl-delivered PermissionRequest hook never managed to
	// deliver.
	//
	// The first non-hook signal, and the first arm-then-fire one — the hold is
	// placed the moment the tool is seen open, but does not apply until
	// stalledEditToolThreshold has passed (see the policy's ripe rule). Named
	// for the condition rather than the producer, so the string matches the
	// classifier rule id that reads it.
	SignalOpenToolStalled SignalKind = "open_tool_stalled"
)

// compactHoldTimeout bounds the SignalCompactInProgress hold (#657). Normally
// the hold clears when the manual compact_boundary lands, but an interrupted or
// failed /compact may never write one — without a ceiling the session would be
// re-held working on every refreshStaleSessions tick and stranded forever (the
// very failure #656 fixed). A real manual compaction runs at most a few minutes
// (the #656 live evidence was ~161s), so this timeout sits comfortably beyond
// any genuine window: after it elapses an orphaned hold is dropped and the
// session re-classifies normally.
const compactHoldTimeout = 5 * time.Minute

// stalledEditToolThreshold is how long a permission-gated file-edit tool
// (Edit/Write/MultiEdit/NotebookEdit) may stay open before it is read as a held
// permission prompt and SignalOpenToolStalled applies — the transcript-based
// fallback for when the PermissionRequest hook can't reach the daemon (#488,
// ClassifyState's open_tool_stalled rule).
//
// It is deliberately NOT the detector's staleWorkingRefreshInterval. That
// constant is a polling cadence (how often a lingering working session is
// re-read); reusing it as the "a human is looking at a prompt" threshold
// conflated two unrelated quantities. Edit tools are usually near-instant
// (observed median ~0.1s, mean ~1.4s), but a real minority run long —
// legitimately-executing, prompt-free edits of 14–16s have been observed — so a
// 5s gate sat inside that tail and mislabelled slow-but-progressing edits as
// stalled, flickering working→waiting→working (#1130). 30s clears the observed
// tail with margin while still catching a genuinely held prompt, which stays
// open until the user answers.
//
// Measured from the hold's HeldSince — first observation, not most recent — so
// a fresh tool_use is never flagged on the spot. That is what the policy's ripe
// rule and the arm-once HoldIfAbsent exist to guarantee; #1319 moved this
// constant here from the detector when the rule became a policy row.
const stalledEditToolThreshold = 30 * time.Second

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

// holdContext is everything a staleness rule may read: the metrics the signal
// is about to be folded onto, and the clock. Deliberately unexported, like the
// policy table it serves — no caller outside this package can write a policy,
// so widening it later (see the arming caveat below) costs nothing.
//
// The clock is the part #1297 added, and the reason the fourth hook signal
// could finally migrate. Before it, stale could only ask "do the metrics
// contradict me", never "have I been held too long" — so
// SignalCompactInProgress, whose rule is half wall-clock, was inexpressible as
// a policy row and stayed a hand-rolled map on SessionDetector.
//
// The clock serves both halves of a grace timer, which took two changes to
// get to. #1297 added the *expiry* half, which is what Phase 5 of #1129 needs:
// a process probe that re-Holds on every poll where it sees model API traffic
// expresses "no traffic for N seconds" as a stale rule, because a re-Hold
// resets HeldSince. #1319 added the *arming* half — see signalPolicy.ripe —
// because expiry alone could not express "apply only once N has passed":
// Overlay applied on the first pass after Hold, unconditionally, so
// markStalledEditTool (#488) had to stay a hand-rolled overlay map on
// SessionDetector and #1129 Phase 6 (buffer byte-identical for N snapshots)
// could not have landed as a row either.
//
// Now is injected by the caller rather than read from time.Now here, so
// staleness stays testable and — for the offline replay harness, which runs on
// the transcript's virtual timeline — deterministic.
type holdContext struct {
	// Metrics is the freshly-rebuilt transcript metrics for this pass. Never
	// nil: Overlay returns early on nil metrics rather than calling a policy.
	Metrics *SessionMetrics

	// HeldSince is when Hold recorded this signal — arrival, or the most recent
	// re-Hold, which resets it. One axis, not two: a policy needing "first
	// armed" and "last refreshed" at once would need a second timestamp here.
	HeldSince time.Time

	// Now is this pass's clock, on the same timeline as HeldSince — wall time
	// in the daemon, the transcript's virtual time under replay. A policy reads
	// it rather than calling time.Now itself; that is what keeps replay
	// deterministic.
	Now time.Time

	// Payload is what the signal carried beyond its own existence. The zero
	// value is correct for a signal whose whole content is "this happened",
	// which is every row but SignalTurnDone.
	Payload SignalPayload
}

// signalPolicy declares how one kind of held signal behaves — how long it
// outranks lower tiers, when it stops being true, and what it folds onto
// metrics. It is deliberately data rather than code: before #1288 each of
// these was a hand-written overlay method on SessionDetector with its own
// lifecycle rule, plus another copy in the replay harness, and the remaining
// phases of #1129 would have added six more. Adding a signal should mean
// adding a row here.
type signalPolicy struct {
	// kind is the signal this policy governs. Carried in the row rather than
	// used as a map key so the ordered table below is the single declaration
	// of both *what* the policies are and *what order* they apply in — see
	// signalPolicies' comment on why that order is load-bearing.
	kind SignalKind

	// tier is where this signal sits on the authority ladder. Read by
	// TierOf, which is what the classifier's rules resolve their own tier
	// through — so this is the single source of truth for a signal's
	// authority, not a second copy of something the classifier restates.
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
	stale func(c holdContext) bool

	// ripe reports that the held signal is ready to be applied. Nil means
	// "ripe on arrival", which is every hook signal: a hook fires because the
	// condition it names just became true, so there is nothing to wait for.
	//
	// It is stale's mirror image, and the pair is what makes an arm-then-fire
	// rule expressible. stale answers "is this over?" and ends the hold; ripe
	// answers "has this started?" and does not — an unripe hold is neither
	// applied nor dropped nor consumed, it simply waits for a later pass. A
	// rule that must observe a condition persist for a while before believing
	// it (SignalOpenToolStalled's threshold; #1129 Phase 6's byte-identical
	// screen buffer) is inexpressible without it, because Overlay would
	// otherwise apply on the first pass after Hold, unconditionally.
	//
	// Evaluated after stale, so a signal that is both over and not yet ripe is
	// dropped rather than left holding: "this never came due and no longer
	// describes reality" is an expiry, not a pending arm.
	ripe func(c holdContext) bool

	// apply folds the signal onto the metrics the classifier is about to
	// read. It takes the same holdContext the predicates do — Metrics is the
	// object it writes to, and the clock and Payload are there for a rule that
	// wants to fold an elapsed value or hook-delivered text onto metrics.
	apply func(c holdContext)
}

// signalPolicies is the declared behaviour of every out-of-band signal, in
// the order Overlay applies them.
//
// THE ORDER IS LOAD-BEARING, which is why this is an ordered slice and not a
// map keyed by SignalKind: a map range would silently randomise it.
// SignalIdlePrompt's staleness test calls IsAgentDone, which reads the
// HookTurnDone that SignalTurnDone.apply has just set. Applied in this order, a
// Stop hook and an idle_prompt hook arriving for the same turn agree — the turn
// is done, so the idle hold survives and correctly holds the session in
// waiting. Reverse them and idle_prompt evaluates staleness against metrics
// that do not yet know the turn ended: on any adapter whose transcript tail is
// not literally "turn_done", IsAgentDone reads false, the hold is dropped as
// stale, and the correction the hook exists to deliver is thrown away one
// instruction before it would have been applied.
//
// The order also matters for the last row: SignalOpenToolStalled's ripe rule
// reads PermissionPending, which SignalPermissionPrompt's apply sets on the
// same pass. It sits last so it observes the hook's verdict rather than
// racing it — the same position the hand-rolled overlay it replaced ran in.
//
// Every row but the last is TierHook. That is not a redundancy to factor out —
// it is the current state of a ladder whose other tiers are filed and unbuilt
// (Phases 4-7 of #1129), and the field is what lets a Phase 4 OTel signal land
// as a row here rather than as another bespoke overlay. SignalOpenToolStalled
// is the first non-hook row: a transcript-tier inference, held because it must
// observe a condition persist before it is believed rather than because it
// arrived on another goroutine.
var signalPolicies = []signalPolicy{
	{
		kind: SignalPermissionPrompt,
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
		stale: func(c holdContext) bool { return c.Metrics.LastWasToolDenial },
		apply: func(c holdContext) { c.Metrics.PermissionPending = true },
	},

	{
		kind:        SignalTurnDone,
		tier:        TierHook,
		consumeOnce: true,
		apply: func(c holdContext) {
			c.Metrics.HookTurnDone = true
			if c.Payload.LastAssistantText != "" {
				c.Metrics.LastAssistantText = c.Payload.LastAssistantText
			}
			// Only ever adds to the cue verdict, never clears it: the hook
			// can push a finished turn to waiting, but must not mask a cue
			// the transcript already found on its own.
			if c.Payload.WaitingCue {
				c.Metrics.PendingWaitingCue = true
			}
		},
	},

	{
		kind: SignalIdlePrompt,
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
		stale: func(c holdContext) bool { return !c.Metrics.IsAgentDone() },
		apply: func(c holdContext) { c.Metrics.IdlePromptPending = true },
	},

	{
		kind: SignalCompactInProgress,
		tier: TierHook,
		// Persistent: a manual /compact writes nothing to the transcript for
		// tens of seconds to minutes, and the hold has to survive every
		// re-evaluation in that window or the stale pre-compact turn_done
		// leaks the session back to ready (#657).
		//
		// The first row whose staleness reads the clock, and the reason
		// holdContext exists. It clears on the first of:
		//
		//   - the manual compact_boundary landing: the normal path —
		//     compaction finished, release working → ready (#656);
		//   - compactHoldTimeout elapsing — see that constant for why the
		//     ceiling is there and why five minutes.
		//
		// Last in the table, which is where the hand-rolled overlay it
		// replaced ran. Nothing depends on that position: its staleness reads
		// only transcript-derived state and the clock, and the field it
		// applies is read by no other policy.
		stale: func(c holdContext) bool {
			return c.Metrics.SawManualCompactBoundary || c.Now.Sub(c.HeldSince) >= compactHoldTimeout
		},
		apply: func(c holdContext) { c.Metrics.CompactInProgress = true },
	},

	{
		kind: SignalOpenToolStalled,
		tier: TierTranscript,
		// The first arm-then-fire row, and the first non-hook one. The producer
		// arms it (HoldIfAbsent) the moment it sees a permission-gated edit tool
		// open; ripe is what keeps it from firing on that same pass.
		//
		// Persistent: a genuinely held prompt stays open indefinitely, and the
		// hold has to survive every re-evaluation in between or the window
		// restarts and the flag can never come due.
		//
		// Ends when the tool closes — the tool_result arriving is the whole
		// end-of-life notice, whether the user approved, rejected, or the edit
		// simply finished executing.
		stale: func(c holdContext) bool { return !c.Metrics.HasOpenEditPermissionTool() },
		// Two "not yet" conditions, and neither is an expiry — both leave the
		// hold in place:
		//
		//   - under the threshold: the tool may still be legitimately executing
		//     (#1130), so believing it stalled now would route an actively
		//     working session to waiting;
		//   - PermissionPending: the hook already said the prompt is open and
		//     the classifier prefers it, so this fallback is redundant. Not
		//     stale, though — releasing the prompt while the tool stays open
		//     must leave the fallback armed, with its original clock.
		//
		// PermissionPending is set by the SignalPermissionPrompt row's apply,
		// which is why this row is last in the table: it reads a field an
		// earlier row writes on the same pass.
		ripe: func(c holdContext) bool {
			return !c.Metrics.PermissionPending &&
				c.Now.Sub(c.HeldSince) >= stalledEditToolThreshold
		},
		apply: func(c holdContext) { c.Metrics.OpenToolStalled = true },
	},
}

// TierOf reports the authority tier of a signal kind, or TierNone for a kind
// with no declared policy. It is how the classifier's rules resolve their own
// tier, so a signal's authority is declared exactly once — in its policy row —
// rather than restated by every rule that reads it and left to drift.
func TierOf(kind SignalKind) SignalTier {
	for _, p := range signalPolicies {
		if p.kind == kind {
			return p.tier
		}
	}
	return TierNone
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
	held map[string]map[SignalKind]heldSignal
}

// heldSignal is one stored hold: what the signal carried, and when it arrived.
// The arrival time is what a time-based staleness rule reads through
// holdContext.HeldSince.
type heldSignal struct {
	payload SignalPayload
	heldAt  time.Time
}

// NewSignalHolds returns an empty, ready-to-use store.
func NewSignalHolds() *SignalHolds {
	return &SignalHolds{held: map[string]map[SignalKind]heldSignal{}}
}

// Hold records that kind has fired for sessionID at time at, replacing any
// previous hold of the same kind — a second Stop hook for the same session
// describes the same turn ending, and its payload is the fresher one.
//
// Replacing resets the arrival time along with the payload, so a re-fired
// signal restarts any timeout its policy measures from it.
func (h *SignalHolds) Hold(sessionID string, kind SignalKind, p SignalPayload, at time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.held[sessionID] == nil {
		h.held[sessionID] = map[SignalKind]heldSignal{}
	}
	h.held[sessionID][kind] = heldSignal{payload: p, heldAt: at}
}

// HoldIfAbsent records kind for sessionID at time at only if no hold of that
// kind is already in place, and reports whether it placed one.
//
// It is Hold's arm-once counterpart, and the producer-side half of an
// arm-then-fire policy. Hold deliberately *resets* HeldSince, which is correct
// for a signal that genuinely re-fires (a second Stop hook describes a new turn
// ending) but wrong for a condition a producer merely keeps observing: a
// re-arming poll would push the arming deadline out by one poll interval every
// poll, so a ripe rule measured from HeldSince could never come due. The
// SignalOpenToolStalled producer re-observes the same open tool on every
// classify pass, which is exactly that shape.
func (h *SignalHolds) HoldIfAbsent(sessionID string, kind SignalKind, p SignalPayload, at time.Time) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.held[sessionID][kind]; ok {
		return false
	}
	if h.held[sessionID] == nil {
		h.held[sessionID] = map[SignalKind]heldSignal{}
	}
	h.held[sessionID][kind] = heldSignal{payload: p, heldAt: at}
	return true
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
// now is this pass's clock, injected rather than read here so a time-based
// policy (SignalCompactInProgress today; the Phase 5/6 grace timers next) stays
// testable and replays deterministically on the transcript's virtual timeline.
//
// Each hold runs the same three-way gate: stale drops it unapplied, an unripe
// ripe leaves it untouched for a later pass, and otherwise it is applied (and
// consumed, if consume-once).
//
// Note that staleness is evaluated before apply on every pass, including the
// first: a signal that is already contradicted when it arrives is discarded
// rather than applied once. That matters for a late signal (the ~6s
// idle_prompt, or a retrospective OTel span) that lands after the condition it
// describes has already ended.
func (h *SignalHolds) Overlay(sessionID string, m *SessionMetrics, now time.Time) {
	if m == nil {
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	holds := h.held[sessionID]
	if len(holds) == 0 {
		return
	}

	for _, policy := range signalPolicies {
		hs, ok := holds[policy.kind]
		if !ok {
			continue
		}

		c := holdContext{Metrics: m, HeldSince: hs.heldAt, Now: now, Payload: hs.payload}

		if policy.stale != nil && policy.stale(c) {
			delete(holds, policy.kind)
			continue
		}

		// Not yet due: leave the hold exactly as it is. Deliberately not a
		// delete and deliberately not a consume — an unripe hold has not
		// happened yet, so there is nothing to apply and nothing to expire,
		// and consuming it here would discard the signal one pass before it
		// came due.
		if policy.ripe != nil && !policy.ripe(c) {
			continue
		}

		policy.apply(c)
		if policy.consumeOnce {
			delete(holds, policy.kind)
		}
	}

	if len(holds) == 0 {
		delete(h.held, sessionID)
	}
}
