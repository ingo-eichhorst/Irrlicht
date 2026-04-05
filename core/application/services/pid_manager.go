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
	"syscall"
	"time"

	"irrlicht/core/domain/session"
	"irrlicht/core/ports/outbound"
)

// PIDManager manages the process lifecycle for sessions. It discovers PIDs,
// registers them with ProcessWatcher, handles exits, and sweeps dead processes.
type PIDManager struct {
	pw          outbound.ProcessWatcher    // optional — nil disables PID tracking
	repo        outbound.SessionRepository // shared with SessionDetector
	log         outbound.Logger
	broadcaster outbound.PushBroadcaster // optional
	readyTTL    time.Duration            // max idle time for ready sessions

	discoverPIDByCWD func(string, func([]int) int) (int, error) // CWD-based discovery

	// onSessionDeleted is called when a session is deleted so the caller can
	// clean up its own tracking structures (e.g. projectSessions map).
	onSessionDeleted func(sessionID string)

	// pendingPIDs stores PIDs discovered by background goroutines, to be
	// applied by processActivity on its next run. This avoids a race where
	// HandlePIDAssigned's load-modify-save overwrites a state transition
	// made by processActivity (e.g. working → ready).
	pendingMu   sync.Mutex
	pendingPIDs map[string]int
}

// NewPIDManager creates a PIDManager with the given dependencies.
// pw and broadcaster may be nil (optional).
func NewPIDManager(
	pw outbound.ProcessWatcher,
	repo outbound.SessionRepository,
	log outbound.Logger,
	broadcaster outbound.PushBroadcaster,
	readyTTL time.Duration,
	discoverPIDByCWD func(string, func([]int) int) (int, error),
	onSessionDeleted func(sessionID string),
) *PIDManager {
	return &PIDManager{
		pw:               pw,
		repo:             repo,
		log:              log,
		broadcaster:      broadcaster,
		readyTTL:         readyTTL,
		discoverPIDByCWD: discoverPIDByCWD,
		onSessionDeleted: onSessionDeleted,
		pendingPIDs:      make(map[string]int),
	}
}

// HandleProcessExit deletes a session when its process exits.
func (pm *PIDManager) HandleProcessExit(pid int, sessionID string) {
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
		if s.ParentSessionID == parentID {
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
		if old.ParentSessionID != "" || strings.HasPrefix(old.SessionID, "proc-") {
			continue
		}

		pm.log.LogInfo("session-detector", old.SessionID,
			fmt.Sprintf("replaced by new session %s (same pid %d) — deleting", sessionID, pid))

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

// TryDiscoverPID finds the PID for a session by matching Claude processes
// by working directory. Prefers unclaimed PIDs but falls back to already-
// claimed PIDs when no unclaimed candidate exists (the /clear scenario where
// the same process starts a new transcript). Returns true if a PID was found.
func (pm *PIDManager) TryDiscoverPID(sessionID, cwd string) bool {
	if pm.pw == nil || pm.discoverPIDByCWD == nil || cwd == "" {
		return false
	}
	// Check if session already has a PID.
	state, _ := pm.repo.Load(sessionID)
	if state != nil && state.PID > 0 {
		return true
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

	if pid, err := pm.discoverPIDByCWD(cwd, disambiguate); err == nil && pid > 0 {
		pm.log.LogInfo("session-detector", sessionID,
			fmt.Sprintf("discovered pid %d via cwd %s", pid, cwd))
		pm.HandlePIDAssigned(pid, sessionID)
		return true
	}
	return false
}

// DiscoverPIDWithRetry tries to discover a PID immediately, then retries at
// 500ms, 1s, 2s intervals. This covers the timing where the Claude process
// hasn't started yet or CWD isn't resolved at session creation time.
func (pm *PIDManager) DiscoverPIDWithRetry(sessionID, cwd string) {
	if pm.TryDiscoverPID(sessionID, cwd) {
		return
	}
	for _, delay := range []time.Duration{500 * time.Millisecond, 1 * time.Second, 2 * time.Second} {
		time.Sleep(delay)
		state, _ := pm.repo.Load(sessionID)
		if state == nil || state.PID > 0 {
			return
		}
		if pm.TryDiscoverPID(sessionID, cwd) {
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
			if state.ParentSessionID != "" {
				if state.State == session.StateReady || isStaleTranscript(state.TranscriptPath) {
					_ = pm.repo.Delete(state.SessionID)
					pm.broadcast(outbound.PushTypeDeleted, state)
				}
				continue
			}
			// Claude Code sessions with PID=0 that are ready: the process
			// likely exited before CWD discovery succeeded. Clean up quickly
			// (30s) rather than waiting the full readyTTL. Non-Claude
			// adapters (Pi, Codex) legitimately have PID=0 — they don't
			// use PID-based lifecycle.
			if state.PID == 0 && state.State == session.StateReady &&
				state.Adapter == "claude-code" &&
				time.Since(time.Unix(state.UpdatedAt, 0)) > 30*time.Second {
				pm.log.LogInfo("session-detector", state.SessionID,
					"ready session with no PID for >30s, deleting")
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
	// Track the newest session per PID for deduplication.
	newestByPID := make(map[int]*session.SessionState)

	for _, state := range states {
		switch {
		case state.PID > 0:
			if err := syscall.Kill(state.PID, 0); err == syscall.ESRCH {
				pm.log.LogInfo("session-detector-seed", state.SessionID,
					fmt.Sprintf("pid %d dead, deleting session", state.PID))
				pm.deleteWithChildren(state)
				continue
			}
			if pm.pw != nil {
				if err := pm.pw.Watch(state.PID, state.SessionID); err != nil {
					pm.log.LogError("session-detector-seed", state.SessionID,
						fmt.Sprintf("failed to watch existing pid %d: %v", state.PID, err))
				}
			}

			// Track newest non-subagent session per PID for dedup below.
			if state.ParentSessionID == "" && !strings.HasPrefix(state.SessionID, "proc-") {
				if prev, ok := newestByPID[state.PID]; !ok || state.FirstSeen > prev.FirstSeen {
					newestByPID[state.PID] = state
				}
			}

		case state.PID == 0 && state.ParentSessionID == "" && state.Adapter == "claude-code" && isStaleTranscript(state.TranscriptPath):
			// Orphan from exited Claude Code process (PID discovery never
			// succeeded). Child sessions (ParentSessionID set) are exempt —
			// they run inside the parent process and never get their own PID.
			// Non-Claude adapters (Pi, Codex) legitimately have PID=0.
			pm.log.LogInfo("session-detector-seed", state.SessionID,
				"deleting orphan session")
			pm.deleteWithChildren(state)
		}
	}

	// Deduplicate: when multiple non-subagent sessions share a PID (e.g.
	// orphans left by /clear), keep only the newest one.
	for pid, newest := range newestByPID {
		for _, state := range states {
			if state.PID != pid || state.SessionID == newest.SessionID {
				continue
			}
			if state.ParentSessionID != "" || strings.HasPrefix(state.SessionID, "proc-") {
				continue
			}
			// Verify session still exists (may have been deleted above).
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

// broadcast sends a push notification if a broadcaster is configured.
func (pm *PIDManager) broadcast(msgType string, state *session.SessionState) {
	if pm.broadcaster != nil {
		pm.broadcaster.Broadcast(outbound.PushMessage{Type: msgType, Session: state})
	}
}
