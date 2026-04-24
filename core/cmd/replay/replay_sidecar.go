package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"irrlicht/core/adapters/inbound/agents"
	"irrlicht/core/adapters/inbound/agents/claudecode"
	"irrlicht/core/application/services"
	"irrlicht/core/domain/lifecycle"
	"irrlicht/core/domain/session"
	"irrlicht/core/pkg/tailer"
)

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
	sidecarEvents, err := loadAllLifecycleEvents(sidecarPath)
	if err != nil {
		return nil, fmt.Errorf("load sidecar: %w", err)
	}

	primarySessionID := resolvePrimarySession(cfg, sidecarEvents)
	if primarySessionID == "" {
		return nil, fmt.Errorf("sidecar %s has no transcript_new event — cannot identify the primary session", sidecarPath)
	}
	buckets := bucketSidecarEvents(sidecarEvents, primarySessionID)
	if len(buckets.fswatches) == 0 {
		return nil, fmt.Errorf("sidecar has no transcript_activity events with file_size for primary session %s: %s", primarySessionID, sidecarPath)
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
	finalizeSummary(r.report, len(buckets.fswatches), r.stateDurations, r.lastMetrics)
	r.report.Sessions = buildSessionTimelines(sidecarEvents)
	return r.report, nil
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
	// First pass on the primary's own events plus child discovery.
	for _, ev := range sidecarEvents {
		if ev.Kind == lifecycle.KindParentLinked && ev.ParentSessionID == primarySessionID {
			if _, ok := b.children[ev.SessionID]; !ok {
				b.children[ev.SessionID] = &childInfo{finalState: session.StateReady}
			}
		}
		if ev.SessionID != primarySessionID {
			continue
		}
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
	// Second pass over child sessions to gather transitions + last-activity.
	for _, ev := range sidecarEvents {
		ci, ok := b.children[ev.SessionID]
		if !ok {
			continue
		}
		if ev.Timestamp.After(ci.lastActivityAt) {
			ci.lastActivityAt = ev.Timestamp
		}
		if ev.Kind == lifecycle.KindStateTransition {
			b.childTransitions = append(b.childTransitions, ev)
			if ev.NewState != "" {
				ci.finalState = ev.NewState
			}
		}
	}
	// Any child whose final recorded state is working/waiting would have
	// been fast-forwarded to ready by the daemon's stale-sweep once its
	// transcript went quiet for SubagentQuietWindow. Fire a synthetic
	// trigger at lastActivityAt + quiet window so the parent's held-
	// working releases at roughly the virtual time the daemon would have.
	for id, ci := range b.children {
		if ci.finalState == session.StateWorking || ci.finalState == session.StateWaiting {
			b.orphanTriggers = append(b.orphanTriggers, orphanTrigger{
				sessionID: id,
				at:        ci.lastActivityAt.Add(services.SubagentQuietWindow),
			})
		}
	}
	return b
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

	// permissionPending mirrors SessionDetector.permissionPending for the
	// primary session. Set by PermissionRequest hooks and cleared by
	// PostToolUse / PostToolUseFailure hooks, then overlaid onto metrics
	// before ClassifyState so rule 0 can fire.
	permissionPending bool

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
	parser := agents.ParserFor(adapterName)
	t := tailer.NewTranscriptTailer(tmpPath, parser, adapterName)

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

// overlayPermissionPending mirrors SessionDetector's overlay step: set the
// flag on metrics so ClassifyState's rule 0 fires. Tool-denial short-
// circuits the flag — Claude Code doesn't emit PostToolUseFailure on
// denial, so the denial text in the transcript is what clears it.
func (r *sidecarReplayer) overlayPermissionPending(m *session.SessionMetrics) {
	if m == nil || !r.permissionPending {
		return
	}
	if m.LastWasToolDenial {
		r.permissionPending = false
		return
	}
	m.PermissionPending = true
}

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

// runClassifier mirrors SessionDetector.processActivity's force/classify/
// parent-hold/synth-waiting pipeline. Extracted so hook and orphan events
// can re-run classification against the last-known metrics.
func (r *sidecarReplayer) runClassifier(domainMetrics *session.SessionMetrics, virtTime time.Time, eventIdx int, cause transitionCause) {
	if r.state == session.StateReady && domainMetrics.LastEventType != "" {
		r.emit(transitionFromMetrics(eventIdx, virtTime, cause,
			r.state, session.StateWorking, services.ForceReadyToWorkingReason, domainMetrics))
		r.state = session.StateWorking
	}

	newState, reason := services.ClassifyState(r.state, domainMetrics)

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
		r.emit(transitionFromMetrics(eventIdx, virtTime, cause,
			r.state, session.StateWaiting, services.SyntheticWaitingReason, domainMetrics))
		r.state = session.StateWaiting
		newState, reason = services.ClassifyState(r.state, domainMetrics)
	}
	if newState != r.state {
		r.emit(transitionFromMetrics(eventIdx, virtTime, cause,
			r.state, newState, reason, domainMetrics))
		r.state = newState
	}
}

// classifyAt writes transcript bytes up to fileSize, runs the tailer +
// classifier, and mirrors SessionDetector.processActivity's force-r→w +
// ClassifyState pattern. Any emitted transition is added to the report.
func (r *sidecarReplayer) classifyAt(fileSize int64, virtTime time.Time, eventIdx int, cause transitionCause) error {
	target := min(fileSize, int64(len(r.srcBytes)))
	if target > r.lastSize {
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
	domainMetrics := tailerToDomain(metrics)
	r.overlayPermissionPending(domainMetrics)
	r.runClassifier(domainMetrics, virtTime, eventIdx, cause)
	return nil
}

// applyHookEvent mirrors SessionDetector.HandlePermissionHook: update the
// permission-pending flag based on hook type, then trigger a re-
// classification using the last-known metrics. Hook events other than the
// three recognized Claude Code hooks are ignored.
func (r *sidecarReplayer) applyHookEvent(hookEv lifecycle.Event) {
	switch hookEv.HookName {
	case claudecode.HookPermissionRequest:
		r.permissionPending = true
	case claudecode.HookPostToolUse, claudecode.HookPostToolUseFailure:
		r.permissionPending = false
	default:
		return
	}
	if r.lastMetrics == nil {
		return
	}
	domainMetrics := tailerToDomain(r.lastMetrics)
	r.overlayPermissionPending(domainMetrics)
	r.runClassifier(domainMetrics, hookEv.Timestamp, -1, causeHook)
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
		if err := r.classifyAt(d.pendingSize, d.deadline, d.pendingIdx, causeDebounceCoalesce); err != nil {
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
	domainMetrics := tailerToDomain(r.lastMetrics)
	r.overlayPermissionPending(domainMetrics)
	r.runClassifier(domainMetrics, orphan.at, -1, causeEvent)
	return nil
}

// advanceFSEvent processes one fswatcher entry through the debounce state
// machine: fire the pending window when its deadline has passed, then either
// classify immediately (no pending window) or coalesce into the next window.
func (r *sidecarReplayer) advanceFSEvent(fsev lifecycle.Event, i int, d *debounceState) error {
	if d.pending && !fsev.Timestamp.Before(d.deadline) {
		if d.coalesced {
			if err := r.classifyAt(d.pendingSize, d.deadline, d.pendingIdx, causeDebounceCoalesce); err != nil {
				return fmt.Errorf("flush timer at fsev %d: %w", i, err)
			}
		}
		d.pending = false
		d.coalesced = false
	}
	if !d.pending {
		if err := r.classifyAt(fsev.FileSize, fsev.Timestamp, i, causeEvent); err != nil {
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
	if err := r.classifyAt(d.pendingSize, fireTime, d.pendingIdx, causeDebounceCoalesce); err != nil {
		return fmt.Errorf("final flush: %w", err)
	}
	return nil
}
