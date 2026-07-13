package processlifecycle

import (
	"os/exec"
	"strconv"
	"testing"

	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/session"
)

func TestHasRealSessionForPID_TranscriptPathMatch(t *testing.T) {
	sessions := []*session.SessionState{{
		SessionID:      "abc-123",
		PID:            42,
		TranscriptPath: "/home/user/.claude/projects/-Users-user-myproject/abc-123.jsonl",
	}}
	if !HasRealSessionForPID(sessions, "-Users-user-myproject", 42) {
		t.Error("expected match via transcript path projectDir")
	}
}

func TestHasRealSessionForPID_CWDFallback(t *testing.T) {
	// Codex transcript path uses date-based layout — filepath.Base(filepath.Dir(...))
	// returns "10" (the day), not the project directory. The CWD fallback should match.
	sessions := []*session.SessionState{{
		SessionID:      "rollout-2026-04-10",
		PID:            42,
		TranscriptPath: "/home/user/.codex/sessions/2026/04/10/rollout-2026-04-10.jsonl",
		CWD:            "/Users/user/myproject",
	}}
	projectDir := CWDToProjectDir("/Users/user/myproject") // "-Users-user-myproject"
	if !HasRealSessionForPID(sessions, projectDir, 42) {
		t.Error("expected match via CWD fallback for codex-style transcript path")
	}
}

func TestHasRealSessionForPID_SkipsProcSessions(t *testing.T) {
	sessions := []*session.SessionState{{
		SessionID:      "proc-42",
		PID:            42,
		TranscriptPath: "/some/path/proj/session.jsonl",
	}}
	if HasRealSessionForPID(sessions, "proj", 42) {
		t.Error("proc- sessions should be skipped")
	}
}

func TestHasRealSessionForPID_RequiresPIDMatch(t *testing.T) {
	sessions := []*session.SessionState{{
		SessionID:      "abc-123",
		PID:            99,
		TranscriptPath: "/home/user/.claude/projects/-Users-user-myproject/abc-123.jsonl",
		CWD:            "/Users/user/myproject",
	}}
	if HasRealSessionForPID(sessions, "-Users-user-myproject", 42) {
		t.Error("should not match when PID differs")
	}
}

func TestHasRealSessionForPID_RequiresTranscript(t *testing.T) {
	sessions := []*session.SessionState{{
		SessionID: "abc-123",
		PID:       42,
		CWD:       "/Users/user/myproject",
	}}
	if HasRealSessionForPID(sessions, "-Users-user-myproject", 42) {
		t.Error("should not match when TranscriptPath is empty")
	}
}

// TestHandleExitedPIDs_StillAliveNotReaped is a regression test for issue
// #906: a tracked pre-session's PID missing from one poll's matched set (a
// single pgrep/process-name snapshot miss) must not be reaped while the
// process is still actually alive — the live recording showed pre-sessions
// torn down via this path many seconds before the real session ever
// appeared, with no promotion/supersession path ever getting a chance to run.
func TestHandleExitedPIDs_StillAliveNotReaped(t *testing.T) {
	s := NewScanner("irrelevant", "test-adapter", 0)
	pid := liveProcessForTest(t)
	sessionID := "proc-" + strconv.Itoa(pid)
	s.tracked[pid] = trackedProc{sessionID: sessionID, projectDir: "proj"}
	ch := s.Subscribe()

	s.handleExitedPIDs(map[int]bool{}) // pid absent from this poll's live set

	select {
	case ev := <-ch:
		t.Fatalf("expected no removal event for a still-alive pid, got %+v", ev)
	default:
	}
	if _, tracked := s.tracked[pid]; !tracked {
		t.Error("expected pid to remain tracked so the next poll can re-confirm")
	}
}

// TestHandleExitedPIDs_ConfirmedDeadIsReaped verifies the reap path still
// fires, with no added latency, once IsAlive also confirms the process gone.
func TestHandleExitedPIDs_ConfirmedDeadIsReaped(t *testing.T) {
	s := NewScanner("irrelevant", "test-adapter", 0)
	pid := deadPIDForScannerTest(t)
	sessionID := "proc-" + strconv.Itoa(pid)
	s.tracked[pid] = trackedProc{sessionID: sessionID, projectDir: "proj"}
	ch := s.Subscribe()

	s.handleExitedPIDs(map[int]bool{})

	select {
	case ev := <-ch:
		if ev.Type != agent.EventRemoved || ev.SessionID != sessionID {
			t.Errorf("unexpected removal event: %+v", ev)
		}
	default:
		t.Fatal("expected a removal event for a confirmed-dead pid")
	}
	if _, tracked := s.tracked[pid]; tracked {
		t.Error("expected pid to be untracked after confirmed death")
	}
}

// deadPIDForScannerTest spawns and reaps a short-lived process, returning its
// PID. Skips the test if the kernel races us and recycles the PID before we
// can confirm it is dead — keeps the test deterministic (mirrors
// services.deadPIDForTest).
func deadPIDForScannerTest(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start true: %v", err)
	}
	pid := cmd.Process.Pid
	_ = cmd.Wait()
	if IsAlive(pid) {
		t.Skipf("dead pid %d was recycled before test could observe it", pid)
	}
	return pid
}

// liveProcessForTest spawns a long-lived child and returns its PID; the
// process is killed at cleanup.
func liveProcessForTest(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	return cmd.Process.Pid
}
