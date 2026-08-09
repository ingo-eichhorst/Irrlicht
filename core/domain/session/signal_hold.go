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

// permissionPromptHoldTimeout bounds the SignalPermissionPrompt hold (#1360).
//
// WHICH PROMPTS HAVE A SAFETY NET, AND WHICH DO NOT. This matters more than
// the number, because expiry means something different for each group. The
// only transcript-tier signal that can re-derive "a prompt is open" is #488's
// SignalOpenToolStalled, and it fires solely for the five names
// isPermissionGatedEditTool matches (edit/write/multiedit/notebookedit/
// write_file). Against claudecode's installed matcher —
// "Bash|Write|Edit|MultiEdit|NotebookEdit|WebFetch|mcp__.*|AskUserQuestion|ExitPlanMode"
// (hookinstaller.go, hookMatcher) — that covers four of nine alternatives:
//
//   - COVERED (expiry is soft): Write, Edit, MultiEdit, NotebookEdit. If the
//     tool is still open, SignalOpenToolStalled ripens the moment
//     PermissionPending stops being set and re-derives the same waiting, one
//     tier down where anything can still correct it.
//   - NOT COVERED (expiry is silent): Bash, WebFetch, mcp__.*, AskUserQuestion,
//     ExitPlanMode. Nothing re-derives the prompt. The session simply stops
//     reading waiting.
//
// The uncovered group is not the marginal one. hookMatcherPreToolUse is
// exactly "AskUserQuestion|ExitPlanMode" — the #307 fast path, whose entire
// reason to exist is flipping working→waiting without transcript-flush
// latency — so the one prompt class with no fallback at all is also the one
// the fast path was built for. A plan approval and a Bash approval are
// ordinary, not edge cases.
//
// WHY TWELVE HOURS. The ceiling is therefore calibrated against the uncovered
// group, where being wrong is expensive and invisible: a phantom waiting is
// visible and the user clears it by looking, but a dropped waiting means the
// session has stopped advertising that it needs a human and there is no cue to
// go and check — the precise failure this project exists to prevent. The
// governing workflow is an agent left running overnight and reviewed the next
// morning: a session that hits ExitPlanMode at 22:00 must still read waiting
// at 08:00. Twelve hours spans that with margin while still bounding the pin
// inside a single day, so a stuck session heals before the following night
// rather than persisting across two.
//
// An hour was the first cut here and it was wrong — justified by the fallback
// above without checking how far the fallback reaches.
//
// The residual is a weekend: a prompt opened Friday night expires before
// Monday. No finite ceiling covers that, and an effectively infinite one would
// restore the bug. The hold_expired event this drops (see SignalExpiry) is how
// that case is told apart from a session that was never pinned.
//
// Cutting a real wait short remains the lesser error overall, because it is
// bounded and diagnosable, whereas the failure being replaced is neither:
// TierHook forbids every lower tier from retiring this hold, so one missed
// release pins the session at waiting until the daemon restarts. A late
// PostToolUse reaching an already-expired hold is a no-op in Release.
const permissionPromptHoldTimeout = 12 * time.Hour

// idlePromptHoldTimeout bounds the SignalIdlePrompt hold (#1360).
//
// This row has NO transcript-tier fallback at all — not the partial coverage
// permissionPromptHoldTimeout describes, none. #488's SignalOpenToolStalled
// keys off an open edit tool, and an idle prompt is by definition a session
// with no tool open. So every expiry here is the silent kind: the session
// stops reading waiting and nothing re-derives it.
//
// It therefore takes the same value as the permission ceiling, and for the
// same governing reason — an agent left overnight must still read waiting when
// its user looks in the morning. The two constants stay separate rather than
// collapsing into one because their coverage stories differ (that row is
// four-ninths covered, this one is not at all), so a future change to either
// calibration must not silently move the other.
//
// Four hours was the first cut and it was too short: an idle prompt reached at
// 22:00 expired at 02:00 and read ready by breakfast, which is exactly the
// invisible failure the ceiling is supposed to be worth its cost against.
//
// The upside is that this ceiling is rarely the thing that ends the hold. Its
// staleness rule — IsAgentDone going false — fires on any user reply or tool
// open, which is a far more reliable release than the permission row's
// PostToolUse round-trip. The ceiling is a backstop for a transcript that
// stopped being parsed, not the expected path.
const idlePromptHoldTimeout = 12 * time.Hour

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
// SessionDetector.
//
// What the pair delivers is precisely "a condition that has persisted for a
// wall-clock interval". That is NOT the same as "a condition observed N times",
// and #1129 Phase 6 (screen buffer byte-identical for N snapshots) wants the
// latter: elapsed time ripens on the *absence* of evidence, so a stalled or
// descheduled snapshot poller would ripen a stability rule having observed
// nothing. SignalOpenToolStalled does not expose that gap only because the
// classify pass which ripens it is itself the observation; a Phase 6 producer
// would poll on a different cadence than Overlay. Landing Phase 6 as a row
// therefore needs an observation counter on heldSignal (surfaced here as, say,
// Observations int), not just this clock. Deliberately not built here: no row
// today needs it, and guessing its shape from one hypothetical caller is how
// the wrong abstraction gets frozen in.
//
// #1360 then moved the expiry half back *out* of stale, into the declared
// signalPolicy.ceiling — see that field for why a duration a test can read
// beats a clock term only a closure can see. Now stays here regardless: ripe
// reads it, and Overlay measures every ceiling against this same injected
// instant, so an expiry replays on the transcript's timeline like everything
// else.
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
	// must be dropped *without* being applied. Nil means "only consumeOnce,
	// an explicit Release, or the ceiling below ends this hold".
	stale func(c holdContext) bool

	// ceiling bounds how long this hold may survive on the wall clock,
	// measured from HeldSince. Zero means unbounded, which is only defensible
	// for a row a lower tier can still correct — see
	// TestSignalPolicies_HookPersistentHoldsDeclareACeiling for the rows where
	// it is not, and permissionPromptHoldTimeout for how one is calibrated.
	//
	// Declared as data rather than folded into stale as another clock term
	// (#1360), for two reasons the merged form cannot serve:
	//
	//   - Observability. Overlay reports a ceiling expiry back to its caller,
	//     which logs it and records a KindHoldExpired lifecycle event. A stale
	//     closure returns one bool and so cannot say which of its terms fired;
	//     a ceiling hidden inside it would rewrite session state silently,
	//     which is a new debugging blind spot rather than a fix.
	//   - Enforceability. A structural test can read this field. It cannot
	//     read intent out of a closure, and the alternative — probing stale
	//     with a synthetic clock — needs a hand-written "not yet stale"
	//     metrics fixture per row, which is one more table for the author of
	//     the next row to forget to update.
	//
	// A ceiling is a backstop, not an end-of-life rule. stale is evaluated
	// first, so a hold that ended for its own declared reason is not reported
	// as an expiry and only a hold that genuinely ran out of time is.
	//
	// CONSTRAINT for a row that also declares ripe: the ceiling must leave the
	// row a window in which it can actually fire, i.e. it must exceed whatever
	// elapsed threshold ripe measures. Overlay checks ceiling *before* ripe —
	// an expiry beats a pending arm, because "never came due and no longer
	// describes reality" is an expiry — so a ceiling shorter than the ripen
	// threshold would drop the hold before it ever applied and report it as a
	// lost release, which is a lie in the trace rather than a missing signal.
	// No row combines the two today; TestSignalPolicies_HookPersistentHoldsDeclareACeiling
	// enforces it for the first one that does.
	ceiling time.Duration

	// ripe reports that the held signal is ready to be applied. Nil means
	// "ripe on arrival", which is every hook signal: a hook fires because the
	// condition it names just became true, so there is nothing to wait for.
	//
	// It is stale's mirror image, and the pair is what makes an arm-then-fire
	// rule expressible. stale answers "is this over?" and ends the hold; ripe
	// answers "has this started?" and does not — an unripe hold is neither
	// applied nor dropped nor consumed, it simply waits for a later pass. A
	// rule that must observe a condition persist before believing it
	// (SignalOpenToolStalled's threshold) is inexpressible without it, because
	// Overlay would otherwise apply on the first pass after Hold,
	// unconditionally. See holdContext for what this does and does not reach —
	// notably that it measures elapsed time, not observation count.
	//
	// Evaluated after stale, so a signal that is both over and not yet ripe is
	// dropped rather than left holding: "this never came due and no longer
	// describes reality" is an expiry, not a pending arm.
	//
	// stale and ripe are both called on the same holdContext each pass, so a
	// future row whose two predicates share an expensive sub-computation pays
	// for it twice. Free today (every predicate is a field read or a
	// subtraction); if that changes, memoise on holdContext rather than
	// reordering the gate.
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
		//
		// Both of those paths are things that must *happen*. Neither fires if
		// the daemon never sees the POST — a crash, a restart, a port change,
		// an uninstalled hook — or if the adapter stops writing the denial
		// marker in the shape LastWasToolDenial matches. This row sits at the
		// top of the authority ladder, so no lower-tier signal may correct it,
		// and without the ceiling below a session pinned that way stayed
		// pinned for the life of the process (#1360).
		stale:   func(c holdContext) bool { return c.Metrics.LastWasToolDenial },
		ceiling: permissionPromptHoldTimeout,
		apply:   func(c holdContext) { c.Metrics.PermissionPending = true },
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
		//
		// That is a transcript observation, so the rule is exactly as good as
		// the parse: a transcript that stops being read, a rotated file, or an
		// adapter whose turn-done marker changes shape all leave IsAgentDone
		// stuck true and this TierHook hold uncorrectable from below. Hence
		// the ceiling (#1360).
		stale:   func(c holdContext) bool { return !c.Metrics.IsAgentDone() },
		ceiling: idlePromptHoldTimeout,
		apply:   func(c holdContext) { c.Metrics.IdlePromptPending = true },
	},

	{
		kind: SignalCompactInProgress,
		tier: TierHook,
		// Persistent: a manual /compact writes nothing to the transcript for
		// tens of seconds to minutes, and the hold has to survive every
		// re-evaluation in that window or the stale pre-compact turn_done
		// leaks the session back to ready (#657).
		//
		// The first row to be bounded by the clock, and the reason holdContext
		// exists. It clears on the first of:
		//
		//   - the manual compact_boundary landing: the normal path —
		//     compaction finished, release working → ready (#656);
		//   - compactHoldTimeout elapsing — see that constant for why the
		//     ceiling is there and why five minutes.
		//
		// Those two were one merged stale predicate until #1360, which split
		// the clock term out into the declared ceiling below. Behaviour is
		// unchanged — Overlay still drops the hold at >= compactHoldTimeout
		// measured from HeldSince — but the expiry is now reported rather than
		// silent, and this row stopped being the one place the pattern lived.
		//
		// Position-independent: its staleness reads only transcript-derived
		// state, its ceiling is time-only, and the field it applies is read by
		// no other policy. It was last in the table when #1297 added it — where the
		// hand-rolled overlay it replaced ran — and #1319 appended
		// SignalOpenToolStalled after it, which is fine precisely because
		// nothing here depends on being last. The row that genuinely does is
		// SignalOpenToolStalled; see TestSignalPolicies_OrderIsPinned.
		stale:   func(c holdContext) bool { return c.Metrics.SawManualCompactBoundary },
		ceiling: compactHoldTimeout,
		apply:   func(c holdContext) { c.Metrics.CompactInProgress = true },
	},

	{
		kind: SignalOpenToolStalled,
		tier: TierTranscript,
		// The first arm-then-fire row, and the first non-hook one. The producer
		// (SessionDetector.armStalledEditTool) arms it via HoldIfAbsent — see
		// that method for why arm-once and not Hold — the moment it sees a
		// permission-gated edit tool open; ripe is what keeps it from firing on
		// that same pass.
		//
		// Persistent: a genuinely held prompt stays open indefinitely, and the
		// hold has to survive every re-evaluation in between.
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
		//   - PermissionPending: carried over verbatim from the hand-rolled
		//     overlay this row replaced. It is NOT tier arbitration — the
		//     classifier ladder already decides that, and its permission_prompt
		//     rule short-circuits before open_tool_stalled is ever reached, so
		//     the flag changes no state outcome while a prompt is open. What it
		//     affects is the open_tool_stalled bit in the recorded
		//     ClassifierInputs trace. Kept so this migration stays
		//     behaviour-preserving; do not generalise a supersedes-mechanism
		//     from it. Not stale either — releasing the prompt while the tool
		//     stays open must leave the fallback armed, with its original clock.
		//
		// Reading PermissionPending is what pins this row's position; see the
		// table header.
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

// SignalExpiry reports one hold that Overlay dropped because its wall-clock
// ceiling elapsed, rather than because its own staleness rule ended it or a
// Release retired it (#1360).
//
// Overlay returns these so its caller can log the expiry and record it in the
// lifecycle trace. That indirection is the price of the domain layer owning no
// logger: a ceiling that rewrites a session's state without saying so anywhere
// is a debugging blind spot, and the whole failure mode being fixed here is one
// nobody could see from the outside.
type SignalExpiry struct {
	// Kind is the signal whose hold ran out of time.
	Kind SignalKind

	// Tier is the authority that hold had been asserting. A TierHook expiry is
	// the consequential one: while it stood, nothing below it was permitted to
	// correct the session.
	Tier SignalTier

	// HeldFor is how long the hold stood before the ceiling dropped it,
	// measured Now-HeldSince against the clock the caller injected.
	HeldFor time.Duration

	// Ceiling is the bound that was exceeded. Carried alongside HeldFor so a
	// recorded trace stays readable after the constant is retuned — otherwise
	// an old event and a new one are indistinguishable.
	Ceiling time.Duration
}

// SignalRelease reports one hold that ReleaseTier dropped because the channel
// that asserted it was declared dead, rather than because it went stale, was
// consumed, or ran out its ceiling (#1368).
//
// It is deliberately a different type from SignalExpiry, carrying no Ceiling,
// because the two answer different questions and a reader must not have to
// guess which happened. A SignalExpiry says "this hold's release was late past
// the point of belief" — one hold, one session, wall clock. A SignalRelease
// says "every hold from this channel is being dropped because the channel
// itself stopped delivering" — a per-adapter verdict that happens to land on
// this session. Collapsing them into one type with a zero Ceiling would make
// the distinction depend on a reader noticing an absent field.
type SignalRelease struct {
	// Kind is the signal whose hold was dropped.
	Kind SignalKind

	// Tier is the authority the hold had been asserting, and the tier the
	// caller asked to have released. Carried rather than implied so a recorded
	// trace stands alone.
	Tier SignalTier

	// HeldFor is how long the hold had stood, measured Now-HeldSince against
	// the clock the caller injected. Usually long: a hold placed by a channel
	// that has since gone silent was, by definition, placed before it did.
	HeldFor time.Duration
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
	h.holdLocked(sessionID, kind, p, at)
}

// HoldIfAbsent records kind for sessionID at time at, only if no hold of that
// kind is already in place.
//
// THE ARM-ONCE RATIONALE, stated here once for everything that depends on it.
// Hold deliberately *resets* HeldSince, which is right for a signal that
// genuinely re-fires — a second Stop hook describes a new turn ending — but
// wrong for a condition a producer merely keeps observing. Such a producer
// re-arms on every poll, and each re-arm would push a ripe rule's deadline out
// by one poll interval, so the rule could never come due. Any producer of an
// arm-then-fire policy therefore calls this, not Hold.
//
// It must stay one critical section rather than `if !Held() { Hold() }`: two
// producers racing through the gap between those two calls would both see
// "absent", and the second Hold would perform exactly the clock reset this
// method exists to prevent.
func (h *SignalHolds) HoldIfAbsent(sessionID string, kind SignalKind, p SignalPayload, at time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.held[sessionID][kind]; ok {
		return
	}
	h.holdLocked(sessionID, kind, p, at)
}

// holdLocked stores a hold, creating the session's inner map if needed. The
// caller must hold h.mu. Shared by Hold and HoldIfAbsent so heldSignal has one
// construction site.
func (h *SignalHolds) holdLocked(sessionID string, kind SignalKind, p SignalPayload, at time.Time) {
	if h.held[sessionID] == nil {
		h.held[sessionID] = map[SignalKind]heldSignal{}
	}
	h.held[sessionID][kind] = heldSignal{payload: p, heldAt: at}
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

// HasAny reports whether any signal at all is currently held for sessionID.
//
// It exists for scheduling, not classification (#1360). A ceiling can only be
// evaluated by a classify pass, and the daemon's periodic refresh skips
// sessions that are not working — which is every session a hook hold has
// pinned at waiting. The refresh asks this to find the few sessions it must
// revisit anyway, without re-reading the transcript of every idle session on
// the machine.
func (h *SignalHolds) HasAny(sessionID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.held[sessionID]) > 0
}

// DropSession forgets every hold for a session, for when the session itself
// goes away.
func (h *SignalHolds) DropSession(sessionID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.held, sessionID)
}

// ReleaseTier drops every hold on sessionID whose policy declares tier, and
// reports what it dropped (#1368).
//
// This is the mechanism behind "demote the adapter to TierTranscript". Holds
// are how a tier asserts authority over time — a TierHook hold suppresses every
// lower tier for as long as it stands — so demoting a channel while leaving its
// holds in place would demote nothing at all. The session would stay pinned by
// the very channel just declared dead, and the only thing that changed would be
// a line in a diagnostics bundle.
//
// Why this is sound and not a guess: the caller only reaches here after
// observing N completed turns for the adapter with zero hook receipts. A hold
// asserted by that channel is therefore older than those N turns, because
// nothing from the channel has arrived since. The evidence has already been
// collected; this is acting on it.
//
// Releasing does not force a state. It removes a suppression, and the next
// classify pass decides from the transcript — which is exactly what
// TierTranscript means and exactly what the adapter would do if it had no hook
// channel at all. If the channel comes back, the next hook re-asserts its hold
// from scratch.
//
// Two things this API cannot check and the caller therefore owes it. First, the
// evidence above is a CALLER invariant — the domain cannot see turns or
// receipts, so a caller that reaches here without them gets a silent, confident
// wrong answer. Second, it releases every hold of the tier on this session, and
// the verdict driving it is per ADAPTER; those coincide only because a session
// belongs to one adapter and each hook receiver resolves its own transcript
// roots, so every TierHook hold on a session came from that session's adapter.
// That holds today and is worth re-checking if a TierHook-authority signal ever
// arrives on a channel that is not an adapter's own receiver.
//
// Dropping the holds rather than suppressing them is deliberate. A
// Suppress/Unsuppress pair would make recovery symmetric for free, but it
// leaves stale holds alive indefinitely for a channel that may never return and
// so needs a reaper of its own; deletion is the simpler end state, and the
// freshness guard below is the price it pays.
//
// Idempotent and cheap on the common path: a session with no holds returns nil
// without allocating, so the caller may invoke it on every pass while an
// adapter is silent rather than tracking which sessions it has already swept.
//
// now is the pass clock, injected for the same reason Overlay takes one: HeldFor
// must be measured on the transcript's virtual timeline so a recording replays
// deterministically.
func (h *SignalHolds) ReleaseTier(sessionID string, tier SignalTier, now time.Time) []SignalRelease {
	h.mu.Lock()
	defer h.mu.Unlock()

	holds := h.held[sessionID]
	if len(holds) == 0 {
		return nil
	}

	var released []SignalRelease
	// signalPolicies order, not map order: the result reaches a log and a
	// recorded lifecycle trace, and a nondeterministic order there would make
	// two identical runs produce different recordings.
	for _, policy := range signalPolicies {
		if policy.tier != tier {
			continue
		}
		hs, ok := holds[policy.kind]
		if !ok {
			continue
		}
		// A hold placed at or after `now` was asserted AFTER the caller took
		// the verdict it is acting on, so the evidence for that verdict says
		// nothing about it. The receiver runs on its own goroutine: a hook can
		// land, dispatch and place a fresh hold in the window between a classify
		// pass reading the liveness counters and reaching this sweep, and
		// dropping that hold would discard the first signal from a channel that
		// has just come back — while the next pass reports it as recovered.
		// Leaving it stands the hold up against a demotion that predates it,
		// which is the correct reading of both facts.
		if hs.heldAt.After(now) {
			continue
		}
		delete(holds, policy.kind)
		released = append(released, SignalRelease{
			Kind:    policy.kind,
			Tier:    policy.tier,
			HeldFor: now.Sub(hs.heldAt),
		})
	}

	if len(holds) == 0 {
		delete(h.held, sessionID)
	}
	return released
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
// Each hold runs the same four-way gate: stale drops it unapplied, an elapsed
// ceiling drops it unapplied *and* reports it, an unripe ripe leaves it
// untouched for a later pass, and otherwise it is applied (and consumed, if
// consume-once).
//
// Returns the holds dropped by their ceiling, in table order — normally none.
// Callers that ignore the result still get the expiry; what they give up is
// being able to say it happened, which for a TierHook row is the difference
// between a fix and a silent state rewrite (#1360). The daemon's caller logs
// each one and records a lifecycle.KindHoldExpired event.
//
// Note that staleness is evaluated before apply on every pass, including the
// first: a signal that is already contradicted when it arrives is discarded
// rather than applied once. That matters for a late signal (the ~6s
// idle_prompt, or a retrospective OTel span) that lands after the condition it
// describes has already ended.
func (h *SignalHolds) Overlay(sessionID string, m *SessionMetrics, now time.Time) []SignalExpiry {
	if m == nil {
		return nil
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	holds := h.held[sessionID]
	if len(holds) == 0 {
		return nil
	}

	var expired []SignalExpiry

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

		// The backstop, checked after stale so a hold that ended for its own
		// declared reason is never mis-reported as having run out of time.
		// >= rather than >: the deadline itself is outside the window, which
		// is what the compact row's tests have pinned since #657.
		if heldFor := c.Now.Sub(c.HeldSince); policy.ceiling > 0 && heldFor >= policy.ceiling {
			delete(holds, policy.kind)
			expired = append(expired, SignalExpiry{
				Kind:    policy.kind,
				Tier:    policy.tier,
				HeldFor: heldFor,
				Ceiling: policy.ceiling,
			})
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

	return expired
}
