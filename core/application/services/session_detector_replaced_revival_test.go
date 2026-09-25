package services_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"irrlicht/core/application/services"
	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/session"
	"irrlicht/core/ports/inbound"
)

// Log lines the revival decision writes (issue #2059). Each rejection is
// logged, so these tests use them as the barrier that the hook was processed.
const (
	revivedReplacedMsg        = "reviving replaced session: a hook arrived and adapter discovery still names its pid"
	notRevivedNoPIDMatchMsg   = "not reviving replaced session: adapter discovery does not name its old pid"
	notRevivedHolderActiveMsg = "not reviving replaced session: the session now holding its pid is active"
	notRevivedNotHookMsg      = "not reviving replaced session: the activity is not a hook"
)

// replacedRevivalFixture reproduces the #2042 shape: session A holds a live
// PID, a ghost B takes that PID and A is deleted as "replaced", and both
// transcripts are older than orphanTranscriptAge, so A's hooks cannot re-create
// it through the ordinary fresh-transcript path.
type replacedRevivalFixture struct {
	t        *testing.T
	tw       *mockAgentWatcher
	repo     *mockRepo
	log      *mockLogger
	det      *services.SessionDetector
	pid      int
	cwd      string
	pathA    string
	pathB    string
	discover func(transcriptPath string) int
}

const (
	revivalSessionA = "session-a-2059"
	revivalSessionB = "session-b-2059"
)

// newReplacedRevivalFixture builds the detector. discover answers the
// claude-code adapter's PID discovery per transcript; nil means "A's transcript
// maps to the live pid, anything else to nothing".
func newReplacedRevivalFixture(t *testing.T, discover func(f *replacedRevivalFixture, transcriptPath string) int) *replacedRevivalFixture {
	t.Helper()
	f := &replacedRevivalFixture{
		t:    t,
		tw:   newMockAgentWatcher().withIdentity(agent.Identity{Name: "claude-code"}),
		repo: newMockRepo(),
		log:  &mockLogger{},
		pid:  liveProcessForTest(t),
		cwd:  t.TempDir(),
	}
	dir := t.TempDir()
	f.pathA = filepath.Join(dir, revivalSessionA+".jsonl")
	f.pathB = filepath.Join(dir, revivalSessionB+".jsonl")
	old := time.Now().Add(-5 * time.Minute)
	writeTranscript(t, f.pathA, old)
	writeTranscript(t, f.pathB, old)
	if discover == nil {
		discover = func(f *replacedRevivalFixture, tp string) int {
			if tp == f.pathA {
				return f.pid
			}
			return 0
		}
	}
	f.discover = func(tp string) int { return discover(f, tp) }

	f.det = services.NewSessionDetector([]inbound.Watcher{f.tw}, services.SessionDetectorDeps{
		PW:      newMockProcessWatcher(),
		Repo:    f.repo,
		Log:     f.log,
		Git:     &mockGit{},
		Metrics: &mockMetrics{},
		Version: "test",
		PIDDiscovers: map[string]agent.PIDDiscoverFunc{
			"claude-code": func(_, tp string, _ func([]int) int) (int, error) { return f.discover(tp), nil },
		},
	})
	f.det.SetDeletedCooldown(0)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- f.det.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })

	for _, s := range []*session.SessionState{
		{SessionID: revivalSessionA, State: session.StateWorking, Adapter: "claude-code", CWD: f.cwd, TranscriptPath: f.pathA, PID: f.pid, FirstSeen: old.Unix(), UpdatedAt: old.Unix()},
		{SessionID: revivalSessionB, State: session.StateWorking, Adapter: "claude-code", CWD: f.cwd, TranscriptPath: f.pathB, FirstSeen: old.Unix(), UpdatedAt: old.Unix()},
	} {
		if err := f.repo.Save(s); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

// replaceAByB binds A's pid to B, which deletes A as replaced (same pid).
func (f *replacedRevivalFixture) replaceAByB() {
	f.t.Helper()
	f.det.HandlePIDAssigned(f.pid, revivalSessionB)
	if !pollUntil(time.Second, func() bool { s, _ := f.repo.Load(revivalSessionA); return s == nil }) {
		f.t.Fatal("A was never deleted as replaced by B")
	}
}

func (f *replacedRevivalFixture) exists(sid string) bool {
	s, _ := f.repo.Load(sid)
	return s != nil
}

// TestSessionDetector_HookRevivesReplacedSession_Issue2059 is the #2042
// recovery path: a hook for the wrongly replaced session re-creates it, and
// the ghost that took its pid is evicted, without waiting for A's main
// transcript to be written again.
func TestSessionDetector_HookRevivesReplacedSession_Issue2059(t *testing.T) {
	f := newReplacedRevivalFixture(t, nil)
	f.replaceAByB()

	f.det.HandlePermissionHook(revivalSessionA, f.pathA, "PostToolUse")

	if !pollUntil(2*time.Second, func() bool { return f.exists(revivalSessionA) }) {
		t.Fatalf("replaced session %s was not revived by its own hook", revivalSessionA)
	}
	if !pollUntil(5*time.Second, func() bool { return !f.exists(revivalSessionB) }) {
		t.Fatalf("ghost %s still holds pid %d after %s was revived", revivalSessionB, f.pid, revivalSessionA)
	}
	if got := f.repo.pidOf(revivalSessionA); got != f.pid {
		t.Errorf("revived session pid = %d, want %d", got, f.pid)
	}
}

// TestSessionDetector_ReplacedNotRevivedWhenDiscoveryDeclines_Issue2059 is the
// /clear shape on current Claude Code: the pid's metadata names the new
// session, so discovery for the old id finds nothing and it stays deleted.
func TestSessionDetector_ReplacedNotRevivedWhenDiscoveryDeclines_Issue2059(t *testing.T) {
	f := newReplacedRevivalFixture(t, func(*replacedRevivalFixture, string) int { return 0 })
	f.replaceAByB()

	f.det.HandlePermissionHook(revivalSessionA, f.pathA, "PostToolUse")

	if !waitForLogMessage(f.log, notRevivedNoPIDMatchMsg, 2*time.Second) {
		t.Fatalf("no rejection logged; want %q", notRevivedNoPIDMatchMsg)
	}
	if f.exists(revivalSessionA) {
		t.Fatal("A was revived although adapter discovery does not name its pid")
	}
}

// TestSessionDetector_ReplacedNotRevivedWhenDiscoveryNamesOtherPID_Issue2059:
// discovery finding some other process is not evidence for the old pid.
func TestSessionDetector_ReplacedNotRevivedWhenDiscoveryNamesOtherPID_Issue2059(t *testing.T) {
	other := liveProcessForTest(t)
	f := newReplacedRevivalFixture(t, func(*replacedRevivalFixture, string) int { return other })
	f.replaceAByB()

	f.det.HandlePermissionHook(revivalSessionA, f.pathA, "PostToolUse")

	if !waitForLogMessage(f.log, notRevivedNoPIDMatchMsg, 2*time.Second) {
		t.Fatalf("no rejection logged; want %q", notRevivedNoPIDMatchMsg)
	}
	if f.exists(revivalSessionA) {
		t.Fatalf("A was revived although discovery named pid %d, not its old pid %d", other, f.pid)
	}
}

// TestSessionDetector_ReplacedNotRevivedWhileHolderActive_Issue2059: after a
// /clear the new session's transcript is fresh; a late hook for the old id must
// not take the pid back from it.
func TestSessionDetector_ReplacedNotRevivedWhileHolderActive_Issue2059(t *testing.T) {
	f := newReplacedRevivalFixture(t, nil)
	f.replaceAByB()
	writeTranscript(t, f.pathB, time.Now())

	f.det.HandlePermissionHook(revivalSessionA, f.pathA, "PostToolUse")

	if !waitForLogMessage(f.log, notRevivedHolderActiveMsg, 2*time.Second) {
		t.Fatalf("no rejection logged; want %q", notRevivedHolderActiveMsg)
	}
	if f.exists(revivalSessionA) || !f.exists(revivalSessionB) {
		t.Fatal("A was revived over an active holder of its pid")
	}
}

// TestSessionDetector_ReplacedNotRevivedByTranscriptEvent_Issue2059: only hook
// activity is live evidence; a watcher event on a stale transcript is not.
func TestSessionDetector_ReplacedNotRevivedByTranscriptEvent_Issue2059(t *testing.T) {
	f := newReplacedRevivalFixture(t, nil)
	f.replaceAByB()

	f.tw.ch <- agent.Event{Type: agent.EventActivity, SessionID: revivalSessionA, TranscriptPath: f.pathA}

	if !waitForLogMessage(f.log, notRevivedNotHookMsg, 2*time.Second) {
		t.Fatalf("no rejection logged; want %q", notRevivedNotHookMsg)
	}
	if f.exists(revivalSessionA) {
		t.Fatal("A was revived by a stale-transcript watcher event")
	}
}

// TestSessionDetector_ExitedSessionNotRevivedByHook_Issue2059 is a lock: only
// a same-pid replacement records the evidence a revival needs; a session
// deleted because its process exited stays deleted.
func TestSessionDetector_ExitedSessionNotRevivedByHook_Issue2059(t *testing.T) {
	f := newReplacedRevivalFixture(t, nil)
	f.det.HandleProcessExit(f.pid, revivalSessionA, "pid exited")
	if !pollUntil(time.Second, func() bool { return !f.exists(revivalSessionA) }) {
		t.Fatal("A was not deleted on process exit")
	}

	f.det.HandlePermissionHook(revivalSessionA, f.pathA, "PostToolUse")

	if pollUntil(500*time.Millisecond, func() bool { return f.exists(revivalSessionA) }) {
		t.Fatal("a session deleted on process exit was revived by a hook")
	}
}
