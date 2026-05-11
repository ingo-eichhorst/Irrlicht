package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"irrlicht/core/domain/session"
)

// Contract-test shared helpers. Files in this package whose names contain
// "_contract_test.go" use these to lock down wire and persistence shapes.
//
// Refresh every golden via:
//
//	UPDATE_CONTRACT_GOLDENS=1 go test ./core/...

const updateContractGoldensEnv = "UPDATE_CONTRACT_GOLDENS"

// compareContractGolden is the byte-identity comparator used by every
// contract test in this package.
func compareContractGolden(t *testing.T, got []byte, goldenPath string) {
	t.Helper()
	if os.Getenv(updateContractGoldensEnv) == "1" {
		if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
			t.Fatalf("mkdir golden dir: %v", err)
		}
		if err := os.WriteFile(goldenPath, got, 0o644); err != nil {
			t.Fatalf("write golden %s: %v", goldenPath, err)
		}
		return
	}
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden %s: %v (run with %s=1 to create)", goldenPath, err, updateContractGoldensEnv)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("contract drift in %s; run %s=1 go test ./... to refresh\n--- want (%d bytes) ---\n%s\n--- got (%d bytes) ---\n%s",
			goldenPath, updateContractGoldensEnv, len(want), want, len(got), got)
	}
}

// buildContractSessionState mirrors the fixture in
// core/adapters/outbound/filesystem/repository_contract_test.go. Kept in
// sync by hand — if the SessionState shape changes, both fixtures and both
// goldens are regenerated together via the UPDATE env var.
func buildContractSessionState() *session.SessionState {
	waitingStart := int64(1700000050)
	return &session.SessionState{
		Version:         1,
		SessionID:       "00000000-0000-0000-0000-000000000001",
		State:           session.StateWorking,
		Adapter:         "claude-code",
		CompactionState: session.CompactionStateNotCompacting,
		Model:           "claude-sonnet-4-6",
		CWD:             "/tmp/test-cwd",
		TranscriptPath:  "/tmp/test-transcript.jsonl",
		GitBranch:       "main",
		ProjectName:     "test-project",
		FirstSeen:       1700000000,
		UpdatedAt:       1700000100,
		Confidence:      "high",
		EventCount:      42,
		LastEvent:       "assistant",
		LastMatcher:     "claude-stop-hook",
		Metrics: &session.SessionMetrics{
			ElapsedSeconds:         300,
			TotalTokens:            10000,
			ModelName:              "claude-sonnet-4-6",
			ContextWindow:          200000,
			ContextUtilization:     5.0,
			PressureLevel:          "low",
			HasOpenToolCall:        true,
			OpenToolCallCount:      1,
			OpenSubagents:          2,
			LastEventType:          "tool_use",
			LastOpenToolNames:      []string{"Bash"},
			EstimatedCostUSD:       0.0123,
			CumInputTokens:         8000,
			CumOutputTokens:        2000,
			CumCacheReadTokens:     1500,
			CumCacheCreationTokens: 500,
			LastAssistantText:      "Working on it.",
			PermissionMode:         "default",
			Tasks: []session.Task{
				{ID: "task-1", Subject: "first task", ActiveForm: "Doing first task", Status: "in_progress"},
				{ID: "task-2", Subject: "second task", Status: "pending"},
			},
		},
		PID: 12345,
		Launcher: &session.Launcher{
			TermProgram:    "iTerm.app",
			ITermSessionID: "w0t0p0:ABCDEF",
			TTY:            "/dev/ttys001",
		},
		ParentSessionID:    "00000000-0000-0000-0000-000000000002",
		DaemonVersion:      "0.3.13",
		LastTranscriptSize: 1024,
		WaitingStartTime:   &waitingStart,
	}
}
