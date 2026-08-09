package session

import (
	"sync"
	"time"
)

// waitingExitDwell is how long a proposal to LEAVE StateWaiting must survive
// before it is published (#1366).
//
// WHY SIX SECONDS. The dwell only earns its cost if it outlasts the correction
// it exists to absorb. The slowest authoritative signal that can re-assert
// waiting after a lower tier has already concluded the session moved on is
// claudecode's Notification/idle_prompt hook, measured at roughly six seconds
// late in #1141 — that lag is the whole reason #1366 was opened, since #1355
// multiplies the number of adapters delivering signals on that schedule.
// Below six seconds the dwell cannot absorb the flap it was built for; far
// above it, a session the user has just unblocked reads as stuck for long
// enough to look like a bug of its own. Six is the measured number plus
// nothing, deliberately: the residual risk of being slightly short is a flap
// that reaches the UI, which is exactly the pre-#1366 behaviour and therefore
// no regression, while the risk of being long is a *new* failure mode.
//
// On the ticker-driven path the observed latency is up to this value plus one
// staleWorkingRefreshInterval (5s), because a pass has to actually run for the
// dwell to be re-examined. That is accounted for and accepted; see the
// direction argument below for why erring long here is the cheap side.
const waitingExitDwell = 6 * time.Second

// graceFor is the asymmetry of #1366, in one function: which direction gets a
// grace period, and — just as load-bearing — which does not.
//
// EXACTLY ONE EDGE IS DEBOUNCED: waiting→working. Everything else, entering
// waiting most of all, publishes on the pass that decides it.
//
// The direction comes from the same argument that set
// permissionPromptHoldTimeout to twelve hours. The two errors are not mirror
// images:
//
//   - A PHANTOM waiting is self-limiting. The session claims it needs a human,
//     the human looks, and looking is what corrects it. The cost is one glance.
//   - A DROPPED OR LATE waiting is silent. The session has stopped advertising
//     that it needs a human and there is no cue to go and check — nothing
//     prompts the look that would reveal the error. An agent that went quiet
//     behind an unanswered prompt at 22:00 is still unanswered at 08:00, and
//     the only thing that was ever going to fix that is the badge.
//
// So the grace is spent entirely on staying in waiting a moment too long, and
// never on getting there a moment too late. A symmetric dwell would buy flap
// suppression on the working→waiting edge at the price of delaying every real
// prompt, which trades the cheap error for the expensive one.
//
// WHY waiting→ready IS *NOT* DEBOUNCED, though it is also a waiting exit. What
// the grace protects is not the literal string "waiting" but the ADVERTISEMENT
// THAT A HUMAN IS NEEDED. waiting and ready both carry that advertisement;
// working is the only state that withdraws it. waiting→ready therefore cannot
// commit the silent error above — the cue stays up, only its wording changes —
// so debouncing it buys nothing and costs real accuracy. Two existing
// behaviours prove the cost is not hypothetical: ESC-cancelling a permission
// prompt resolves waiting→ready (TestSessionDetector_Activity_Cancellation
// FromWaiting_TransitionsToReady) and #1173's idle-prompt reconciliation
// requires that a completed next turn reads ready rather than leaking the
// stale waiting (TestSessionDetector_IdlePromptHook_ReconcilesReadyToWaiting).
// Both are a user action or an authoritative signal, not a flap, and both
// failed against a version of this function that debounced every waiting exit.
//
// ALSO NOT DEBOUNCED, and not an oversight:
//
//   - working→ready. Raising the come-look cue, so delaying it spends the
//     grace in the expensive direction. It is also the ordinary end of every
//     turn — the most common transition in the system.
//   - ready→working. Reversing it wrongly would hide a finished turn, which is
//     the same silent class of error as a dropped waiting.
//
// The residual, stated plainly rather than left to be discovered: the
// working→ready→waiting sequence a late idle_prompt produces still reaches the
// UI as two transitions. #1366 does not claim to remove it.
func graceFor(current, candidate string) time.Duration {
	if current == StateWaiting && candidate == StateWorking {
		return waitingExitDwell
	}
	return 0
}

// StateDwell is the classifier's grace timer (#1366): the record of state
// changes that have been decided but not yet published, so a decision that is
// reversed before its dwell elapses never reaches the UI at all.
//
// It is shaped after SignalHolds on purpose — per-session map, its own mutex
// because holds and hooks arrive on HTTP handler goroutines while the event
// loop classifies, and a clock passed IN on every call rather than read here.
// A time.Now() inside this type would make the offline replay harness
// non-deterministic in the same way it would inside a signalPolicy: the
// caller's pass clock is wall time in the daemon and the transcript's virtual
// time under replay, and only the caller knows which.
//
// The zero value is not usable; call NewStateDwell. A nil *StateDwell is
// usable and disables hysteresis entirely — every proposed change publishes
// immediately, i.e. exactly the pre-#1366 behaviour. That is what keeps the
// many SessionDetector struct literals in the test suite meaningful rather
// than panicking, and it is why TestSessionDetector_WiresAStateDwell exists:
// nothing else would notice production forgetting to construct one.
type StateDwell struct {
	mu      sync.Mutex
	pending map[string]pendingExit
}

// pendingExit is one decided-but-unpublished state change.
//
// from and to are DEFENCE IN DEPTH, not a live invariant, and it is worth
// being exact about that rather than letting a reader assume either. Today
// graceFor returns non-zero for exactly one pair, so any entry that exists is
// necessarily waiting→working and the shape check in Admit cannot fail — a
// mutation replacing it with a bare presence test keeps the whole suite green.
// They are kept because they are what makes a future second debounced edge
// safe by construction instead of by remembering: the moment graceFor grows a
// row, "the elapsed time on file describes the edge being proposed" stops
// being free, and publishing a stale edge is the one error class this whole
// mechanism must not commit.
type pendingExit struct {
	from  string
	to    string
	since time.Time
}

// NewStateDwell returns an empty grace-timer store.
func NewStateDwell() *StateDwell {
	return &StateDwell{pending: make(map[string]pendingExit)}
}

// Admit decides whether a classified state change may be published now, and is
// the only method that advances a dwell. It must be called on EVERY classify
// pass, including the passes where the classifier re-decides the state the
// session already has — that is how a reversal is observed. Calling it only
// when current != candidate would leave a reversed proposal on the books and
// publish it later, which is the precise opposite of the intent.
//
// now is this pass's clock, on the same timeline as holdContext.Now.
//
// It reports true when the caller should apply the transition to candidate.
// False means either that there is nothing to publish (candidate == current)
// or that the change is still serving its dwell.
func (d *StateDwell) Admit(sessionID, current, candidate string, now time.Time) bool {
	if d == nil {
		return candidate != current
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	// The proposal was reversed — or never existed. Either way the session is
	// where it should be, and anything outstanding is void: it described a
	// change the classifier no longer wants.
	if candidate == current {
		delete(d.pending, sessionID)
		return false
	}

	grace := graceFor(current, candidate)
	if grace == 0 {
		delete(d.pending, sessionID)
		return true
	}

	p, ok := d.pending[sessionID]
	if !ok || p.from != current || p.to != candidate {
		// First sighting of this proposal, or the proposal changed shape. The
		// clock restarts rather than carrying over, which errs toward staying
		// in waiting — the cheap side of graceFor's asymmetry.
		d.pending[sessionID] = pendingExit{from: current, to: candidate, since: now}
		return false
	}
	if now.Sub(p.since) < grace {
		return false
	}
	delete(d.pending, sessionID)
	return true
}

// Pending reports whether a session has a decided-but-unpublished change
// outstanding.
//
// This exists for scheduling, not for reading state: a dwell can only be
// resolved by a later classify pass, and the ticker that drives those passes
// skips non-working sessions unless something says otherwise. Without this,
// a dwell started on the last pass a session ever receives would never be
// re-examined and the change would be lost rather than delayed — which would
// make the grace timer a dropper, exactly the failure graceFor argues against.
// See refreshStaleSessions.
func (d *StateDwell) Pending(sessionID string) bool {
	if d == nil {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	_, ok := d.pending[sessionID]
	return ok
}

// DropSession forgets any outstanding proposal for a session — because the
// session went away, or because something authoritative happened that must not
// be debounced. Both callers are deliberate; see admitTransition.
func (d *StateDwell) DropSession(sessionID string) {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.pending, sessionID)
}
