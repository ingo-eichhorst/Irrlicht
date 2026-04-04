// PIDManager handles process lifecycle for sessions: PID discovery (lsof +
// CWD fallback), ProcessWatcher registration, exit handling, and periodic
// liveness sweeps. It was extracted from SessionDetector to separate process
// management (~250 lines) from session detection.
package services

import (
	"context"
	"fmt"
	"strings"
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

	discoverPID      func(string) (int, error)                    // lsof-based discovery
	discoverPIDByCWD func(string, func([]int) int) (int, error) // optional CWD fallback

	// onSessionDeleted is called when a session is deleted so the caller can
	// clean up its own tracking structures (e.g. projectSessions map).
	onSessionDeleted func(sessionID string)
}

// NewPIDManager creates a PIDManager with the given dependencies.
// pw and broadcaster may be nil (optional).
func NewPIDManager(
	pw outbound.ProcessWatcher,
	repo outbound.SessionRepository,
	log outbound.Logger,
	broadcaster outbound.PushBroadcaster,
	readyTTL time.Duration,
	discoverPID func(string) (int, error),
	onSessionDeleted func(sessionID string),
) *PIDManager {
	return &PIDManager{
		pw:               pw,
		repo:             repo,
		log:              log,
		broadcaster:      broadcaster,
		readyTTL:         readyTTL,
		discoverPID:      discoverPID,
		onSessionDeleted: onSessionDeleted,
	}
}

// SetCWDDiscovery sets an optional fallback PID discovery function that finds
// a process by matching its working directory. Called when lsof on the
// transcript file fails to find a PID.
func (pm *PIDManager) SetCWDDiscovery(fn func(string, func([]int) int) (int, error)) {
	pm.discoverPIDByCWD = fn
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

	if err := pm.repo.Delete(sessionID); err != nil {
		pm.log.LogError("process-exit", sessionID,
			fmt.Sprintf("failed to delete session: %v", err))
		return
	}

	pm.broadcast(outbound.PushTypeDeleted, state)
}

// HandlePIDAssigned records a newly-discovered PID for a session, registers it
// with the ProcessWatcher, and cleans up old sessions that shared the same PID.
// This handles the /clear scenario: the CLI process stays alive (same PID) but
// starts a new transcript, making the old session obsolete.
func (pm *PIDManager) HandlePIDAssigned(pid int, sessionID string) {
	if pid <= 0 {
		return
	}

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

	// Clean up old sessions that had the same PID (e.g. /clear).
	// Subagent sessions share the parent's PID, so skip cleanup when
	// either side is a subagent.
	if state.ParentSessionID != "" {
		return
	}

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

// TryDiscoverPID attempts lsof-on-transcript (primary), then CWD-based
// discovery (fallback). Returns true if a PID was found and assigned.
func (pm *PIDManager) TryDiscoverPID(sessionID, transcriptPath, cwd string) bool {
	if pm.pw == nil {
		return false
	}
	// Check if session already has a PID.
	state, _ := pm.repo.Load(sessionID)
	if state != nil && state.PID > 0 {
		return true
	}

	// Primary: lsof on transcript file.
	if transcriptPath != "" {
		if pid, err := pm.discoverPID(transcriptPath); err == nil && pid > 0 {
			pm.log.LogInfo("session-detector", sessionID,
				fmt.Sprintf("lsof discovered pid %d", pid))
			pm.HandlePIDAssigned(pid, sessionID)
			return true
		}
	}

	// Fallback: CWD-based discovery.
	if pm.discoverPIDByCWD != nil && cwd != "" {
		disambiguate := func(pids []int) int {
			best := 0
			for _, p := range pids {
				if p > best {
					best = p
				}
			}
			return best
		}
		if pid, err := pm.discoverPIDByCWD(cwd, disambiguate); err == nil && pid > 0 {
			pm.log.LogInfo("session-detector", sessionID,
				fmt.Sprintf("cwd fallback discovered pid %d", pid))
			pm.HandlePIDAssigned(pid, sessionID)
			return true
		}
	}
	return false
}

// DiscoverPIDWithRetry tries to discover a PID immediately, then retries at
// 500ms, 1s, 2s intervals. This covers the common timing issue where the CLI
// hasn't opened the transcript file yet at session creation time.
func (pm *PIDManager) DiscoverPIDWithRetry(sessionID, transcriptPath, cwd string) {
	if pm.TryDiscoverPID(sessionID, transcriptPath, cwd) {
		return
	}
	for _, delay := range []time.Duration{500 * time.Millisecond, 1 * time.Second, 2 * time.Second} {
		time.Sleep(delay)
		state, _ := pm.repo.Load(sessionID)
		if state == nil || state.PID > 0 {
			return
		}
		if pm.TryDiscoverPID(sessionID, transcriptPath, cwd) {
			return
		}
	}
}

// SweepDeadPIDs periodically checks all sessions for dead processes and deletes
// them. This is a safety net for cases where kqueue misses an exit (PID not
// registered, daemon restart window, race conditions). Blocks until ctx is
// cancelled.
func (pm *PIDManager) SweepDeadPIDs(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pm.CheckPIDLiveness()
		}
	}
}

// CheckPIDLiveness checks all sessions for dead PIDs and stale state.
func (pm *PIDManager) CheckPIDLiveness() {
	states, err := pm.repo.ListAll()
	if err != nil {
		return
	}
	for _, state := range states {
		if state.PID > 0 {
			if err := syscall.Kill(state.PID, 0); err == syscall.ESRCH {
				pm.HandleProcessExit(state.PID, state.SessionID)
			}
		}
	}

	// Sweep stale sessions that can't be cleaned up via PID liveness:
	// - Ready sessions (idle beyond TTL)
	// - Working/waiting sessions with PID=0 (zombies where PID discovery
	//   failed and no kqueue/sweep cleanup path can fire)
	if pm.readyTTL > 0 {
		for _, state := range states {
			if !state.IsStale(pm.readyTTL) {
				continue
			}
			if state.State == session.StateReady || state.PID == 0 {
				pm.log.LogInfo("session-detector", state.SessionID,
					fmt.Sprintf("%s session (pid=%d) idle for >%v, deleting",
						state.State, state.PID, pm.readyTTL))
				_ = pm.repo.Delete(state.SessionID)
				pm.broadcast(outbound.PushTypeDeleted, state)
			}
		}
	}
}

// SeedPIDs cleans up dead sessions and registers alive PIDs with ProcessWatcher
// during startup. Called from SessionDetector.seedFromDisk.
func (pm *PIDManager) SeedPIDs(states []*session.SessionState) {
	for _, state := range states {
		switch {
		case state.PID > 0:
			if err := syscall.Kill(state.PID, 0); err == syscall.ESRCH {
				pm.log.LogInfo("session-detector-seed", state.SessionID,
					fmt.Sprintf("pid %d dead, deleting session", state.PID))
				_ = pm.repo.Delete(state.SessionID)
				pm.broadcast(outbound.PushTypeDeleted, state)
				continue
			}
			if pm.pw != nil {
				if err := pm.pw.Watch(state.PID, state.SessionID); err != nil {
					pm.log.LogError("session-detector-seed", state.SessionID,
						fmt.Sprintf("failed to watch existing pid %d: %v", state.PID, err))
				}
			}

		case state.PID == 0 && isStaleTranscript(state.TranscriptPath):
			// Orphan from exited Claude Code process (never assigned a PID
			// because Claude Code doesn't keep transcript files open).
			pm.log.LogInfo("session-detector-seed", state.SessionID,
				"deleting orphan session")
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
