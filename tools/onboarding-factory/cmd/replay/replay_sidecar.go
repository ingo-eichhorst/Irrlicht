package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"irrlicht/core/adapters/inbound/agents/claudecode"
	"irrlicht/core/application/replayengine"
	"irrlicht/core/application/services"
	"irrlicht/core/domain/lifecycle"
	"irrlicht/core/domain/session"
	"irrlicht/core/pkg/tailer"
)

// errSidecarNotDrivable marks a sidecar that parsed cleanly but carries
// nothing a replay can be stepped through, in the two shapes replaydata/
// actually contains:
//
//   - No transcript_activity event carries a file_size for the primary session.
//   - No transcript_new event names a real session — every session id is the
//     synthetic proc-<pid> form that findPrimarySessionID skips.
//
// The sentinel is keyed on those symptoms, NOT on the adapter, and the
// distinction is worth stating because the obvious reading is wrong. For most
// of these fallbacks the cause really is an adapter property: opencode and
// hermes are agent.ProcessOwnedStore, with no transcript file for the daemon to
// watch and so no fswatcher fire to record, and aider is agent.FilesUnderCWD,
// with no session identifier of its own — which is why nearly every aider
// recording lands here. The size of the population is
// censusOfTheCommittedCatalog.PairedButUngraded, machine-generated, and is not
// restated here: the count this sentence used to carry described the catalog
// before #1517 widened the walk to pair transcript.md, and so silently stopped
// including the aider recordings the very next clause names.
//
// The remaining two are recording-level, and they are the reason this comment
// does not claim otherwise: mistral-vibe is agent.FilesUnderRoot
// (core/adapters/inbound/agents/vibe/agent.go:33), and its normal recordings do
// carry transcript_activity and replay sidecar-driven. Only
// regressions/906-presession-early-removal-{before,after}-scanner-fix fall
// back, because they were captured for a presession/scanner bug and contain no
// fswatcher fires at all.
//
// So a half-captured recording of a file-watched adapter degrades to
// transcript-only with a message that reads like the benign case. That is
// accepted deliberately — those two committed recordings depend on it — but it
// is a symptom-keyed fallback, not a proof that nothing is being masked. What
// keeps it honest: a sidecar with malformed lines is NOT eligible (see
// replayWithSidecar), every other sidecar error is still fatal, and every
// fallback is written into the report's SidecarFallback field, because a
// replay quietly running in its weaker mode is precisely the failure issue
// #1326 is about.
var errSidecarNotDrivable = errors.New("sidecar cannot drive a replay")

// notDrivableError carries the fallback reason in two forms: Reason is
// machine-independent and is what lands in a committed golden, while Error()
// appends the sidecar path for a human reading stderr. Keeping the path out of
// Reason is load-bearing — replaydata/ paths are absolute at replay time, so a
// golden built from the full message would differ between clones.
type notDrivableError struct {
	Reason string
	Path   string
}

func (e *notDrivableError) Error() string { return e.Reason + ": " + e.Path }

// Unwrap makes errors.Is(err, errSidecarNotDrivable) true for this type.
func (e *notDrivableError) Unwrap() error { return errSidecarNotDrivable }

// replayWithSidecar runs a deterministic replay driven by a lifecycle-events
// sidecar. Each transcript_activity event in the sidecar is one fswatcher
// fire the daemon observed; we feed the tailer the exact bytes the daemon
// had at that moment and call the classifier. Hook events (KindHookReceived)
// are interleaved by timestamp — when a permission-request hook fires, we
// emit a working→waiting transition without a tailer call, mirroring the
// daemon's behavior where a permission request pauses the agent.
func replayWithSidecar(transcriptPath, sidecarPath string, cfg reportSettings) (*replayReport, error) {
	srcBytes, err := os.ReadFile(transcriptPath)
	if err != nil {
		return nil, fmt.Errorf("read transcript: %w", err)
	}
	sidecarEvents, malformed, err := loadLifecycleEventsCountingMalformed(sidecarPath)
	if err != nil {
		return nil, fmt.Errorf("load sidecar: %w", err)
	}
	// notDrivable is only ever returned for a sidecar that parsed cleanly.
	// A corrupt or truncated one can reach the same two conditions below with
	// its events silently dropped by the parse, and falling back there would
	// hide real damage behind a message that reads exactly like the benign
	// aider / store-adapter case.
	notDrivable := func(reason string) error {
		if malformed > 0 {
			return fmt.Errorf("sidecar %s has %d malformed line(s); refusing to fall back to transcript-only (%s)",
				sidecarPath, malformed, reason)
		}
		return &notDrivableError{Reason: errSidecarNotDrivable.Error() + ": " + reason, Path: sidecarPath}
	}

	primarySessionID := resolvePrimarySession(cfg, sidecarEvents)
	if primarySessionID == "" {
		return nil, notDrivable("no transcript_new event names a real session")
	}
	buckets := bucketSidecarEvents(sidecarEvents, primarySessionID)
	if len(buckets.fswatches) == 0 {
		return nil, notDrivable(fmt.Sprintf(
			"no transcript_activity events with file_size for primary session %s", primarySessionID))
	}

	r, cleanup, err := newSidecarReplayer(transcriptPath, srcBytes, cfg, buckets.fswatches, buckets.children)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	if err := r.runDebouncedTimeline(buckets, cfg.DebounceWindow); err != nil {
		return nil, err
	}

	r.extendWindowToLastTransition()
	r.addDuration(r.state, r.report.Summary.LastEventTime.Sub(r.prevTransitionAt))
	finalizeSummary(r.report, len(buckets.fswatches), r.stateDurations,
		summaryMetrics(transcriptPath, cfg, r.lastMetrics), cfg.Adapter)
	r.report.Sessions = buildSessionTimelines(sidecarEvents)
	return r.report, nil
}

// summaryMetrics returns the token/cost/model vector for the report summary,
// read from a full parse of the transcript rather than from the sidecar
// timeline, falling back to the timeline's last snapshot if that parse fails.
//
// The MEASURED effect: taking metrics from the sidecar timeline cost 112
// goldens their totals when #1326 turned sidecar mode on catalog-wide — 16 lost
// model_name, 36 lost estimated_cost_usd, 64 lost cum_input_tokens — and
// `of verify` flipped to FAIL for five cells (codex/pi model-context-display,
// antigravity model-context-display, kiro-cli model-identification and
// model-context-display), because internal/validate/observations.go reads this
// block as the catalog's only source of truth for token/cost/model. Reading
// them from a full parse restores all 112 byte-for-byte.
//
// The CAUSE is not fully established, and is deliberately not asserted here.
// Two candidates were checked and one was ruled out: relaxing
// flushPendingDebounce's `!d.coalesced` early-return changes nothing (measured
// on codex/2-1_basic-turn — still 1 transition), so a skipped trailing flush is
// NOT it. Incomplete fs-event coverage is real but explains only 30 of the
// sidecar-drivable population — censusOfTheCommittedCatalog.Recordings, which
// is machine-generated precisely so a denominator is not carried by hand here
// (#1518) — and only 1 of the 22 worst-degraded (the other 21 have 100%
// coverage). What is established is that the sidecar timeline's
// last observed metrics are frequently not the transcript's final metrics.
//
// The split this function implements is sound regardless of that cause:
// transitions describe *when the daemon looked* and stay sidecar-driven; the
// metric vector describes *what the transcript ultimately contains* and is a
// property of the file, not of the observation schedule. It also keeps this
// block byte-identical to pre-#1326 output, confining the golden churn to the
// transitions the change is actually about.
//
// Cost: a second full engine run per sidecar recording (~0.8 ms each, ~240 ms
// over the catalog, invisible in the byte-identity test's wall clock because
// its subtests are parallel). Reusing the already-warm tailer instead would be
// free but shifts 8 of 399 goldens, so it needs those explained first.
func summaryMetrics(transcriptPath string, cfg reportSettings, fallback *tailer.SessionMetrics) *tailer.SessionMetrics {
	res, err := runTranscriptEngine(transcriptPath, cfg)
	if err != nil || res == nil || res.LastMetrics == nil {
		return fallback
	}
	return res.LastMetrics
}

// resolvePrimarySession picks the session under replay: the --session flag
// when set, otherwise the first transcript_new event in the sidecar.
func resolvePrimarySession(cfg reportSettings, sidecarEvents []lifecycle.Event) string {
	if cfg.SessionFilter != "" {
		return cfg.SessionFilter
	}
	return findPrimarySessionID(sidecarEvents)
}

// sidecarBuckets holds the four timeline streams extracted from a sidecar for
// a single primary session.
//
// For /continue sessions the same session ID spans multiple daemon lifetimes
// — the daemon is deaf between a process_exited and the next lifecycle
// birth, so fs events arriving in that gap were never classified by the
// live daemon and must be skipped during replay (issue #144). A lifecycle
// birth is either a transcript_new (fresh session) or a state_transition
// with empty prev_state (resumed session — the "new session created" marker
// the daemon writes when it re-attaches).
type sidecarBuckets struct {
	fswatches        []lifecycle.Event
	hookEvents       []lifecycle.Event
	processExits     []lifecycle.Event
	lifecycleStarts  []lifecycle.Event
	childTransitions []lifecycle.Event
	orphanTriggers   []orphanTrigger
	// children is seeded with one entry per subagent discovered via
	// parent_linked. finalState carries the last recorded state; state
	// is the mutable field the timeline walk updates.
	children map[string]*childInfo
}

// childInfo tracks a single subagent for the parent-hold check. finalState
// is computed once from the sidecar (used to decide whether an orphan
// trigger is synthesized); state is the mutable state that the timeline
// walk updates as child transitions fire.
type childInfo struct {
	lastActivityAt time.Time
	finalState     string
	state          string
}

// orphanTrigger synthesizes the stale-sweep promotion that the live
// daemon's finishOrphanedChildren would have emitted for a child whose
// transcript went quiet while still working/waiting.
type orphanTrigger struct {
	sessionID string
	at        time.Time
}

// bucketSidecarEvents walks sidecarEvents once and partitions those for the
// primary session into the streams the replay needs. It also discovers
// subagent sessions linked to the primary and collects their transitions
// and stale-sweep orphan triggers so the parent-hold check mirrors the
// live daemon.
func bucketSidecarEvents(sidecarEvents []lifecycle.Event, primarySessionID string) sidecarBuckets {
	b := sidecarBuckets{children: map[string]*childInfo{}}
	bucketPrimaryEvents(sidecarEvents, primarySessionID, &b)
	bucketChildEvents(sidecarEvents, &b)
	b.orphanTriggers = computeOrphanTriggers(b.children)
	return b
}

// bucketPrimaryEvents is the first pass over sidecarEvents: it partitions the
// primary session's own events into the fswatch/process-exit/hook/lifecycle-
// start streams, and discovers subagent sessions linked to the primary via
// parent_linked.
func bucketPrimaryEvents(sidecarEvents []lifecycle.Event, primarySessionID string, b *sidecarBuckets) {
	for _, ev := range sidecarEvents {
		if ev.Kind == lifecycle.KindParentLinked && ev.ParentSessionID == primarySessionID {
			if _, ok := b.children[ev.SessionID]; !ok {
				b.children[ev.SessionID] = &childInfo{finalState: session.StateReady}
			}
		}
		if ev.SessionID != primarySessionID {
			continue
		}
		bucketPrimaryEventByKind(ev, b)
	}
}

// bucketPrimaryEventByKind routes one primary-session event into its stream.
func bucketPrimaryEventByKind(ev lifecycle.Event, b *sidecarBuckets) {
	switch ev.Kind {
	case lifecycle.KindTranscriptActivity:
		if ev.FileSize > 0 {
			b.fswatches = append(b.fswatches, ev)
		}
	case lifecycle.KindProcessExited:
		b.processExits = append(b.processExits, ev)
	case lifecycle.KindHookReceived:
		b.hookEvents = append(b.hookEvents, ev)
	case lifecycle.KindTranscriptNew:
		b.lifecycleStarts = append(b.lifecycleStarts, ev)
	case lifecycle.KindStateTransition:
		if ev.PrevState == "" {
			b.lifecycleStarts = append(b.lifecycleStarts, ev)
		}
	}
}

// bucketChildEvents is the second pass, over child sessions discovered by
// bucketPrimaryEvents: it gathers each child's state transitions and tracks
// its last-activity time (consumed by computeOrphanTriggers).
func bucketChildEvents(sidecarEvents []lifecycle.Event, b *sidecarBuckets) {
	for _, ev := range sidecarEvents {
		ci, ok := b.children[ev.SessionID]
		if !ok {
			continue
		}
		if ev.Timestamp.After(ci.lastActivityAt) {
			ci.lastActivityAt = ev.Timestamp
		}
		if ev.Kind != lifecycle.KindStateTransition {
			continue
		}
		b.childTransitions = append(b.childTransitions, ev)
		if ev.NewState != "" {
			ci.finalState = ev.NewState
		}
	}
}

// computeOrphanTriggers synthesizes the stale-sweep promotion that the live
// daemon's finishOrphanedChildren would have emitted for each child whose
// final recorded state is still working/waiting — fired at lastActivityAt +
// quiet window so the parent's held-working releases at roughly the virtual
// time the daemon would have.
func computeOrphanTriggers(children map[string]*childInfo) []orphanTrigger {
	var triggers []orphanTrigger
	for id, ci := range children {
		if ci.finalState != session.StateWorking && ci.finalState != session.StateWaiting {
			continue
		}
		triggers = append(triggers, orphanTrigger{
			sessionID: id,
			at:        ci.lastActivityAt.Add(services.SubagentQuietWindow),
		})
	}
	return triggers
}

// sidecarReplayer bundles the mutable state that the sidecar-driven replay
// threads through every timeline entry: the growing transcript mirror, the
// tailer, the report under construction, and the classifier's current state.
type sidecarReplayer struct {
	srcBytes []byte
	tmp      *os.File
	lastSize int64
	tailer   *tailer.TranscriptTailer

	report           *replayReport
	state            string
	prevTransitionAt time.Time
	stateDurations   map[string]time.Duration
	lastMetrics      *tailer.SessionMetrics

	// signals holds out-of-band hook signals for the primary session, using
	// the same session.SignalHolds mechanism and the same declared policies
	// as the live SessionDetector.
	//
	// It used to be a local bool plus a hand-written overlay method that
	// "mirrored" the detector's — a fourth copy of a lifecycle rule that no
	// test compared against the original, so the harness whose entire job is
	// catching classifier regressions could itself drift out of agreement
	// with the classifier (#1288). Sharing the mechanism makes that class of
	// drift unrepresentable.
	signals *session.SignalHolds

	// fswatches is the primary session's recorded transcript_activity stream,
	// kept so classifyAt can widen a clamped read to the next stat the daemon
	// plausibly reached. See readBoundaryFor (#1342).
	fswatches []lifecycle.Event

	// children carries the subagents discovered via parent_linked. Each
	// entry's state is updated as child transitions fire on the timeline;
	// anyChildActive reads the map to decide whether to hold the parent
	// in working when the classifier would transition it to ready.
	children map[string]*childInfo
}

// newSidecarReplayer allocates the scratch transcript mirror, opens the
// tailer, seeds the report summary from the fswatcher window, and emits the
// initial-state transition. The returned cleanup closes the scratch files.
// children is the set of subagents linked to the primary; their state
// entries start at StateReady and are updated as child transitions fire.
func newSidecarReplayer(transcriptPath string, srcBytes []byte, cfg reportSettings, fswatches []lifecycle.Event, children map[string]*childInfo) (*sidecarReplayer, func(), error) {
	tmpDir, err := os.MkdirTemp("", "irrlicht-replay-sidecar-")
	if err != nil {
		return nil, nil, err
	}
	tmpPath := filepath.Join(tmpDir, "transcript.jsonl")
	tmp, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		os.RemoveAll(tmpDir)
		return nil, nil, err
	}
	cleanup := func() {
		tmp.Close()
		os.RemoveAll(tmpDir)
	}

	adapterName := cfg.Adapter
	if adapterName == "" {
		adapterName = claudecode.AdapterName
	}
	parser := parserFor(adapterName)
	t := tailer.NewTranscriptTailer(tmpPath, parser, adapterName)
	// Replay must reflect only the transcript, never the operator's local
	// config, so goldens stay reproducible across machines (issue #440).
	t.DisableModelConfigFallback()

	report := &replayReport{
		SchemaVersion:    1,
		SourceTranscript: transcriptPath,
		GeneratedAt:      time.Now().UTC(),
		Settings:         cfg,
	}
	report.Summary.TotalEvents = len(fswatches)
	report.Summary.FirstEventTime = fswatches[0].Timestamp
	report.Summary.LastEventTime = fswatches[len(fswatches)-1].Timestamp
	report.Summary.WallClockDuration = report.Summary.LastEventTime.Sub(report.Summary.FirstEventTime)

	// children seeds with StateReady so anyChildActive returns false until
	// a real child transition fires on the timeline.
	for _, ci := range children {
		ci.state = session.StateReady
	}
	r := &sidecarReplayer{
		srcBytes:         srcBytes,
		tmp:              tmp,
		tailer:           t,
		report:           report,
		state:            session.StateReady,
		prevTransitionAt: fswatches[0].Timestamp,
		stateDurations:   map[string]time.Duration{},
		children:         children,
		signals:          session.NewSignalHolds(),
		fswatches:        fswatches,
	}
	r.emit(transition{
		EventIndex:  -1,
		VirtualTime: fswatches[0].Timestamp,
		Cause:       causeInit,
		PrevState:   "",
		NewState:    r.state,
		Reason:      "initial state",
	})
	return r, cleanup, nil
}

// extendWindowToLastTransition widens the reported observation window to cover
// a transition emitted after the last recorded fs event.
//
// A debounce window fires at its DEADLINE, which is one debounce interval past
// the event that opened it, so the process-exit and end-of-timeline flushes
// both stamp transitions later than Summary.LastEventTime. emit() charges the
// interval before each transition to the state it left, so those transitions
// contribute duration outside the reported window and state_durations sums past
// wall_clock_session_duration — while the trailing addDuration silently drops
// its now-negative remainder.
//
// The transition's timestamp is the honest one and is NOT clamped: the daemon's
// timer really did fire there (for codex/2-1_basic-turn, 2ms from the deadline
// this replayer computes), so it is the window that was too narrow.
//
// Measured by counting goldens whose state_durations sum exceeds
// wall_clock_session_duration: 42 of 399 on origin/main (all 42 sidecar-driven,
// none transcript-only; 41 exceed by exactly the 2s debounce window and one by
// 104s), 132 of 365 with #1342's two fixes but this extension disabled, and 0
// of 399 with it. So this is not a cosmetic tidy-up — the debounce fix alone
// would have tripled a pre-existing inconsistency (#1342).
func (r *sidecarReplayer) extendWindowToLastTransition() {
	n := len(r.report.Transitions)
	if n == 0 {
		return
	}
	last := r.report.Transitions[n-1].VirtualTime
	if !last.After(r.report.Summary.LastEventTime) {
		return
	}
	r.report.Summary.LastEventTime = last
	r.report.Summary.WallClockDuration = last.Sub(r.report.Summary.FirstEventTime)
}

// emit appends a transition to the report and updates the running
// prev-state duration counter. Callers supply the virtual time; the Index is
// assigned here so Transitions is always densely numbered in emission order.
func (r *sidecarReplayer) emit(tr transition) {
	tr.Index = len(r.report.Transitions)
	r.report.Transitions = append(r.report.Transitions, tr)
	r.addDuration(tr.PrevState, tr.VirtualTime.Sub(r.prevTransitionAt))
	r.prevTransitionAt = tr.VirtualTime
}

// addDuration accumulates state-duration time against s, ignoring negative or
// zero deltas (which can occur when two events share a virtual timestamp).
func (r *sidecarReplayer) addDuration(s string, d time.Duration) {
	if d > 0 {
		r.stateDurations[s] += d
	}
}

// replaySessionKey is the SignalHolds key for the session under replay. The
// replayer is single-session by construction, so one fixed key stands in for
// the live detector's per-session map.
const replaySessionKey = "primary"

// anyChildActive reports whether any subagent discovered via parent_linked
// is still working or waiting. Used by runClassifier to hold the parent
// in its current state when the classifier would otherwise return ready.
func (r *sidecarReplayer) anyChildActive() bool {
	for _, ci := range r.children {
		if ci.state == session.StateWorking || ci.state == session.StateWaiting {
			return true
		}
	}
	return false
}

// transitionCtx bundles the where/why of a classification pass — which
// timeline event triggered it (eventIdx, -1 for a synthetic hook/orphan
// re-classification), the virtual time to stamp any emitted transition
// with, and the cause tag recorded on the report. Passed as one value
// instead of three separate parameters through runClassifier's call chain.
type transitionCtx struct {
	eventIdx int
	virtTime time.Time
	cause    transitionCause
}

// runClassifier mirrors SessionDetector.processActivity's force/classify/
// parent-hold/synth-waiting pipeline. Extracted so hook and orphan events
// can re-run classification against the last-known metrics.
//
// grew mirrors the daemon's transcriptGrew||ev.Synthetic gate on the
// force-bounce (issue #905): callers pass true only when this pass either
// wrote real new transcript bytes (classifyAt) or is a hook-synthetic
// re-classification that legitimately precedes the flush (applyHookEvent).
// A zero-growth fswatcher pass — mistral-vibe's content-less slash-command
// touch — must not force ready back to working on stale LastEventType.
func (r *sidecarReplayer) runClassifier(domainMetrics *session.SessionMetrics, ctx transitionCtx, grew bool) {
	// Mirrors the daemon's skipClassification short-circuit (#329) exactly.
	// classifyAt is responsible for handing this function a pass the daemon
	// could actually have had — see readBoundaryFor there (#1342).
	if domainMetrics.NoSubstantiveActivity {
		return
	}
	if shouldForceReadyToWorking(r.state, domainMetrics, grew) {
		r.emit(transitionFromMetrics(ctx.eventIdx, ctx.virtTime, ctx.cause,
			r.state, session.StateWorking, services.ForceReadyToWorkingReason, domainMetrics))
		r.state = session.StateWorking
	}

	newState, reason := services.ClassifyState(r.state, domainMetrics)
	r.applyParentHoldAndSynthesizedWaiting(newState, reason, domainMetrics, ctx)
}

// shouldForceReadyToWorking mirrors SessionDetector's force-r→w guard (issue
// #905): a ready session only bounces back to working on real transcript
// growth or a synthetic hook event, never a content-less touch.
func shouldForceReadyToWorking(state string, domainMetrics *session.SessionMetrics, grew bool) bool {
	return state == session.StateReady && domainMetrics.LastEventType != "" && grew
}

// applyParentHoldAndSynthesizedWaiting applies the parent-child hold and
// synthesized-waiting adjustments to newState/reason, then commits whichever
// state change (if any) results.
func (r *sidecarReplayer) applyParentHoldAndSynthesizedWaiting(newState, reason string, domainMetrics *session.SessionMetrics, ctx transitionCtx) {
	// Parent-child hold: if any child is still working/waiting, keep the
	// parent in its current state rather than letting it transition to
	// ready. Matches SessionDetector's behaviour when children are live.
	parentHeldWorking := false
	if newState == session.StateReady && r.anyChildActive() {
		newState = r.state
		reason = ""
		parentHeldWorking = true
	}

	if !parentHeldWorking && services.ShouldSynthesizeCollapsedWaiting(r.state, newState, domainMetrics) {
		r.emit(transitionFromMetrics(ctx.eventIdx, ctx.virtTime, ctx.cause,
			r.state, session.StateWaiting, services.SyntheticWaitingReason, domainMetrics))
		r.state = session.StateWaiting
		newState, reason = services.ClassifyState(r.state, domainMetrics)
	}
	// Deliberately NOT mirroring the #1366 grace timer here, for the same
	// reason #988 kept ShouldSynthesizeCollapsedTurnBoundary out of the
	// transcript engine: only the live path has real timing to gate on.
	//
	// The dwell is a *publication* policy — it decides what reaches the UI,
	// and SessionDetector.applyStateTransition is the thing it sits in front
	// of. This replayer has no UI; its output model is the transition stream
	// emit() builds, and runExtendedCheck diffs state_transition events only.
	// So the goldens are a regression net for the parsers, the tailer and the
	// classifier, and debouncing that net lowers its resolution: a correctly
	// derived waiting→working with no later event behind it would simply stop
	// appearing, and a future regression that stopped deriving it would then
	// be invisible. Measured blast radius at the time of writing, so the
	// trade-off is a number rather than a hunch: 41 such transitions across 15
	// goldens in 4 adapters (claudecode 9, codex 3, hermes 2, pi 1).
	//
	// The classifier is untouched by #1366 — ClassifyStateTiered is byte for
	// byte what it was — so this file keeps measuring exactly what it measured
	// before, and no golden moves.
	if newState != r.state {
		r.emit(transitionFromMetrics(ctx.eventIdx, ctx.virtTime, ctx.cause,
			r.state, newState, reason, domainMetrics))
		r.state = newState
	}
}

// readTo advances the replayed transcript to boundary and re-runs the tailer,
// reporting whether any new bytes were consumed. Both of classifyAt's reads go
// through it — the nominal one at the recorded stat, and readBoundaryFor's
// widening — so the write cursor is maintained in exactly one place.
func (r *sidecarReplayer) readTo(boundary int64) (*tailer.SessionMetrics, bool, error) {
	grew := boundary > r.lastSize
	if grew {
		if _, err := r.tmp.Write(r.srcBytes[r.lastSize:boundary]); err != nil {
			return nil, false, err
		}
		r.lastSize = boundary
	}
	metrics, err := r.tailer.TailAndProcess()
	return metrics, grew, err
}

// classifyAt writes transcript bytes up to fileSize, runs the tailer +
// classifier, and mirrors SessionDetector.processActivity's force-r→w +
// ClassifyState pattern. Any emitted transition is added to the report.
func (r *sidecarReplayer) classifyAt(fileSize int64, ctx transitionCtx) error {
	metrics, grew, err := r.readTo(min(fileSize, int64(len(r.srcBytes))))
	if err != nil {
		return err
	}
	// A pass that parsed only non-substantive lines and left LastEventType
	// empty is the signature of a read boundary the daemon never had — widen
	// it once and re-tail. See readBoundaryFor (#1342).
	if wider, ok := r.readBoundaryFor(ctx.eventIdx, r.lastSize, metrics); ok {
		if metrics, _, err = r.readTo(wider); err != nil {
			return err
		}
		grew = true
	}
	r.lastMetrics = metrics
	domainMetrics := replayengine.TailerToDomain(metrics)
	// Virtual time, not time.Now: a time-based staleness policy must decide
	// from the transcript's own timeline, or the same recording would replay
	// differently on a slow machine and the goldens would stop being a
	// regression net.
	//
	// Overlay's []SignalExpiry return is deliberately discarded here and at
	// the other two call sites in this file (#1360). The daemon turns an
	// expiry into a log line and a lifecycle.KindHoldExpired event; this
	// replayer has no event sink to write one to — its output model is the
	// transition stream that emit() builds — so surfacing expiries would mean
	// adding an event kind to the golden format for every adapter at once.
	// The *effect* of a ceiling still replays faithfully, because the hold is
	// dropped either way and the resulting transition is what the goldens
	// compare (runExtendedCheck diffs state_transition events only).
	r.signals.Overlay(replaySessionKey, domainMetrics, ctx.virtTime)
	r.runClassifier(domainMetrics, ctx, grew)
	return nil
}

// readBoundaryFor decides whether a clamped classify pass should be widened,
// and to where. It returns (boundary, true) only for the one shape that is
// provably an artifact of the recording rather than something the daemon saw.
//
// The sidecar records file_size from the fswatcher's stat at fire time —
// SessionDetector.handleTranscriptEvent writes `FileSize: ev.Size` BEFORE
// calling onActivity, whose RefreshOnActivity then tails to EOF. So the daemon's read
// always reached at least that stat and usually past it, since the agent keeps
// appending during the tens of milliseconds the read takes. classifyAt clamps
// to the stat, which for codex and pi — both of whom open a transcript with a
// multi-kilobyte session_meta — can land exactly on the end of that header and
// manufacture a "header line only" pass with no counterpart in the daemon's
// history. NoSubstantiveActivity is then true by construction, #329's
// short-circuit fires, and for 7 recordings that was the ONLY classification
// the replay ever got: every later fs event coalesced into a debounce window
// that process_exited cancelled before its deadline (correctly — the daemon's
// timer really was cancelled), so the session's one recorded transition was
// lost and its golden kept nothing but the synthetic init row (#1342).
//
// The widening has two parts, and they answer different halves of the same
// question:
//
//   - A SIZE step, to the NEXT recorded stat for this session, or to the whole
//     transcript when there is no next event. That is the daemon's own
//     semantics read back off the recording: its single read absorbed whatever
//     had been written by the time it finished, and the next fswatcher fire is
//     the tightest upper bound the sidecar offers on where that was.
//   - A TIME step, clusterBoundary, which additionally takes every later stat
//     dequeued within readBoundaryClusterWindow of this pass. #1478 added it;
//     that constant's doc comment carries the inference, its two measured
//     walls, and why it is calibrated rather than derived.
//
// The time step is strictly additive — it can only widen further, never
// narrow — which is what keeps it clear of the 35 gemini-cli recordings that a
// REPLACEMENT time bound broke (see the rejected 200ms variant below).
//
// KNOWN COST, and it is a real one: the bound is the next stat's SIZE, never
// its TIME. When the next fswatcher fire is seconds away, the widening reads
// bytes that provably did not exist at the pass being classified, so the
// transition is reproduced at the right place in the ORDER but earlier than
// the daemon made it. Measured over 114 firings (one per recording, every one
// at eventIdx 0): gap p50=42ms, p75=13.5s, p90=15.9s, max=31.0s — so the
// "single read absorbs what was written" story above holds for the median and
// is false for the 42 firings whose gap exceeds 1s. 20 goldens now pin a first
// transition more than 1s ahead of their own events.jsonl (worst:
// mistral-vibe/2-12_context-compaction, 30.976s early). The three recordings
// this actually rescues have gaps of 1ms, 5ms and 60ms — the win comes
// entirely from the justified regime, the drift entirely from the other one.
//
// A gap bound was tried and REJECTED on measurement, not taste. Capping the
// widening at 200ms keeps all three #1342 tests green with an unchanged
// knownZeroTransition set and collapses the six worst drifts (-30.976s ->
// -0.022s), but it breaks 35 gemini-cli recordings that HEAD reproduces
// exactly (-0.000s -> +10..+28s): there the long gap is idle time AFTER a
// write the daemon did absorb. The sidecar cannot distinguish "gap is idle
// after the write" from "gap is before the write happened" — the same
// limitation the one remaining knownZeroTransition entry concedes — so the bounded
// variant is worse on the aggregate (175/118 vs 146/88 recordings drifting
// >1s/>5s). Net across the catalog the widening still MOVES TIMES TOWARD the
// daemon: 46 recordings closer, 20 further; >1s drift 171 -> 146, >5s 117 -> 88.
//
// Nothing asserts transition TIMESTAMPS today — not the goldens, not
// compareOrdered, which walks prev_state/new_state index-by-index and never
// the time — which is why the cost above is invisible to every gate in this
// package. Tracked separately; do not read a green replay-fixtures run as
// evidence that timings are right.
//
// Two properties make this safe, and both were measured (see the PR for #1342):
//
//   - It widens the BOUNDARY, never the verdict. #329's guard is re-applied
//     unchanged to the re-tailed metrics, so a pass that is still
//     non-substantive at the wider boundary still skips — which is what keeps
//     codex/1-1_session-start replaying the zero transitions its daemon
//     recorded. An earlier draft narrowed the guard itself instead
//     (skip only when LastEventType != ""), and that classified on no evidence:
//     it fabricated a ready→working in exactly those goldens, turning two that
//     AGREED with their recording into two that disagreed.
//   - It fires only on the artifact's signature, so it is not the global
//     one-event lookahead that was also tried and rejected. Widening every
//     leading-edge pass fixed 3 of the 7 and made overall extended-check
//     divergence WORSE (146 → 153 recordings).
//
// MEASURED over all sidecar-drivable recordings, stating the cost as well as
// the win, since these two numbers are the whole argument for the change.
//
// Both tables below are HISTORY — each row is a model that was evaluated, most
// of them rejected, and those rows are fixed by the measurement that rejected
// them, so they are snapshots and are not re-derived.
//
// One row is an exception worth naming rather than leaving for a reader to
// trip over: the second table's SHIPPED row describes the model that is
// actually running, so its zero/fabricated/divergent columns were the current
// figures on the day it was written and will silently stop being so the first
// time replay fidelity moves. The live values are censusOfTheCommittedCatalog
// in issue1503_census_test.go, machine-generated and re-derived on every test
// run (#1503); when they disagree with the shipped row below, the census is
// right and the row is a snapshot. A hand-typed copy is how that row and
// tools/replay-fixtures.sh came to disagree by more than fifty recordings.
//
//	                                zero-transition  fabricated  divergent
//	main                                         20           1        196
//	debounce fix only                             7           1        146
//	+ narrowing #329's guard (rejected)           0           3        145
//	+ readBoundaryFor (shipped)                   4           1        145
//
// "fabricated" is recordings replaying a transition the daemon never logged;
// its single case pre-dates #1342 and neither fix moves it. The rejected draft
// bought the last 4 by inventing 2 — a golden that asserts the WRONG thing,
// which is strictly worse than one that asserts nothing and is the failure
// this whole ticket is about.
//
// #1478 then supplied the time-aware boundary those 4 needed, and reached 3 of
// them. Measured the same way, over the same population, with #1480's timing
// harness added to the columns because a boundary change moves WHEN a
// transition fires and nothing before #1480 could see that:
//
// "divergent" is extendedCheck.Diverges — one named predicate, which is where
// the definition now lives rather than in this sentence (#1503). The rows below
// extend the series above rather than starting a new one on a narrower
// definition; that they DO is the thing worth checking, because #1478 computed
// this column with a plausible near-miss spelling and every row came out one
// low. Diverges' doc comment names the two near-misses and the single committed
// recording that separates them.
//
// The table is #1478's calibration, measured over the catalog as it stood then
// and kept as the evidence for the window that shipped. Its columns are counts
// over that population, so they no longer equal the live gate's figures — #1517
// widened the walk and the shipped row's divergent/pairs columns moved with it.
// Read it as a comparison BETWEEN the rows, which is what it was built for, not
// as a current measurement:
//
//	                          zero  fabricated  divergent  drift>1s  pairs
//	#1476 as shipped             4           1        145       119    818
//	+ 2ms cluster window         4           1        143       118    821
//	+ 10ms cluster (shipped)     1           1        140       105    826
//	+ 28ms cluster (rejected)    1           2        141       106    826
//	+ 69ms cluster (rejected)    0           3        142       116    825
//
// The shipped row is best on every column but zero, and the plateau it sits on
// runs 10-25ms. The exception is the point: the ONLY way to improve zero is the
// 69ms row, which costs two fabrications AND makes drift worse (105 -> 116)
// rather than trading one axis for another. That is the trade #1342 refused and
// #1478 refuses again.
//
// The one recording that remains is pinned in knownZeroTransition in
// issue1342_debounce_test.go, with the wall that keeps it there.
//
// consumed is the replayer's current write cursor (r.lastSize), NOT the pass's
// nominal target: a pass whose fileSize sits BELOW the cursor writes nothing,
// and comparing against the target there would return a boundary behind the
// cursor and slice srcBytes backwards.
func (r *sidecarReplayer) readBoundaryFor(eventIdx int, consumed int64, metrics *tailer.SessionMetrics) (int64, bool) {
	// The upper bound is not reachable from today's four call sites, but it is
	// load-bearing now that clusterBoundary indexes fswatches[eventIdx]
	// directly: before #1478 an out-of-range-high index was merely inert.
	if eventIdx < 0 || eventIdx >= len(r.fswatches) || metrics == nil {
		return 0, false
	}
	if !metrics.NoSubstantiveActivity || metrics.LastEventType != "" {
		return 0, false
	}
	wider := int64(len(r.srcBytes))
	if next := eventIdx + 1; next < len(r.fswatches) {
		wider = min(r.fswatches[next].FileSize, wider)
	}
	if w := r.clusterBoundary(eventIdx); w > wider {
		wider = w
	}
	if wider <= consumed {
		return 0, false
	}
	return wider, true
}

// readBoundaryClusterWindow is how far past a classify pass's own fswatcher
// fire a LATER recorded stat is still treated as bytes the SAME daemon read
// absorbed. It is the time-aware half of the boundary #1478 asked for.
//
// WHAT MAKES IT SOUND, AND WHERE THAT STOPS. The sidecar's `ts` is the
// daemon's DEQUEUE time — SessionDetector.record stamps time.Now() in the
// processing loop — while `file_size` is the watcher's stat, taken earlier in
// the watcher goroutine and never recorded. The detector loop is serial, so
// event j>i is dequeued only after the read for pass i has returned; a j that
// dequeues microseconds later was therefore ALREADY QUEUED while that read
// ran, which means its watcher fire — and so the size it observed — preceded
// the read's completion. That is the inference, and it is why the window is
// small: it is a bound on how long one read takes, not on how long a session
// is idle.
//
// It is an INFERENCE and not a proof, and the gap is worth stating plainly
// because it is the whole reason a constant is needed at all. A small
// inter-dequeue gap is also consistent with "the loop went idle and event j
// arrived just then", and the recording cannot tell those apart: the watcher's
// stat time is not a field of lifecycle.Event and is unrecoverable. So the
// window is CALIBRATED against the committed catalog rather than derived from
// first principles, and both of its walls are measured facts about that
// catalog rather than taste:
//
//   - FLOOR 3ms. Below it the rescue is incomplete — at 2.7ms only two of the
//     three #1478 recordings reproduce their transitions, because the third's
//     four fires span further than that.
//   - CEILING 28ms. At and above it replay FABRICATES: codex/2-1_basic-turn's
//     18-54-06 recording gains a transition its daemon never logged. A SECOND
//     wall follows closely at 52ms, where codex/1-1_session-start joins it —
//     its first two fires are 51.663ms apart. Both walls matter: a 60ms
//     "compromise" reaching for the pinned recording would clear neither.
//
// The ceiling is the load-bearing number, and it did not have to land where it
// did: those are EXACTLY the two goldens that #1342's rejected guard-narrowing
// broke (see readBoundaryFor's table). Two unrelated mechanisms — narrowing
// #329's guard, and widening this window — fabricate first in the same two
// recordings, which is independent evidence that the wall is a property of the
// catalog and not of either heuristic.
//
// 10ms is the geometric midpoint of [3ms, 28ms] (9.17ms, rounded), and it sits
// at the low edge of the plateau where every figure #1480 measures is jointly
// best (see the table in readBoundaryFor). Low edge rather than middle because
// the costs are asymmetric: too high fabricates, which is the failure this
// whole line of work exists to prevent, while too low merely leaves a
// recording pinned in knownZeroTransition, which is the status quo.
//
// It is corroborated by a measurement that does not depend on any of the
// above. The daemon's own in-pass read+classify latency is directly
// observable — a state_transition and the transcript_activity that produced it
// are both stamped by d.record in the same loop iteration — and over the 1157
// such pairs in the catalog that population has a near-empty decade at 5-10ms
// (7 pairs, 0.6%) separating a dense sub-5ms mode (47.4%) from the 10-50ms
// tail. 10ms sits just above the reads that dominate.
//
// A var rather than a const so TestReadBoundaryClusterWindow_BothWallsAreMeasured
// can drive it past each wall and observe the failure — the calibration's own
// mutation evidence, committed rather than described.
var readBoundaryClusterWindow = 10 * time.Millisecond

// clusterBoundary returns the largest recorded stat whose dequeue timestamp
// sits within readBoundaryClusterWindow of the pass at eventIdx, or 0 when no
// later event qualifies.
//
// It is purely ADDITIVE to the one-step widening in readBoundaryFor, which is
// what keeps it off the 35 gemini-cli recordings that reproduce exactly today:
// there the next fire is seconds away, so no later stat is within the window
// and this contributes nothing, leaving the one-step boundary untouched. A
// variant that REPLACED the one-step widening with a time bound is the 200ms
// gap bound rejected during #1476's review, and it broke exactly those 35.
//
// Two details of the scan, stated because the obvious reading of each is wrong:
//
// The window is ANCHORED, not chained — every candidate is compared to the
// pass's own timestamp, and `at` is never reassigned — so the rule can never go
// transitive across an idle period no matter how many events follow. The
// `break` is therefore a pure early exit, valid because the fswatch stream is
// Seq-sorted and its dequeue timestamps are monotonic (re-verified for #1517's
// widened walk: of the catalog's 396 committed sidecars, 393 carry a
// transcript_activity stream and NONE has a non-monotonic timestamp in Seq
// order — including the two recordings the widening newly reaches).
// Do NOT "restore" a chained form by comparing j to j-1: that would silently
// turn a bounded 10ms rule into an unbounded walk and invalidate the
// calibration.
//
// The fold takes the MAXIMUM rather than the last qualifying stat because sizes
// in a burst are not guaranteed monotonic — a reconcile-sweep fire can
// re-report an older stat. No committed recording exercises that inside a
// window (measured), so the catalog cannot witness it; TestClusterBoundary
// covers it synthetically instead of leaving the choice unfalsifiable.
func (r *sidecarReplayer) clusterBoundary(eventIdx int) int64 {
	if readBoundaryClusterWindow <= 0 {
		return 0
	}
	at := r.fswatches[eventIdx].Timestamp
	var reach int64
	for j := eventIdx + 1; j < len(r.fswatches); j++ {
		if r.fswatches[j].Timestamp.Sub(at) > readBoundaryClusterWindow {
			break
		}
		if v := min(r.fswatches[j].FileSize, int64(len(r.srcBytes))); v > reach {
			reach = v
		}
	}
	return reach
}

// applyHookEvent mirrors SessionDetector.HandlePermissionHook: apply the hook's
// signal effect, then trigger a re-classification using the last-known metrics.
//
// Both sides read the same session.HookSignal table, so a hook the daemon
// honours can no longer be silently dropped here. That divergence was real:
// this function used to carry its own switch, which recognized
// PermissionRequest but fell through to default on PreToolUse — a hook the
// daemon holds SignalPermissionPrompt on, and one that replaydata/ carries two
// of (issue #1320).
//
// Why it stayed invisible is worth stating precisely, because the obvious
// answer is wrong. It is *not* that a PermissionRequest follows each PreToolUse
// closely enough to be equivalent — replaying those two recordings by hand
// shows the corrected hold moving the transition 19ms and 136ms earlier and
// flipping its cause from hook to debounce_coalesce. It stayed invisible
// because no committed gate reaches *this function* over a recording that
// carries a hook: the two tests that do drive replayWithSidecar over real
// fixtures (10-full-lifecycle-839f0678, 13-full-lifecycle-continue-8a525d27)
// contain zero hook_received events, and every recording that does carry one
// is only ever replayed transcript-only — replay-fixtures.sh and the
// byte-identity golden never enable sidecar mode, because resolveInputPaths
// auto-detects only a sibling named <transcript>.events.jsonl while every
// sidecar in replaydata/ is named plain events.jsonl. See issue #1326.
//
// Hooks absent from the table are still ignored here: Notification and
// PreCompact could be rows, but no recording in replaydata/ fires one — add
// each as a table row together with the first recording that does. Stop WAS
// in that sentence until #1695; see hookSignalEffects for why a payload-free
// row is a floor rather than a wrong answer, and TestStopHookIsGradedByTheCommittedCatalog
// for the recording that now grades it.
//
// NoSubstantiveActivity is cleared before the overlay, and that one line is
// what made the Stop channel reachable at all (#1695). The flag is PER PASS —
// tailer.go sets it as `scan.linesParsed > 0 && !scan.substantive`, and
// session_detector_activity.go's own comment says so in as many words — but
// r.lastMetrics is the last TRANSCRIPT batch's metrics, and a hook pass is a
// different pass that parses no transcript lines. Carrying the flag across was
// therefore asserting something the harness never observed, and runClassifier's
// mirror of #329's short-circuit then swallowed the hook: measured on codex's
// 2-13_turn-end-terminal-text, the Stop overlaid HookTurnDone and the
// classifier answered `ready / "agent finished turn → ready"` — and the
// short-circuit above discarded it, one instruction before it would have been
// emitted. The daemon does not have this problem because a hook's synthetic
// activity event goes through processActivity, whose RefreshOnActivity re-tails
// and RECOMPUTES the flag for that pass; false is what a zero-line pass yields.
//
// The declared limit: false is an approximation of that recomputation, not the
// recomputation. A daemon pass that read new-but-non-substantive bytes between
// the last fs event and the hook would compute true and skip, where this
// computes false and classifies. That case cannot be reconstructed from the
// sidecar (it records no stat for the hook's own moment), and the approximation
// errs toward reproducing the daemon's recorded transition rather than toward
// dropping it — which is the direction the whole extended check is scored in.
// hookRetiresSessionError reports whether a hook effect asserts a turn boundary
// — the one hook effect that can retire a session error (#1799). A Release is
// the opposite assertion and must not qualify.
//
// Named rather than inlined so the harness's mirror of the daemon rule is a
// thing a test can hold, because no committed recording reaches it: as of #1799
// the corpus has no recording carrying BOTH a hook event and a session error.
// Reproduce with
//
//	grep -rl '"hook_name"' replaydata/agents/{claudecode,copilot}/scenarios/2-{9,14}*/recordings/*/events.jsonl
//
// which matches nothing. The fixtures that produce errors pre-date hook
// recording, so replay fidelity here is guarded by the unit test rather than by
// a golden — a real gap until one of those cells is re-recorded with hooks
// installed.
func hookRetiresSessionError(effect session.HookSignalEffect) bool {
	return effect.Signal == session.SignalTurnDone && !effect.Release
}

// retireSessionErrorOnHookBoundary mirrors SessionDetector.HandleStopHook for
// the offline harness (#1799): a hook-delivered turn boundary retires the
// tailer's sticky session error under the SAME rule the daemon applies. Without
// it the harness would reproduce an error → ready → error flicker the daemon no
// longer has, and the extended check would score that divergence against the
// recording rather than against the bug.
//
// It touches TWO copies, and the second is what the daemon does not need. The
// tailer call ends the sticky field, so every LATER pass stays clear; the cached
// r.lastMetrics is THIS pass's view, because a hook pass classifies off that
// cache rather than re-tailing. The daemon re-tails via the synthetic activity
// event HandleStopHook dispatches, so one call suffices there.
//
// Both copies are retired under ClearedByTurnBoundary rather than
// unconditionally. An earlier draft blanked the cached copy outright, which
// would have shown a terminal failure as retired for exactly the pass a viewer
// renders while the tailer still held it — the two views disagreeing is worse
// than either answer.
func (r *sidecarReplayer) retireSessionErrorOnHookBoundary(effect session.HookSignalEffect) {
	if !hookRetiresSessionError(effect) {
		return
	}
	r.tailer.IngestTurnBoundary()
	if r.lastMetrics != nil && r.lastMetrics.SessionError.ClearedByTurnBoundary() {
		r.lastMetrics.SessionError = nil
	}
}

func (r *sidecarReplayer) applyHookEvent(hookEv lifecycle.Event) {
	effect, ok := session.HookSignal(hookEv.HookName)
	if !ok {
		return
	}
	r.signals.ApplyHook(replaySessionKey, effect, hookEv.Timestamp)
	r.retireSessionErrorOnHookBoundary(effect)
	if r.lastMetrics == nil {
		return
	}
	domainMetrics := replayengine.TailerToDomain(r.lastMetrics)
	domainMetrics.NoSubstantiveActivity = false
	r.signals.Overlay(replaySessionKey, domainMetrics, hookEv.Timestamp)
	r.runClassifier(domainMetrics, transitionCtx{eventIdx: -1, virtTime: hookEv.Timestamp, cause: causeHook}, true)
}

// Timeline kinds for the merged event stream in runDebouncedTimeline.
const (
	timelineFS = iota
	timelineHook
	timelineProcessExit
	timelineLifecycleStart
	timelineChildTransition
	timelineChildOrphan
)

// timelineEntry is one row in the merged, timestamp-ordered replay stream.
// Synthetic orphan triggers carry no sidecar seq, so timestamp is the
// primary sort key with seq as a tiebreak for real events.
type timelineEntry struct {
	kind int
	idx  int
	seq  int64
	ts   time.Time
}

// buildTimeline interleaves the sidecar streams (plus synthetic child
// transitions and orphan triggers) and returns them sorted by timestamp,
// with sidecar seq as tiebreak. For real events whose timestamps are
// monotonic with their seqs this is equivalent to a seq-only sort; orphan
// triggers need timestamp-primary ordering so they land at the right
// moment in virtual time.
func buildTimeline(b sidecarBuckets) []timelineEntry {
	cap := len(b.fswatches) + len(b.hookEvents) + len(b.processExits) +
		len(b.lifecycleStarts) + len(b.childTransitions) + len(b.orphanTriggers)
	timeline := make([]timelineEntry, 0, cap)
	for i, ev := range b.fswatches {
		timeline = append(timeline, timelineEntry{kind: timelineFS, idx: i, seq: ev.Seq, ts: ev.Timestamp})
	}
	for i, ev := range b.hookEvents {
		timeline = append(timeline, timelineEntry{kind: timelineHook, idx: i, seq: ev.Seq, ts: ev.Timestamp})
	}
	for i, ev := range b.processExits {
		timeline = append(timeline, timelineEntry{kind: timelineProcessExit, idx: i, seq: ev.Seq, ts: ev.Timestamp})
	}
	for i, ev := range b.lifecycleStarts {
		timeline = append(timeline, timelineEntry{kind: timelineLifecycleStart, idx: i, seq: ev.Seq, ts: ev.Timestamp})
	}
	for i, ev := range b.childTransitions {
		timeline = append(timeline, timelineEntry{kind: timelineChildTransition, idx: i, seq: ev.Seq, ts: ev.Timestamp})
	}
	for i, orphan := range b.orphanTriggers {
		timeline = append(timeline, timelineEntry{kind: timelineChildOrphan, idx: i, ts: orphan.at})
	}
	sort.SliceStable(timeline, func(i, j int) bool {
		if !timeline[i].ts.Equal(timeline[j].ts) {
			return timeline[i].ts.Before(timeline[j].ts)
		}
		return timeline[i].seq < timeline[j].seq
	})
	return timeline
}

// debounceState tracks the pending debounce window as the timeline advances.
// Kept as a struct (not closures) so runDebouncedTimeline can stay under the
// 80-line budget with named helpers.
type debounceState struct {
	pending       bool
	coalesced     bool
	deadline      time.Time
	pendingSize   int64
	pendingIdx    int
	alive         bool
	debounceDelay time.Duration
}

// runDebouncedTimeline applies the daemon's debounce state machine over the
// merged timeline. Hook events bypass debounce — they fire immediately
// regardless of the pending window, matching the live daemon.
//
// alive tracks whether a daemon lifetime is currently attached. fs/hook
// events arriving between process_exited and the next lifecycle-start were
// never processed by a live daemon and must be skipped. When the primary
// session has no lifecycle-start markers at all (e.g. --session targeting a
// subagent or a synthetic session whose birth isn't in the sidecar), the
// replay starts alive so it behaves like a single lifetime rather than
// silently dropping every fs event.
func (r *sidecarReplayer) runDebouncedTimeline(b sidecarBuckets, debounceCfg time.Duration) error {
	timeline := buildTimeline(b)
	debounce := debounceCfg
	if debounce <= 0 {
		debounce = 2 * time.Second
	}
	d := debounceState{
		alive:         len(b.lifecycleStarts) == 0,
		debounceDelay: debounce,
	}

	for _, entry := range timeline {
		handled, err := r.applyTimelineControlEntry(entry, b, &d)
		if err != nil {
			return err
		}
		if handled {
			continue
		}
		if !d.alive {
			continue
		}
		if err := r.advanceFSEvent(b.fswatches[entry.idx], entry.idx, &d); err != nil {
			return err
		}
	}
	return r.flushPendingDebounce(b.fswatches, d)
}

// flushDebounceIfExpired fires a pending debounce whose window has already
// closed as of the given virtual time. Called before non-fs events (hooks,
// orphan synth) whose virtual time may have overtaken the pending window —
// the live daemon's debounce timer would have fired naturally, but the
// replay only catches it on the next fs event. Catching up here prevents
// the flush from smearing into a later fs event's output.
func (r *sidecarReplayer) flushDebounceIfExpired(atTs time.Time, d *debounceState) error {
	if !d.pending || atTs.Before(d.deadline) {
		return nil
	}
	if d.coalesced {
		if err := r.classifyAt(d.pendingSize, transitionCtx{eventIdx: d.pendingIdx, virtTime: d.deadline, cause: causeDebounceCoalesce}); err != nil {
			return err
		}
	}
	d.pending = false
	d.coalesced = false
	return nil
}

// applyTimelineControlEntry handles the non-fs timeline entries: lifecycle
// starts, process exits, hooks, child state transitions, and synthetic
// orphan promotions. Returns (handled, err) where handled=true means the
// caller should skip the fs processing branch for this entry.
func (r *sidecarReplayer) applyTimelineControlEntry(entry timelineEntry, b sidecarBuckets, d *debounceState) (bool, error) {
	switch entry.kind {
	case timelineLifecycleStart:
		d.alive = true
		return true, nil
	case timelineProcessExit:
		// A window whose deadline already passed is not "pending" — the
		// daemon's timer fired it, and the transitions it produced are in the
		// sidecar. Catch it up before tearing down, exactly as the hook and
		// orphan branches do (issue #1342). Without this, a recording whose fs
		// events all land inside one debounce window replayed ZERO transitions:
		// the single classify pass ran on the first event against a partial
		// transcript, every later event only extended the window, and this
		// branch then discarded it — leaving a golden that held nothing but the
		// synthetic init row and so could not fail when the classifier
		// regressed. It also explains why relaxing flushPendingDebounce's
		// early-return measured as no-change: by the time that trailing flush
		// is reached, the reset below has already zeroed d.pending, d.coalesced
		// and d.pendingSize, so there is nothing left for it to fire.
		if err := r.flushDebounceIfExpired(entry.ts, d); err != nil {
			return true, fmt.Errorf("flush before process exit: %w", err)
		}
		// Daemon torn down: a pending timer that has NOT yet expired is
		// genuinely cancelled (not fired), and the next lifetime starts a fresh
		// session in ready. Reset state so lifetime-2 events don't coalesce
		// with lifetime-1 debounce.
		*d = debounceState{debounceDelay: d.debounceDelay}
		r.state = session.StateReady
		return true, nil
	case timelineHook:
		if !d.alive {
			return true, nil
		}
		if err := r.flushDebounceIfExpired(b.hookEvents[entry.idx].Timestamp, d); err != nil {
			return true, fmt.Errorf("flush before hook: %w", err)
		}
		r.applyHookEvent(b.hookEvents[entry.idx])
		return true, nil
	case timelineChildTransition:
		// Child state changes drive the parent-hold check. Apply regardless
		// of the parent's alive flag — child transitions are recorded from
		// their own daemon lifetime, which may not align with the parent.
		ev := b.childTransitions[entry.idx]
		if ci, ok := r.children[ev.SessionID]; ok {
			ci.state = ev.NewState
		}
		return true, nil
	case timelineChildOrphan:
		return true, r.applyChildOrphan(b.orphanTriggers[entry.idx], d)
	}
	return false, nil
}

// applyChildOrphan fires a synthetic orphan-promotion for a child whose
// transcript went quiet while still working/waiting. Flushes any pending
// debounce that's now expired, then re-runs the classifier so the parent's
// working→ready transition fires at the virtual time the daemon would
// have emitted it.
func (r *sidecarReplayer) applyChildOrphan(orphan orphanTrigger, d *debounceState) error {
	ci, ok := r.children[orphan.sessionID]
	if !ok {
		return nil
	}
	if ci.state != session.StateWorking && ci.state != session.StateWaiting {
		return nil
	}
	ci.state = session.StateReady
	if !d.alive || r.lastMetrics == nil {
		return nil
	}
	if err := r.flushDebounceIfExpired(orphan.at, d); err != nil {
		return fmt.Errorf("flush before orphan: %w", err)
	}
	// If the flush itself released the hold and transitioned the parent to
	// ready, there's nothing left for the orphan to re-classify — skip to
	// avoid a spurious force-back-to-working against stale metrics.
	if r.state == session.StateReady {
		return nil
	}
	domainMetrics := replayengine.TailerToDomain(r.lastMetrics)
	r.signals.Overlay(replaySessionKey, domainMetrics, orphan.at)
	// grew=false: r.state != StateReady is already guaranteed by the check
	// above, so the force-bounce branch never evaluates this value here.
	r.runClassifier(domainMetrics, transitionCtx{eventIdx: -1, virtTime: orphan.at, cause: causeEvent}, false)
	return nil
}

// advanceFSEvent processes one fswatcher entry through the debounce state
// machine: fire the pending window when its deadline has passed, then either
// classify immediately (no pending window) or coalesce into the next window.
func (r *sidecarReplayer) advanceFSEvent(fsev lifecycle.Event, i int, d *debounceState) error {
	if d.pending && !fsev.Timestamp.Before(d.deadline) {
		if d.coalesced {
			if err := r.classifyAt(d.pendingSize, transitionCtx{eventIdx: d.pendingIdx, virtTime: d.deadline, cause: causeDebounceCoalesce}); err != nil {
				return fmt.Errorf("flush timer at fsev %d: %w", i, err)
			}
		}
		d.pending = false
		d.coalesced = false
	}
	if !d.pending {
		if err := r.classifyAt(fsev.FileSize, transitionCtx{eventIdx: i, virtTime: fsev.Timestamp, cause: causeEvent}); err != nil {
			return fmt.Errorf("classify fsev %d: %w", i, err)
		}
		d.pending = true
		d.deadline = fsev.Timestamp.Add(d.debounceDelay)
		return nil
	}
	d.coalesced = true
	d.deadline = fsev.Timestamp.Add(d.debounceDelay)
	d.pendingSize = fsev.FileSize
	d.pendingIdx = i
	return nil
}

// flushPendingDebounce fires the leftover coalesced window after the last
// fswatcher event. Matches the live daemon's behaviour of emitting one final
// classify pass for activity that arrived within a debounce window that
// never closed naturally.
func (r *sidecarReplayer) flushPendingDebounce(fswatches []lifecycle.Event, d debounceState) error {
	if !d.pending || !d.coalesced {
		return nil
	}
	lastFs := fswatches[len(fswatches)-1]
	fireTime := lastFs.Timestamp.Add(d.debounceDelay)
	if err := r.classifyAt(d.pendingSize, transitionCtx{eventIdx: d.pendingIdx, virtTime: fireTime, cause: causeDebounceCoalesce}); err != nil {
		return fmt.Errorf("final flush: %w", err)
	}
	return nil
}
