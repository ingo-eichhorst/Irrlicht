package processlifecycle

import (
	"testing"

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
