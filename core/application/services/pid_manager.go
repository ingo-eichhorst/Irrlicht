// PIDManager handles process lifecycle for sessions: CWD-based PID discovery,
// ProcessWatcher registration, exit handling, and periodic liveness sweeps.
// It was extracted from SessionDetector to separate process management from
// session detection.
package services

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/lifecycle"
	"irrlicht/core/domain/session"
	"irrlicht/core/ports/outbound"
)

// LiveCWDsFunc returns the set of working directories currently held by live
// processes whose binary name matches processName. Implementations live in
// the processlifecycle adapter; the function is injected to preserve the
// hexagonal layering. A nil result with a nil error means "no live processes
// matched"; a non-nil error means the lookup failed and callers should treat
// the answer as unknown (do NOT delete sessions on this signal).
type LiveCWDsFunc func(processName string) (map[string]struct{}, error)

// LauncherEnvReader captures the terminal/IDE identity from the process env
// of pid. Returns nil when env cannot be read or no launcher is identifiable.
// Implementations must never block longer than a couple of seconds and must
// never prompt the user (no TCC). The real implementation lives in the
// processlifecycle adapter and is injected to preserve the hexagonal layering.
type LauncherEnvReader func(pid int) *session.Launcher

// BackgroundReader reports adapter-specific background-agent metadata for a PID
// (e.g. Claude Code's kind:"bg" registry entry for an Agent-View background
// agent). Returns nil for ordinary interactive sessions or PIDs the adapter
// doesn't recognize. The real reader lives in the claudecode adapter and is
// injected to preserve the hexagonal layering (#744).
type BackgroundReader func(pid int) *session.BackgroundAgent

// PIDManager manages the process lifecycle for sessions. It discovers PIDs,
// registers them with ProcessWatcher, handles exits, and sweeps dead processes.
type PIDManager struct {
	pw          outbound.ProcessWatcher    // optional — nil disables PID tracking
	repo        outbound.SessionRepository // shared with SessionDetector
	log         outbound.Logger
	broadcaster outbound.PushBroadcaster // optional
	readyTTL    time.Duration            // max idle time for ready sessions

	// pidDiscovers maps adapter name → PID discovery function.
	// Nil or missing entry means no PID discovery for that adapter.
	pidDiscovers map[string]agent.PIDDiscoverFunc

	// processNames maps adapter name → OS process name (the binary `pgrep -x`
	// would match). Used by the startup zombie sweep to detect orphaned
	// sessions of DB-backed adapters (OpenCode), where transcript-mtime
	// staleness can't tell a live session from a historical row.
	// Nil or missing entry disables the DB-backed-orphan check for that
	// adapter — those sessions are kept until their PID is discovered.
	processNames map[string]string

	// liveCWDs is the live-process lookup used by the DB-backed-orphan
	// branch of the startup zombie sweep. Injected from main.go (typically
	// processlifecycle.LiveCWDs). Nil disables the branch.
	liveCWDs LiveCWDsFunc

	// launcherEnv reads launcher env from a PID. Optional — nil skips capture.
	launcherEnv LauncherEnvReader

	// background reads background-agent metadata from a PID. Optional — nil
	// skips capture (#744).
	background BackgroundReader

	// argvExcluders maps adapter name → its Process.ExcludeArgv predicate, and
	// readArgv reads a live PID's argv. Together they let the liveness sweep
	// reap a session bound to a still-alive PID that is actually the adapter's
	// background infra (e.g. Claude Code's --bg-spare pool helper sharing the
	// session's cwd) rather than the interactive process — the ghost in #727.
	// Both nil disables the check; installed once at startup via SetInfraReaper.
	argvExcluders map[string]func([]string) bool
	readArgv      func(pid int) []string

	// requireKnownHost maps adapter name → whether Process.RequireKnownHost is
	// set, and isKnownHost checks a PID's process ancestry against known
	// terminals/IDEs. Together they let session admission reject a candidate
	// PID that is real and alive but was launched by something other than an
	// interactive terminal or IDE (e.g. CodexBar keeping an Antigravity `agy`
	// process running for quota polling — issue #784). Both nil disables the
	// check; installed once at startup via SetHostGate.
	requireKnownHost map[string]bool
	isKnownHost      func(pid int) bool

	// onSessionDeleted is called when a session is deleted so the caller can
	// clean up its own tracking structures (e.g. projectSessions map).
	onSessionDeleted func(sessionID string)

	// onChildDeleted is called when a child session is removed by the
	// liveness sweep so the SessionDetector can re-evaluate the parent.
	// Without this the parent can be left stuck in `working` forever
	// when it was being held active solely because of the just-deleted
	// child (see hasActiveChildren in session_detector.go).
	onChildDeleted func(parentID string)

	// onSessionSuperseded is called when a presession is retired because a
	// real session was reconciled onto the same identity (same PID, or same
	// adapter+project/CWD). Unlike onSessionDeleted, it carries BOTH ids, so
	// a subsystem that keeps its own per-session state (e.g. TerminalObserver's
	// dialog-edge cache, or a Waiting state SessionDetector already persisted
	// onto the presession's own row via a live terminal-observer signal) can
	// carry that state forward onto the new id instead of losing it — the
	// presession row is about to be deleted outright, unlike onSessionDeleted's
	// other callers which only need to forget the old id (issue #997).
	onSessionSuperseded func(oldID, newID string)

	// pendingPIDs stores PIDs discovered by background goroutines, to be
	// applied by processActivity on its next run. This avoids a race where
	// HandlePIDAssigned's load-modify-save overwrites a state transition
	// made by processActivity (e.g. working → ready).
	pendingMu   sync.Mutex
	pendingPIDs map[string]int

	// assignMu serializes every critical section that reads or writes the repo's
	// shared *SessionState pointers: HandlePIDAssigned's load-modify-save +
	// same-PID cleanup scan, claimedPIDs' scan, AND the SessionDetector event
	// loop's own load-modify-save in processActivity (via WithSessionStateLock).
	// The PID-discovery goroutine spawned by processActivity runs concurrently
	// with that same processActivity call; without a shared lock, assignPIDLocked
	// writes state.PID while processActivity writes state.State on the same
	// pointer (issue #606). Never held across detector callbacks (Watch/delete
	// run after assignPIDLocked releases it) so it can't invert with any
	// SessionDetector lock.
	assignMu sync.Mutex

	// recorder captures lifecycle events for offline replay (optional).
	// Set by SessionDetector.SetRecorder.
	recorder    outbound.EventRecorder
	recorderSeq *int64 // shared with SessionDetector for monotonic ordering
}

// PIDManagerDeps bundles NewPIDManager's dependencies. PW and Broadcaster may
// be nil (optional). ProcessNames + LiveCWDs may both be nil — that disables
// the DB-backed-orphan branch of the startup zombie sweep.
type PIDManagerDeps struct {
	PW               outbound.ProcessWatcher
	Repo             outbound.SessionRepository
	Log              outbound.Logger
	Broadcaster      outbound.PushBroadcaster
	ReadyTTL         time.Duration
	PIDDiscovers     map[string]agent.PIDDiscoverFunc
	ProcessNames     map[string]string
	LiveCWDs         LiveCWDsFunc
	OnSessionDeleted func(sessionID string)
}

// NewPIDManager creates a PIDManager with the given dependencies.
func NewPIDManager(deps PIDManagerDeps) *PIDManager {
	return &PIDManager{
		pw:               deps.PW,
		repo:             deps.Repo,
		log:              deps.Log,
		broadcaster:      deps.Broadcaster,
		readyTTL:         deps.ReadyTTL,
		pidDiscovers:     deps.PIDDiscovers,
		processNames:     deps.ProcessNames,
		liveCWDs:         deps.LiveCWDs,
		onSessionDeleted: deps.OnSessionDeleted,
		pendingPIDs:      make(map[string]int),
	}
}

// SetRecorder enables lifecycle event recording on this PIDManager.
// The shared sequence counter ensures monotonic ordering across the
// SessionDetector and PIDManager.
func (pm *PIDManager) SetRecorder(r outbound.EventRecorder, seq *int64) {
	pm.recorder = r
	pm.recorderSeq = seq
}

// SetChildDeletedHandler registers a callback invoked whenever a child
// session is deleted by the liveness sweep. The parent's ID is passed so
// the caller can re-evaluate the parent, which may have been held in
// `working` solely because of that child.
func (pm *PIDManager) SetChildDeletedHandler(fn func(parentID string)) {
	pm.onChildDeleted = fn
}

// SetSessionSupersededHandler registers a callback invoked whenever a
// presession is retired in favor of a reconciled real session — from every
// PIDManager-owned path that performs that reconciliation (same-PID match at
// PID-assignment time, and both the seed-time and periodic pre-session
// sweeps). Both ids are passed so the caller can re-key any per-session state
// it owns onto the new id before the presession row is deleted (issue #997).
func (pm *PIDManager) SetSessionSupersededHandler(fn func(oldID, newID string)) {
	pm.onSessionSuperseded = fn
}

// SetLauncherEnvReader installs a reader that captures launcher identity
// (terminal/IDE env vars) from a session's PID. Called once at startup.
// Nil disables launcher capture.
func (pm *PIDManager) SetLauncherEnvReader(fn LauncherEnvReader) {
	pm.launcherEnv = fn
}

// SetBackgroundReader installs a reader that flags a session as a background
// agent (e.g. a detached Claude Code Agent View bg agent) when its PID is
// assigned. Nil disables the check. Called once at startup (#744).
func (pm *PIDManager) SetBackgroundReader(fn BackgroundReader) {
	pm.background = fn
}

// SetInfraReaper installs the seam the liveness sweep uses to reap a session
// bound to a still-alive PID that is the adapter's background infrastructure
// rather than the interactive session (issue #727). excluders maps adapter name
// → Process.ExcludeArgv; readArgv reads a PID's argv. Both nil (the default, and
// what tests/demo mode leave) disables the check. Called once at startup.
func (pm *PIDManager) SetInfraReaper(excluders map[string]func([]string) bool, readArgv func(pid int) []string) {
	pm.argvExcluders = excluders
	pm.readArgv = readArgv
}

// SetHostGate installs the seam session admission uses to reject a candidate
// PID launched by something other than a known terminal or IDE (issue #784).
// requireKnownHost maps adapter name → Process.RequireKnownHost; isKnownHost
// resolves a PID's process ancestry. Both nil (the default, and what
// tests/demo mode leave) disables the check. Called once at startup.
func (pm *PIDManager) SetHostGate(requireKnownHost map[string]bool, isKnownHost func(pid int) bool) {
	pm.requireKnownHost = requireKnownHost
	pm.isKnownHost = isKnownHost
}

// RequiresKnownHost reports whether adapter has opted into the host-ancestry
// admission gate (Process.RequireKnownHost), so callers can decide whether
// it's worth resolving a cwd before calling AllowsSession.
func (pm *PIDManager) RequiresKnownHost(adapter string) bool {
	return pm.requireKnownHost[adapter]
}

// AllowsSession reports whether a new session should be admitted for adapter,
// given the candidate PID (if any) a synchronous one-shot discovery attempt
// finds at cwd. Adapters that don't opt into RequireKnownHost always return
// true — this check is additive and inert everywhere except antigravity.
//
// When the adapter opts in: a PID discovered now but whose ancestry doesn't
// resolve to a known terminal/IDE rejects admission outright, so no
// SessionState (and no menu-bar circle) is ever created for it. No PID found
// yet fails open — discovery is best-effort and a real session's process is
// virtually always already running by the time its transcript file appears,
// but a rare timing race must not block a legitimate session forever.
//
// Uses the same claim-aware disambiguator as TryDiscoverPID (via sessionID)
// so this check and the real PID binding that follows always agree on which
// PID they're looking at — a mismatch would let the gate ancestry-check a
// stale/unrelated PID sharing the same cwd instead of the one that's actually
// about to be bound.
func (pm *PIDManager) AllowsSession(sessionID, adapter, cwd, transcriptPath string) bool {
	if !pm.requireKnownHost[adapter] {
		return true
	}
	discover := pm.pidDiscovers[adapter]
	if discover == nil {
		return true
	}
	pid, err := discover(cwd, transcriptPath, pm.claimAwareDisambiguate(sessionID))
	if err != nil || pid <= 0 {
		return true
	}
	if pm.isKnownHost == nil {
		return true
	}
	return pm.isKnownHost(pid)
}

// captureLauncher invokes the launcher-env reader if one is installed and
// the session does not yet have a launcher recorded. Safe to call multiple
// times; only populates on the first successful read.
func (pm *PIDManager) captureLauncher(state *session.SessionState, pid int) {
	if pm.launcherEnv == nil || state == nil || state.Launcher != nil || pid <= 0 {
		return
	}
	if l := pm.launcherEnv(pid); l != nil {
		state.Launcher = l
	}
}

// captureBackground flags state as a background agent when the reader recognizes
// its PID, stamping Detached from the captured Launcher TTY. Must run AFTER
// captureLauncher so the controlling TTY is known. Set-once: a no-op once
// Background is already set, when no reader is installed, or for an unrecognized
// PID. Returns true when it set Background so callers can persist (#744).
func (pm *PIDManager) captureBackground(state *session.SessionState, pid int) bool {
	if pm.background == nil || state == nil || state.Background != nil || pid <= 0 {
		return false
	}
	bg := pm.background(pid)
	if bg == nil {
		return false
	}
	// No controlling terminal ⇒ no window/tab owns it — the "detached" signature.
	bg.Detached = state.Launcher == nil || state.Launcher.TTY == ""
	state.Background = bg
	return true
}

// record emits a lifecycle event if recording is enabled.
func (pm *PIDManager) record(ev lifecycle.Event) {
	if pm.recorder == nil {
		return
	}
	if pm.recorderSeq != nil {
		ev.Seq = atomic.AddInt64(pm.recorderSeq, 1)
	}
	if ev.Timestamp.IsZero() {
		ev.Timestamp = time.Now()
	}
	pm.recorder.Record(ev)
}

// HandleProcessExit deletes a session when its process exits. reason describes
// the triggering edge (e.g. "pid exited (ESRCH)") and is recorded on both the
// KindProcessExited event and the resulting deletion, so a trace explains why
// the session went away (issue #757).
func (pm *PIDManager) HandleProcessExit(pid int, sessionID, reason string) {
	pm.record(lifecycle.Event{Kind: lifecycle.KindProcessExited, SessionID: sessionID, PID: pid, Reason: reason})

	if pm.onSessionDeleted != nil {
		pm.onSessionDeleted(sessionID)
	}

	state, err := pm.repo.Load(sessionID)
	if err != nil || state == nil {
		pm.log.LogInfo("process-exit", sessionID,
			fmt.Sprintf("pid %d exited but session not found (already cleaned up)", pid))
		return
	}

	pm.log.LogInfo("process-exit", sessionID,
		fmt.Sprintf("pid %d exited, deleting session (was %s)", pid, state.State))

	pm.deleteWithChildren(state, reason)
}

// CleanupZombies is a one-shot synchronous startup sweep that deletes any
// persisted session whose process is provably gone. The same dead-PID and
// PID=0-orphan predicates also run later via SeedPIDs in seedFromDisk, but
// seedFromDisk executes inside the detector goroutine that starts after the
// HTTP server is already serving — so without this synchronous pre-pass the
// API briefly returns zombies inherited from the previous daemon run.
//
// Three predicates, all narrower than CheckPIDLiveness so we never delete an
// in-flight session (at startup nothing is in-flight, but the predicates
// stay conservative anyway in case CleanupZombies is ever called later):
//  1. Known PID and syscall.Kill returns ESRCH        → process exited.
//  2. PID == 0, not a subagent, transcript file has
//     not been modified within orphanTranscriptAge    → orphan that
//     never bound.
//  3. PID == 0, not a subagent, DB-backed transcript
//     (path contains "?session="), no live process of
//     the adapter's binary owns the session's CWD     → DB-backed orphan
//     (the carryover-state case for OpenCode where
//     isStaleTranscript can't help — the WAL is shared
//     across all sessions in the DB).
//
// Note: a "live PID, old record" case is intentionally NOT included. A
// long-idle but still-running agent (user away from keyboard for >2 min)
// would match that predicate and be wiped on the next daemon restart, even
// though the process is fine. Detecting recycled PIDs reliably needs an
// adapter-specific process-name cross-check, which is out of scope here.
//
// Thread-safety (issue #628): this reads shared *SessionState fields without
// assignMu, but it is a startup-only synchronous pass — main.go calls it
// before SessionDetector.Run, so the event loop hasn't started and no
// PID-discovery goroutine (spawned only by onNewSession/processActivity) can
// exist yet to write state.PID concurrently.
//
// Returns the number of sessions deleted.
func (pm *PIDManager) CleanupZombies() int {
	states, err := pm.repo.ListAll()
	if err != nil {
		return 0
	}
	// Memoize live-CWD lookups per adapter for the duration of this sweep —
	// when M ghost candidates share an adapter, the lookup is identical and
	// each call shells out to pgrep. At startup with heavy carryover state,
	// M can easily reach 10+.
	liveLookup := pm.newLiveLookupCache()
	deleted := 0
	for _, state := range states {
		if !pm.isStartupZombie(state, liveLookup) {
			continue
		}
		pm.log.LogInfo("startup-cleanup", state.SessionID,
			fmt.Sprintf("zombie session (pid=%d, state=%s, adapter=%s) — deleting", state.PID, state.State, state.Adapter))
		pm.deleteWithChildren(state, fmt.Sprintf("startup zombie sweep: pid=%d state=%s", state.PID, state.State))
		deleted++
	}
	return deleted
}

// newLiveLookupCache returns a memoizing adapter→live-CWDs lookup backed by
// pm.liveCWDs / pm.processNames. Returns nil when liveCWDs is unset (the
// DB-backed-orphan branch is disabled). The returned closure caches both
// successful results and the "no process name registered" / "lookup failed"
// states so that repeat calls within a single sweep don't re-fork pgrep.
func (pm *PIDManager) newLiveLookupCache() func(adapter string) map[string]struct{} {
	if pm.liveCWDs == nil {
		return nil
	}
	cache := make(map[string]map[string]struct{})
	cached := make(map[string]bool)
	return func(adapter string) map[string]struct{} {
		if cached[adapter] {
			return cache[adapter]
		}
		cached[adapter] = true
		name, ok := pm.processNames[adapter]
		if !ok || name == "" {
			return nil
		}
		live, err := pm.liveCWDs(name)
		if err != nil {
			return nil
		}
		cache[adapter] = live
		return live
	}
}

// HasLiveProcessInCWD reports whether a live process of adapter's binary
// currently has cwd as its working directory. Used by the stale-transcript
// rescue (issue #576): when consent is granted after sessions started, the
// backfill sweep sees idle transcripts whose process is still alive. Returns
// false when the lookup is unavailable or fails — "unknown" must preserve
// the orphan-skip behavior, never force a session into existence. The cwd is
// canonicalized via EvalSymlinks before the membership test because LiveCWDs
// builds its set from OS-canonical CWDOf paths, while transcript-derived
// cwds may carry symlink components (macOS /var → /private/var).
func (pm *PIDManager) HasLiveProcessInCWD(adapter, cwd string) bool {
	if pm.liveCWDs == nil || cwd == "" {
		return false
	}
	name, ok := pm.processNames[adapter]
	if !ok || name == "" {
		return false
	}
	live, err := pm.liveCWDs(name)
	if err != nil || len(live) == 0 {
		return false
	}
	if resolved, err := filepath.EvalSymlinks(cwd); err == nil {
		cwd = resolved
	}
	_, alive := live[cwd]
	return alive
}

// isStartupZombie returns true for sessions whose process is provably gone.
// Mirrors the predicate documented on CleanupZombies. liveLookup may be nil
// (disables the DB-backed-orphan branch); callers that need the branch must
// supply one — typically pm.newLiveLookupCache().
func (pm *PIDManager) isStartupZombie(state *session.SessionState, liveLookup func(adapter string) map[string]struct{}) bool {
	if state == nil {
		return false
	}
	if state.PID > 0 {
		return syscall.Kill(state.PID, 0) == syscall.ESRCH
	}
	// Subagents share their parent's PID and are cleaned up via child-
	// specific paths in CheckPIDLiveness.
	if state.ParentSessionID != "" {
		return false
	}
	// DB-backed adapters: WAL is shared across sessions, so transcript-mtime
	// staleness is meaningless. Fall back to "is any process of this adapter
	// owning the session's CWD?" — no owner ⇒ orphan.
	if isDBBackedTranscriptPath(state.TranscriptPath) && state.CWD != "" && liveLookup != nil {
		if live := liveLookup(state.Adapter); live != nil {
			if _, alive := live[state.CWD]; !alive {
				return true
			}
		}
	}
	return isStaleTranscript(state.TranscriptPath)
}

// deleteSession removes one session through a single choke point: caller-side
// tracking is evicted via onSessionDeleted (history rings, projectSessions —
// the leak in #593), the removal is logged and recorded for offline replay
// (same transcript_removed convention as the same-PID cleanup, issue #169),
// and the deletion is broadcast so clients drop the row. Every PIDManager
// session removal must route through here so no path leaks history again.
func (pm *PIDManager) deleteSession(s *session.SessionState, reason string) {
	pm.record(lifecycle.Event{
		Kind:           lifecycle.KindTranscriptRemoved,
		SessionID:      s.SessionID,
		Adapter:        s.Adapter,
		TranscriptPath: s.TranscriptPath,
		Reason:         reason,
	})
	if pm.onSessionDeleted != nil {
		pm.onSessionDeleted(s.SessionID)
	}
	_ = pm.repo.Delete(s.SessionID)
	pm.log.LogInfo("session-cleanup", s.SessionID,
		fmt.Sprintf("deleted (%s, state=%s)", reason, s.State))
	pm.broadcast(outbound.PushTypeDeleted, s)
}

// deleteWithChildren removes a session and all its child sessions (subagents).
// reason is recorded on the parent's deletion event so the trace explains why
// it was reaped; children carry "parent deleted" (issue #757).
func (pm *PIDManager) deleteWithChildren(state *session.SessionState, reason string) {
	if states, err := pm.repo.ListAll(); err == nil {
		for _, s := range states {
			if s.ParentSessionID == state.SessionID {
				pm.deleteSession(s, "parent deleted")
			}
		}
	}
	pm.deleteSession(state, reason)
}

// cleanupChildren removes all child sessions of the given parent.
func (pm *PIDManager) cleanupChildren(parentID string) {
	states, err := pm.repo.ListAll()
	if err != nil {
		return
	}
	for _, s := range states {
		if s.ParentSessionID == parentID && !strings.Contains(s.TranscriptPath, "?session=") {
			pm.deleteSession(s, "parent finished — child cleanup")
		}
	}
}

// HandlePIDAssigned records a newly-discovered PID for a session, registers it
// with the ProcessWatcher, and cleans up old sessions that shared the same PID.
//
// The PID is saved directly to the repo AND stored as a pending PID. The direct
// save ensures the PID is persisted even if no subsequent processActivity call
// happens. The pending PID is a safety net: if this direct save races with a
// concurrent processActivity and overwrites a state transition (e.g. working →
// ready), the next processActivity call will consume the pending PID and
// re-classify the state correctly.
func (pm *PIDManager) HandlePIDAssigned(pid int, sessionID string) {
	if pid <= 0 {
		return
	}

	pm.record(lifecycle.Event{Kind: lifecycle.KindPIDDiscovered, SessionID: sessionID, PID: pid})

	// Store pending PID FIRST so processActivity can correct any stale state
	// that our direct save below might overwrite.
	pm.pendingMu.Lock()
	pm.pendingPIDs[sessionID] = pid
	pm.pendingMu.Unlock()

	// Assign the PID and collect stale same-PID sessions under assignMu so
	// concurrent assignments don't race on the shared SessionState pointers.
	// Callbacks (Watch, delete) run after the lock is released.
	state, stale := pm.assignPIDLocked(pid, sessionID)
	if state == nil {
		return
	}

	// Register with ProcessWatcher for exit monitoring.
	if pm.pw != nil {
		if err := pm.pw.Watch(pid, sessionID); err != nil {
			pm.log.LogError(logComponentSessionDetector, sessionID,
				fmt.Sprintf("failed to watch pid %d: %v", pid, err))
		}
	}

	pm.cleanupStalePIDHolders(stale, sessionID, pid)
}

// cleanupStalePIDHolders deletes every session in stale — other sessions that
// held pid before sessionID claimed it (e.g. the /clear pattern, where the
// same process starts a new transcript under a new UUID). A non-subagent PID
// can only belong to one session at a time, so a session claiming a PID makes
// any other non-subagent holder of that PID stale. Each deletion emits
// transcript_removed so the pattern is recoverable from the offline replay
// stream — without it, replay-based analysis sees the old UUID's
// session_created and state transitions but never a corresponding removal,
// and the session looks "leaked" in the recording (issue #169).
func (pm *PIDManager) cleanupStalePIDHolders(stale []*session.SessionState, sessionID string, pid int) {
	for _, old := range stale {
		pm.log.LogInfo(logComponentSessionDetector, old.SessionID,
			fmt.Sprintf("replaced by new session %s (same pid %d) — deleting", sessionID, pid))

		pm.record(lifecycle.Event{
			Kind:           lifecycle.KindTranscriptRemoved,
			SessionID:      old.SessionID,
			Adapter:        old.Adapter,
			TranscriptPath: old.TranscriptPath,
		})

		// Fire before the delete so a re-key handler's own Load(old.SessionID)
		// is guaranteed to still succeed (issue #997).
		if pm.onSessionSuperseded != nil {
			pm.onSessionSuperseded(old.SessionID, sessionID)
		}
		if pm.onSessionDeleted != nil {
			pm.onSessionDeleted(old.SessionID)
		}

		_ = pm.repo.Delete(old.SessionID)
		pm.broadcast(outbound.PushTypeDeleted, old)
	}
}

// assignPIDLocked persists pid onto sessionID's state and returns it along with
// the other non-subagent sessions currently holding the same PID (the stale
// ones the caller should delete). It holds assignMu across the load-modify-save
// and the same-PID scan so concurrent assignments can't race on the repo's
// shared SessionState pointers. Returns (nil, nil) when there is nothing to do
// (session gone, or PID already assigned). Subagent sessions share the parent's
// PID, so no cleanup is reported for them.
func (pm *PIDManager) assignPIDLocked(pid int, sessionID string) (*session.SessionState, []*session.SessionState) {
	pm.assignMu.Lock()
	defer pm.assignMu.Unlock()

	state, _ := pm.repo.Load(sessionID)
	if state == nil || state.PID == pid {
		return nil, nil
	}

	state.PID = pid
	pm.captureLauncher(state, pid)
	pm.captureBackground(state, pid) // after captureLauncher: needs the TTY (#744)
	state.UpdatedAt = time.Now().Unix()
	_ = pm.repo.Save(state)

	if state.ParentSessionID != "" {
		return state, nil
	}

	states, err := pm.repo.ListAll()
	if err != nil {
		return state, nil
	}
	var stale []*session.SessionState
	for _, old := range states {
		if old.SessionID == sessionID || old.PID != pid || old.ParentSessionID != "" {
			continue
		}
		stale = append(stale, old)
	}
	return state, stale
}

// ConsumePendingPID returns and removes a pending PID for the given session.
// Called by processActivity to atomically apply PID assignment during the
// normal state-update flow, avoiding the race with direct Save.
func (pm *PIDManager) ConsumePendingPID(sessionID string) (int, bool) {
	pm.pendingMu.Lock()
	defer pm.pendingMu.Unlock()
	pid, ok := pm.pendingPIDs[sessionID]
	if ok {
		delete(pm.pendingPIDs, sessionID)
	}
	return pid, ok
}

// WithSessionStateLock runs fn while holding assignMu, the lock that also
// guards assignPIDLocked's write to the shared *SessionState. The SessionDetector
// event loop wraps its own load-modify-save of session state in this so the
// PID-discovery goroutine it spawns can't write state.PID concurrently with the
// loop writing state.State on the same pointer (issue #606). fn must not call
// back into any PIDManager method that takes assignMu (assignPIDLocked,
// claimedPIDs) — those run only from the spawned goroutine, never inline.
func (pm *PIDManager) WithSessionStateLock(fn func()) {
	pm.assignMu.Lock()
	defer pm.assignMu.Unlock()
	fn()
}

// claimedPIDs returns the set of PIDs already assigned to sessions other than
// excludeSessionID.
func (pm *PIDManager) claimedPIDs(excludeSessionID string) map[int]bool {
	// Guarded by assignMu: this scan reads state.PID across all sessions and
	// would otherwise race a concurrent assignPIDLocked write.
	pm.assignMu.Lock()
	defer pm.assignMu.Unlock()
	states, err := pm.repo.ListAll()
	if err != nil {
		return nil
	}
	claimed := make(map[int]bool)
	for _, s := range states {
		if s.PID > 0 && s.SessionID != excludeSessionID {
			claimed[s.PID] = true
		}
	}
	return claimed
}

// TryDiscoverPID finds the PID for a session using the adapter-specific
// discovery function. Prefers unclaimed PIDs but falls back to already-
// claimed PIDs when no unclaimed candidate exists (the /clear scenario where
// the same process starts a new transcript). Returns true if a PID was found.
func (pm *PIDManager) TryDiscoverPID(sessionID, cwd, transcriptPath, adapter string) bool {
	state, _ := pm.repo.Load(sessionID)
	if state != nil && state.PID > 0 {
		return true
	}

	// Pre-sessions encode their PID in the session ID by construction
	// (processlifecycle/scanner.go mints `proc-<pid>`). Skip adapter-level
	// CWD discovery — it can misattribute the PID to a sibling process
	// sharing the same CWD during the new agent's brief pre-`cd` window
	// (issue #345). This bypass intentionally runs before the pw / discoverFn
	// guards below: it only needs to parse the ID and call HandlePIDAssigned,
	// which is safe regardless of whether the adapter has a PIDForSession
	// registered or whether the daemon has a live ProcessWatcher.
	if strings.HasPrefix(sessionID, "proc-") {
		var pid int
		if _, err := fmt.Sscanf(sessionID, "proc-%d", &pid); err == nil && pid > 0 {
			pm.log.LogInfo(logComponentSessionDetector, sessionID,
				fmt.Sprintf("encoded pid %d for %s pre-session", pid, adapter))
			pm.HandlePIDAssigned(pid, sessionID)
			return true
		}
		return false
	}

	if pm.pw == nil {
		return false
	}
	discoverFn := pm.pidDiscovers[adapter]
	if discoverFn == nil {
		return false
	}

	if pid, err := discoverFn(cwd, transcriptPath, pm.claimAwareDisambiguate(sessionID)); err == nil && pid > 0 {
		pm.log.LogInfo(logComponentSessionDetector, sessionID,
			fmt.Sprintf("discovered pid %d for %s session", pid, adapter))
		pm.HandlePIDAssigned(pid, sessionID)
		return true
	}
	return false
}

// claimAwareDisambiguate builds a disambiguator that prefers the highest
// unclaimed PID among candidates sharing a cwd, falling back to the highest
// claimed PID when every candidate is already claimed by another session
// (the /clear scenario). Shared by TryDiscoverPID and AllowsSession so both
// pick the same PID for the same session — they used to disagree (AllowsSession
// passed a nil disambiguate, which falls back to each adapter's own default,
// e.g. antigravity's lowestPID), letting the admission gate ancestry-check a
// different, possibly stale, PID than the one that would actually get bound.
func (pm *PIDManager) claimAwareDisambiguate(sessionID string) func([]int) int {
	claimed := pm.claimedPIDs(sessionID)
	return func(pids []int) int {
		bestUnclaimed, bestAny := 0, 0
		for _, p := range pids {
			if p > bestAny {
				bestAny = p
			}
			if !claimed[p] && p > bestUnclaimed {
				bestUnclaimed = p
			}
		}
		if bestUnclaimed > 0 {
			return bestUnclaimed
		}
		return bestAny
	}
}

// DiscoverPIDWithRetry tries to discover a PID immediately, then retries at
// 500ms, 1s, 2s intervals. This covers the timing where the agent process
// hasn't started yet or the transcript file isn't open yet.
func (pm *PIDManager) DiscoverPIDWithRetry(sessionID, cwd, transcriptPath, adapter string) {
	if pm.TryDiscoverPID(sessionID, cwd, transcriptPath, adapter) {
		return
	}
	for _, delay := range []time.Duration{500 * time.Millisecond, 1 * time.Second, 2 * time.Second} {
		time.Sleep(delay)
		state, _ := pm.repo.Load(sessionID)
		if state == nil || state.PID > 0 {
			return
		}
		if pm.TryDiscoverPID(sessionID, cwd, transcriptPath, adapter) {
			return
		}
	}
}

// SweepDeadPIDs periodically checks all sessions for dead processes and deletes
// them. This is a safety net for cases where kqueue misses an exit (PID not
// registered, daemon restart window, race conditions). Blocks until ctx is
// cancelled. The sweep interval backs off from 5s to 15s when no dead PIDs
// are found for several consecutive sweeps.
func (pm *PIDManager) SweepDeadPIDs(ctx context.Context) {
	const baseInterval = 5 * time.Second
	const backoffInterval = 15 * time.Second
	const cleanThreshold = 3

	ticker := time.NewTicker(baseInterval)
	defer ticker.Stop()

	cleanSweeps := 0
	currentInterval := baseInterval

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			foundDead := pm.CheckPIDLiveness()

			if foundDead {
				cleanSweeps = 0
			} else {
				cleanSweeps++
			}

			var targetInterval time.Duration
			if cleanSweeps >= cleanThreshold {
				targetInterval = backoffInterval
			} else {
				targetInterval = baseInterval
			}
			if targetInterval != currentInterval {
				ticker.Reset(targetInterval)
				currentInterval = targetInterval
			}
		}
	}
}

// livenessSnapshot freezes the volatile *SessionState fields CheckPIDLiveness
// bases its decisions on (PID/State/UpdatedAt, plus the immutable identity
// fields it branches on). The values are read once under assignMu so the sweep
// never reads state.PID/UpdatedAt mid-write by a discovery goroutine's
// assignPIDLocked (issue #628). The state pointer is retained only for the
// delete callbacks, which run after assignMu is released.
type livenessSnapshot struct {
	state           *session.SessionState
	pid             int
	sessionState    string
	updatedAt       int64
	parentSessionID string
	transcriptPath  string
	adapter         string
}

// snapshotLivenessStates reads every session's liveness-relevant fields under
// assignMu — the same lock assignPIDLocked takes — so the sweep's reads of
// state.PID/UpdatedAt don't race a concurrent discovery write. The lock is held
// only for the field copies; no callback or syscall runs inside it.
func (pm *PIDManager) snapshotLivenessStates() []livenessSnapshot {
	pm.assignMu.Lock()
	defer pm.assignMu.Unlock()
	states, err := pm.repo.ListAll()
	if err != nil {
		return nil
	}
	snaps := make([]livenessSnapshot, 0, len(states))
	for _, state := range states {
		snaps = append(snaps, livenessSnapshot{
			state:           state,
			pid:             state.PID,
			sessionState:    state.State,
			updatedAt:       state.UpdatedAt,
			parentSessionID: state.ParentSessionID,
			transcriptPath:  state.TranscriptPath,
			adapter:         state.Adapter,
		})
	}
	return snaps
}

// CheckPIDLiveness checks all sessions for dead PIDs and stale state.
// Returns true if any dead PID was found and cleaned up.
//
// Runs off the event loop (the SweepDeadPIDs goroutine), so its reads of the
// shared *SessionState fields would otherwise race a concurrent discovery
// goroutine's assignPIDLocked write of state.PID/UpdatedAt (issue #628). The
// fields are snapshotted under assignMu first (snapshotLivenessStates); the
// sweep then acts on the immutable snapshot, never holding assignMu across a
// delete callback (the invariant documented on assignMu).
func (pm *PIDManager) CheckPIDLiveness() bool {
	// Retire proc-* ghosts whose real session was PID-bound to a sibling
	// process — the seed-time sweep can't reach them (issue #645).
	pm.sweepSupersededPreSessionsPeriodic()

	snaps := pm.snapshotLivenessStates()
	foundDead := false
	for _, snap := range snaps {
		if pm.reapDeadOrInfraPID(snap) {
			foundDead = true
		}
	}

	// Sweep stale sessions that can't be cleaned up via PID liveness:
	// - Ready sessions (idle beyond TTL)
	// - Working/waiting sessions with PID=0 (zombies where PID discovery
	//   failed and no kqueue/sweep cleanup path can fire)
	// - Child sessions: ready or stale transcript (finished/zombie subagents)
	if pm.readyTTL > 0 {
		for _, snap := range snaps {
			pm.sweepStaleSnapshot(snap)
		}
	}
	return foundDead
}

// IsPIDAlive reports whether pid still refers to a live OS process, using the
// same syscall.Kill(pid, 0) probe reapDeadOrInfraPID already uses for
// dead-PID reaping. Exposed as a standalone predicate so callers outside the
// periodic sweep (e.g. parentProcessLive, issue #999) can reuse the exact
// same liveness semantics instead of re-deriving them. A pid <= 0 is never
// alive (mirrors reapDeadOrInfraPID's own guard, which never calls this probe
// for such a pid, but a defensive check keeps this exported predicate safe
// for any future caller). Note: like reapDeadOrInfraPID's own check, this
// can't distinguish a still-alive original process from an unrelated process
// that later reused the same PID — an existing, accepted risk, not one this
// helper introduces.
func (pm *PIDManager) IsPIDAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) != syscall.ESRCH
}

// parentProcessLive is the child-session analog of a superseded live
// pre-session (see finalizeNewSession's liveSignal computation in
// session_detector_activity.go): proof that a subagent's parent OS process is
// still running right now, as opposed to the daemon cold-rediscovering an
// already-finished historical subagent after a restart (issue #999). Returns
// false — and thus no synthesis — when the parent session is unknown or was
// never PID-bound (state.PID <= 0), so a genuinely live but
// very-recently-spawned parent (PID discovery still in flight) is treated the
// same as a dead one: a narrow, deliberately accepted false-negative window
// traded for guaranteed safety against a backlog flood.
//
// Lives on PIDManager rather than SessionDetector because every dependency it
// touches — repo, WithSessionStateLock, IsPIDAlive — already lives here; it is
// pure PID-liveness plumbing, not session-detection policy (unlike
// holdParentWorkingForNewChild, which mutates the parent's classified state
// and stays a SessionDetector-level decision).
//
// Runs under WithSessionStateLock, matching holdParentWorkingForNewChild's
// existing parent-load pattern: the parent may have its own PID-discovery
// goroutine in flight, and reading its fields without the lock would race
// that goroutine's assignPIDLocked write to the same shared *SessionState
// (issue #606).
func (pm *PIDManager) parentProcessLive(parentID string) bool {
	live := false
	pm.WithSessionStateLock(func() {
		parent, err := pm.repo.Load(parentID)
		if err != nil || parent == nil || parent.PID <= 0 {
			return
		}
		live = pm.IsPIDAlive(parent.PID)
	})
	return live
}

// reapDeadOrInfraPID handles the two ways a bound PID can prove the session is
// gone: the process has actually exited (ESRCH), or — for the case ESRCH can
// never catch — the PID is alive but is the adapter's background infra (e.g.
// Claude Code's --bg-spare pool helper) that discovery mis-bound rather than
// the interactive session; such a helper outlives every session, so it must
// be recognized by argv instead of liveness (issue #727). Returns true when it
// reaped the session via HandleProcessExit. A PID <= 0 is left untouched —
// PID=0 ghosts are handled by the readyTTL sweep below, not here.
func (pm *PIDManager) reapDeadOrInfraPID(snap livenessSnapshot) bool {
	if snap.pid <= 0 {
		return false
	}
	if syscall.Kill(snap.pid, 0) == syscall.ESRCH {
		pm.HandleProcessExit(snap.pid, snap.state.SessionID, "pid exited (ESRCH)")
		return true
	}
	if pm.isBoundToInfra(snap) {
		pm.log.LogInfo(logComponentSessionDetector, snap.state.SessionID,
			fmt.Sprintf("pid %d is %s background infra, not the session — reaping ghost",
				snap.pid, snap.adapter))
		pm.HandleProcessExit(snap.pid, snap.state.SessionID,
			fmt.Sprintf("infra-bound ghost: pid %d is %s background infra, not the session", snap.pid, snap.adapter))
		return true
	}
	return false
}

// sweepStaleSnapshot applies the readyTTL-branch special cases to one
// snapshot, in priority order: finished/zombie subagents, PID=0 ghosts whose
// transcript went stale shortly after creation, and finally the general
// idle-beyond-TTL reap. Each case is mutually exclusive with the others for a
// given snapshot (a child session is fully handled by reapStaleChild and
// never falls through to the PID=0 or TTL checks), mirroring the original
// continue-chained control flow.
func (pm *PIDManager) sweepStaleSnapshot(snap livenessSnapshot) {
	// Child sessions: clean up immediately when ready, or when stale
	// (transcript stopped updating — zombie from a previous run). Exception:
	// DB-backed adapters (TranscriptPath contains "?session=") manage their
	// own session lifetime via maxAge; their subagents are persistent
	// historical records, not transient process-bound children.
	if snap.parentSessionID != "" {
		pm.reapStaleChild(snap)
		return
	}

	// Sessions with PID=0 that are ready: the process likely exited before
	// PID discovery succeeded. Clean up quickly (30s) rather than waiting the
	// full readyTTL — BUT only if the transcript itself is stale. A
	// freshly-written transcript with no PID yet means PID discovery is
	// still catching up (e.g. Claude hasn't written ~/.claude/sessions/
	// <pid>.json yet, or multiple claude processes share a cwd and the
	// metadata lookup is retrying). Deleting under those conditions causes
	// the flap loop in issue #109.
	if pm.reapUnboundReadyGhost(snap) {
		return
	}

	if !isStaleUpdatedAt(snap.updatedAt, pm.readyTTL) {
		return
	}
	// Don't delete sessions whose process is still alive.
	if snap.pid > 0 && syscall.Kill(snap.pid, 0) == nil {
		return
	}
	if snap.sessionState == session.StateReady || snap.pid == 0 {
		pm.log.LogInfo(logComponentSessionDetector, snap.state.SessionID,
			fmt.Sprintf("%s session (pid=%d) idle for >%v, deleting",
				snap.sessionState, snap.pid, pm.readyTTL))
		pm.deleteWithChildren(snap.state,
			fmt.Sprintf("%s session (pid=%d) idle >%v — liveness sweep", snap.sessionState, snap.pid, pm.readyTTL))
	}
}

// reapStaleChild deletes snap when it's a finished or zombie subagent (ready,
// its transcript stopped updating, or its transcript is confirmed deleted)
// and notifies onChildDeleted so the parent — which may have been held in
// `working` solely because of this child — gets re-evaluated. Without that
// nudge the parent stays stuck until its own next transcript event, which may
// never come for a finished session. DB-backed adapters are exempt: they
// manage their own subagent lifetime via maxAge.
func (pm *PIDManager) reapStaleChild(snap livenessSnapshot) {
	if strings.Contains(snap.transcriptPath, "?session=") {
		return
	}
	// A subagent whose transcript has been deleted outright (its worktree
	// was torn down mid-run, or similar) is orphaned regardless of its own
	// State or inherited PID — subagents share their parent's PID rather than
	// owning one, so a live PID proves nothing about this specific child, and
	// the sweep is its only backstop (see the comment on isStartupZombie).
	// isDeletedTranscript can't tell "deleted" apart from "not written yet",
	// so it's only trusted once the child has existed for orphanTranscriptAge
	// — long past any normal spawn-to-first-write race.
	transcriptGone := isStaleTranscript(snap.transcriptPath) ||
		(isDeletedTranscript(snap.transcriptPath) &&
			time.Since(time.Unix(snap.state.FirstSeen, 0)) > orphanTranscriptAge)
	if snap.sessionState != session.StateReady && !transcriptGone {
		return
	}
	reason := "child ready — liveness sweep"
	if snap.sessionState != session.StateReady {
		reason = "child transcript stale — liveness sweep"
	}
	pm.deleteSession(snap.state, reason)
	if pm.onChildDeleted != nil {
		pm.onChildDeleted(snap.parentSessionID)
	}
}

// reapUnboundReadyGhost deletes snap when it's a ready, non-child session
// that never got a PID and whose transcript has been stale for over 30s —
// the pre-session-never-bound-a-process signature. Returns true when it
// deleted the session.
func (pm *PIDManager) reapUnboundReadyGhost(snap livenessSnapshot) bool {
	if snap.pid != 0 || snap.sessionState != session.StateReady {
		return false
	}
	if time.Since(time.Unix(snap.updatedAt, 0)) <= 30*time.Second {
		return false
	}
	if !isStaleTranscript(snap.transcriptPath) {
		return false
	}
	pm.log.LogInfo(logComponentSessionDetector, snap.state.SessionID,
		"ready session with no PID and stale transcript for >30s, deleting")
	pm.deleteWithChildren(snap.state,
		"ghost reaped: PID=0, ready, stale transcript >30s — pre-session never bound a process")
	return true
}

// isBoundToInfra reports whether snap's still-alive bound PID is one of the
// adapter's background-infra processes (per its Process.ExcludeArgv) rather than
// the interactive session — AND the session has been inert long enough that
// reaping cannot interrupt real work. It returns false when no reaper is wired,
// the adapter declares no ExcludeArgv, the PID's argv is unreadable (never reap
// on absence of evidence — the ExcludeArgv contract), the session is a subagent
// (it shares the parent's PID), or either staleness guard fails. The two guards
// (stale transcript AND stale UpdatedAt) make this strictly narrower than the
// TTL-branch reap below: a genuinely active session — including one blocked on a
// permission prompt, which is bound to the real `claude` PID, not infra — is
// never reaped here.
func (pm *PIDManager) isBoundToInfra(snap livenessSnapshot) bool {
	if pm.readArgv == nil || snap.pid <= 0 || snap.parentSessionID != "" {
		return false
	}
	exclude := pm.argvExcluders[snap.adapter]
	if exclude == nil {
		return false
	}
	if !isStaleTranscript(snap.transcriptPath) || !isStaleUpdatedAt(snap.updatedAt, pm.readyTTL) {
		return false
	}
	return exclude(pm.readArgv(snap.pid))
}

// isStaleUpdatedAt mirrors SessionState.IsStale on a snapshotted UpdatedAt so
// the staleness test reads the frozen value rather than the live pointer's
// (issue #628).
func isStaleUpdatedAt(updatedAt int64, maxAge time.Duration) bool {
	if maxAge <= 0 {
		return false
	}
	return time.Since(time.Unix(updatedAt, 0)) > maxAge
}

// SeedPIDs cleans up dead sessions and registers alive PIDs with ProcessWatcher
// during startup. Called from SessionDetector.seedFromDisk.
//
// Thread-safety (issue #628): SeedPIDs and its helpers (handleAlivePIDState,
// backfillLauncher, dedupeByPID, sweepSupersededPreSessions) read and write
// shared *SessionState fields without assignMu. This is safe because
// seedFromDisk runs synchronously inside SessionDetector.Run BEFORE the event
// loop's select begins and BEFORE the SweepDeadPIDs goroutine is spawned — so
// no PID-discovery goroutine (spawned only by onNewSession/processActivity from
// inside that loop) can be in flight to write state.PID concurrently.
func (pm *PIDManager) SeedPIDs(states []*session.SessionState) {
	newestByPID := pm.seedAlivePIDs(states)
	pm.dedupeByPID(states, newestByPID)
	pm.sweepSupersededPreSessions(states)
}

// seedAlivePIDs walks all seeded sessions, deletes dead ones, watches alive
// PIDs, backfills missing launcher info, and records the newest non-subagent
// session per PID for later dedup.
func (pm *PIDManager) seedAlivePIDs(states []*session.SessionState) map[int]*session.SessionState {
	newestByPID := make(map[int]*session.SessionState)
	for _, state := range states {
		switch {
		case state.PID > 0:
			if pm.handleAlivePIDState(state) {
				pm.trackNewestByPID(state, newestByPID)
			}
		case state.PID == 0 && isOrphanAtSeed(state):
			// Orphan from exited process (PID discovery never succeeded).
			// Child sessions (ParentSessionID set) are exempt — they run
			// inside the parent process and never get their own PID.
			// cwdMissing also catches zombies re-touched by `claude --resume`
			// after the worktree was deleted (#321).
			pm.log.LogInfo(logComponentSessionDetectorSeed, state.SessionID, "deleting orphan session")
			pm.deleteWithChildren(state, "orphan session at seed — PID discovery never succeeded")
		}
	}
	return newestByPID
}

// trackNewestByPID records state as the newest non-subagent, non-proc-*
// session for its PID, for later dedup via dedupeByPID. Subagents (identified
// by ParentSessionID) and proc-* placeholders are exempt from tracking.
func (pm *PIDManager) trackNewestByPID(state *session.SessionState, newestByPID map[int]*session.SessionState) {
	if state.ParentSessionID != "" || strings.HasPrefix(state.SessionID, "proc-") {
		return
	}
	if prev, ok := newestByPID[state.PID]; ok && prev.FirstSeen >= state.FirstSeen {
		return
	}
	newestByPID[state.PID] = state
}

// isOrphanAtSeed reports whether state is an orphan from an exited process at
// seed time: not a child session, and either its transcript is stale or its
// CWD no longer exists.
func isOrphanAtSeed(state *session.SessionState) bool {
	return state.ParentSessionID == "" && (isStaleTranscript(state.TranscriptPath) || cwdMissing(state.CWD))
}

// handleAlivePIDState processes a state whose PID > 0: deletes it when the
// process is dead, otherwise watches it and backfills launcher metadata.
// Returns true when the state remains alive after processing.
func (pm *PIDManager) handleAlivePIDState(state *session.SessionState) bool {
	if syscall.Kill(state.PID, 0) == syscall.ESRCH {
		pm.log.LogInfo(logComponentSessionDetectorSeed, state.SessionID,
			fmt.Sprintf("pid %d dead, deleting session", state.PID))
		pm.deleteWithChildren(state, fmt.Sprintf("pid %d dead at seed (ESRCH)", state.PID))
		return false
	}
	if pm.pw != nil {
		if err := pm.pw.Watch(state.PID, state.SessionID); err != nil {
			pm.log.LogError(logComponentSessionDetectorSeed, state.SessionID,
				fmt.Sprintf("failed to watch existing pid %d: %v", state.PID, err))
		}
	}
	pm.backfillLauncher(state)
	// Re-attach the background-agent badge to sessions persisted before the
	// field existed, or restored after a daemon restart (#744). Runs after
	// backfillLauncher so Detached reflects the refreshed TTY.
	if pm.captureBackground(state, state.PID) {
		state.UpdatedAt = time.Now().Unix()
		_ = pm.repo.Save(state)
	}
	return true
}

// backfillLauncher reattempts Launcher capture for pre-existing sessions that
// shipped before newer fields existed. Each missing field is filled in
// independently from a fresh env read, without clobbering fields that are
// already populated (the stored env-based identity is the authoritative one).
//
// Currently backfills:
//   - TTY: shipped after the initial Launcher type — older sessions are missing it.
//   - KittyPID: shipped to support per-process kitty activation (issue #326).
//     Without it, KittyActivator on macOS falls back to bundle-level activation
//     which can pick the wrong kitty when multiple kitty.app instances run.
func (pm *PIDManager) backfillLauncher(state *session.SessionState) {
	if state.Launcher == nil {
		pm.captureLauncher(state, state.PID)
		if state.Launcher != nil {
			pm.touchAndSave(state)
		}
		return
	}
	if pm.launcherEnv == nil {
		return
	}
	needs := launcherBackfillNeedsFor(state.Launcher)
	if !needs.any() {
		return
	}
	fresh := pm.launcherEnv(state.PID)
	if fresh == nil {
		return
	}
	if applyLauncherBackfill(state.Launcher, needs, fresh) {
		pm.touchAndSave(state)
	}
}

// touchAndSave bumps UpdatedAt and persists state, ignoring the save error
// (mirrors the existing best-effort backfill-save call sites).
func (pm *PIDManager) touchAndSave(state *session.SessionState) {
	state.UpdatedAt = time.Now().Unix()
	_ = pm.repo.Save(state)
}

// launcherBackfillNeeds tracks which Launcher fields are missing and should
// be refreshed from a fresh env read.
type launcherBackfillNeeds struct {
	tty, kittyPID, kittyListen, kittyWindow bool
}

func (n launcherBackfillNeeds) any() bool {
	return n.tty || n.kittyPID || n.kittyListen || n.kittyWindow
}

// launcherBackfillNeedsFor computes which fields of l are missing and
// eligible for backfill. The three Kitty-specific fields only ever need
// backfilling for a kitty launcher.
func launcherBackfillNeedsFor(l *session.Launcher) launcherBackfillNeeds {
	isKitty := l.TermProgram == "kitty"
	return launcherBackfillNeeds{
		tty:         l.TTY == "",
		kittyPID:    isKitty && l.KittyPID == 0,
		kittyListen: isKitty && l.KittyListenOn == "",
		kittyWindow: isKitty && l.KittyWindowID == "",
	}
}

// applyLauncherBackfill copies each field fresh has that l is missing per
// needs. Returns true when any field was updated.
func applyLauncherBackfill(l *session.Launcher, needs launcherBackfillNeeds, fresh *session.Launcher) bool {
	updated := false
	if needs.tty && fresh.TTY != "" {
		l.TTY = fresh.TTY
		updated = true
	}
	if needs.kittyPID && fresh.KittyPID != 0 {
		l.KittyPID = fresh.KittyPID
		updated = true
	}
	if needs.kittyListen && fresh.KittyListenOn != "" {
		l.KittyListenOn = fresh.KittyListenOn
		updated = true
	}
	if needs.kittyWindow && fresh.KittyWindowID != "" {
		l.KittyWindowID = fresh.KittyWindowID
		updated = true
	}
	return updated
}

// dedupeByPID removes non-subagent sessions that share a PID with a newer
// sibling (e.g. orphans left by /clear). Subagent and proc-* sessions are
// exempt from the dedup.
func (pm *PIDManager) dedupeByPID(states []*session.SessionState, newestByPID map[int]*session.SessionState) {
	for pid, newest := range newestByPID {
		for _, state := range states {
			if !isDedupDeleteCandidate(state, pid, newest) {
				continue
			}
			if s, _ := pm.repo.Load(state.SessionID); s == nil {
				continue
			}
			pm.removeSessionUntracked(logComponentSessionDetectorSeed, state,
				fmt.Sprintf("duplicate pid %d (keeping %s) — deleting", pid, newest.SessionID), "")
		}
	}
}

// removeSessionUntracked deletes s via the same log→onSessionSuperseded→
// onSessionDeleted→repo.Delete→broadcast sequence used by the reconciliation
// sweeps (dedup and pre-session supersession). Unlike deleteSession/
// deleteWithChildren, it does NOT emit a lifecycle recorder event — these
// paths reconcile bookkeeping artifacts (a duplicate PID row, a superseded
// proc-* placeholder) rather than tearing down a session whose disappearance
// belongs in the offline replay trace. tag and msg are the caller's log
// identity and message, kept verbatim so log output is unchanged by this
// extraction. supersededBy is the reconciled session's id s is being retired
// in favor of (empty for a plain same-PID dedup, which has no single
// superseding identity to re-key state onto — issue #997).
func (pm *PIDManager) removeSessionUntracked(tag string, s *session.SessionState, msg string, supersededBy string) {
	pm.log.LogInfo(tag, s.SessionID, msg)
	if supersededBy != "" && pm.onSessionSuperseded != nil {
		pm.onSessionSuperseded(s.SessionID, supersededBy)
	}
	if pm.onSessionDeleted != nil {
		pm.onSessionDeleted(s.SessionID)
	}
	_ = pm.repo.Delete(s.SessionID)
	pm.broadcast(outbound.PushTypeDeleted, s)
}

// isDedupDeleteCandidate returns true when state is a non-subagent,
// non-proc session sharing pid with newest but is not newest itself.
func isDedupDeleteCandidate(state *session.SessionState, pid int, newest *session.SessionState) bool {
	if state.PID != pid || state.SessionID == newest.SessionID {
		return false
	}
	return state.ParentSessionID == "" && !strings.HasPrefix(state.SessionID, "proc-")
}

// preSessionSweepGrace is how long a proc-* pre-session must exist before the
// periodic sweep is allowed to retire it via the adapter+CWD fallback. The
// PID-strict scanner check (HasRealSessionForPID, issue #113) deliberately
// keeps a freshly-opened process's pre-session alive even when an active
// session already exists in the same cwd (two claude instances in one dir).
// The periodic CWD fallback would kill such a legitimate pre-session on sight,
// so it waits out this grace window — long enough for normal transcript
// creation + PID binding to complete — before treating an unbound proc-* as a
// permanent ghost. The PID-match path needs no grace and is exempt.
const preSessionSweepGrace = 90 * time.Second

// presessionMatch reports the kind of real session that supersedes a proc-*
// pre-session. The two paths carry different safety properties, so the periodic
// sweep gates them differently (PID = always safe; CWD = grace-period guarded).
type presessionMatch int

const (
	matchNone presessionMatch = iota
	matchPID                  // a real session is PID-bound to the same process
	matchCWD                  // same adapter + cwd, but a different (or no) PID
)

// sweepSupersededPreSessions deletes proc-* pre-sessions once a matching
// real session exists. Match is by PID (preferred) or by adapter + CWD for
// adapters like Codex whose PID discovery may not have completed yet. This is
// the seed-time variant: it runs once during startup before the event loop and
// the SweepDeadPIDs goroutine begin, so it needs no grace period or locking.
// The periodic equivalent is sweepSupersededPreSessionsPeriodic.
func (pm *PIDManager) sweepSupersededPreSessions(states []*session.SessionState) {
	for _, proc := range states {
		if !strings.HasPrefix(proc.SessionID, "proc-") {
			continue
		}
		if s, _ := pm.repo.Load(proc.SessionID); s == nil {
			continue
		}
		if candidate, _ := findSupersedingSession(proc, states); candidate != nil {
			pm.removeSessionUntracked(logComponentSessionDetectorSeed, proc,
				fmt.Sprintf("pre-session superseded by %s — deleting", candidate.SessionID), candidate.SessionID)
		}
	}
}

// sweepSupersededPreSessionsPeriodic retires proc-* pre-sessions whose real
// session was PID-bound to a sibling process (issue #645). The seed-time sweep
// only runs once at startup, before the scanner's first poll mints the ghost;
// and the scanner's own live check (HasRealSessionForPID) is PID-strict, so a
// proc-A pre-session whose session bound to a distinct PID B is never retired.
//
// It reuses findSupersedingSession (the single matching policy) and gates the
// adapter+CWD fallback behind two #113 guards: the pre-session must be older
// than preSessionSweepGrace, and the superseding session's PID must be alive
// and distinct from the proc-* PID (the ghost signature — a real session
// running under a sibling process). The PID-match path is always safe and
// retires with no grace, mirroring the seed-time behaviour.
//
// Runs off the event loop (CheckPIDLiveness, on the SweepDeadPIDs ticker). It
// snapshots the session list and the proc-* identity fields under assignMu so
// its reads never race a concurrent discovery goroutine's assignPIDLocked
// write of state.PID (issue #628); the snapshot is then acted on without the
// lock held across delete callbacks. Deletion routes through onSessionDeleted
// (which records the proc-* id in deletedSessions) so the scanner — whose own
// PID-strict check would never mark this ghost superseded — cannot re-mint it.
func (pm *PIDManager) sweepSupersededPreSessionsPeriodic() {
	pm.assignMu.Lock()
	states, err := pm.repo.ListAll()
	if err != nil {
		pm.assignMu.Unlock()
		return
	}
	type victim struct {
		state     *session.SessionState
		candidate string
	}
	var victims []victim
	now := time.Now()
	for _, proc := range states {
		if !strings.HasPrefix(proc.SessionID, "proc-") {
			continue
		}
		candidate, kind := findSupersedingSession(proc, states)
		if kind == matchNone {
			continue
		}
		if kind == matchCWD && !preSessionCWDGuardPasses(proc, candidate, now) {
			continue
		}
		victims = append(victims, victim{state: proc, candidate: candidate.SessionID})
	}
	pm.assignMu.Unlock()

	for _, v := range victims {
		if s, _ := pm.repo.Load(v.state.SessionID); s == nil {
			continue
		}
		pm.removeSessionUntracked(logComponentSessionDetector, v.state,
			fmt.Sprintf("pre-session superseded by %s (PID-bound to a sibling) — deleting", v.candidate), v.candidate)
	}
}

// preSessionCWDGuardPasses implements the #113 guard for the matchCWD path: a
// freshly-opened process in a directory that already has an active session
// must keep its pre-session, so retirement is only allowed once the
// pre-session has existed past preSessionSweepGrace AND the superseding
// session is bound to a live PID distinct from proc's own — the ghost
// signature (a real session running under a sibling process), as opposed to
// a legitimate sibling whose own PID discovery just hasn't completed yet.
func preSessionCWDGuardPasses(proc, candidate *session.SessionState, now time.Time) bool {
	if now.Sub(time.Unix(proc.FirstSeen, 0)) < preSessionSweepGrace {
		return false
	}
	if candidate.PID <= 0 || candidate.PID == proc.PID {
		return false
	}
	return syscall.Kill(candidate.PID, 0) != syscall.ESRCH
}

// findSupersedingSession returns the first real (non-proc) session that
// supersedes proc, plus the kind of match. PID match takes precedence over the
// adapter+CWD fallback (the fallback covers adapters like Codex whose PID
// discovery may not have completed yet). Returns (nil, matchNone) when nothing
// matches. This is the canonical superseding-match policy — both the seed-time
// and periodic sweeps delegate here so the predicate can't drift.
func findSupersedingSession(proc *session.SessionState, states []*session.SessionState) (*session.SessionState, presessionMatch) {
	var cwdMatch *session.SessionState
	for _, candidate := range states {
		if strings.HasPrefix(candidate.SessionID, "proc-") || candidate.TranscriptPath == "" {
			continue
		}
		if proc.PID > 0 && proc.PID == candidate.PID {
			return candidate, matchPID
		}
		if cwdMatch == nil && proc.CWD != "" && proc.Adapter == candidate.Adapter && proc.CWD == candidate.CWD {
			cwdMatch = candidate
		}
	}
	if cwdMatch != nil {
		return cwdMatch, matchCWD
	}
	return nil, matchNone
}

// broadcast sends a push notification if a broadcaster is configured.
func (pm *PIDManager) broadcast(msgType string, state *session.SessionState) {
	if pm.broadcaster != nil {
		pm.broadcaster.Broadcast(outbound.PushMessage{Type: msgType, Session: state})
	}
}
