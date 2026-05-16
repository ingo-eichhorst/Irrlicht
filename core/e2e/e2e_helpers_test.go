// Shared helpers and stubs for the e2e test package.
package e2e_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"irrlicht/core/adapters/inbound/agents/processlifecycle"
	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/session"
)

// realTempDir returns a temp dir with macOS /var → /private/var symlinks resolved,
// so paths match what lsof reports for process CWDs.
func realTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("EvalSymlinks(%s): %v", dir, err)
	}
	return real
}

func waitForSession(repo *memRepo, id string, timeout time.Duration) bool {
	deadline := time.After(timeout)
	for {
		select {
		case <-deadline:
			return false
		case <-time.After(50 * time.Millisecond):
		}
		if s, _ := repo.Load(id); s != nil {
			return true
		}
	}
}

// fakeProcessName returns a deterministic per-run process name that won't
// collide with a real "claude" binary pgrep picks up.
func fakeProcessName() string {
	return fmt.Sprintf("irrlicht-e2e-%d", os.Getpid())
}

// startFakeClaudeProcess symlinks /bin/sleep under a unique name so pgrep -x
// sees our test-controlled "claude" stand-in, starts it with a controlled
// CWD, and registers a t.Cleanup to kill and reap it. Returns the running
// command and its CWD.
func startFakeClaudeProcess(t *testing.T) (*exec.Cmd, string) {
	t.Helper()
	return startFakeClaudeProcessNamed(t, fakeProcessName())
}

// startFakeClaudeProcessNamed is like startFakeClaudeProcess but uses an
// explicit shared process name, so multiple concurrent processes can be
// launched under the same agent name (each with its own CWD).
func startFakeClaudeProcessNamed(t *testing.T, name string) (*exec.Cmd, string) {
	t.Helper()
	binDir := realTempDir(t)
	binPath := filepath.Join(binDir, name)
	if err := os.Symlink("/bin/sleep", binPath); err != nil {
		t.Fatalf("symlink /bin/sleep → %s: %v", binPath, err)
	}
	fakeCWD := realTempDir(t)
	cmd := exec.Command(binPath, "60")
	cmd.Dir = fakeCWD
	if err := cmd.Start(); err != nil {
		t.Fatalf("start fake process: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	return cmd, fakeCWD
}

// assertWatchersExited asserts every named done-channel closes within
// timeout — used to catch goroutine leaks after context cancellation.
func assertWatchersExited(t *testing.T, timeout time.Duration, watchers map[string]chan struct{}) {
	t.Helper()
	for name, ch := range watchers {
		select {
		case <-ch:
		case <-time.After(timeout):
			t.Errorf("%s did not exit within %s of context cancel — possible goroutine leak", name, timeout)
		}
	}
}

// realSessionCheckerFor returns a production-equivalent sessionChecker
// backed by the given memRepo — the same predicate
// processlifecycle.HasRealSessionForPID used by the daemon.
func realSessionCheckerFor(repo *memRepo) func(string, int) bool {
	return func(projectDir string, pid int) bool {
		sessions, err := repo.ListAll()
		if err != nil {
			return false
		}
		return processlifecycle.HasRealSessionForPID(sessions, projectDir, pid)
	}
}

// --- stubs -------------------------------------------------------------------

type memRepo struct {
	mu     sync.Mutex
	states map[string]*session.SessionState
}

func newMemRepo() *memRepo {
	return &memRepo{states: make(map[string]*session.SessionState)}
}

func (r *memRepo) Load(id string) (*session.SessionState, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.states[id]
	if !ok {
		return nil, errors.New("not found")
	}
	return s, nil
}

func (r *memRepo) Save(s *session.SessionState) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.states[s.SessionID] = s
	return nil
}

func (r *memRepo) Delete(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.states, id)
	return nil
}

func (r *memRepo) ListAll() ([]*session.SessionState, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*session.SessionState, 0, len(r.states))
	for _, s := range r.states {
		out = append(out, s)
	}
	return out, nil
}

type nopLogger struct{}

func (l *nopLogger) LogInfo(_, _, _ string)                                  {}
func (l *nopLogger) LogError(_, _, _ string)                                 {}
func (l *nopLogger) LogProcessingTime(_, _ string, _ int64, _ int, _ string) {}
func (l *nopLogger) Close() error                                            { return nil }

type stubGit struct{}

func (g *stubGit) GetBranch(_ string) string               { return "main" }
func (g *stubGit) GetProjectName(dir string) string        { return filepath.Base(dir) }
func (g *stubGit) GetGitRoot(_ string) string              { return "" }
func (g *stubGit) GetBranchFromTranscript(_ string) string { return "" }
func (g *stubGit) GetCWDFromTranscript(_ string) string    { return "" }

type stubMetrics struct{}

func (m *stubMetrics) ComputeMetrics(_, _ string) (*session.SessionMetrics, error) {
	return nil, nil
}

func (m *stubMetrics) PruneEntry(_ string) {}

func (m *stubMetrics) IngestRateLimit(_ string, _ *session.RateLimitSnapshot) {}

// testIdentity is the shared Identity value e2e tests stamp on every
// watcher they hand to NewSessionDetector. NewSessionDetector's panic
// guard requires a non-zero Identity; the actual Name doesn't matter to
// these tests beyond clearing the guard.
var testIdentity = agent.Identity{Name: "test"}

// mockWatcher's identity defaults to testIdentity so callers that don't
// care about the adapter name (the common case) don't have to set it.
// Callers that need a different identity override the field directly.
type mockWatcher struct {
	ch       chan agent.Event
	identity agent.Identity
}

func newMockWatcher(buf int) *mockWatcher {
	return &mockWatcher{
		ch:       make(chan agent.Event, buf),
		identity: testIdentity,
	}
}

func (w *mockWatcher) Watch(ctx context.Context) error  { <-ctx.Done(); return ctx.Err() }
func (w *mockWatcher) Subscribe() <-chan agent.Event    { return w.ch }
func (w *mockWatcher) Unsubscribe(_ <-chan agent.Event) {}
func (w *mockWatcher) Identity() agent.Identity         { return w.identity }
