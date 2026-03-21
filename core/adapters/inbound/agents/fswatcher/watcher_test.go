package fswatcher

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"

	"irrlicht/core/domain/agent"
)

const testAdapter = "test-agent"

// helper: create a minimal projects root with one project subdir.
func setupFakeProjects(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "projects")
	if err := os.MkdirAll(root, 0755); err != nil {
		t.Fatal(err)
	}
	// Create one project subdirectory.
	projDir := filepath.Join(root, "-Users-test-myproject")
	if err := os.MkdirAll(projDir, 0755); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestNewWithRoot(t *testing.T) {
	w := NewWithRoot("/tmp/fake", testAdapter, 0)
	if w.Root() != "/tmp/fake" {
		t.Errorf("Root() = %q, want /tmp/fake", w.Root())
	}
	if w.Adapter() != testAdapter {
		t.Errorf("Adapter() = %q, want %q", w.Adapter(), testAdapter)
	}
}

func TestExtractSessionID(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"/a/b/c586d52b-1c58-47e4-9a79-cf7cd38edbeb.jsonl", "c586d52b-1c58-47e4-9a79-cf7cd38edbeb"},
		{"/a/b/not-a-transcript.json", ""},
		{"/a/b/", ""},
		{"simple.jsonl", "simple"},
	}
	for _, tt := range tests {
		got := extractSessionID(tt.path)
		if got != tt.want {
			t.Errorf("extractSessionID(%q) = %q, want %q", tt.path, got, tt.want)
		}
	}
}

func TestWatch_EmitsNewSession(t *testing.T) {
	root := setupFakeProjects(t)
	w := NewWithRoot(root, testAdapter, 0)

	ch := w.Subscribe()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	watchErr := make(chan error, 1)
	go func() { watchErr <- w.Watch(ctx) }()

	// Give watcher time to start.
	time.Sleep(100 * time.Millisecond)

	// Create a new transcript file.
	transcriptPath := filepath.Join(root, "-Users-test-myproject", "abc-123.jsonl")
	if err := os.WriteFile(transcriptPath, []byte(`{"type":"user"}`+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	select {
	case ev := <-ch:
		if ev.Type != agent.EventNewSession {
			t.Errorf("event type = %q, want %q", ev.Type, agent.EventNewSession)
		}
		if ev.Adapter != testAdapter {
			t.Errorf("adapter = %q, want %q", ev.Adapter, testAdapter)
		}
		if ev.SessionID != "abc-123" {
			t.Errorf("session ID = %q, want %q", ev.SessionID, "abc-123")
		}
		if ev.ProjectDir != "-Users-test-myproject" {
			t.Errorf("project dir = %q, want %q", ev.ProjectDir, "-Users-test-myproject")
		}
		if ev.Size == 0 {
			t.Error("expected non-zero size for new file")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for new session event")
	}

	cancel()
	if err := <-watchErr; err != nil && err != context.Canceled {
		t.Errorf("Watch returned unexpected error: %v", err)
	}
}

func TestWatch_EmitsActivity(t *testing.T) {
	root := setupFakeProjects(t)

	// Pre-create a transcript file.
	transcriptPath := filepath.Join(root, "-Users-test-myproject", "sess-001.jsonl")
	if err := os.WriteFile(transcriptPath, []byte(`{"type":"init"}`+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	w := NewWithRoot(root, testAdapter, 0)
	ch := w.Subscribe()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	watchErr := make(chan error, 1)
	go func() { watchErr <- w.Watch(ctx) }()

	time.Sleep(100 * time.Millisecond)

	// Append to existing transcript — this triggers an Activity event.
	f, err := os.OpenFile(transcriptPath, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(`{"type":"assistant"}` + "\n")
	f.Close()

	select {
	case ev := <-ch:
		if ev.Type != agent.EventActivity {
			t.Errorf("event type = %q, want %q", ev.Type, agent.EventActivity)
		}
		if ev.Adapter != testAdapter {
			t.Errorf("adapter = %q, want %q", ev.Adapter, testAdapter)
		}
		if ev.SessionID != "sess-001" {
			t.Errorf("session ID = %q, want %q", ev.SessionID, "sess-001")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for activity event")
	}

	cancel()
	if err := <-watchErr; err != nil && err != context.Canceled {
		t.Errorf("Watch returned unexpected error: %v", err)
	}
}

func TestWatch_EmitsRemoved(t *testing.T) {
	root := setupFakeProjects(t)

	// Pre-create a transcript file.
	transcriptPath := filepath.Join(root, "-Users-test-myproject", "sess-rm.jsonl")
	if err := os.WriteFile(transcriptPath, []byte(`{"type":"init"}`+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	w := NewWithRoot(root, testAdapter, 0)
	ch := w.Subscribe()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	watchErr := make(chan error, 1)
	go func() { watchErr <- w.Watch(ctx) }()

	time.Sleep(100 * time.Millisecond)

	// Remove the transcript file.
	if err := os.Remove(transcriptPath); err != nil {
		t.Fatal(err)
	}

	select {
	case ev := <-ch:
		if ev.Type != agent.EventRemoved {
			t.Errorf("event type = %q, want %q", ev.Type, agent.EventRemoved)
		}
		if ev.Adapter != testAdapter {
			t.Errorf("adapter = %q, want %q", ev.Adapter, testAdapter)
		}
		if ev.SessionID != "sess-rm" {
			t.Errorf("session ID = %q, want %q", ev.SessionID, "sess-rm")
		}
		if ev.Size != 0 {
			t.Errorf("size = %d, want 0 for removed file", ev.Size)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for removed event")
	}

	cancel()
	if err := <-watchErr; err != nil && err != context.Canceled {
		t.Errorf("Watch returned unexpected error: %v", err)
	}
}

func TestWatch_NewProjectDir(t *testing.T) {
	root := setupFakeProjects(t)
	w := NewWithRoot(root, testAdapter, 0)
	ch := w.Subscribe()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	watchErr := make(chan error, 1)
	go func() { watchErr <- w.Watch(ctx) }()

	time.Sleep(100 * time.Millisecond)

	// Create a new project directory and put a transcript in it.
	newProjDir := filepath.Join(root, "-Users-test-newproject")
	if err := os.MkdirAll(newProjDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Give the watcher time to register the new directory.
	time.Sleep(100 * time.Millisecond)

	transcriptPath := filepath.Join(newProjDir, "new-sess.jsonl")
	if err := os.WriteFile(transcriptPath, []byte(`{"type":"start"}`+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	select {
	case ev := <-ch:
		if ev.Type != agent.EventNewSession {
			t.Errorf("event type = %q, want %q", ev.Type, agent.EventNewSession)
		}
		if ev.SessionID != "new-sess" {
			t.Errorf("session ID = %q, want %q", ev.SessionID, "new-sess")
		}
		if ev.ProjectDir != "-Users-test-newproject" {
			t.Errorf("project dir = %q, want %q", ev.ProjectDir, "-Users-test-newproject")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for event from new project dir")
	}

	cancel()
	if err := <-watchErr; err != nil && err != context.Canceled {
		t.Errorf("Watch returned unexpected error: %v", err)
	}
}

func TestWatch_IgnoresNonJSONL(t *testing.T) {
	root := setupFakeProjects(t)
	w := NewWithRoot(root, testAdapter, 0)
	ch := w.Subscribe()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	watchErr := make(chan error, 1)
	go func() { watchErr <- w.Watch(ctx) }()

	time.Sleep(100 * time.Millisecond)

	// Create a non-.jsonl file — should not trigger an event.
	nonTranscript := filepath.Join(root, "-Users-test-myproject", "config.json")
	if err := os.WriteFile(nonTranscript, []byte(`{}`), 0644); err != nil {
		t.Fatal(err)
	}

	select {
	case ev := <-ch:
		t.Errorf("unexpected event for non-.jsonl file: %+v", ev)
	case <-time.After(300 * time.Millisecond):
		// Good — no event.
	}

	cancel()
	if err := <-watchErr; err != nil && err != context.Canceled {
		t.Errorf("Watch returned unexpected error: %v", err)
	}
}

func TestWatch_EmptyRoot_BlocksUntilCancel(t *testing.T) {
	w := &Watcher{} // empty root
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	err := w.Watch(ctx)
	if err != context.DeadlineExceeded {
		t.Errorf("Watch error = %v, want context.DeadlineExceeded", err)
	}
}

func TestSubscribeUnsubscribe(t *testing.T) {
	w := NewWithRoot(t.TempDir(), testAdapter, 0)
	ch := w.Subscribe()

	w.subMu.Lock()
	if len(w.subs) != 1 {
		t.Fatalf("subs count = %d, want 1", len(w.subs))
	}
	w.subMu.Unlock()

	w.Unsubscribe(ch)

	w.subMu.Lock()
	if len(w.subs) != 0 {
		t.Fatalf("subs count after unsubscribe = %d, want 0", len(w.subs))
	}
	w.subMu.Unlock()
}

func TestWatch_WaitsForRoot(t *testing.T) {
	tmp := t.TempDir()
	root := filepath.Join(tmp, "projects")
	// root doesn't exist yet.

	w := NewWithRoot(root, testAdapter, 0)
	ch := w.Subscribe()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	watchErr := make(chan error, 1)
	go func() { watchErr <- w.Watch(ctx) }()

	time.Sleep(100 * time.Millisecond)

	// Now create root and a project dir.
	if err := os.MkdirAll(filepath.Join(root, "-test-proj"), 0755); err != nil {
		t.Fatal(err)
	}

	// Give watcher time to detect root and add watches.
	time.Sleep(200 * time.Millisecond)

	// Write a transcript.
	tp := filepath.Join(root, "-test-proj", "late-sess.jsonl")
	if err := os.WriteFile(tp, []byte(`{"type":"start"}`+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	select {
	case ev := <-ch:
		if ev.Type != agent.EventNewSession {
			t.Errorf("event type = %q, want %q", ev.Type, agent.EventNewSession)
		}
		if ev.SessionID != "late-sess" {
			t.Errorf("session ID = %q, want %q", ev.SessionID, "late-sess")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for event after delayed root creation")
	}

	cancel()
	if err := <-watchErr; err != nil && err != context.Canceled {
		t.Errorf("Watch returned unexpected error: %v", err)
	}
}

func TestHandleEvent_MaxAge_SkipsStaleFile(t *testing.T) {
	root := setupFakeProjects(t)
	projDir := filepath.Join(root, "-Users-test-myproject")

	w := NewWithRoot(root, testAdapter, 1*time.Hour)
	ch := w.Subscribe()

	// Create a transcript file and backdate its mtime to 2 hours ago.
	stalePath := filepath.Join(projDir, "stale-sess.jsonl")
	if err := os.WriteFile(stalePath, []byte(`{"type":"user"}`+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	staleTime := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(stalePath, staleTime, staleTime); err != nil {
		t.Fatal(err)
	}

	// Create a fresh transcript file (mtime = now).
	freshPath := filepath.Join(projDir, "fresh-sess.jsonl")
	if err := os.WriteFile(freshPath, []byte(`{"type":"user"}`+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Simulate fsnotify Write events for both files via handleEvent directly.
	w.handleEvent(nil, fsnotify.Event{Name: stalePath, Op: fsnotify.Write})
	w.handleEvent(nil, fsnotify.Event{Name: freshPath, Op: fsnotify.Write})

	// Only the fresh file should produce an event.
	select {
	case ev := <-ch:
		if ev.SessionID != "fresh-sess" {
			t.Errorf("expected fresh-sess, got %q", ev.SessionID)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for fresh event")
	}

	// No more events should be queued.
	select {
	case ev := <-ch:
		t.Errorf("unexpected extra event: %+v", ev)
	default:
	}
}

func TestHandleEvent_MaxAge_Zero_DisablesFilter(t *testing.T) {
	root := setupFakeProjects(t)
	projDir := filepath.Join(root, "-Users-test-myproject")

	w := NewWithRoot(root, testAdapter, 0) // maxAge=0 → no filtering
	ch := w.Subscribe()

	stalePath := filepath.Join(projDir, "old-sess.jsonl")
	if err := os.WriteFile(stalePath, []byte(`{"type":"user"}`+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	staleTime := time.Now().Add(-30 * 24 * time.Hour) // 30 days ago
	if err := os.Chtimes(stalePath, staleTime, staleTime); err != nil {
		t.Fatal(err)
	}

	w.handleEvent(nil, fsnotify.Event{Name: stalePath, Op: fsnotify.Write})

	select {
	case ev := <-ch:
		if ev.SessionID != "old-sess" {
			t.Errorf("expected old-sess, got %q", ev.SessionID)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out — event should have been emitted with maxAge=0")
	}
}
