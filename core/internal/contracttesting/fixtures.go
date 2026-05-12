// Package contracttesting holds shared test fixtures consumed by the M0
// contract tests in multiple packages.
//
// The same SessionState fixture is exercised by:
//   - core/adapters/outbound/filesystem/repository_contract_test.go
//     (snapshots the on-disk JSON written by SessionRepository.Save)
//   - core/cmd/irrlichd/push_contract_test.go
//     (snapshots the WebSocket envelope shape for each PushMessage type
//     that carries a SessionState)
//
// Keeping it in one place prevents the goldens in those two packages
// from drifting silently when the SessionState shape evolves: a change
// here regenerates both via UPDATE_CONTRACT_GOLDENS=1.
//
// The package lives under core/internal/ so production code cannot
// import it.
package contracttesting

import "irrlicht/core/domain/session"

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
