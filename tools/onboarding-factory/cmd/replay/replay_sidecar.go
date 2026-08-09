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
// of the 56 fallbacks the cause really is an adapter property: opencode (30)
// and hermes (24) are agent.ProcessOwnedStore, with no transcript file for the
// daemon to watch and so no fswatcher fire to record, and aider is
// agent.FilesUnderCWD, with no session identifier of its own.
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
// NOT it. Incomplete fs-event coverage is real but explains only 30 of 311
// sidecar-drivable recordings, and only 1 of the 22 worst-degraded (the other
// 21 have 100% coverage). What is established is that the sidecar timeline's
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

// classifyAt writes transcript bytes up to fileSize, runs the tailer +
// classifier, and mirrors SessionDetector.processActivity's force-r→w +
// ClassifyState pattern. Any emitted transition is added to the report.
func (r *sidecarReplayer) classifyAt(fileSize int64, ctx transitionCtx) error {
	target := min(fileSize, int64(len(r.srcBytes)))
	grew := target > r.lastSize
	if grew {
		if _, err := r.tmp.Write(r.srcBytes[r.lastSize:target]); err != nil {
			return err
		}
		r.lastSize = target
	}

	metrics, err := r.tailer.TailAndProcess()
	if err != nil {
		return err
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
// Hooks absent from the table are still ignored here. Stop could not be a table
// row (its effect needs a payload lifecycle.Event does not carry); Notification
// and PreCompact could be, but no recording in replaydata/ fires one — add each
// as a table row together with the first recording that does.
func (r *sidecarReplayer) applyHookEvent(hookEv lifecycle.Event) {
	effect, ok := session.HookSignal(hookEv.HookName)
	if !ok {
		return
	}
	r.signals.ApplyHook(replaySessionKey, effect, hookEv.Timestamp)
	if r.lastMetrics == nil {
		return
	}
	domainMetrics := replayengine.TailerToDomain(r.lastMetrics)
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
		// Daemon torn down: pending debounce timer is cancelled (not fired),
		// and the next lifetime starts a fresh session in ready. Reset state
		// so lifetime-2 events don't coalesce with lifetime-1 debounce.
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
