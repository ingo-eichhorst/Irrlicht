package filesystem_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"irrlicht/core/adapters/outbound/filesystem"
	"irrlicht/core/domain/session"
)

const updateContractGoldensEnv = "UPDATE_CONTRACT_GOLDENS"

// TestContract_SessionStateOnDisk locks the on-disk JSON shape produced by
// SessionRepository.Save() for a SessionState with every persisted field
// populated. The macOS Swift app and the dashboard read these files; a silent
// change to the JSON shape is a wire-protocol regression.
//
// Refresh the golden with:
//
//	UPDATE_CONTRACT_GOLDENS=1 go test ./core/adapters/outbound/filesystem/...
//
// The Subagents field uses an unexported type from the session package and is
// intentionally left nil — covering it would require an exported constructor
// in domain/session, which is out of scope for this safety-net test.
func TestContract_SessionStateOnDisk(t *testing.T) {
	state := buildContractSessionState()
	repo := filesystem.NewWithDir(t.TempDir())
	if err := repo.Save(state); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(repo.InstancesDir(), state.SessionID+".json"))
	if err != nil {
		t.Fatalf("read written file: %v", err)
	}

	goldenPath := filepath.Join("testdata", "session_state.golden.json")
	compareContractGolden(t, got, goldenPath)
}

// buildContractSessionState produces a deterministic SessionState fixture that
// exercises every JSON-persisted field. Hardcoded UUIDs and timestamps make
// the golden stable across machines and runs.
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

// compareContractGolden is the byte-identity comparator shared by every
// contract test in this package. Mirrors core/cmd/replay/fixtures_test.go's
// pattern with a different env var so contract goldens and replay goldens
// refresh independently.
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
