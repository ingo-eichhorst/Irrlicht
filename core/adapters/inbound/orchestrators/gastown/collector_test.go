package gastown

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

)

// helper: create a minimal Gas Town root with daemon/state.json.
func setupFakeRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()

	daemonDir := filepath.Join(root, "daemon")
	if err := os.MkdirAll(daemonDir, 0755); err != nil {
		t.Fatal(err)
	}

	// rigs.json (marker file)
	if err := os.WriteFile(filepath.Join(root, "rigs.json"), []byte(`{}`), 0644); err != nil {
		t.Fatal(err)
	}

	state := daemonState{
		Running:        true,
		PID:            42,
		StartedAt:      time.Date(2026, 3, 15, 12, 0, 0, 0, time.UTC),
		LastHeartbeat:  time.Date(2026, 3, 20, 18, 0, 0, 0, time.UTC),
		HeartbeatCount: 100,
	}
	data, _ := json.Marshal(state)
	if err := os.WriteFile(filepath.Join(daemonDir, "state.json"), data, 0644); err != nil {
		t.Fatal(err)
	}

	return root
}

func TestNew_DetectsValidRoot(t *testing.T) {
	root := setupFakeRoot(t)
	t.Setenv("GT_ROOT", root)

	c := New()

	if !c.Detected() {
		t.Fatal("expected Detected() == true")
	}
	if c.Root() != root {
		t.Fatalf("Root() = %q, want %q", c.Root(), root)
	}

	s := c.daemonState()
	if s == nil {
		t.Fatal("daemonState() returned nil")
	}
	if !s.Running {
		t.Error("expected Running == true")
	}
	if s.PID != 42 {
		t.Errorf("PID = %d, want 42", s.PID)
	}
	if s.HeartbeatCount != 100 {
		t.Errorf("HeartbeatCount = %d, want 100", s.HeartbeatCount)
	}
}

func TestNew_RejectsInvalidRoot(t *testing.T) {
	t.Setenv("GT_ROOT", t.TempDir()) // empty dir — no daemon/ or rigs.json

	c := New()

	if c.Detected() {
		t.Fatal("expected Detected() == false for empty dir")
	}
	if c.Root() != "" {
		t.Fatalf("Root() = %q, want empty", c.Root())
	}
}

func TestNew_RejectsMissingRigsJSON(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "daemon"), 0755)
	// No rigs.json
	t.Setenv("GT_ROOT", root)

	c := New()
	if c.Detected() {
		t.Fatal("expected Detected() == false without rigs.json")
	}
}

func TestNew_RejectsMissingDaemonDir(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "rigs.json"), []byte(`{}`), 0644)
	// No daemon/
	t.Setenv("GT_ROOT", root)

	c := New()
	if c.Detected() {
		t.Fatal("expected Detected() == false without daemon/")
	}
}

func TestWatch_NotifiesOnFileChange(t *testing.T) {
	root := setupFakeRoot(t)
	t.Setenv("GT_ROOT", root)

	c := New()
	if !c.Detected() {
		t.Fatal("expected detected")
	}

	ch := c.Subscribe()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	watchErr := make(chan error, 1)
	go func() { watchErr <- c.Watch(ctx) }()

	// Give the watcher time to start.
	time.Sleep(100 * time.Millisecond)

	// Write updated state to trigger the watcher.
	updated := daemonState{
		Running:        true,
		PID:            99,
		StartedAt:      time.Date(2026, 3, 20, 12, 0, 0, 0, time.UTC),
		LastHeartbeat:  time.Date(2026, 3, 20, 19, 0, 0, 0, time.UTC),
		HeartbeatCount: 200,
	}
	data, _ := json.Marshal(updated)
	if err := os.WriteFile(stateFilePath(root), data, 0644); err != nil {
		t.Fatal(err)
	}

	select {
	case got := <-ch:
		if got.PID != 99 {
			t.Errorf("subscriber got PID=%d, want 99", got.PID)
		}
		if got.HeartbeatCount != 200 {
			t.Errorf("subscriber got HeartbeatCount=%d, want 200", got.HeartbeatCount)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for state change notification")
	}

	// Verify daemonState() is updated too.
	s := c.daemonState()
	if s == nil || s.PID != 99 {
		t.Errorf("daemonState().PID = %v, want 99", s)
	}

	cancel()
	if err := <-watchErr; err != nil && err != context.Canceled {
		t.Errorf("Watch returned unexpected error: %v", err)
	}
}

func TestWatch_NoDetection_BlocksUntilCancel(t *testing.T) {
	t.Setenv("GT_ROOT", t.TempDir()) // not a valid root

	c := New()
	if c.Detected() {
		t.Fatal("expected not detected")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	err := c.Watch(ctx)
	if err != context.DeadlineExceeded {
		t.Errorf("Watch error = %v, want context.DeadlineExceeded", err)
	}
}

func TestSubscribeUnsubscribe(t *testing.T) {
	root := setupFakeRoot(t)
	t.Setenv("GT_ROOT", root)

	c := New()
	ch := c.Subscribe()

	c.subMu.Lock()
	if len(c.subs) != 1 {
		t.Fatalf("subs count = %d, want 1", len(c.subs))
	}
	c.subMu.Unlock()

	c.Unsubscribe(ch)

	c.subMu.Lock()
	if len(c.subs) != 0 {
		t.Fatalf("subs count after unsubscribe = %d, want 0", len(c.subs))
	}
	c.subMu.Unlock()
}

func TestResolveRoot_EnvOverridesDefault(t *testing.T) {
	t.Setenv("GT_ROOT", "/custom/path")
	got := resolveRoot()
	if got != "/custom/path" {
		t.Errorf("resolveRoot() = %q, want /custom/path", got)
	}
}

func TestIsGasTownRoot(t *testing.T) {
	root := setupFakeRoot(t)
	if !isGasTownRoot(root) {
		t.Error("expected isGasTownRoot() == true for valid root")
	}
	if isGasTownRoot(t.TempDir()) {
		t.Error("expected isGasTownRoot() == false for empty dir")
	}
}

func TestReadStateFile_InvalidJSON(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "state.json")
	os.WriteFile(path, []byte(`{invalid`), 0644)

	_, err := readStateFile(path)
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestReadStateFile_Missing(t *testing.T) {
	_, err := readStateFile("/nonexistent/state.json")
	if err == nil {
		t.Error("expected error for missing file")
	}
}
