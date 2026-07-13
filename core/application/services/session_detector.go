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
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/backchannel"
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

// Logger component tags shared by SessionDetector's collaborators, split
// across this file, session_detector_activity.go, session_detector_lifecycle.go,
// session_detector_subagent.go, and pid_manager.go.
const (
	// logComponentSessionDetector tags every log line the detector's steady
	// -state event handling emits.
	logComponentSessionDetector = "session-detector"
	// logComponentSessionDetectorSeed tags log lines emitted during the
	// initial-scan seeding pass, distinct from steady-state handling.
	logComponentSessionDetectorSeed = "session-detector-seed"
)

// compactHoldTimeout bounds the PreCompact force-working hold (#657). Normally
// the hold clears when the manual compact_boundary lands, but an interrupted or
// failed /compact may never write one — without a ceiling the session would be
// re-held working on every refreshStaleSessions tick and stranded forever (the
// very failure #656 fixed). A real manual compaction runs at most a few minutes
// (the #656 live evidence was ~161s), so this timeout sits comfortably beyond
// any genuine window: after it elapses an orphaned hold is dropped and the
// session re-classifies normally.
const compactHoldTimeout = 5 * time.Minute

// SubagentQuietWindow is how long a subagent's transcript must have been
// silent before finishOrphanedChildren will promote it to ready.
//
// The window has to survive the worst-case normal gap between transcript
// writes for an actively-running subagent. Background Task agents routinely
// sit with no writes for 5-15 seconds while waiting on API responses —
// session b27fdaef-6de4-403a-b277-790fe8d803bb showed a 9-second gap that
// falsely tripped a 2-second window (bumped to 30s to fix). A background
// research subagent making several WebSearch/WebFetch calls sits on a
// coarser latency budget than a single API round-trip: session
// d491a5f9-fc21-4fd1-a1df-f9dfcdc91fec (issue #881) showed a genuine
// 61-second gap between transcript writes mid-run, which the 30-second
// window falsely tripped — the child was promoted and deleted, and the
// parent surfaced "ready" for 67 seconds before the subagent's real
// completion landed. 90 seconds keeps comfortable headroom over that
// observed worst case while staying well short of the 2-minute
// stale-transcript sweep, which is the fallback cleanup path for anything
// this function misses.
const SubagentQuietWindow = 90 * time.Second

// debounceEntry holds debounce state for a single session.
type debounceEntry struct {
	timer   *time.Timer
	latest  agent.Event
	pending bool // true when timer is running with a coalesced event
}

// identifiedEvent is the merge-channel element produced by Run(): each
// per-watcher drain goroutine wraps its inbound agent.Event with its
// watcher's Identity (captured once via inbound.Watcher.Identity()) so
// the dispatcher can tag downstream lifecycle records without bouncing
// the redundant adapter string through every agent.Event payload.
type identifiedEvent struct {
	Identity agent.Identity
	Event    agent.Event
}

// SessionDetector watches transcript files to detect sessions and orchestrate
// lifecycle management. It is a thin coordinator that delegates state
// classification, metadata enrichment, and PID management to focused
// collaborators.
type SessionDetector struct {
	watchers    []inbound.Watcher
	repo        outbound.SessionRepository
	log         outbound.Logger
	broadcaster outbound.PushBroadcaster // optional
	version     string                   // daemon version stamped on new sessions

	// merged is the fan-in channel every per-watcher drain goroutine sends
	// into and Run consumes. Created at construction (not in Run) so
	// watchers can be registered via AddWatcher before or after Run starts
	// — the consent wizard grants/revokes monitoring at any time (#570).
	// Never closed; Run exits on ctx cancellation instead.
	merged chan identifiedEvent

	enricher       *metadataEnricher
	pidMgr         *PIDManager
	costTracker    outbound.CostTracker    // optional; nil = disabled
	historyTracker outbound.HistoryTracker // optional; nil = disabled
	cacheBloat     *CacheBloatDetector     // optional; nil = disabled (#374)
	metrics        outbound.MetricsCollector

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

	// hostGateRejected tracks session IDs the host-ancestry admission gate
	// (issue #784) has already rejected. No cooldown/expiry, unlike
	// deletedSessions — a rejected PID's process ancestry (e.g. CodexBar,
	// not a terminal) won't change for the life of that process, so there's
	// no legitimate retry case to allow. Exists specifically to close a gap
	// where the debounce-coalesce path re-enters onNewSession with an empty
	// Identity, which would otherwise bypass the gate on a same-window retry
	// (see the PID-manager AllowsSession call site in onNewSession).
	hostGateRejected map[string]struct{}

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

	// compactPending tracks sessions in a manual /compact: sessionID → the Unix
	// time the PreCompact hook fired. Set by HandleCompactHook; cleared when the
	// compact_boundary lands (SawManualCompactBoundary) or compactHoldTimeout
	// elapses (the safety net for an interrupted compaction that never writes a
	// boundary). While set, processActivity overlays CompactInProgress so
	// ClassifyState holds the session in working through the silent compaction
	// window (#657). Guarded by permMu — same goroutine-crossing story as
	// permissionPending.
	compactPending map[string]int64 // sessionID → unix seconds (hook fire time)

	// editToolOpenSince tracks, per session, the Unix time a permission-gated
	// file-edit tool first appeared open. Guarded by permMu. Drives the
	// OpenToolStalled transcript fallback (#488): an edit tool open past
	// staleWorkingRefreshInterval means the agent is blocked on a permission
	// prompt, not executing. Cleared when the tool closes or the session is
	// removed.
	editToolOpenSince map[string]int64 // sessionID → unix seconds

	// bgLiveProbe answers "does this session still have a live background
	// process?" from its output-file paths. Defaults to anyLiveOutputWriter
	// (lsof); tests override it. See issue #445.
	bgLiveProbe backgroundProbe

	// bgPIDProbe is the alternate liveness path for adapters that report a
	// backgrounded command's PID rather than an output file (Gemini CLI).
	// Defaults to anyLivePID (kill(pid, 0)); tests override it. See issue #661.
	bgPIDProbe backgroundPIDProbe

	// bgMu guards bgLive / bgProbing. The probe (lsof) runs off the event-loop
	// goroutine so a slow filesystem can't stall every other session's
	// processing; processActivity reads the last-known liveness from bgLive
	// (optimistically alive on first sight) and a completed probe nudges the
	// event loop to re-classify. bgProbing is the per-session in-flight guard.
	// See issue #445.
	bgMu      sync.Mutex
	bgLive    map[string]bool
	bgProbing map[string]bool

	// consentGate (optional) reports whether an adapter's transcripts may
	// be read (#570). Gates the two paths that read PERSISTED sessions'
	// transcripts outside the (already consent-gated) watcher pipeline:
	// the startup seed and the stale-working refresh. Nil = allow all —
	// tests and replay tooling that construct detectors directly are not
	// consent-managed.
	consentGate func(adapter string) bool

	// uiSignals carries edge-triggered terminal read-back signals from
	// TerminalObserver's ticker goroutine into the event loop, so the
	// resulting state mutation runs on the single writer (like debouncedEvents)
	// and never races processActivity. Non-blocking sender; a dropped signal is
	// re-sent on the observer's next poll (issue #732).
	uiSignals chan terminalUISignal

	// onSessionSuperseded is called whenever a presession is retired in favor
	// of a reconciled real session, from cleanupPreSessionsForProject's own
	// project/CWD match — the one reconciliation path that deletes its row
	// directly rather than through PIDManager. Installed via
	// SetSessionSupersededHandler, which also forwards to pidMgr's own hook so
	// external code has a single registration point covering every
	// reconciliation path (issue #997).
	onSessionSuperseded func(oldID, newID string)
}

// terminalUISignal is an edge in a session's rendered-terminal UI state,
// produced by TerminalObserver and applied on the event-loop goroutine.
type terminalUISignal struct {
	sessionID string
	ui        backchannel.UIKind
}

// SessionDetectorDeps bundles NewSessionDetector's dependencies beyond the
// watcher list. PW and Broadcaster may be nil (optional).
type SessionDetectorDeps struct {
	PW           outbound.ProcessWatcher
	Repo         outbound.SessionRepository
	Log          outbound.Logger
	Git          outbound.GitResolver
	Metrics      outbound.MetricsCollector
	Broadcaster  outbound.PushBroadcaster
	Version      string
	ReadyTTL     time.Duration
	PIDDiscovers map[string]agent.PIDDiscoverFunc
	ProcessNames map[string]string
	LiveCWDs     LiveCWDsFunc
}

// NewSessionDetector creates a SessionDetector with all required
// dependencies.
//
// Panics if any supplied watcher has a zero-value Identity. Every
// downstream session created from that watcher's events would otherwise
// have an empty Adapter field — a silent partial-failure mode (the
// adapter-aware code paths fall back gracefully, but logs and the
// /api/v1/agents endpoint surface "" instead of the real name).
func NewSessionDetector(watchers []inbound.Watcher, deps SessionDetectorDeps) *SessionDetector {
	for i, w := range watchers {
		if w.Identity() == (agent.Identity{}) {
			panic(fmt.Sprintf("session_detector: watchers[%d] (%T) has no Identity — call .WithIdentity() before passing it to NewSessionDetector", i, w))
		}
	}
	det := &SessionDetector{
		watchers:          watchers,
		merged:            make(chan identifiedEvent, 16),
		repo:              deps.Repo,
		log:               deps.Log,
		broadcaster:       deps.Broadcaster,
		version:           deps.Version,
		enricher:          newMetadataEnricher(deps.Git, deps.Metrics),
		metrics:           deps.Metrics,
		projectSessions:   make(map[string]string),
		deletedSessions:   make(map[string]int64),
		hostGateRejected:  make(map[string]struct{}),
		debounce:          make(map[string]*debounceEntry),
		debouncedEvents:   make(chan agent.Event, 64),
		deletedCooldown:   10 * time.Second,
		permissionPending: make(map[string]bool),
		compactPending:    make(map[string]int64),
		editToolOpenSince: make(map[string]int64),
		bgLiveProbe:       anyLiveOutputWriter,
		bgPIDProbe:        anyLivePID,
		bgLive:            make(map[string]bool),
		bgProbing:         make(map[string]bool),
		uiSignals:         make(chan terminalUISignal, 64),
	}
	det.pidMgr = NewPIDManager(PIDManagerDeps{
		PW:               deps.PW,
		Repo:             deps.Repo,
		Log:              deps.Log,
		Broadcaster:      deps.Broadcaster,
		ReadyTTL:         deps.ReadyTTL,
		PIDDiscovers:     deps.PIDDiscovers,
		ProcessNames:     deps.ProcessNames,
		LiveCWDs:         deps.LiveCWDs,
		OnSessionDeleted: det.removeFromProjectSessions,
	})
	det.pidMgr.SetChildDeletedHandler(det.reevaluateParent)
	return det
}

// SetDeletedCooldown overrides the deleted-session cooldown.
// Intended for tests that need immediate re-creation.
func (d *SessionDetector) SetDeletedCooldown(dur time.Duration) {
	d.deletedCooldown = dur
}

// SetSessionSupersededHandler registers fn to run whenever any presession
// reconciliation path retires a presession in favor of a real session — both
// the PIDManager-owned paths (same-PID match at PID-assignment time, and the
// seed-time/periodic pre-session sweeps) and cleanupPreSessionsForProject's
// own project/CWD match, which deletes its row directly rather than through
// PIDManager. A single call here covers every path (issue #997).
func (d *SessionDetector) SetSessionSupersededHandler(fn func(oldID, newID string)) {
	d.onSessionSuperseded = fn
	d.pidMgr.SetSessionSupersededHandler(fn)
}

// SetBackgroundProbeForTest overrides the background-process liveness probe so
// tests can simulate live / dead background processes without real lsof. See
// issue #445.
func (d *SessionDetector) SetBackgroundProbeForTest(p func(outputPaths []string) bool) {
	d.bgLiveProbe = p
}

// SetBackgroundPIDProbeForTest overrides the PID-liveness probe so tests can
// simulate live / dead background PIDs without real OS processes. See issue
// #661.
func (d *SessionDetector) SetBackgroundPIDProbeForTest(p func(pids []string) bool) {
	d.bgPIDProbe = p
}

// RunPIDLivenessSweepForTest runs one iteration of the liveness sweep
// synchronously. Intended for tests that need to exercise the sweep's
// child-cleanup path without waiting for the real 5-second ticker.
func (d *SessionDetector) RunPIDLivenessSweepForTest() {
	d.pidMgr.CheckPIDLiveness()
}

// RunStaleSessionRefreshForTest runs one iteration of the stale-working
// refresh synchronously. Intended for tests that need to exercise the
// periodic re-classification pass without waiting for the real
// staleWorkingRefreshInterval ticker.
func (d *SessionDetector) RunStaleSessionRefreshForTest() {
	d.refreshStaleSessions()
}

// CleanupZombies runs a one-shot startup sweep that deletes persisted
// sessions whose process is provably gone. Call before the daemon starts
// serving requests so the API never returns stale records inherited from a
// prior daemon run. Returns the number of sessions deleted.
func (d *SessionDetector) CleanupZombies() int {
	return d.pidMgr.CleanupZombies()
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

// SetCacheBloatDetector wires the optional cache-creation regression detector
// (#374). When set, each processActivity pass drives it so it can flag a
// session whose cache-creation per turn regresses against the project baseline.
// Pass nil to disable.
func (d *SessionDetector) SetCacheBloatDetector(c *CacheBloatDetector) {
	d.cacheBloat = c
}

// SetLauncherEnvReader installs a reader that captures terminal/IDE identity
// from a session's PID when the PID is first assigned.
func (d *SessionDetector) SetLauncherEnvReader(fn LauncherEnvReader) {
	d.pidMgr.SetLauncherEnvReader(fn)
}

// SetBackgroundReader installs a reader that flags a session as a detached
// background agent (e.g. a Claude Code Agent View bg agent) when its PID is
// first assigned (#744).
func (d *SessionDetector) SetBackgroundReader(fn BackgroundReader) {
	d.pidMgr.SetBackgroundReader(fn)
}

// SetInfraReaper installs the liveness-sweep seam that reaps a session bound to
// a still-alive PID which is actually the adapter's background infrastructure
// (e.g. Claude Code's --bg-spare helper) rather than the session (#727). Both
// args nil disables the check. Call before Run.
func (d *SessionDetector) SetInfraReaper(excluders map[string]func([]string) bool, readArgv func(pid int) []string) {
	d.pidMgr.SetInfraReaper(excluders, readArgv)
}

// SetHostGate installs the session-admission seam that rejects a candidate PID
// launched by something other than a known terminal or IDE (#784). Both args
// nil disables the check. Call before Run.
func (d *SessionDetector) SetHostGate(requireKnownHost map[string]bool, isKnownHost func(pid int) bool) {
	d.pidMgr.SetHostGate(requireKnownHost, isKnownHost)
}

// SetConsentGate installs the per-adapter observe-consent check (#570).
// Call before Run. Production wires PermissionService.ObserveGranted; nil
// (the default) allows everything.
func (d *SessionDetector) SetConsentGate(fn func(adapter string) bool) {
	d.consentGate = fn
}

// observeAllowed reports whether the adapter's transcripts may be read.
func (d *SessionDetector) observeAllowed(adapter string) bool {
	return d.consentGate == nil || d.consentGate(adapter)
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

// classifierInputs snapshots the transient SessionMetrics signals that drive
// ClassifyState into a lifecycle.ClassifierInputs for attaching to recorded
// state-transition events (issue #757). Returns nil when metrics is nil so the
// event's omitempty Inputs field stays absent.
func classifierInputs(m *session.SessionMetrics) *lifecycle.ClassifierInputs {
	if m == nil {
		return nil
	}
	return &lifecycle.ClassifierInputs{
		HasLiveBackgroundProcess:          m.HasLiveBackgroundProcess,
		PermissionPending:                 m.PermissionPending,
		CompactInProgress:                 m.CompactInProgress,
		OpenToolStalled:                   m.OpenToolStalled,
		SawUserBlockingToolClosedThisPass: m.SawUserBlockingToolClosedThisPass,
		SawManualCompactBoundary:          m.SawManualCompactBoundary,
		NoSubstantiveActivity:             m.NoSubstantiveActivity,
		HasOpenToolCall:                   m.HasOpenToolCall,
		LastOpenToolNames:                 m.LastOpenToolNames,
		LastEventType:                     m.LastEventType,
		LastWasUserInterrupt:              m.LastWasUserInterrupt,
		LastWasToolDenial:                 m.LastWasToolDenial,
	}
}

// AddWatcher registers a watcher with the running (or not-yet-running)
// detector: a drain goroutine subscribes to the watcher's events and fans
// them into the merged channel until ctx is cancelled. The caller owns the
// watcher's Watch lifecycle and shares the same ctx, so cancelling it stops
// both the watcher and its drain — this is how the permission service
// starts/stops per-agent monitoring on grant/revoke (#570).
//
// Panics on a zero-value Identity, matching the NewSessionDetector contract.
func (d *SessionDetector) AddWatcher(ctx context.Context, w inbound.Watcher) {
	if w.Identity() == (agent.Identity{}) {
		panic(fmt.Sprintf("session_detector: AddWatcher(%T) has no Identity — call .WithIdentity() first", w))
	}
	go d.drainWatcher(ctx, w)
}

// drainWatcher subscribes to one watcher and forwards its events (tagged
// with the watcher's Identity) into the merged channel until ctx is
// cancelled or the watcher closes the subscription.
func (d *SessionDetector) drainWatcher(ctx context.Context, w inbound.Watcher) {
	ch := w.Subscribe()
	defer w.Unsubscribe(ch)
	id := w.Identity()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			select {
			case d.merged <- identifiedEvent{Identity: id, Event: ev}:
			case <-ctx.Done():
				return
			}
		}
	}
}

// Run subscribes to all Watcher event streams, fans them into the merged
// channel, and processes events until ctx is cancelled. It blocks for the
// lifetime of the detector.
//
// Each per-watcher drain goroutine captures the watcher's Identity once
// and tags every event with it as the event flows into the merged
// channel; this is how the adapter name reaches handleTranscriptEvent
// for lifecycle recording and SessionState bootstrap.
func (d *SessionDetector) Run(ctx context.Context) error {
	for _, w := range d.watchers {
		go d.drainWatcher(ctx, w)
	}

	// Seed project sessions map from existing sessions on disk.
	d.seedFromDisk()

	// Periodic liveness sweep: detect dead PIDs that kqueue missed.
	go d.pidMgr.SweepDeadPIDs(ctx)

	d.log.LogInfo(logComponentSessionDetector, "", "started — listening for transcript events")

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
		case idEv := <-d.merged:
			d.handleTranscriptEvent(idEv.Identity, idEv.Event)
		case ev := <-d.debouncedEvents:
			// Coalesced events from debounce timers — process in the event
			// loop goroutine so processActivity never runs concurrently.
			d.processActivityWithoutIdentity(ev)
		case sig := <-d.uiSignals:
			// Terminal read-back edges (issue #732) — applied here so the
			// state mutation shares the single writer with processActivity.
			d.handleTerminalUISignal(sig)
		case <-refreshTicker.C:
			d.refreshStaleSessions()
		}
	}
}

// Terminal read-back reasons stamped on the state transitions a UI signal
// drives (issue #732). The transition history surfaces these verbatim.
const (
	TerminalUIDetectedReason = "trust dialog detected (terminal read-back)"
	TerminalUIClearedReason  = "trust dialog cleared (terminal read-back)"
)

// EnqueueTerminalUISignal hands an edge-triggered terminal read-back signal to
// the event loop. Non-blocking: if the buffer is full the signal is dropped and
// re-sent on the observer's next poll, so a momentary backlog never blocks the
// observer's ticker. Implements TerminalObserver's sink.
func (d *SessionDetector) EnqueueTerminalUISignal(sessionID string, ui backchannel.UIKind) {
	select {
	case d.uiSignals <- terminalUISignal{sessionID: sessionID, ui: ui}:
	default:
	}
}

// terminalUITransition computes the state/reason/uiReason for a terminal UI
// edge. ok is false when the edge is a no-op the caller should skip without
// recording: the rising edge finding the session already waiting (e.g. the
// claudecode hook beat us to it), the clearing edge finding a waiting state
// we're not responsible for, or the clearing edge's re-classification
// independently landing back on waiting.
func terminalUITransition(state *session.SessionState, ui backchannel.UIKind) (newState, reason, uiReason string, ok bool) {
	if ui == backchannel.UIKindTrustDialog {
		// Rising edge. Already waiting means nothing to do — no double-count.
		if state.State == session.StateWaiting {
			return "", "", "", false
		}
		return session.StateWaiting, TerminalUIDetectedReason, TerminalUIDetectedReason, true
	}

	// Clearing edge. Only undo a waiting we are responsible for.
	if state.State != session.StateWaiting {
		return "", "", "", false
	}
	// Re-classify from a WORKING base, not from the current waiting state:
	// ClassifyState is a no-op when called with currentState == waiting and
	// nil/ambiguous metrics, which would strand the session in waiting forever
	// once the dialog we forced is gone. From a working base it re-derives
	// ready/working from the metrics, while a genuine transcript reason to
	// keep waiting (an open user-blocking tool, a question cue) still routes
	// back to waiting — in which case newState == waiting and the caller
	// leaves it untouched.
	newState, reason = ClassifyState(session.StateWorking, state.Metrics)
	if newState == state.State {
		return "", "", "", false // transcript independently keeps it waiting — leave it
	}
	if reason == "" {
		reason = TerminalUIClearedReason
	}
	return newState, reason, TerminalUIClearedReason, true
}

// handleTerminalUISignal folds a rendered-terminal UI edge into the session
// lifecycle. It runs on the event-loop goroutine, but the load-modify-save runs
// under WithSessionStateLock — the same lock processActivity and the async
// PID-discovery path (assignPIDLocked) take — so a concurrent PID assignment
// can't clobber the transition (or vice versa). A trust dialog on screen forces
// waiting (a state the transcript never records, and that needs no hook); when
// it clears, the session re-classifies — the transcript/process observers
// remain authoritative for everything else.
func (d *SessionDetector) handleTerminalUISignal(sig terminalUISignal) {
	d.pidMgr.WithSessionStateLock(func() {
		state, err := d.repo.Load(sig.sessionID)
		if err != nil {
			return // session gone since the signal was queued
		}

		newState, reason, uiReason, ok := terminalUITransition(state, sig.ui)
		if !ok {
			return
		}

		// Record only once we are actually acting, so a no-op edge never inflates
		// the lifecycle log (the rising edge returns above without recording too).
		d.record(lifecycle.Event{
			Kind: lifecycle.KindUIDetected, SessionID: sig.sessionID,
			Adapter: state.Adapter, UIKind: string(sig.ui), Reason: uiReason,
		})

		now := time.Now().Unix()
		d.record(lifecycle.Event{
			Kind: lifecycle.KindStateTransition, SessionID: sig.sessionID,
			PrevState: state.State, NewState: newState, Reason: reason,
		})
		state.State = newState
		state.UpdatedAt = now
		switch newState {
		case session.StateWaiting:
			state.WaitingStartTime = &now
		case session.StateWorking:
			state.WaitingStartTime = nil
		}
		if err := d.repo.Save(state); err != nil {
			d.log.LogError(logComponentSessionDetector, sig.sessionID,
				fmt.Sprintf("failed to save terminal-UI update: %v", err))
			return
		}
		d.broadcast(outbound.PushTypeUpdated, state)
	})
}

// handleTranscriptEvent dispatches a transcript event to the appropriate handler.
