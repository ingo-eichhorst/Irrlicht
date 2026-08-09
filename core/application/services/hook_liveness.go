// hook_liveness.go implements the per-adapter hook-liveness watchdog (#1368).
//
// The failure it catches has three parts that must ALL hold, and the third is
// the only one anybody could previously see:
//
//	permission granted  +  install effect succeeded  +  zero hooks arriving
//
// Any two of those without the third is a different diagnosis with a different
// fix, and this file is careful never to answer for one of them:
//
//   - granted but the effect FAILED — the entries were never written. Already
//     visible as PermissionView.EffectError and a permissions LogError (#1362).
//     The watchdog stays disarmed for that adapter, so it can never re-report
//     that failure in weaker words.
//   - entries written and later REMOVED by something else — #1372, not built.
//     The watchdog will eventually call such a channel silent, because from in
//     here it is silent; what it does NOT do is claim to know why. Its log line
//     and its lifecycle event say "nothing is arriving", never "the entries are
//     present", and hooks.json names #1372 as the thing that would tell them
//     apart.
//   - hooks arriving under names we do not recognize — #1364. Those still count
//     as receipts, because the channel is demonstrably alive; the unknown-event
//     counters in the same hooks.json section are what say the names are wrong.
//
// Turns, not wall time. A window measured in minutes cannot distinguish a dead
// channel from a user who went to lunch — which is the exact ambiguity the
// issue opens with. Measured against completed turns, silence is only ever read
// when the agent was demonstrably doing something, and "the user had a quiet
// hour" produces no evidence at all rather than false evidence.
//
// The turn signal is deliberately independent of the channel under test: it is
// a rising edge of transcript-derived IsAgentDone, observed before the hook
// overlay is applied, so a dead hook channel cannot suppress the very
// measurement that would convict it.
package services

import (
	"sort"
	"sync"

	"irrlicht/core/domain/session"
)

// HookLivenessTransition is what one OnActivity pass decided.
type HookLivenessTransition int

const (
	// HookLivenessUnchanged is the overwhelmingly common answer: nothing to
	// report.
	HookLivenessUnchanged HookLivenessTransition = iota
	// HookLivenessSilent means the adapter just crossed the threshold. Reported
	// once per silent episode, not once per turn — a channel that stays dead
	// must not write a log line per turn forever, the same argument #1364 makes
	// for reporting an unrecognized name once.
	HookLivenessSilent
	// HookLivenessRecovered means a receipt arrived for an adapter that had been
	// declared silent. This is the event that makes the demotion a health
	// signal rather than a latch.
	HookLivenessRecovered
)

// HookLivenessChange is one transition, carrying everything its log line and
// lifecycle event need so the caller reads no state back out of the watchdog.
type HookLivenessChange struct {
	Transition  HookLivenessTransition
	Adapter     string
	SilentTurns int
	Threshold   int
	// Receipts is the adapter's lifetime receipt total at the moment of the
	// verdict. Zero and non-zero are different bugs: zero says this channel has
	// never once worked in this process (a bad install, a port mismatch, a CLI
	// that does not run our hooks), non-zero says it worked and then stopped
	// (an upgrade, a rewritten config, a crashed listener).
	Receipts uint64

	// Silent is whether the adapter's channel is demoted as of this pass. It
	// rides along on every transition — including Unchanged — so the caller can
	// act on the verdict without a second lock acquisition and a second adapter
	// resolution that could disagree with this one.
	Silent bool
}

// HookChannelHealth is one adapter's row in the diagnostics bundle.
type HookChannelHealth struct {
	Adapter string `json:"adapter"`
	// Armed is whether the watchdog is watching this adapter at all: its hook
	// consent is granted AND its install effect reported success. A disarmed row
	// is published rather than omitted, because "not watched" and "watched and
	// healthy" are different answers to the same question and a missing row
	// would read as the second.
	Armed bool `json:"armed"`
	// Receipts is the lifetime count of consent-passed requests this adapter's
	// receiver was handed.
	Receipts uint64 `json:"receipts"`
	// TurnsSinceReceipt is how many consecutive completed turns have been
	// observed for this adapter with no receipt.
	TurnsSinceReceipt int `json:"turns_since_receipt"`
	// Silent is whether the channel is currently demoted to TierTranscript.
	Silent bool `json:"silent"`
}

// HookLivenessConfig holds the watchdog's tunables and the static facts it
// needs about the agent registry.
type HookLivenessConfig struct {
	// SilentTurns is the threshold; <= 0 disables the watchdog entirely.
	SilentTurns int
	// DefaultAdapter is the adapter a session with an empty Adapter belongs to,
	// injected for the same reason DiagnosticsService takes one: the
	// application layer must not import the inbound adapter that owns the
	// constant.
	DefaultAdapter string
	// Receipts reads one adapter's lifetime receipt total. Injected rather than
	// imported because application/services must not import adapters/inbound —
	// the same hexagonal seam HookHealthSnapshot crosses, and reusing it is why
	// this feature adds no second one. Per adapter rather than "hand me the
	// whole table": the production caller wants one row per pass, and mirroring
	// the package-level getter's granularity is what would import a whole-map
	// copy onto the classify path. Nil means no source is wired, which the
	// watchdog treats as "cannot observe", never as "zero".
	Receipts func(adapter string) uint64

	// Adapters is every adapter that has a hook receiver. Supplied so the
	// diagnostics snapshot can report a channel that has produced no traffic at
	// all — which is the single most interesting row in a bundle from a user
	// whose hooks never worked, and the one a map built from live counters can
	// never contain.
	Adapters []string
}

// hookChannelState is one adapter's watch state.
//
// There is deliberately no `silent bool`: it is exactly silentTurns >= the
// threshold, and the threshold is fixed at construction, so storing it would be
// a second field that has to agree with the first forever. The pair going out of
// step is not a hypothetical — it produces a row reading `"silent": true,
// "turns_since_receipt": 0`, which hooksView's own note calls an accusation
// rather than a measurement.
type hookChannelState struct {
	// receipts is the total observed at the last pass that saw the counter
	// move. Successive readings are compared against it rather than anything
	// being reset, so the adapter-side counter stays monotonic and no two
	// readers can steal each other's deltas.
	receipts    uint64
	silentTurns int
}

// HookLivenessWatchdog tracks, per adapter, completed turns against hook
// receipts, and reports the crossing in both directions.
//
// Unlike CacheBloatDetector — which it otherwise mirrors, down to the
// rising-edge turn detection — this one needs a mutex. Its writer is the
// detector's single event-loop goroutine, but Snapshot is read from the
// diagnostics HTTP handler on another.
type HookLivenessWatchdog struct {
	mu  sync.Mutex
	cfg HookLivenessConfig

	// readyFn reports whether an adapter's hook channel is EXPECTED to deliver:
	// consent granted and the install effect not failed. Nil means not wired
	// yet, which reads as disarmed — the daemon builds the watchdog before the
	// permission service exists, and no hook can have been served in between.
	readyFn func(adapter string) bool

	state map[string]*hookChannelState
	// lastDone is the rising-edge memory for turn detection, keyed by session.
	lastDone map[string]bool
}

// NewHookLivenessWatchdog builds a watchdog. It is inert until SetChannelReady
// has been called — the one collaborator that genuinely cannot be supplied
// here, because the permission service does not exist yet at this point in
// startup. Everything else arrives in cfg.
func NewHookLivenessWatchdog(cfg HookLivenessConfig) *HookLivenessWatchdog {
	return &HookLivenessWatchdog{
		cfg:      cfg,
		state:    map[string]*hookChannelState{},
		lastDone: map[string]bool{},
	}
}

// SetChannelReady wires the "is this channel expected to deliver" predicate.
// Called after the permission service exists, which is later in startup than
// the watchdog itself — see readyFn.
func (w *HookLivenessWatchdog) SetChannelReady(fn func(adapter string) bool) {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.readyFn = fn
}

// enabled reports whether the watchdog will do anything at all.
func (w *HookLivenessWatchdog) enabled() bool {
	return w != nil && w.cfg.SilentTurns > 0
}

// resolve maps a session's adapter string onto the key this watchdog counts
// under. Shared by every entry point rather than repeated at each: a
// disagreement here would silently split one channel's evidence across two
// keys, which is the failure the empty-adapter case exists to prevent.
func (w *HookLivenessWatchdog) resolve(adapter string) string {
	if adapter == "" {
		return w.cfg.DefaultAdapter
	}
	return adapter
}

// OnActivity is called once per processActivity pass, before the hook overlay
// is applied. It reports at most one transition.
//
// Recovery is checked on EVERY pass; demotion only on a turn boundary. That
// asymmetry is the point: convicting a channel requires evidence that turns
// went by, but acquitting it requires only that a hook arrived, and making the
// user wait for another turn boundary to be believed would be a latch wearing a
// different hat.
func (w *HookLivenessWatchdog) OnActivity(state *session.SessionState) HookLivenessChange {
	if !w.enabled() || state == nil || state.Metrics == nil {
		return HookLivenessChange{}
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	// Update the rising-edge memory FIRST and unconditionally. Every early
	// return below would otherwise leave it stale, and a missed falling edge
	// makes the next real turn boundary invisible.
	//
	// `seen` is what keeps a session's FIRST observation from counting as a
	// completed turn. An unseeded lookup returns false, so a session whose
	// transcript is already at a turn boundary the first time it is observed
	// would otherwise register a rising edge for a turn that finished before
	// the watchdog was watching — and the daemon re-reads every persisted
	// working session at startup, so a restart alone could donate one phantom
	// turn per session and convict a channel that has not been given a single
	// chance to deliver. This is the same argument the receipts baseline below
	// makes ("hooks served before we were watching are not evidence of
	// silence"), applied to the other side of the ratio.
	done := state.Metrics.IsAgentDone()
	wasDone, seen := w.lastDone[state.SessionID]
	w.lastDone[state.SessionID] = done
	turnBoundary := seen && done && !wasDone

	adapter := w.resolve(state.Adapter)
	if adapter == "" {
		return HookLivenessChange{}
	}

	if w.cfg.Receipts == nil || w.readyFn == nil || !w.readyFn(adapter) {
		// Disarmed: no consent, a failed install, or not yet wired. Forget any
		// accumulated silence rather than banking it — consent can be revoked
		// and re-granted, and turns counted while the channel was legitimately
		// absent are not evidence against it.
		//
		// A demotion in force is cleared here WITHOUT a recovered event. The
		// channel did not come back; it stopped being watched, and reporting a
		// recovery for it would put a false all-clear in the trace.
		delete(w.state, adapter)
		return HookLivenessChange{}
	}

	st := w.state[adapter]
	if st == nil {
		// First armed pass. Baseline against the counter as it stands rather
		// than against zero: hooks served before the watchdog started watching
		// are not evidence of silence, and starting from zero would credit them
		// as a recovery on the very next pass.
		st = &hookChannelState{receipts: w.receipts(adapter)}
		w.state[adapter] = st
		return HookLivenessChange{}
	}

	if cur := w.receipts(adapter); cur > st.receipts {
		wasSilent := w.silentLocked(adapter)
		st.receipts = cur
		st.silentTurns = 0
		if wasSilent {
			return HookLivenessChange{
				Transition: HookLivenessRecovered,
				Adapter:    adapter,
				Threshold:  w.cfg.SilentTurns,
				Receipts:   cur,
			}
		}
		return HookLivenessChange{}
	}

	if !turnBoundary {
		return HookLivenessChange{Silent: w.silentLocked(adapter)}
	}

	// The counter keeps climbing past the threshold, so the snapshot can show
	// how long this has been going on; the verdict is reported only on the
	// crossing itself, because a channel that stays dead must not write a log
	// line per turn forever.
	st.silentTurns++
	if st.silentTurns != w.cfg.SilentTurns {
		return HookLivenessChange{Silent: w.silentLocked(adapter)}
	}
	return HookLivenessChange{
		Transition:  HookLivenessSilent,
		Adapter:     adapter,
		SilentTurns: st.silentTurns,
		Threshold:   w.cfg.SilentTurns,
		Receipts:    st.receipts,
		Silent:      true,
	}
}

// Silent reports whether adapter's hook channel is currently demoted.
//
// This is what the detector consults before deciding to release hook-tier
// holds. It is a pure read of the decision OnActivity made — deliberately not a
// re-derivation from live counters, so the demotion, the log line, the
// lifecycle event and every hold released under it all describe the same
// verdict rather than four near-simultaneous ones that could disagree.
func (w *HookLivenessWatchdog) Silent(adapter string) bool {
	if !w.enabled() {
		return false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.silentLocked(w.resolve(adapter))
}

// silentLocked is the one definition of "this channel is currently demoted".
// Caller holds w.mu.
func (w *HookLivenessWatchdog) silentLocked(adapter string) bool {
	st := w.state[adapter]
	return st != nil && st.silentTurns >= w.cfg.SilentTurns
}

// Forget drops a session's rising-edge memory when the session goes away, so
// the map does not grow for the life of the process.
func (w *HookLivenessWatchdog) Forget(sessionID string) {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.lastDone, sessionID)
}

// Snapshot returns one row per hook-receiving adapter, sorted by name.
//
// Every configured adapter appears, including ones with no state and no
// traffic. That is the difference between a bundle that says "claude-code:
// armed, 0 receipts, 9 turns, SILENT" and one that simply has no claude-code
// row — and the second is indistinguishable from a bundle collected before the
// feature existed.
func (w *HookLivenessWatchdog) Snapshot() []HookChannelHealth {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()

	names := make(map[string]struct{}, len(w.cfg.Adapters)+len(w.state))
	for _, a := range w.cfg.Adapters {
		names[a] = struct{}{}
	}
	for a := range w.state {
		names[a] = struct{}{}
	}

	out := make([]HookChannelHealth, 0, len(names))
	for a := range names {
		row := HookChannelHealth{Adapter: a, Receipts: w.receipts(a)}
		if w.readyFn != nil {
			row.Armed = w.readyFn(a)
		}
		if st := w.state[a]; st != nil {
			row.TurnsSinceReceipt = st.silentTurns
			row.Silent = w.silentLocked(a)
		}
		out = append(out, row)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Adapter < out[j].Adapter })
	return out
}

// receipts reads one adapter's counter. Caller holds w.mu.
func (w *HookLivenessWatchdog) receipts(adapter string) uint64 {
	if w.cfg.Receipts == nil {
		return 0
	}
	return w.cfg.Receipts(adapter)
}
