// SessionDetector orchestrates AgentWatchers + ProcessWatcher to detect
// and manage agent sessions from transcript file activity.
//
// It subscribes to one or more AgentWatcher event streams and delegates to
// three focused collaborators:
//   - StateClassifier: pure functions for state transition logic
//   - metadataEnricher: git metadata resolution and metrics computation
//   - PIDManager: process lifecycle (discovery, exit, liveness sweeps)
package services

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/lifecycle"
	"irrlicht/core/domain/session"
	"irrlicht/core/ports/inbound"
	"irrlicht/core/ports/outbound"
)

// orphanTranscriptAge is the maximum age of a transcript file for it to be
// considered active. Files older than this during initial scan are treated as
// orphans left by exited processes and skipped.
const orphanTranscriptAge = 2 * time.Minute

// activityDebounceWindow is the debounce window for transcript activity
// events. The first event fires immediately; subsequent events within this
// window are coalesced into a single processing when the timer expires.
const activityDebounceWindow = 2 * time.Second

// staleWorkingRefreshInterval is how often the event loop checks for working
// sessions that haven't received a transcript activity event recently. When
// all file-system watcher events for a session are dropped (e.g. subscriber
// channel overflow during concurrent bursts), the tailer's lastOffset falls
// behind and the classifier never sees the pending tool call. Re-reading the
// transcript on this interval catches the missed events.
const staleWorkingRefreshInterval = 5 * time.Second

// SubagentQuietWindow is how long a subagent's transcript must have been
// silent before finishOrphanedChildren will promote it to ready.
//
// The window has to survive the worst-case normal gap between transcript
// writes for an actively-running subagent. Background Task agents routinely
// sit with no writes for 5-15 seconds while waiting on API responses —
// session b27fdaef-6de4-403a-b277-790fe8d803bb showed a 9-second gap that
// falsely tripped a 2-second window and re-created the child session on
// the very next write. 30 seconds comfortably covers normal API latency
// while still being 4× faster than the 2-minute stale-transcript sweep,
// which is the fallback cleanup path for anything this function misses.
const SubagentQuietWindow = 30 * time.Second

// debounceEntry holds debounce state for a single session.
type debounceEntry struct {
	timer   *time.Timer
	latest  agent.Event
	pending bool // true when timer is running with a coalesced event
}

// SessionDetector watches transcript files to detect sessions and orchestrate
// lifecycle management. It is a thin coordinator that delegates state
// classification, metadata enrichment, and PID management to focused
// collaborators.
type SessionDetector struct {
	watchers    []inbound.AgentWatcher
	repo        outbound.SessionRepository
	log         outbound.Logger
	broadcaster outbound.PushBroadcaster // optional
	version     string                   // daemon version stamped on new sessions

	enricher       *metadataEnricher
	pidMgr         *PIDManager
	costTracker    outbound.CostTracker    // optional; nil = disabled
	historyTracker outbound.HistoryTracker // optional; nil = disabled

	// projectSessions tracks sessionID → projectDir for pre-session cleanup.
	mu              sync.Mutex
	projectSessions map[string]string // sessionID → projectDir

	// deletedSessions tracks session IDs that were explicitly deleted (process
	// exit, /clear cleanup) with their deletion timestamp. Prevents late-
	// arriving transcript activity from re-creating a session that was
	// intentionally removed. The timestamp enables --continue detection:
	// activity arriving well after deletion (>10s) indicates a genuine
	// --continue, not ghost events from a dying process.
	deletedSessions map[string]int64

	// debounce coalesces rapid transcript activity events per session.
	debounceMu sync.Mutex
	debounce   map[string]*debounceEntry

	// debouncedEvents receives coalesced events from debounce timer callbacks.
	// Timer callbacks send here instead of calling processActivity directly,
	// so all processActivity calls run in the single event-loop goroutine
	// and never overlap for the same session.
	debouncedEvents chan agent.Event

	// deletedCooldown is the minimum time after deletion before a session
	// can be re-created from transcript activity (e.g. --continue). Prevents
	// ghost sessions from late-arriving writes of a dying process.
	deletedCooldown time.Duration

	// recorder captures lifecycle events for offline replay (optional).
	recorder    outbound.EventRecorder
	recorderSeq int64

	// permMu guards permissionPending. The map tracks sessions with an active
	// PermissionRequest hook that hasn't been cleared by PostToolUse/
	// PostToolUseFailure. Written by HandlePermissionHook (HTTP handler
	// goroutine), read by processActivity (event-loop goroutine).
	permMu            sync.Mutex
	permissionPending map[string]bool // sessionID → true
}

// NewSessionDetector creates a SessionDetector with all required dependencies.
// pw and broadcaster may be nil (optional).
func NewSessionDetector(
	watchers []inbound.AgentWatcher,
	pw outbound.ProcessWatcher,
	repo outbound.SessionRepository,
	log outbound.Logger,
	git outbound.GitResolver,
	metrics outbound.MetricsCollector,
	broadcaster outbound.PushBroadcaster,
	version string,
	readyTTL time.Duration,
	pidDiscovers map[string]agent.PIDDiscoverFunc,
) *SessionDetector {
	det := &SessionDetector{
		watchers:          watchers,
		repo:              repo,
		log:               log,
		broadcaster:       broadcaster,
		version:           version,
		enricher:          newMetadataEnricher(git, metrics),
		projectSessions:   make(map[string]string),
		deletedSessions:   make(map[string]int64),
		debounce:          make(map[string]*debounceEntry),
		debouncedEvents:   make(chan agent.Event, 64),
		deletedCooldown:   10 * time.Second,
		permissionPending: make(map[string]bool),
	}
	det.pidMgr = NewPIDManager(
		pw, repo, log, broadcaster, readyTTL,
		pidDiscovers, det.removeFromProjectSessions,
	)
	det.pidMgr.SetChildDeletedHandler(det.reevaluateParent)
	return det
}

// SetDeletedCooldown overrides the deleted-session cooldown.
// Intended for tests that need immediate re-creation.
func (d *SessionDetector) SetDeletedCooldown(dur time.Duration) {
	d.deletedCooldown = dur
}

// RunPIDLivenessSweepForTest runs one iteration of the liveness sweep
// synchronously. Intended for tests that need to exercise the sweep's
// child-cleanup path without waiting for the real 5-second ticker.
func (d *SessionDetector) RunPIDLivenessSweepForTest() {
	d.pidMgr.CheckPIDLiveness()
}

// SetRecorder enables lifecycle event recording. When set, the detector and
// its PIDManager will emit lifecycle events to the recorder for offline replay.
func (d *SessionDetector) SetRecorder(r outbound.EventRecorder) {
	d.recorder = r
	d.pidMgr.SetRecorder(r, &d.recorderSeq)
}

// SetCostTracker wires an optional CostTracker; after each successful
// repo.Save the detector records a snapshot for downstream cost-window
// queries. Pass nil to disable.
func (d *SessionDetector) SetCostTracker(c outbound.CostTracker) {
	d.costTracker = c
}

// SetHistoryTracker wires an optional HistoryTracker that records per-session
// state-transition timelines in memory. Pass nil to disable.
func (d *SessionDetector) SetHistoryTracker(h outbound.HistoryTracker) {
	d.historyTracker = h
}

// SetLauncherEnvReader installs a reader that captures terminal/IDE identity
// from a session's PID when the PID is first assigned.
func (d *SessionDetector) SetLauncherEnvReader(fn LauncherEnvReader) {
	d.pidMgr.SetLauncherEnvReader(fn)
}

// recordCost is a helper that calls the optional CostTracker and logs but
// does not propagate errors — cost tracking must never block the detector.
func (d *SessionDetector) recordCost(state *session.SessionState) {
	if d.costTracker == nil || state == nil {
		return
	}
	if err := d.costTracker.RecordSnapshot(state); err != nil {
		d.log.LogError("cost-tracker", state.SessionID, err.Error())
	}
}

// record emits a lifecycle event if recording is enabled. It assigns a
// monotonic sequence number and fills in the timestamp if missing.
func (d *SessionDetector) record(ev lifecycle.Event) {
	if ev.Kind == lifecycle.KindStateTransition && ev.NewState != "" && d.historyTracker != nil {
		ts := ev.Timestamp
		if ts.IsZero() {
			ts = time.Now()
		}
		d.historyTracker.OnTransition(ev.SessionID, ev.NewState, ts)
	}
	if d.recorder == nil {
		return
	}
	ev.Seq = atomic.AddInt64(&d.recorderSeq, 1)
	if ev.Timestamp.IsZero() {
		ev.Timestamp = time.Now()
	}
	d.recorder.Record(ev)
}

// Run subscribes to all AgentWatcher event streams, fans them into a single
// channel, and processes events until ctx is cancelled. It blocks for the
// lifetime of the detector.
func (d *SessionDetector) Run(ctx context.Context) error {
	merged := make(chan agent.Event, 16)
	var wg sync.WaitGroup

	for _, w := range d.watchers {
		ch := w.Subscribe()
		wg.Add(1)
		go func(watcher inbound.AgentWatcher, ch <-chan agent.Event) {
			defer wg.Done()
			defer watcher.Unsubscribe(ch)
			for {
				select {
				case <-ctx.Done():
					return
				case ev, ok := <-ch:
					if !ok {
						return
					}
					select {
					case merged <- ev:
					case <-ctx.Done():
						return
					}
				}
			}
		}(w, ch)
	}

	// Close merged when all watcher goroutines exit.
	go func() {
		wg.Wait()
		close(merged)
	}()

	// Seed project sessions map from existing sessions on disk.
	d.seedFromDisk()

	// Periodic liveness sweep: detect dead PIDs that kqueue missed.
	go d.pidMgr.SweepDeadPIDs(ctx)

	d.log.LogInfo("session-detector", "", "started — listening for transcript events")

	// Periodic refresh catches missed fswatcher events. When the subscriber
	// channel overflows during concurrent bursts (multiple sessions + subagent
	// transcripts on the same watcher), events are silently dropped and the
	// tailer never sees the pending tool call. Re-reading the transcript on a
	// short interval recovers within seconds instead of stalling until the
	// next user action.
	refreshTicker := time.NewTicker(staleWorkingRefreshInterval)
	defer refreshTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ev, ok := <-merged:
			if !ok {
				// Watcher goroutines exited (usually because ctx was cancelled).
				// Return the context error if set, nil otherwise.
				return ctx.Err()
			}
			d.handleTranscriptEvent(ev)
		case ev := <-d.debouncedEvents:
			// Coalesced events from debounce timers — process in the event
			// loop goroutine so processActivity never runs concurrently.
			d.processActivity(ev)
		case <-refreshTicker.C:
			d.refreshStaleSessions()
		}
	}
}

// handleTranscriptEvent dispatches a transcript event to the appropriate handler.
