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

	byCommit, rootDirs := s.indexByCommit(sessions)
	if len(byCommit) == 0 {
		return 0
	}

	reverted := s.collectRevertedSHAs(rootDirs)
	if len(reverted) == 0 {
		return 0
	}

	return s.flipReverted(byCommit, reverted)
}

// indexByCommit indexes un-reverted, git-tracked sessions by their HEAD
// commit, and collects one representative directory per unique repo root so
// each project's history is scanned exactly once.
func (s *YieldSweeper) indexByCommit(sessions []*session.SessionState) (map[string][]*session.SessionState, map[string]string) {
	byCommit := make(map[string][]*session.SessionState)
	rootDirs := make(map[string]string)
	for _, st := range sessions {
		if st == nil || st.HeadCommit == "" || st.YieldState == session.YieldReverted {
			continue
		}
		byCommit[st.HeadCommit] = append(byCommit[st.HeadCommit], st)
		s.recordRootDir(rootDirs, st.CWD)
	}
	return byCommit, rootDirs
}

// recordRootDir resolves cwd's git root and, if no representative directory
// is recorded for that root yet, records cwd as its sample directory for the
// revert scan. A no-op for a non-git or empty cwd.
func (s *YieldSweeper) recordRootDir(rootDirs map[string]string, cwd string) {
	if cwd == "" {
		return
	}
	root := s.git.GetGitRoot(cwd)
	if root == "" {
		return
	}
	if _, seen := rootDirs[root]; !seen {
		rootDirs[root] = cwd
	}
}

// collectRevertedSHAs gathers every reverted commit SHA across the deduped
// project roots.
func (s *YieldSweeper) collectRevertedSHAs(rootDirs map[string]string) map[string]bool {
	reverted := make(map[string]bool)
	for _, dir := range rootDirs {
		for _, sha := range s.git.RevertedCommits(dir) {
			reverted[sha] = true
		}
	}
	return reverted
}

// flipReverted flips YieldState to reverted for every session indexed under a
// reverted commit SHA, and returns the count actually flipped.
func (s *YieldSweeper) flipReverted(byCommit map[string][]*session.SessionState, reverted map[string]bool) int {
	flipped := 0
	for sha := range reverted {
		for _, snap := range byCommit[sha] {
			if s.flipOne(snap, sha) {
				flipped++
			}
		}
	}
	return flipped
}

// flipOne re-loads snap fresh immediately before writing — the snapshot from
// Sweep's ListAll is stale by the time the per-project git scans finish, and
// the detector may have re-saved this session in the meantime — and, if it's
// still un-reverted with the same HEAD commit sha, flips its YieldState to
// reverted and persists it. Returns true when the flip was made.
func (s *YieldSweeper) flipOne(snap *session.SessionState, sha string) bool {
	fresh, err := s.store.Load(snap.SessionID)
	if err != nil || fresh == nil {
		return false
	}
	// The session may already be reverted (idempotent) or have moved its HEAD
	// since the snapshot, in which case the correlation no longer holds —
	// leave it for the next sweep.
	if fresh.YieldState == session.YieldReverted || fresh.HeadCommit != sha {
		return false
	}
	// UpdatedAt is deliberately not bumped: the cost was incurred when the
	// session ran, so it must stay in its original yield window even though
	// the revert was detected later.
	fresh.YieldState = session.YieldReverted
	if err := s.store.Save(fresh); err != nil {
		s.logError(fmt.Sprintf("failed to persist reverted yield for %s", fresh.SessionID), err)
		return false
	}
	return true
}

func (s *YieldSweeper) logError(msg string, err error) {
	if s.log != nil {
		s.log.LogError("yield-sweeper", "", fmt.Sprintf("%s: %v", msg, err))
	}
}
