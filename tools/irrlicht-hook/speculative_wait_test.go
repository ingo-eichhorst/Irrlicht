package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// setupTestLogger initialises the package-level logger for tests.
func setupTestLogger(t *testing.T) {
	t.Helper()
	var err error
	logger, err = NewStructuredLogger()
	if err != nil {
		t.Fatalf("failed to create logger: %v", err)
	}
	t.Cleanup(func() { logger.Close() })
}

// writeSessionState writes a session state JSON file directly into the
// instances directory used by the hook, returning the file path.
func writeSessionState(t *testing.T, state *SessionState) string {
	t.Helper()
	dir := getInstancesDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("failed to create instances dir: %v", err)
	}
	path := filepath.Join(dir, state.SessionID+".json")
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		t.Fatalf("failed to marshal state: %v", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("failed to write state file: %v", err)
	}
	t.Cleanup(func() { os.Remove(path) })
	return path
}

func TestRunSpeculativeWait_TransitionsToWaiting(t *testing.T) {
	setupTestLogger(t)
	speculativeWaitDelay = 0 // no sleep in tests

	sid := "test-specwait-" + t.Name()
	state := &SessionState{
		Version:   1,
		SessionID: sid,
		State:     "working",
		LastEvent: "PreToolUse",
		FirstSeen: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
	}
	writeSessionState(t, state)

	runSpeculativeWait(sid)

	got, err := loadSessionState(sid)
	if err != nil {
		t.Fatalf("failed to load state after runSpeculativeWait: %v", err)
	}
	if got.State != "waiting" {
		t.Errorf("expected state=waiting, got %q", got.State)
	}
}

func TestRunSpeculativeWait_NoOpWhenPostToolUseArrived(t *testing.T) {
	setupTestLogger(t)
	speculativeWaitDelay = 0

	sid := "test-specwait-postuse-" + t.Name()
	state := &SessionState{
		Version:   1,
		SessionID: sid,
		State:     "working",
		LastEvent: "PostToolUse", // PostToolUse already arrived
		FirstSeen: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
	}
	writeSessionState(t, state)

	runSpeculativeWait(sid)

	got, err := loadSessionState(sid)
	if err != nil {
		t.Fatalf("failed to load state: %v", err)
	}
	if got.State != "working" {
		t.Errorf("expected state=working (unchanged), got %q", got.State)
	}
}

func TestRunSpeculativeWait_NoOpWhenAlreadyWaiting(t *testing.T) {
	setupTestLogger(t)
	speculativeWaitDelay = 0

	sid := "test-specwait-already-" + t.Name()
	state := &SessionState{
		Version:   1,
		SessionID: sid,
		State:     "waiting", // already in waiting (Notification arrived first)
		LastEvent: "Notification",
		FirstSeen: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
	}
	writeSessionState(t, state)

	runSpeculativeWait(sid)

	got, err := loadSessionState(sid)
	if err != nil {
		t.Fatalf("failed to load state: %v", err)
	}
	if got.State != "waiting" {
		t.Errorf("expected state=waiting (unchanged), got %q", got.State)
	}
	// LastEvent must not have been touched by the speculative wait
	if got.LastEvent != "Notification" {
		t.Errorf("expected LastEvent=Notification (unchanged), got %q", got.LastEvent)
	}
}

func TestRunSpeculativeWait_NoOpWhenSessionGone(t *testing.T) {
	setupTestLogger(t)
	speculativeWaitDelay = 0

	// Don't write any state file — session doesn't exist
	sid := "test-specwait-gone-" + t.Name()

	// Should return without error or panic
	runSpeculativeWait(sid)
}

func TestRunSpeculativeWait_SetsWaitingStartTime(t *testing.T) {
	setupTestLogger(t)
	speculativeWaitDelay = 0

	sid := "test-specwait-timer-" + t.Name()
	state := &SessionState{
		Version:   1,
		SessionID: sid,
		State:     "working",
		LastEvent: "PreToolUse",
		FirstSeen: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
	}
	writeSessionState(t, state)

	before := time.Now().Unix()
	runSpeculativeWait(sid)

	got, err := loadSessionState(sid)
	if err != nil {
		t.Fatalf("failed to load state: %v", err)
	}
	if got.WaitingStartTime == nil {
		t.Error("expected WaitingStartTime to be set")
	} else if *got.WaitingStartTime < before {
		t.Errorf("WaitingStartTime %d is before test start %d", *got.WaitingStartTime, before)
	}
}

func TestApprovalProneToolsMap(t *testing.T) {
	// Verify the expected tools are present
	expected := []string{"Bash", "Write", "Edit", "MultiEdit"}
	for _, tool := range expected {
		if !approvalProneTools[tool] {
			t.Errorf("expected %q to be in approvalProneTools", tool)
		}
	}
	// Non-approval tool should not be present
	if approvalProneTools["Read"] {
		t.Error("Read should not be in approvalProneTools")
	}
}
