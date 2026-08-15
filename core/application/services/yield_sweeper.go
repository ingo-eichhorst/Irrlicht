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
	GetGitRoot(dir string) (root string, answered bool)
	RevertedCommits(dir string) (shas []string, answered bool)
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
	unresolved := 0
	for _, st := range sessions {
		if st == nil || st.HeadCommit == "" || st.YieldState == session.YieldReverted {
			continue
		}
		byCommit[st.HeadCommit] = append(byCommit[st.HeadCommit], st)
		if !s.recordRootDir(rootDirs, st.CWD) {
			unresolved++
		}
	}
	if unresolved > 0 {
		s.logError("yield sweep could not resolve a repo root",
			fmt.Errorf("%d session cwd(s) unread: git did not answer, so their project was not scanned", unresolved))
	}
	return byCommit, rootDirs
}

// recordRootDir resolves cwd's git root and, if that root is not recorded yet,
// records it as the directory the revert scan will run in. A no-op for a
// non-git or empty cwd.
//
// resolved is false only when the root probe never ANSWERED — a different fact
// from "not a repo", and the one the caller counts and reports rather than
// dropping.
func (s *YieldSweeper) recordRootDir(rootDirs map[string]string, cwd string) (resolved bool) {
	if cwd == "" {
		return true
	}
	root, answered := s.git.GetGitRoot(cwd)
	if !answered {
		// A cwd whose root never resolved never enters rootDirs, so
		// collectRevertedSHAs — which walks that map — cannot report it. It is
		// counted here instead and folded into the same single log line, or
		// the sweep goes completely silent for history it never read, one step
		// UPSTREAM of where #1543 fixed exactly that (#1551 review finding 4).
		return false
	}
	if root == "" {
		return true
	}
	if _, seen := rootDirs[root]; !seen {
		// The ROOT is the scan directory, not the cwd it was resolved from
		// (#1551 QA, B2). GetGitRoot walks up to the nearest existing ancestor
		// before asking git, so it answers for a cleaned-up worktree — but the
		// cwd itself may no longer exist, and running the revert scan there
		// fails at chdir before exec, which is a PERMANENT non-answer. That
		// left the repo unscanned forever and, once this PR added the report,
		// logging that it was unscanned every 30 minutes. root is guaranteed to
		// exist: git just read its .git directory to produce it.
		rootDirs[root] = root
	}
	return true
}

// collectRevertedSHAs gathers every reverted commit SHA across the deduped
// project roots.
func (s *YieldSweeper) collectRevertedSHAs(rootDirs map[string]string) map[string]bool {
	reverted := make(map[string]bool)
	unread := 0
	for _, dir := range rootDirs {
		shas, answered := s.git.RevertedCommits(dir)
		if !answered {
			// #1543: "git could not be run" is not "this repo has no
			// reverts". The sweep is idempotent and re-runs every interval, so
			// the correct response is to skip this root and say so — not to
			// let an unscanned repo count as a scanned one with no findings,
			// which is how a sweep reports "nothing flipped" for history it
			// never read.
			unread++
			continue
		}
		for _, sha := range shas {
			reverted[sha] = true
		}
	}
	if unread > 0 {
		s.logError("yield sweep could not read git history",
			fmt.Errorf("%d of %d project root(s) unscanned: git did not answer", unread, len(rootDirs)))
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
