// Package contracttesting holds shared test fixtures and the byte-identity
// golden comparator used by the contract tests in multiple packages.
// Cross-package centralisation here prevents drift between goldens that
// must move together when a shape changes.
//
// The package lives under core/internal/ so production code cannot
// import it.
package contracttesting

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"irrlicht/core/domain/session"
)

// UpdateGoldensEnv is the env var that flips CompareGolden from
// byte-identity check into refresh-the-golden-file mode.
const UpdateGoldensEnv = "UPDATE_CONTRACT_GOLDENS"

// CompareGolden is the byte-identity comparator every contract test
// invokes. With UpdateGoldensEnv=1 it writes got to goldenPath (creating
// parents as needed); otherwise it reads the golden and fails the test
// on any mismatch with a diff hint.
func CompareGolden(t *testing.T, got []byte, goldenPath string) {
	t.Helper()
	if os.Getenv(UpdateGoldensEnv) == "1" {
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
		t.Fatalf("read golden %s: %v (run with %s=1 to create)", goldenPath, err, UpdateGoldensEnv)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("contract drift in %s; run %s=1 go test ./... to refresh\n--- want (%d bytes) ---\n%s\n--- got (%d bytes) ---\n%s",
			goldenPath, UpdateGoldensEnv, len(want), want, len(got), got)
	}
}

// BuildFullSessionState produces a deterministic SessionState fixture
// that exercises every JSON-persisted field. Hardcoded UUIDs and
// timestamps make the resulting golden stable across machines and runs.
//
// The Subagents field uses an unexported type from the session package
// and is intentionally left nil — covering it would require an exported
// constructor in domain/session, which is out of scope for this
// safety-net test.
func BuildFullSessionState() *session.SessionState {
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
