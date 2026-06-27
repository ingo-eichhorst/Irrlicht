package services_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"irrlicht/core/adapters/outbound/git"
	"irrlicht/core/application/services"
	"irrlicht/core/domain/session"
)

// memYieldStore is an in-memory yieldSessionStore for sweeper tests.
type memYieldStore struct{ sessions []*session.SessionState }

func (m *memYieldStore) ListAll() ([]*session.SessionState, error)     { return m.sessions, nil }
func (m *memYieldStore) Load(id string) (*session.SessionState, error) { return m.get(id), nil }
func (m *memYieldStore) Save(s *session.SessionState) error {
	for i, e := range m.sessions {
		if e.SessionID == s.SessionID {
			m.sessions[i] = s
			return nil
		}
	}
	m.sessions = append(m.sessions, s)
	return nil
}
func (m *memYieldStore) get(id string) *session.SessionState {
	for _, s := range m.sessions {
		if s.SessionID == id {
			return s
		}
	}
	return nil
}

func yieldGitInit(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("evalsymlinks: %v", err)
	}
	yieldRunGit(t, dir, "init")
	yieldRunGit(t, dir, "config", "user.email", "test@example.com")
	yieldRunGit(t, dir, "config", "user.name", "Test")
	yieldRunGit(t, dir, "config", "commit.gpgsign", "false")
	return dir
}

func yieldRunGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func yieldCommit(t *testing.T, dir, name, content string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	yieldRunGit(t, dir, "add", name)
	yieldRunGit(t, dir, "commit", "-m", "add "+name)
	return yieldRunGit(t, dir, "rev-parse", "HEAD")
}

// Scenario A: a revert of commit A tags session-A without affecting session-B.
func TestYieldSweeper_RevertCorrelates(t *testing.T) {
	dir := yieldGitInit(t)
	shaA := yieldCommit(t, dir, "a.txt", "A")
	shaB := yieldCommit(t, dir, "b.txt", "B")
	store := &memYieldStore{sessions: []*session.SessionState{
		{SessionID: "sess-a", State: session.StateReady, CWD: dir, HeadCommit: shaA, YieldState: session.YieldProductive},
		{SessionID: "sess-b", State: session.StateReady, CWD: dir, HeadCommit: shaB, YieldState: session.YieldProductive},
	}}
	yieldRunGit(t, dir, "revert", "--no-edit", shaA)

	sw := services.NewYieldSweeper(store, git.New(), &mockLogger{}, 0)
	if n := sw.Sweep(); n != 1 {
		t.Fatalf("want 1 flip, got %d", n)
	}
	if got := store.get("sess-a").YieldState; got != session.YieldReverted {
		t.Errorf("sess-a: want reverted, got %q", got)
	}
	if got := store.get("sess-b").YieldState; got != session.YieldProductive {
		t.Errorf("sess-b: want productive (untouched), got %q", got)
	}
}

// Scenario B: a revert living only on an unmerged branch still counts — the
// documented v1 default is "any reachable revert in `--all` history".
func TestYieldSweeper_RevertOnUnmergedBranch(t *testing.T) {
	dir := yieldGitInit(t)
	shaA := yieldCommit(t, dir, "a.txt", "A")
	yieldRunGit(t, dir, "checkout", "-b", "revert-branch")
	yieldRunGit(t, dir, "revert", "--no-edit", shaA)
	yieldRunGit(t, dir, "checkout", "-") // back to the primary branch; revert stays unmerged
	store := &memYieldStore{sessions: []*session.SessionState{
		{SessionID: "sess-a", State: session.StateReady, CWD: dir, HeadCommit: shaA, YieldState: session.YieldProductive},
	}}

	sw := services.NewYieldSweeper(store, git.New(), &mockLogger{}, 0)
	if n := sw.Sweep(); n != 1 {
		t.Fatalf("want 1 flip (--all counts unmerged reverts), got %d", n)
	}
	if got := store.get("sess-a").YieldState; got != session.YieldReverted {
		t.Errorf("want reverted, got %q", got)
	}
}

// Scenario C: a non-git CWD (no HeadCommit) stays permanently unknown without
// erroring out the sweep.
func TestYieldSweeper_NonGitCWD(t *testing.T) {
	store := &memYieldStore{sessions: []*session.SessionState{
		{SessionID: "sess-x", State: session.StateReady, CWD: t.TempDir(), HeadCommit: "", YieldState: session.YieldUnknown},
	}}
	sw := services.NewYieldSweeper(store, git.New(), &mockLogger{}, 0)
	if n := sw.Sweep(); n != 0 {
		t.Fatalf("want 0 flips, got %d", n)
	}
	if got := store.get("sess-x").YieldState; got != session.YieldUnknown {
		t.Errorf("want unknown, got %q", got)
	}
}

// Running the sweep twice produces no further state changes.
func TestYieldSweeper_Idempotent(t *testing.T) {
	dir := yieldGitInit(t)
	shaA := yieldCommit(t, dir, "a.txt", "A")
	yieldRunGit(t, dir, "revert", "--no-edit", shaA)
	store := &memYieldStore{sessions: []*session.SessionState{
		{SessionID: "sess-a", State: session.StateReady, CWD: dir, HeadCommit: shaA, YieldState: session.YieldProductive},
	}}
	sw := services.NewYieldSweeper(store, git.New(), &mockLogger{}, 0)
	if n := sw.Sweep(); n != 1 {
		t.Fatalf("first sweep: want 1, got %d", n)
	}
	if n := sw.Sweep(); n != 0 {
		t.Fatalf("second sweep: want 0 (idempotent), got %d", n)
	}
}

// fakeYieldGit returns a precomputed revert list, isolating the sweeper's
// correlation cost from git's own `git log` scan.
type fakeYieldGit struct {
	root     string
	reverted []string
}

func (f *fakeYieldGit) GetGitRoot(string) string        { return f.root }
func (f *fakeYieldGit) RevertedCommits(string) []string { return f.reverted }

// 1K sessions correlated against 10K reverted SHAs must complete well under the
// 2s DoD bar. This measures the daemon's matching cost; the `git log` scan over
// a real 10K-commit repo is git's concern (exercised by the adapter test).
func TestYieldSweeper_Performance(t *testing.T) {
	const nSessions, nReverts = 1000, 10000
	reverted := make([]string, 0, nReverts)
	for i := 0; i < nReverts; i++ {
		reverted = append(reverted, fmt.Sprintf("%040x", i))
	}
	sessions := make([]*session.SessionState, 0, nSessions)
	for i := 0; i < nSessions; i++ {
		sessions = append(sessions, &session.SessionState{
			SessionID:  fmt.Sprintf("s%d", i),
			State:      session.StateReady,
			CWD:        "/repo",
			HeadCommit: fmt.Sprintf("%040x", i), // all within the reverted set
			YieldState: session.YieldProductive,
		})
	}
	store := &memYieldStore{sessions: sessions}
	sw := services.NewYieldSweeper(store, &fakeYieldGit{root: "/repo", reverted: reverted}, &mockLogger{}, 0)

	start := time.Now()
	flipped := sw.Sweep()
	elapsed := time.Since(start)

	if elapsed > 2*time.Second {
		t.Errorf("sweep took %v, want < 2s", elapsed)
	}
	if flipped != nSessions {
		t.Errorf("want %d flips, got %d", nSessions, flipped)
	}
}
