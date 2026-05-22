// PIDManager handles process lifecycle for sessions: CWD-based PID discovery,
// ProcessWatcher registration, exit handling, and periodic liveness sweeps.
// It was extracted from SessionDetector to separate process management from
// session detection.
package services

import (
	"context"
	"fmt"
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

	// onSessionDeleted is called when a session is deleted so the caller can
	// clean up its own tracking structures (e.g. projectSessions map).
	onSessionDeleted func(sessionID string)

	// onChildDeleted is called when a child session is removed by the
	// liveness sweep so the SessionDetector can re-evaluate the parent.
	// Without this the parent can be left stuck in `working` forever
	// when it was being held active solely because of the just-deleted
	// child (see hasActiveChildren in session_detector.go).
	onChildDeleted func(parentID string)

	// pendingPIDs stores PIDs discovered by background goroutines, to be
	// applied by processActivity on its next run. This avoids a race where
	// HandlePIDAssigned's load-modify-save overwrites a state transition
	// made by processActivity (e.g. working → ready).
	pendingMu   sync.Mutex
	pendingPIDs map[string]int

	// recorder captures lifecycle events for offline replay (optional).
	// Set by SessionDetector.SetRecorder.
	recorder    outbound.EventRecorder
	recorderSeq *int64 // shared with SessionDetector for monotonic ordering
}

// NewPIDManager creates a PIDManager with the given dependencies.
// pw and broadcaster may be nil (optional). processNames + liveCWDs may
// both be nil — that disables the DB-backed-orphan branch of the startup
// zombie sweep.
func NewPIDManager(
	pw outbound.ProcessWatcher,
	repo outbound.SessionRepository,
	log outbound.Logger,
	broadcaster outbound.PushBroadcaster,
	readyTTL time.Duration,
	pidDiscovers map[string]agent.PIDDiscoverFunc,
	processNames map[string]string,
	liveCWDs LiveCWDsFunc,
	onSessionDeleted func(sessionID string),
) *PIDManager {
	return &PIDManager{
		pw:               pw,
		repo:             repo,
		log:              log,
		broadcaster:      broadcaster,
		readyTTL:         readyTTL,
		pidDiscovers:     pidDiscovers,
		processNames:     processNames,
		liveCWDs:         liveCWDs,
		onSessionDeleted: onSessionDeleted,
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

// SetLauncherEnvReader installs a reader that captures launcher identity
// (terminal/IDE env vars) from a session's PID. Called once at startup.
// Nil disables launcher capture.
func (pm *PIDManager) SetLauncherEnvReader(fn LauncherEnvReader) {
	pm.launcherEnv = fn
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

// HandleProcessExit deletes a session when its process exits.
func (pm *PIDManager) HandleProcessExit(pid int, sessionID string) {
	pm.record(lifecycle.Event{Kind: lifecycle.KindProcessExited, SessionID: sessionID, PID: pid})

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

	pm.deleteWithChildren(state)
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
		pm.deleteWithChildren(state)
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

// deleteWithChildren removes a session and all its child sessions (subagents).
func (pm *PIDManager) deleteWithChildren(state *session.SessionState) {
	if states, err := pm.repo.ListAll(); err == nil {
		for _, s := range states {
			if s.ParentSessionID == state.SessionID {
				_ = pm.repo.Delete(s.SessionID)
				pm.broadcast(outbound.PushTypeDeleted, s)
			}
		}
	}
	_ = pm.repo.Delete(state.SessionID)
	pm.broadcast(outbound.PushTypeDeleted, state)
}

// cleanupChildren removes all child sessions of the given parent.
func (pm *PIDManager) cleanupChildren(parentID string) {
	states, err := pm.repo.ListAll()
	if err != nil {
		return
	}
	for _, s := range states {
		if s.ParentSessionID == parentID && !strings.Contains(s.TranscriptPath, "?session=") {
			_ = pm.repo.Delete(s.SessionID)
			pm.broadcast(outbound.PushTypeDeleted, s)
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

	// Load latest state and save PID directly for immediate persistence.
	state, _ := pm.repo.Load(sessionID)
	if state == nil || state.PID == pid {
		return
	}

	state.PID = pid
	pm.captureLauncher(state, pid)
	state.UpdatedAt = time.Now().Unix()
	_ = pm.repo.Save(state)

	// Register with ProcessWatcher for exit monitoring.
	if pm.pw != nil {
		if err := pm.pw.Watch(pid, sessionID); err != nil {
			pm.log.LogError("session-detector", sessionID,
				fmt.Sprintf("failed to watch pid %d: %v", pid, err))
		}
	}

	// Subagent sessions share the parent's PID, so skip cleanup when
	// either side is a subagent.
	if state.ParentSessionID != "" {
		return
	}

	// Clean up old sessions that had the same PID (e.g. /clear).
	// A non-subagent PID can only belong to one session at a time —
	// if a new session claims a PID, the old one is stale.
	states, err := pm.repo.ListAll()
	if err != nil {
		return
	}

	for _, old := range states {
		if old.SessionID == sessionID || old.PID != pid {
			continue
		}
		if old.ParentSessionID != "" {
			continue
		}

		pm.log.LogInfo("session-detector", old.SessionID,
			fmt.Sprintf("replaced by new session %s (same pid %d) — deleting", sessionID, pid))

		// Emit transcript_removed for the offline replay stream so the
		// /clear pattern (one PID, UUID rotates) is recoverable from
		// events.jsonl. Without this, replay-based analysis sees the
		// old UUID's session_created and state transitions but never a
		// corresponding removal — the session looks "leaked" in the
		// recording. Issue #169.
		pm.record(lifecycle.Event{
			Kind:           lifecycle.KindTranscriptRemoved,
			SessionID:      old.SessionID,
			Adapter:        old.Adapter,
			TranscriptPath: old.TranscriptPath,
		})

		if pm.onSessionDeleted != nil {
			pm.onSessionDeleted(old.SessionID)
		}

		_ = pm.repo.Delete(old.SessionID)
		pm.broadcast(outbound.PushTypeDeleted, old)
	}
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

// claimedPIDs returns the set of PIDs already assigned to sessions other than
// excludeSessionID.
func (pm *PIDManager) claimedPIDs(excludeSessionID string) map[int]bool {
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
			pm.log.LogInfo("session-detector", sessionID,
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

	// Prefer unclaimed PIDs (multiple instances in same dir), but allow
	// claimed PIDs when all candidates are claimed (/clear scenario).
	claimed := pm.claimedPIDs(sessionID)
	disambiguate := func(pids []int) int {
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

	if pid, err := discoverFn(cwd, transcriptPath, disambiguate); err == nil && pid > 0 {
		pm.log.LogInfo("session-detector", sessionID,
			fmt.Sprintf("discovered pid %d for %s session", pid, adapter))
		pm.HandlePIDAssigned(pid, sessionID)
		return true
	}
	return false
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

// CheckPIDLiveness checks all sessions for dead PIDs and stale state.
// Returns true if any dead PID was found and cleaned up.
func (pm *PIDManager) CheckPIDLiveness() bool {
	states, err := pm.repo.ListAll()
	if err != nil {
		return false
	}
	foundDead := false
	for _, state := range states {
		if state.PID > 0 {
			if err := syscall.Kill(state.PID, 0); err == syscall.ESRCH {
				pm.HandleProcessExit(state.PID, state.SessionID)
				foundDead = true
			}
		}
	}

	// Sweep stale sessions that can't be cleaned up via PID liveness:
	// - Ready sessions (idle beyond TTL)
	// - Working/waiting sessions with PID=0 (zombies where PID discovery
	//   failed and no kqueue/sweep cleanup path can fire)
	// - Child sessions: ready or stale transcript (finished/zombie subagents)
	if pm.readyTTL > 0 {
		for _, state := range states {
			// Child sessions: clean up immediately when ready, or when stale
			// (transcript stopped updating — zombie from a previous run).
			// Exception: DB-backed adapters (TranscriptPath contains "?session=")
			// manage their own session lifetime via maxAge; their subagents are
			// persistent historical records, not transient process-bound children.
			if state.ParentSessionID != "" {
				if !strings.Contains(state.TranscriptPath, "?session=") &&
					(state.State == session.StateReady || isStaleTranscript(state.TranscriptPath)) {
					parentID := state.ParentSessionID
					_ = pm.repo.Delete(state.SessionID)
					pm.broadcast(outbound.PushTypeDeleted, state)
					// Re-evaluate the parent: it may have been held in
					// `working` only because of this child. Without this
					// nudge the parent stays stuck until its own next
					// transcript event, which may never come for a
					// finished session.
					if pm.onChildDeleted != nil {
						pm.onChildDeleted(parentID)
					}
				}
				continue
			}
			// Sessions with PID=0 that are ready: the process likely exited
			// before PID discovery succeeded. Clean up quickly (30s) rather
			// than waiting the full readyTTL — BUT only if the transcript
			// itself is stale. A freshly-written transcript with no PID
			// yet means PID discovery is still catching up (e.g. Claude
			// hasn't written ~/.claude/sessions/<pid>.json yet, or multiple
			// claude processes share a cwd and the metadata lookup is
			// retrying). Deleting under those conditions causes the flap
			// loop in issue #109.
			if state.PID == 0 && state.State == session.StateReady &&
				time.Since(time.Unix(state.UpdatedAt, 0)) > 30*time.Second &&
				isStaleTranscript(state.TranscriptPath) {
				pm.log.LogInfo("session-detector", state.SessionID,
					"ready session with no PID and stale transcript for >30s, deleting")
				pm.deleteWithChildren(state)
				continue
			}

			if !state.IsStale(pm.readyTTL) {
				continue
			}
			// Don't delete sessions whose process is still alive.
			if state.PID > 0 {
				if err := syscall.Kill(state.PID, 0); err == nil {
					continue
				}
			}
			if state.State == session.StateReady || state.PID == 0 {
				pm.log.LogInfo("session-detector", state.SessionID,
					fmt.Sprintf("%s session (pid=%d) idle for >%v, deleting",
						state.State, state.PID, pm.readyTTL))
				pm.deleteWithChildren(state)
			}
		}
	}
	return foundDead
}

// SeedPIDs cleans up dead sessions and registers alive PIDs with ProcessWatcher
// during startup. Called from SessionDetector.seedFromDisk.
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
				if state.ParentSessionID == "" && !strings.HasPrefix(state.SessionID, "proc-") {
					if prev, ok := newestByPID[state.PID]; !ok || state.FirstSeen > prev.FirstSeen {
						newestByPID[state.PID] = state
					}
				}
			}
		case state.PID == 0 && state.ParentSessionID == "" && (isStaleTranscript(state.TranscriptPath) || cwdMissing(state.CWD)):
			// Orphan from exited process (PID discovery never succeeded).
			// Child sessions (ParentSessionID set) are exempt — they run
			// inside the parent process and never get their own PID.
			// cwdMissing also catches zombies re-touched by `claude --resume`
			// after the worktree was deleted (#321).
			pm.log.LogInfo("session-detector-seed", state.SessionID, "deleting orphan session")
			pm.deleteWithChildren(state)
		}
	}
	return newestByPID
}

// handleAlivePIDState processes a state whose PID > 0: deletes it when the
// process is dead, otherwise watches it and backfills launcher metadata.
// Returns true when the state remains alive after processing.
func (pm *PIDManager) handleAlivePIDState(state *session.SessionState) bool {
	if err := syscall.Kill(state.PID, 0); err == syscall.ESRCH {
		pm.log.LogInfo("session-detector-seed", state.SessionID,
			fmt.Sprintf("pid %d dead, deleting session", state.PID))
		pm.deleteWithChildren(state)
		return false
	}
	if pm.pw != nil {
		if err := pm.pw.Watch(state.PID, state.SessionID); err != nil {
			pm.log.LogError("session-detector-seed", state.SessionID,
				fmt.Sprintf("failed to watch existing pid %d: %v", state.PID, err))
		}
	}
	pm.backfillLauncher(state)
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
			state.UpdatedAt = time.Now().Unix()
			_ = pm.repo.Save(state)
		}
		return
	}
	if pm.launcherEnv == nil {
		return
	}
	missingTTY := state.Launcher.TTY == ""
	isKitty := state.Launcher.TermProgram == "kitty"
	missingKittyPID := isKitty && state.Launcher.KittyPID == 0
	missingKittyListen := isKitty && state.Launcher.KittyListenOn == ""
	missingKittyWindow := isKitty && state.Launcher.KittyWindowID == ""
	if !missingTTY && !missingKittyPID && !missingKittyListen && !missingKittyWindow {
		return
	}
	fresh := pm.launcherEnv(state.PID)
	if fresh == nil {
		return
	}
	updated := false
	if missingTTY && fresh.TTY != "" {
		state.Launcher.TTY = fresh.TTY
		updated = true
	}
	if missingKittyPID && fresh.KittyPID != 0 {
		state.Launcher.KittyPID = fresh.KittyPID
		updated = true
	}
	if missingKittyListen && fresh.KittyListenOn != "" {
		state.Launcher.KittyListenOn = fresh.KittyListenOn
		updated = true
	}
	if missingKittyWindow && fresh.KittyWindowID != "" {
		state.Launcher.KittyWindowID = fresh.KittyWindowID
		updated = true
	}
	if updated {
		state.UpdatedAt = time.Now().Unix()
		_ = pm.repo.Save(state)
	}
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
			pm.log.LogInfo("session-detector-seed", state.SessionID,
				fmt.Sprintf("duplicate pid %d (keeping %s) — deleting", pid, newest.SessionID))
			if pm.onSessionDeleted != nil {
				pm.onSessionDeleted(state.SessionID)
			}
			_ = pm.repo.Delete(state.SessionID)
			pm.broadcast(outbound.PushTypeDeleted, state)
		}
	}
}

// isDedupDeleteCandidate returns true when state is a non-subagent,
// non-proc session sharing pid with newest but is not newest itself.
func isDedupDeleteCandidate(state *session.SessionState, pid int, newest *session.SessionState) bool {
	if state.PID != pid || state.SessionID == newest.SessionID {
		return false
	}
	return state.ParentSessionID == "" && !strings.HasPrefix(state.SessionID, "proc-")
}

// sweepSupersededPreSessions deletes proc-* pre-sessions once a matching
// real session exists. Match is by PID (preferred) or by adapter + CWD for
// adapters like Codex whose PID discovery may not have completed yet.
func (pm *PIDManager) sweepSupersededPreSessions(states []*session.SessionState) {
	for _, proc := range states {
		if !strings.HasPrefix(proc.SessionID, "proc-") {
			continue
		}
		if s, _ := pm.repo.Load(proc.SessionID); s == nil {
			continue
		}
		if candidate := findSupersedingSession(proc, states); candidate != nil {
			pm.log.LogInfo("session-detector-seed", proc.SessionID,
				fmt.Sprintf("pre-session superseded by %s — deleting", candidate.SessionID))
			if pm.onSessionDeleted != nil {
				pm.onSessionDeleted(proc.SessionID)
			}
			_ = pm.repo.Delete(proc.SessionID)
			pm.broadcast(outbound.PushTypeDeleted, proc)
		}
	}
}

// findSupersedingSession returns the first real (non-proc) session that
// matches proc by PID or adapter+CWD, or nil when no candidate matches.
func findSupersedingSession(proc *session.SessionState, states []*session.SessionState) *session.SessionState {
	for _, candidate := range states {
		if strings.HasPrefix(candidate.SessionID, "proc-") || candidate.TranscriptPath == "" {
			continue
		}
		if proc.PID > 0 && proc.PID == candidate.PID {
			return candidate
		}
		if proc.CWD != "" && proc.Adapter == candidate.Adapter && proc.CWD == candidate.CWD {
			return candidate
		}
	}
	return nil
}

// broadcast sends a push notification if a broadcaster is configured.
func (pm *PIDManager) broadcast(msgType string, state *session.SessionState) {
	if pm.broadcaster != nil {
		pm.broadcaster.Broadcast(outbound.PushMessage{Type: msgType, Session: state})
	}
}
