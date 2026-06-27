package services

import (
	"context"
	"fmt"
	"time"

	"irrlicht/core/domain/session"
	"irrlicht/core/ports/outbound"
)

// DefaultYieldSweepInterval is how often the yield sweep runs when no interval
// is configured (#373).
const DefaultYieldSweepInterval = 30 * time.Minute

// yieldSessionStore is the narrow slice of the session repository the sweeper
// needs: list persisted sessions, re-load one fresh before flipping it, and
// write back the flipped yield verdict.
type yieldSessionStore interface {
	ListAll() ([]*session.SessionState, error)
	Load(sessionID string) (*session.SessionState, error)
	Save(state *session.SessionState) error
}

// yieldGitProbe is the narrow git surface the sweeper needs: resolve a repo
// root (to dedupe projects) and list the commits a repo has reverted.
type yieldGitProbe interface {
	GetGitRoot(dir string) string
	RevertedCommits(dir string) []string
}

// YieldSweeper periodically correlates `git revert` commits back to the
// sessions that authored the reverted work, flipping their YieldState to
// reverted (#373). It is idempotent — a session already reverted is skipped, so
// a second pass over unchanged history changes nothing — and fault-tolerant: a
// non-git, permission-denied, or deleted CWD is silently skipped rather than
// aborting the whole sweep.
type YieldSweeper struct {
	store    yieldSessionStore
	git      yieldGitProbe
	log      outbound.Logger
	interval time.Duration
}

// NewYieldSweeper builds a sweeper. A non-positive interval falls back to
// DefaultYieldSweepInterval.
func NewYieldSweeper(store yieldSessionStore, git yieldGitProbe, log outbound.Logger, interval time.Duration) *YieldSweeper {
	if interval <= 0 {
		interval = DefaultYieldSweepInterval
	}
	return &YieldSweeper{store: store, git: git, log: log, interval: interval}
}

// Run sweeps once at startup, then every interval until ctx is cancelled.
func (s *YieldSweeper) Run(ctx context.Context) {
	s.Sweep()
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.Sweep()
		}
	}
}

// Sweep runs one correlation pass and returns the number of sessions newly
// flipped to reverted. Safe to call repeatedly; only un-reverted sessions with
// a recorded HeadCommit are ever touched.
func (s *YieldSweeper) Sweep() int {
	sessions, err := s.store.ListAll()
	if err != nil {
		s.logError("failed to list sessions for yield sweep", err)
		return 0
	}

	// Index un-reverted, git-tracked sessions by their HEAD commit, and collect
	// one representative directory per unique repo root so each project's
	// history is scanned exactly once.
	byCommit := make(map[string][]*session.SessionState)
	rootDirs := make(map[string]string)
	for _, st := range sessions {
		if st == nil || st.HeadCommit == "" || st.YieldState == session.YieldReverted {
			continue
		}
		byCommit[st.HeadCommit] = append(byCommit[st.HeadCommit], st)
		if st.CWD != "" {
			if root := s.git.GetGitRoot(st.CWD); root != "" {
				if _, seen := rootDirs[root]; !seen {
					rootDirs[root] = st.CWD
				}
			}
		}
	}
	if len(byCommit) == 0 {
		return 0
	}

	// Gather every reverted SHA across the deduped project roots.
	reverted := make(map[string]bool)
	for _, dir := range rootDirs {
		for _, sha := range s.git.RevertedCommits(dir) {
			reverted[sha] = true
		}
	}
	if len(reverted) == 0 {
		return 0
	}

	flipped := 0
	for sha := range reverted {
		for _, snap := range byCommit[sha] {
			// Re-load fresh immediately before writing: the snapshot from
			// ListAll above is stale by the time the per-project git scans
			// finish, and the detector may have re-saved this session in the
			// meantime. Writing back the fresh state (with only YieldState
			// flipped) avoids stomping a concurrent update.
			fresh, err := s.store.Load(snap.SessionID)
			if err != nil || fresh == nil {
				continue
			}
			// The session may already be reverted (idempotent) or have moved
			// its HEAD since the snapshot, in which case the correlation no
			// longer holds — leave it for the next sweep.
			if fresh.YieldState == session.YieldReverted || fresh.HeadCommit != sha {
				continue
			}
			// UpdatedAt is deliberately not bumped: the cost was incurred when
			// the session ran, so it must stay in its original yield window
			// even though the revert was detected later.
			fresh.YieldState = session.YieldReverted
			if err := s.store.Save(fresh); err != nil {
				s.logError(fmt.Sprintf("failed to persist reverted yield for %s", fresh.SessionID), err)
				continue
			}
			flipped++
		}
	}
	return flipped
}

func (s *YieldSweeper) logError(msg string, err error) {
	if s.log != nil {
		s.log.LogError("yield-sweeper", "", fmt.Sprintf("%s: %v", msg, err))
	}
}
