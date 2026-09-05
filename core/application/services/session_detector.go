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

// maxIdleProjectResolveAttempts bounds how many refreshStaleSessions ticks
// retry CWD/project resolution for an idle (waiting/ready) session whose
// ProjectName never resolved — e.g. mistral-vibe's meta.json sidecar hadn't
// been written yet at the moment the transcript went quiet (#1021). Without
// a cap, a session in a non-git cwd, or on an adapter with no sidecar at
// all, would be re-read on this ticker forever.
const maxIdleProjectResolveAttempts = 12

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

	// logComponentProcessExit tags the process-exit path's own log lines.
	// Named rather than repeated inline so the two call sites in
	// pid_manager.go cannot drift apart on a typo and split the stream in
	// two silently.
	logComponentProcessExit = "process-exit"
)

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
	costTracker    outbound.CostTracker       // optional; nil = disabled
	autonomySpans  outbound.AutonomySpanStore // optional; nil = disabled (#1905)
	historyTracker outbound.HistoryTracker    // optional; nil = disabled
	cacheBloat     *CacheBloatDetector        // optional; nil = disabled (#374)
	hookLiveness   *HookLivenessWatchdog      // optional; nil = disabled (#1368)
	metrics        outbound.MetricsCollector

	// projectSessions tracks sessionID → projectDir for pre-session cleanup.
	mu              sync.Mutex
	projectSessions map[string]string // sessionID → projectDir

	// autonomyRecovered holds the open autonomy runs a PREVIOUS daemon left in
	// the span store's journal, keyed by session id, waiting to be adopted by
	// the session they belong to as it is rediscovered (#1905 recording).
	//
	// An entry is removed when it is adopted, and settled — closed where the
	// previous daemon last saw it alive — when autonomyRecoveryDeadline passes
	// without adoption. That deadline is what stops recovery from becoming
	// resurrection: a session that finished or went away while the daemon was
	// down is never rediscovered, so its run closes at the last instant anyone
	// measured rather than being credited with the downtime.
	//
	// Guarded by mu, like projectSessions beside it: written on the event-loop
	// goroutine and read by the refresh ticker's.
	autonomyRecovered        map[string]outbound.AutonomySpan
	autonomyRecoveryDeadline int64

	// autonomyOpenSyncAt and autonomyOpenSyncKey throttle the open-run journal
	// rewrite on the refresh ticker: it happens when the set of open runs
	// CHANGED, or once every autonomyOpenSyncInterval to move their last-seen
	// instant forward. Also under mu.
	autonomyOpenSyncAt  int64
	autonomyOpenSyncKey string

	// deletedSessions tracks session IDs that were explicitly deleted (process
	// exit, /clear cleanup) with their deletion timestamp. Prevents late-
	// arriving transcript activity from re-creating a session that was
	// intentionally removed. The timestamp enables --continue detection:
	// activity arriving well after deletion (>10s) indicates a genuine
	// --continue, not ghost events from a dying process.
	deletedSessions map[string]int64

	// deletedStates caches the last known *session.SessionState for a session
	// removed via PIDManager.deleteSession (process exit, the ready-TTL/
	// liveness sweep), keyed the same as deletedSessions and cleared at the
	// same points. It exists so an authoritative turn-done hook (opencode's
	// session.idle, hermes' on_session_end — anything that sets
	// agent.Event.Terminal) that loses the race with that exact teardown
	// still has a session to classify against instead of a repo row that is
	// simply gone (issue #1772). Not populated for /clear or the dedup/
	// supersession reconciliation paths, which delete the repo row directly
	// without going through deleteSession — reviving those would be exactly
	// the ghost-session class deletedCooldown exists to prevent. See
	// cacheDeletedSnapshot and processActivity's Terminal branch.
	deletedStates map[string]*session.SessionState

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

	// signals holds the out-of-band state signals (hook-delivered today,
	// OTel/process/screen in the later phases of #1129) between arrival on
	// their own goroutine and the classify pass that consumes them. It
	// replaces the three hand-rolled per-signal overlay maps this struct
	// carried before #1288 — permissionPending, hookTurnDone and
	// idlePromptPending — plus compactPending, which #1297 folded in. Each had
	// its own lifecycle rule spelled out in its own overlay method. The
	// per-signal policy now lives in session.signalPolicies.
	//
	// editToolOpenSince, the last holdout — its rule fires only *after* a
	// threshold, which no policy row could express — folded in too once #1319
	// gave signalPolicy an arming predicate. No hand-rolled per-signal overlay
	// map is left on this struct.
	//
	// Carries its own lock (hooks arrive on HTTP handler goroutines, the
	// event loop classifies), so it is deliberately NOT guarded by idleMu.
	signals *session.SignalHolds

	// dwell is the grace timer state changes serve before they are published
	// (#1366). Like signals it carries its own lock and takes the pass clock
	// as an argument, so it is deliberately NOT guarded by mu. A nil value
	// disables hysteresis; see session.StateDwell.
	dwell *session.StateDwell

	// now is this detector's clock, read once per classify pass and threaded
	// into everything that reasons about elapsed time — the signal holds'
	// ceilings and ripeness rules (holdContext.Now) and the #1366 dwell. nil
	// means time.Now; tests replace it to drive both mechanisms on a virtual
	// timeline instead of sleeping. Read through nowFn, never directly.
	now func() time.Time

	// idleMu guards idleProjectRetryAttempts below. Written from HTTP handler
	// goroutines, read by processActivity (event-loop goroutine).
	idleMu sync.Mutex

	// idleProjectRetryAttempts tracks, per session, how many
	// refreshStaleSessions ticks have retried CWD/project resolution while
	// idle with an unresolved ProjectName. Guarded by idleMu. Capped at
	// maxIdleProjectResolveAttempts (#1021); cleared when the session is
	// removed (onRemoved).
	idleProjectRetryAttempts map[string]int // sessionID → attempts so far

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
	// bgInconclusive counts consecutive inconclusive probe verdicts per
	// session, so holding the previous verdict through a slow lsof stays
	// bounded (issue #1299). Reset by any conclusive verdict.
	bgInconclusive map[string]int

	// consentGate (optional) reports whether an adapter's transcripts may
	// be read (#570). Gates the two paths that read PERSISTED sessions'
	// transcripts outside the (already consent-gated) watcher pipeline:
	// the startup seed and the stale-working refresh. Nil = allow all —
	// tests and replay tooling that construct detectors directly are not
	// consent-managed.
	consentGate func(adapter string) bool
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

// newSessionDetector is the one function in this package allowed to write a
// SessionDetector composite literal, and it owns every field whose zero value
// is unusable. Callers assign dependencies onto the result.
//
// Three kinds of field live here, and only the first is what "nil maps" would
// suggest (#1450, the sibling of #1400):
//
//   - The eight maps. A write to a nil map panics — and it panics in the
//     writer, several frames from the literal that caused it: the reproduced
//     stack for a bare literal bottoms out at removeFromProjectSessions, which
//     tells you nothing about where the detector was built.
//   - The two channels. A nil channel never panics, which is worse. The
//     sender that carries a `default` arm (debouncedEvents) drops every event
//     silently and forever, and Run's receive on a nil `merged` parks for the
//     life of the process, so the detector processes nothing and reports no
//     error.
//   - Five fields that are neither. deletedCooldown at zero disables the
//     ghost-session guard it exists to be; a nil signals is dereferenced
//     without a guard (session.SignalHolds has no nil-receiver arms); dwell,
//     bgLiveProbe and bgPIDProbe are nil-guarded but their nil silently
//     disables state hysteresis and background-process liveness. A reflection
//     walk over map/chan kinds cannot see any of these, which is exactly why
//     the allocator, and not a test, is what owns them.
func newSessionDetector() *SessionDetector {
	return &SessionDetector{
		merged:                   make(chan identifiedEvent, 16),
		projectSessions:          make(map[string]string),
		deletedSessions:          make(map[string]int64),
		deletedStates:            make(map[string]*session.SessionState),
		hostGateRejected:         make(map[string]struct{}),
		debounce:                 make(map[string]*debounceEntry),
		debouncedEvents:          make(chan agent.Event, 64),
		deletedCooldown:          10 * time.Second,
		signals:                  session.NewSignalHolds(),
		dwell:                    session.NewStateDwell(),
		idleProjectRetryAttempts: make(map[string]int),
		autonomyRecovered:        make(map[string]outbound.AutonomySpan),
		bgLiveProbe:              anyLiveOutputWriter,
		bgPIDProbe:               anyLivePID,
		bgLive:                   make(map[string]bool),
		bgProbing:                make(map[string]bool),
		bgInconclusive:           make(map[string]int),
	}
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
	det := newSessionDetector()
	det.watchers = watchers
	det.repo = deps.Repo
	det.log = deps.Log
	det.broadcaster = deps.Broadcaster
	det.version = deps.Version
	det.enricher = newMetadataEnricher(deps.Git, deps.Metrics)
	det.metrics = deps.Metrics
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
		OnSessionRemoved: det.cacheDeletedSnapshot,
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
// PIDManager. A single call here covers every path (issue #997). Stored only
// on pidMgr — cleanupPreSessionsForProject reads it back from there (same
// package) rather than keeping a second copy on SessionDetector.
func (d *SessionDetector) SetSessionSupersededHandler(fn func(oldID, newID string)) {
	d.pidMgr.SetSessionSupersededHandler(fn)
}

// SetBackgroundProbeForTest overrides the background-process liveness probe so
// tests can simulate live / dead background processes without real lsof. See
// issue #445. The second return is "was this conclusive?" — pass false to
// simulate an lsof timeout (issue #1299), not to mean "dead".
func (d *SessionDetector) SetBackgroundProbeForTest(p func(outputPaths []string) (alive, conclusive bool)) {
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

// nowFn reads this detector's clock. The indirection is what lets a test drive
// the #1360 ceilings and the #1366 dwell on a virtual timeline; production
// leaves the field nil and gets time.Now.
//
// WHAT MUST COME THROUGH HERE: every instant that is later compared against
// another instant by the classify pipeline. Concretely that is the pass clock
// (holdContext.Now and the dwell), every SignalHolds placement (HeldSince is
// measured against the pass clock by both ceiling and ripe), and every
// SessionState.UpdatedAt write (reclassifyFromTranscript measures the refresh
// interval against it). Mixing a bare time.Now() into any of those makes two
// stamps that are supposed to share one timeline disagree, and the symptom is
// a guard silently short-circuiting rather than anything failing loudly.
//
// DELIBERATELY EXEMPT, because they are self-consistent wall-clock pairs that
// no pipeline guard reads: the deletedSessions tombstone and its prune
// threshold (session_detector_helpers.go / session_detector_lifecycle.go), and
// historyTracker timestamps, which pair with record()'s own event stamping.
// Naming them is the point — otherwise a reader cannot tell "deliberately wall
// clock" from "missed in the sweep".
func (d *SessionDetector) nowFn() time.Time {
	if d.now != nil {
		return d.now()
	}
	return time.Now()
}

// StartDwellForTest records a decided-but-unpublished state change (#1366)
// with an explicit start time. Exported only so tests outside this package can
// set up a dwell the ticker then has to notice; production starts one solely
// from inside the classify pass, via admitTransition.
func (d *SessionDetector) StartDwellForTest(sessionID, current, candidate string, at time.Time) {
	d.dwell.Admit(sessionID, current, candidate, at)
}

// HoldSignalForTest places an out-of-band signal hold with an explicit
// HeldSince. Exported only so tests outside this package can set up a hold
// that is already older than its policy's ceiling; production code holds
// through ApplyHook, which always stamps time.Now.
func (d *SessionDetector) HoldSignalForTest(sessionID string, kind session.SignalKind, at time.Time) {
	d.signals.Hold(sessionID, kind, session.SignalPayload{}, at)
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

// SetAutonomySpanStore wires an optional AutonomySpanStore (#1905); closed
// autonomy spans are appended to it as sessions leave `working`. Pass nil to
// disable — the open-span bookkeeping on the session still runs, so a daemon
// without a store never accumulates a stale "open since".
//
// It also loads the store's OPEN-RUN JOURNAL, which is what makes a run survive
// the daemon that was measuring it (#1905 recording). Done here rather than in
// startup.go because this is the single wiring point every daemon and every
// test goes through, and a recovery that only ran on one of those paths would
// be a recovery nothing exercises.
func (d *SessionDetector) SetAutonomySpanStore(s outbound.AutonomySpanStore) {
	d.autonomySpans = s
	d.loadRecoverableAutonomySpans()
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

// SetHookLivenessWatchdog wires the optional per-adapter hook-liveness watchdog
// (#1368). When set, each processActivity pass drives it so an adapter whose
// hook channel has stopped delivering is demoted to TierTranscript and its
// pinned hook-tier holds are released. Pass nil to disable.
func (d *SessionDetector) SetHookLivenessWatchdog(w *HookLivenessWatchdog) {
	d.hookLiveness = w
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
//
// isKnownHost is handed the session id it is deciding about so the gate can
// name it when it admits on a walk it could not complete (#1525); see
// PIDManager's field comment for why the id travels inward rather than the
// outcome travelling out.
func (d *SessionDetector) SetHostGate(requireKnownHost map[string]bool, isKnownHost func(sessionID string, pid int) bool) {
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
	in := &lifecycle.ClassifierInputs{
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
		HookTurnDone:                      m.HookTurnDone,
		IdlePromptPending:                 m.IdlePromptPending,
	}
	// #1798: the two facts that make an error transition debuggable from a
	// recording. Guarded rather than unconditional so a healthy session's
	// snapshot is byte-identical to what it was before this field existed —
	// both are omitempty, so a nil error adds nothing to any sidecar.
	if m.SessionError != nil {
		in.SessionErrorClass = m.SessionError.Class
		in.SessionErrorPhase = string(m.SessionError.Phase)
	}
	return in
}

// withProvenance stamps the deciding rule and tier onto a classifier-inputs
// snapshot. Separate from classifierInputs because provenance is a property of
// the *verdict*, not of the metrics: the synthesizers emit transitions that no
// rule decided, and those must record no tier rather than borrow whichever one
// the ladder would have reached on its own.
func withProvenance(in *lifecycle.ClassifierInputs, v StateVerdict) *lifecycle.ClassifierInputs {
	if in == nil || !v.Tier.Known() {
		return in
	}
	in.DecidedByTier = v.Tier.String()
	in.DecidedByRule = v.Rule
	return in
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
		case <-refreshTicker.C:
			d.refreshStaleSessions()
		}
	}
}

// handleTranscriptEvent dispatches a transcript event to the appropriate handler.
