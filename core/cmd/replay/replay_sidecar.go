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

	r, cleanup, err := newSidecarReplayer(transcriptPath, srcBytes, cfg, buckets.fswatches)
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
	fswatches       []lifecycle.Event
	hookEvents      []lifecycle.Event
	processExits    []lifecycle.Event
	lifecycleStarts []lifecycle.Event
}

// bucketSidecarEvents walks sidecarEvents once and partitions those for the
// primary session into the four streams the replay needs.
func bucketSidecarEvents(sidecarEvents []lifecycle.Event, primarySessionID string) sidecarBuckets {
	var b sidecarBuckets
	for _, ev := range sidecarEvents {
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
}

// newSidecarReplayer allocates the scratch transcript mirror, opens the
// tailer, seeds the report summary from the fswatcher window, and emits the
// initial-state transition. The returned cleanup closes the scratch files.
func newSidecarReplayer(transcriptPath string, srcBytes []byte, cfg reportSettings, fswatches []lifecycle.Event) (*sidecarReplayer, func(), error) {
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

	r := &sidecarReplayer{
		srcBytes:         srcBytes,
		tmp:              tmp,
		tailer:           t,
		report:           report,
		state:            session.StateReady,
		prevTransitionAt: fswatches[0].Timestamp,
		stateDurations:   map[string]time.Duration{},
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

	if r.state == session.StateReady && domainMetrics.LastEventType != "" {
		r.emit(transitionFromMetrics(eventIdx, virtTime, cause,
			r.state, session.StateWorking, "force ready→working on first activity", domainMetrics))
		r.state = session.StateWorking
	}

	newState, reason := services.ClassifyState(r.state, domainMetrics)
	if services.ShouldSynthesizeCollapsedWaiting(r.state, newState, domainMetrics) {
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
	return nil
}

// applyHookEvent processes a hook_received event. Permission-request hooks
// (e.g. PreToolUse) pause the agent, producing a working→waiting transition.
// The transcript doesn't change at hook time — only the state machine does.
func (r *sidecarReplayer) applyHookEvent(hookEv lifecycle.Event) {
	if r.state != session.StateWorking {
		return
	}
	r.emit(transition{
		EventIndex:  -1,
		VirtualTime: hookEv.Timestamp,
		Cause:       causeHook,
		PrevState:   r.state,
		NewState:    session.StateWaiting,
		Reason:      fmt.Sprintf("hook: %s (permission pending)", hookEv.HookName),
	})
	r.state = session.StateWaiting
}

// Timeline kinds for the merged event stream in runDebouncedTimeline.
const (
	timelineFS = iota
	timelineHook
	timelineProcessExit
	timelineLifecycleStart
)

// timelineEntry is one row in the merged, seq-ordered replay stream.
type timelineEntry struct {
	kind int
	idx  int
	seq  int64
}

// buildTimeline interleaves the four sidecar streams and returns them sorted
// by sidecar sequence number so the replay walks events in recorded order.
func buildTimeline(b sidecarBuckets) []timelineEntry {
	timeline := make([]timelineEntry, 0, len(b.fswatches)+len(b.hookEvents)+len(b.processExits)+len(b.lifecycleStarts))
	for i, ev := range b.fswatches {
		timeline = append(timeline, timelineEntry{kind: timelineFS, idx: i, seq: ev.Seq})
	}
	for i, ev := range b.hookEvents {
		timeline = append(timeline, timelineEntry{kind: timelineHook, idx: i, seq: ev.Seq})
	}
	for i, ev := range b.processExits {
		timeline = append(timeline, timelineEntry{kind: timelineProcessExit, idx: i, seq: ev.Seq})
	}
	for i, ev := range b.lifecycleStarts {
		timeline = append(timeline, timelineEntry{kind: timelineLifecycleStart, idx: i, seq: ev.Seq})
	}
	sort.SliceStable(timeline, func(i, j int) bool {
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
		if handled := r.applyTimelineControlEntry(entry, b, &d); handled {
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

// applyTimelineControlEntry handles lifecycle-start, process-exit, and hook
// entries, which control the debounce state or short-circuit the fs walker.
// Returns true when the entry was control-flow and the caller should skip
// the fs processing branch.
func (r *sidecarReplayer) applyTimelineControlEntry(entry timelineEntry, b sidecarBuckets, d *debounceState) bool {
	switch entry.kind {
	case timelineLifecycleStart:
		d.alive = true
		return true
	case timelineProcessExit:
		// Daemon torn down: pending debounce timer is cancelled (not fired),
		// and the next lifetime starts a fresh session in ready. Reset state
		// so lifetime-2 events don't coalesce with lifetime-1 debounce.
		*d = debounceState{debounceDelay: d.debounceDelay}
		r.state = session.StateReady
		return true
	case timelineHook:
		if d.alive {
			r.applyHookEvent(b.hookEvents[entry.idx])
		}
		return true
	}
	return false
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
