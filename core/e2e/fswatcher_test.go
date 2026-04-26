package e2e_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"irrlicht/core/adapters/inbound/agents/fswatcher"
	"irrlicht/core/domain/agent"
)

// TestFSWatcher_EmitsEventsForTranscriptCreateAndModify drives the
// fswatcher adapter against a real temp directory using kqueue/inotify and
// asserts the canonical event sequence (NewSession → Activity) for a
// transcript file's create + append lifecycle.
func TestFSWatcher_EmitsEventsForTranscriptCreateAndModify(t *testing.T) {
	root := realTempDir(t)
	projectDir := "-Users-test-myproject"
	if err := os.MkdirAll(filepath.Join(root, projectDir), 0755); err != nil {
		t.Fatalf("mkdir project dir: %v", err)
	}

	w := fswatcher.NewWithRoot(root, "test", 0)
	ch := w.Subscribe()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	watchDone := make(chan struct{})
	go func() { _ = w.Watch(ctx); close(watchDone) }()

	// Give the watcher a moment to attach kqueue/inotify before we touch files.
	time.Sleep(100 * time.Millisecond)

	transcriptPath := filepath.Join(root, projectDir, "abc-create.jsonl")
	if err := os.WriteFile(transcriptPath, []byte(`{"type":"user"}`+"\n"), 0644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	ev := waitForFSEvent(t, ch, 2*time.Second)
	if ev.Type != agent.EventNewSession {
		t.Errorf("create event type: got %q, want %q", ev.Type, agent.EventNewSession)
	}
	if ev.SessionID != "abc-create" {
		t.Errorf("session ID: got %q, want %q", ev.SessionID, "abc-create")
	}
	if ev.ProjectDir != projectDir {
		t.Errorf("project dir: got %q, want %q", ev.ProjectDir, projectDir)
	}
	if ev.TranscriptPath != transcriptPath {
		t.Errorf("transcript path: got %q, want %q", ev.TranscriptPath, transcriptPath)
	}
	if ev.Adapter != "test" {
		t.Errorf("adapter: got %q, want %q", ev.Adapter, "test")
	}

	// Append to the file — fsnotify should emit a Write, which the watcher
	// translates into EventActivity.
	f, err := os.OpenFile(transcriptPath, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatalf("open for append: %v", err)
	}
	if _, err := f.WriteString(`{"type":"assistant"}` + "\n"); err != nil {
		f.Close()
		t.Fatalf("append write: %v", err)
	}
	f.Close()

	ev = waitForFSEvent(t, ch, 2*time.Second)
	if ev.Type != agent.EventActivity {
		t.Errorf("modify event type: got %q, want %q", ev.Type, agent.EventActivity)
	}
	if ev.SessionID != "abc-create" {
		t.Errorf("modify session ID: got %q, want %q", ev.SessionID, "abc-create")
	}
	if ev.Size == 0 {
		t.Error("modify event size should be non-zero")
	}

	cancel()
	select {
	case <-watchDone:
	case <-time.After(2 * time.Second):
		t.Errorf("watcher did not exit within 2s of context cancel — possible goroutine leak")
	}
}

// waitForFSEvent reads the next event from ch within timeout, failing the
// test on timeout.
func waitForFSEvent(t *testing.T, ch <-chan agent.Event, timeout time.Duration) agent.Event {
	t.Helper()
	select {
	case ev := <-ch:
		return ev
	case <-time.After(timeout):
		t.Fatalf("timeout: no fswatcher event within %s", timeout)
		return agent.Event{}
	}
}
