package services_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	gitadapter "irrlicht/core/adapters/outbound/git"
	"irrlicht/core/application/services"
	"irrlicht/core/domain/agent"
	"irrlicht/core/ports/inbound"
)

// presessionInheritFixture runs a SessionDetector with no PID discoverer for
// the real session's adapter, so the real session can only get a PID from
// the pre-session it retires. That is the post-#2044 shape of issue #2042:
// DiscoverPID declines the CWD fallback and returns 0 for a resumed session.
type presessionInheritFixture struct {
	t          *testing.T
	tw         *mockAgentWatcher
	pw         *mockProcessWatcher
	repo       *mockRepo
	det        *services.SessionDetector
	cwd        string
	transcript string
}

const presessionInheritProjectDir = "-Users-test-presession-pid-inherit"

func newPresessionInheritFixture(t *testing.T) *presessionInheritFixture {
	t.Helper()
	return newPresessionInheritFixtureWithDiscover(t, nil)
}

// newPresessionInheritFixtureWithDiscover wires discover as the claude-code
// adapter's PID discovery; nil means the adapter finds nothing.
func newPresessionInheritFixtureWithDiscover(t *testing.T, discover agent.PIDDiscoverFunc) *presessionInheritFixture {
	t.Helper()
	f := &presessionInheritFixture{
		t:          t,
		tw:         newMockAgentWatcher().withIdentity(agent.Identity{Name: "claude-code"}),
		pw:         newMockProcessWatcher(),
		repo:       newMockRepo(),
		cwd:        t.TempDir(),
		transcript: filepath.Join(t.TempDir(), "resumed.jsonl"),
	}
	if err := os.WriteFile(f.transcript, []byte(`{"role":"user","content":"hi"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.det = services.NewSessionDetector([]inbound.Watcher{f.tw}, services.SessionDetectorDeps{
		PW:           f.pw,
		Repo:         f.repo,
		Log:          &mockLogger{},
		Git:          gitadapter.New(),
		Metrics:      &mockMetrics{},
		Version:      "test",
		PIDDiscovers: map[string]agent.PIDDiscoverFunc{"claude-code": discover},
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- f.det.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	time.Sleep(20 * time.Millisecond)
	return f
}

func (f *presessionInheritFixture) addPreSession(pid int) string {
	f.t.Helper()
	sid := fmt.Sprintf("proc-%d", pid)
	f.tw.ch <- agent.Event{Type: agent.EventNewSession, SessionID: sid, ProjectDir: presessionInheritProjectDir, CWD: f.cwd}
	if !pollUntil(time.Second, func() bool { s, _ := f.repo.Load(sid); return s != nil }) {
		f.t.Fatalf("pre-session %s never appeared", sid)
	}
	return sid
}

func (f *presessionInheritFixture) arriveRealSession(sid string) {
	f.t.Helper()
	f.tw.ch <- agent.Event{Type: agent.EventNewSession, SessionID: sid, ProjectDir: presessionInheritProjectDir, CWD: f.cwd, TranscriptPath: f.transcript}
	if !pollUntil(time.Second, func() bool { s, _ := f.repo.Load(sid); return s != nil }) {
		f.t.Fatalf("real session %s never appeared", sid)
	}
}

func (f *presessionInheritFixture) pidOf(sid string) int {
	s, _ := f.repo.Load(sid)
	if s == nil {
		return -1
	}
	return s.PID
}

// pollUntil polls cond until it holds or timeout elapses, and reports which.
func pollUntil(timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return cond()
}

// TestSessionDetector_RealSessionInheritsRetiredPreSessionPID_Issue2042: a
// real session that retires exactly one pre-session whose process is alive
// takes that process's PID, and the process watcher is re-pointed at it.
func TestSessionDetector_RealSessionInheritsRetiredPreSessionPID_Issue2042(t *testing.T) {
	f := newPresessionInheritFixture(t)
	pid := liveProcessForTest(t)
	f.addPreSession(pid)

	const real = "session_resumed_2042"
	f.arriveRealSession(real)

	if !pollUntil(time.Second, func() bool { return f.pidOf(real) == pid }) {
		t.Fatalf("real session pid = %d, want the retired pre-session's live pid %d", f.pidOf(real), pid)
	}
	f.pw.mu.Lock()
	watchedAs := f.pw.watched[pid]
	f.pw.mu.Unlock()
	if watchedAs != real {
		t.Errorf("process watcher has pid %d as %q, want %q", pid, watchedAs, real)
	}
}

// TestSessionDetector_InheritedPreSessionPIDExitEndsRealSession_Issue2042 is
// the incident shape: the short-lived resume process exits right after its
// pre-session is retired, and that exit must end the resumed session instead
// of leaving it in working with PID 0 until the ready-TTL sweep.
func TestSessionDetector_InheritedPreSessionPIDExitEndsRealSession_Issue2042(t *testing.T) {
	f := newPresessionInheritFixture(t)
	pid := liveProcessForTest(t)
	preID := f.addPreSession(pid)

	const real = "session_resumed_exit_2042"
	f.arriveRealSession(real)
	// Let the retire pass finish before the exit arrives: the pre-session row
	// is deleted inside the same call that would hand its PID over.
	if !pollUntil(time.Second, func() bool { s, _ := f.repo.Load(preID); return s == nil }) {
		t.Fatalf("pre-session %s was never retired", preID)
	}

	// The watcher's exit hint names the pre-session, as the real watcher's
	// entry did before the handover.
	f.det.HandleProcessExit(pid, preID, "pid exited")

	if !pollUntil(time.Second, func() bool { s, _ := f.repo.Load(real); return s == nil }) {
		t.Fatalf("real session %s survived the exit of its process (pid on row: %d)", real, f.pidOf(real))
	}
}

// TestSessionDetector_AmbiguousPreSessionsNotInherited_Issue2042 guards the
// single-candidate rule: with two pre-sessions retired for one real session,
// there is no way to tell which process produced it, so neither PID is taken.
func TestSessionDetector_AmbiguousPreSessionsNotInherited_Issue2042(t *testing.T) {
	f := newPresessionInheritFixture(t)
	pidA, pidB := liveProcessForTest(t), liveProcessForTest(t)
	preA, preB := f.addPreSession(pidA), f.addPreSession(pidB)

	const real = "session_ambiguous_2042"
	f.arriveRealSession(real)
	if !pollUntil(time.Second, func() bool {
		a, _ := f.repo.Load(preA)
		b, _ := f.repo.Load(preB)
		return a == nil && b == nil
	}) {
		t.Fatal("pre-sessions were never retired")
	}

	if pollUntil(300*time.Millisecond, func() bool { return f.pidOf(real) != 0 }) {
		t.Fatalf("real session took pid %d from an ambiguous pre-session match (candidates %d, %d)", f.pidOf(real), pidA, pidB)
	}
}

// TestSessionDetector_DeadPreSessionPIDNotInherited_Issue2042 is a lock: a
// pre-session whose process has already exited hands nothing over.
func TestSessionDetector_DeadPreSessionPIDNotInherited_Issue2042(t *testing.T) {
	f := newPresessionInheritFixture(t)
	pid := deadPIDForTest(t)
	preID := f.addPreSession(pid)

	const real = "session_dead_pre_2042"
	f.arriveRealSession(real)
	if !pollUntil(time.Second, func() bool { s, _ := f.repo.Load(preID); return s == nil }) {
		t.Fatalf("pre-session %s was never retired", preID)
	}

	if pollUntil(300*time.Millisecond, func() bool { return f.pidOf(real) != 0 }) {
		t.Fatalf("real session took dead pid %d from its retired pre-session", f.pidOf(real))
	}
}

// TestSessionDetector_AdapterDiscoveryWinsOverPreSessionPID_Issue2042: the
// pre-session match is by project or cwd, not by process, so its PID is only a
// fallback. When the adapter's own discovery finds the session's process (a
// /clear or /new in another window that shares the cwd, while this window's
// pre-session waits), that PID is bound and the pre-session's is not.
func TestSessionDetector_AdapterDiscoveryWinsOverPreSessionPID_Issue2042(t *testing.T) {
	owner := liveProcessForTest(t)
	f := newPresessionInheritFixtureWithDiscover(t, func(string, string, func([]int) int) (int, error) {
		return owner, nil
	})
	bystander := liveProcessForTest(t)
	f.addPreSession(bystander)

	const real = "session_discovered_2042"
	f.arriveRealSession(real)

	if !pollUntil(time.Second, func() bool { return f.pidOf(real) == owner }) {
		t.Fatalf("real session pid = %d, want the adapter-discovered pid %d (pre-session pid %d must not win)", f.pidOf(real), owner, bystander)
	}
}
