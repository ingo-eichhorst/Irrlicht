package fswatcher

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

// New treats an absolute dir as-is so adapters can pass an env-var override
// (e.g. PI_CODING_AGENT_SESSION_DIR=/tmp/pi-sessions) without it being
// silently rejoined under $HOME.
func TestNew_AbsoluteDir_UsedAsIs(t *testing.T) {
	w := New("/tmp/pi-sessions", testAdapter, 0)
	if w.Root() != "/tmp/pi-sessions" {
		t.Errorf("Root() = %q, want /tmp/pi-sessions", w.Root())
	}
}

func TestNew_RelativeDir_JoinedWithHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	w := New(".pi/agent/sessions", testAdapter, 0)
	want := filepath.Join(home, ".pi/agent/sessions")
	if w.Root() != want {
		t.Errorf("Root() = %q, want %q", w.Root(), want)
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

// assertNewSessionEvent checks the identity fields of the first event
// TestWatch_EmitsNewSession expects to see for its newly-created transcript.
func assertNewSessionEvent(t *testing.T, ev agent.Event) {
	t.Helper()
	if ev.Type != agent.EventNewSession {
		t.Errorf("first event type = %q, want %q", ev.Type, agent.EventNewSession)
	}
	if ev.SessionID != "abc-123" {
		t.Errorf("session ID = %q, want %q", ev.SessionID, "abc-123")
	}
	if ev.ProjectDir != "-Users-test-myproject" {
		t.Errorf("project dir = %q, want %q", ev.ProjectDir, "-Users-test-myproject")
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

	// Wait for the watch to attach before mutating files — closes the
	// attach race without a guessed sleep.
	select {
	case <-w.Ready():
	case <-time.After(2 * time.Second):
		t.Fatal("watcher did not signal Ready")
	}

	// Create a new transcript file.
	transcriptPath := filepath.Join(root, "-Users-test-myproject", "abc-123.jsonl")
	if err := os.WriteFile(transcriptPath, []byte(`{"type":"user"}`+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// The first event must be the EventNewSession for the file we created.
	// Its Size, however, is best-effort: fsnotify delivers the create event
	// (inotify IN_CREATE) when the file is opened, which can race ahead of
	// the content write, so the create-time stat may read 0. Production
	// treats it as a lifecycle marker and reads the real size from the
	// following activity (Write) event — KindTranscriptNew carries no size
	// filter while KindTranscriptActivity requires FileSize > 0. Mirror that:
	// assert the new-session identity on the first event, then drain until a
	// non-zero size for this file surfaces.
	gotNew := false
	deadline := time.After(2 * time.Second)
	for {
		select {
		case ev := <-ch:
			if !gotNew {
				assertNewSessionEvent(t, ev)
				gotNew = true
			}
			if ev.SessionID == "abc-123" && ev.Size != 0 {
				goto done
			}
		case <-deadline:
			t.Fatal("timed out waiting for new session event with non-zero size")
		}
	}
done:

	cancel()
	if err := <-watchErr; err != nil && err != context.Canceled {
		t.Errorf("Watch returned unexpected error: %v", err)
	}
}

// TestWatch_MetadataParentIsPresentOnFirstNewEvent proves that an adapter
// whose relationship lives in its first transcript record does not expose a
// zero-byte Create as an unlinked top-level session before the header arrives.
func TestWatch_MetadataParentIsPresentOnFirstNewEvent(t *testing.T) {
	root := setupFakeProjects(t)
	w := NewWithRoot(root, testAdapter, 0).WithParentSessionID(func(path string) string {
		contents, _ := os.ReadFile(path)
		if strings.Contains(string(contents), "parent-thread") {
			return "parent-thread"
		}
		return ""
	})
	ch := w.Subscribe()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- w.Watch(ctx) }()
	select {
	case <-w.Ready():
	case <-time.After(2 * time.Second):
		t.Fatal("watcher did not signal Ready")
	}

	path := filepath.Join(root, "-Users-test-myproject", "child.jsonl")
	if err := os.WriteFile(path, []byte("parent-thread\n"), 0644); err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-ch:
		if ev.Type != agent.EventNewSession {
			t.Errorf("event Type = %q, want new_session", ev.Type)
		}
		if ev.ParentSessionID != "parent-thread" {
			t.Errorf("ParentSessionID = %q, want parent-thread", ev.ParentSessionID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for linked new-session event")
	}

	cancel()
	if err := <-done; err != nil && err != context.Canceled {
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

	// Drain the startup EventNewSession emitted for the pre-existing file.
	select {
	case ev := <-ch:
		if ev.Type != agent.EventNewSession {
			t.Fatalf("startup event type = %q, want %q", ev.Type, agent.EventNewSession)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for startup new_session event")
	}

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

	// Drain the startup EventNewSession emitted for the pre-existing file.
	select {
	case ev := <-ch:
		if ev.Type != agent.EventNewSession {
			t.Fatalf("startup event type = %q, want %q", ev.Type, agent.EventNewSession)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for startup new_session event")
	}

	// Remove the transcript file.
	if err := os.Remove(transcriptPath); err != nil {
		t.Fatal(err)
	}

	select {
	case ev := <-ch:
		if ev.Type != agent.EventRemoved {
			t.Errorf("event type = %q, want %q", ev.Type, agent.EventRemoved)
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

// TestWatch_NestedSubdirWithExistingFiles reproduces the bug where
// Claude Code creates a parent session directory together with a
// nested subagents/ directory and subagent transcript files in rapid
// succession. By the time our handler processes the fsnotify Create
// event for the parent dir, the nested subagents/ dir and its files
// already exist on disk. A shallow emitExistingFiles() walk would
// miss them — the fix is to recursively add watches for the entire
// new subtree and emit events for any .jsonl files anywhere in it.
func TestWatch_NestedSubdirWithExistingFiles(t *testing.T) {
	root := setupFakeProjects(t)
	w := NewWithRoot(root, testAdapter, 0)
	ch := w.Subscribe()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	watchErr := make(chan error, 1)
	go func() { watchErr <- w.Watch(ctx) }()

	time.Sleep(100 * time.Millisecond)

	// Build the entire subtree in one go, mimicking Claude Code's
	// "create parent session + subagents dir + agent transcripts" flow.
	// Directory tree:
	//   <parent>/
	//   <parent>/subagents/
	//   <parent>/subagents/agent-a.jsonl
	//   <parent>/subagents/agent-b.jsonl
	parentDir := filepath.Join(root, "-Users-test-myproject", "parent-session-id")
	subagentsDir := filepath.Join(parentDir, "subagents")
	if err := os.MkdirAll(subagentsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subagentsDir, "agent-a.jsonl"), []byte(`{}`+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subagentsDir, "agent-b.jsonl"), []byte(`{}`+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Collect events for up to 1 second. We expect EventNewSession for
	// both agent-a and agent-b; the exact order doesn't matter because
	// fsnotify may reorder Create events.
	seen := map[string]bool{}
	deadline := time.After(2 * time.Second)
	for len(seen) < 2 {
		select {
		case ev := <-ch:
			if ev.Type == agent.EventNewSession && strings.HasPrefix(ev.SessionID, "agent-") {
				seen[ev.SessionID] = true
			}
		case <-deadline:
			t.Fatalf("timed out: saw %d subagent events, want 2 (%v)", len(seen), seen)
		}
	}

	if !seen["agent-a"] {
		t.Error("missing EventNewSession for agent-a")
	}
	if !seen["agent-b"] {
		t.Error("missing EventNewSession for agent-b")
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

// TestAddExistingDirs_NewestFirst is a regression test for issue #998: the
// historical backlog scan visits root's direct children newest-mtime-first
// instead of filepath.WalkDir's lexical order, so a directory that already
// existed when the scan began — but whose name would sort last lexically
// (as a timestamp-prefixed session directory does) — is still processed
// near the front instead of dead last. Directory names are deliberately
// chosen so lexical order ("dir-mid" < "dir-new" < "dir-old") disagrees
// with mtime order, so a regression to lexical ordering would fail this
// test.
func TestAddExistingDirs_NewestFirst(t *testing.T) {
	root := setupFakeProjects(t)

	mkSessionDir := func(name string, age time.Duration) {
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "sess.jsonl"), []byte(`{}`+"\n"), 0644); err != nil {
			t.Fatal(err)
		}
		// Set the directory's own mtime last — writing the file above
		// already bumped it once, so this Chtimes call must come after.
		mtime := time.Now().Add(-age)
		if err := os.Chtimes(dir, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
	mkSessionDir("dir-old", 3*time.Hour)
	mkSessionDir("dir-mid", 2*time.Hour)
	mkSessionDir("dir-new", 1*time.Hour)

	w := NewWithRoot(root, testAdapter, 0)
	ch := w.Subscribe()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	watchErr := make(chan error, 1)
	go func() { watchErr <- w.Watch(ctx) }()

	// setupFakeProjects' own project dir has no transcript in it, so the
	// only startup events come from our three session dirs.
	var order []string
	deadline := time.After(2 * time.Second)
	for len(order) < 3 {
		select {
		case ev := <-ch:
			if ev.Type == agent.EventNewSession {
				order = append(order, ev.ProjectDir)
			}
		case <-deadline:
			t.Fatalf("timed out: got %d of 3 startup events (%v)", len(order), order)
		}
	}

	want := []string{"dir-new", "dir-mid", "dir-old"}
	for i := range want {
		if order[i] != want[i] {
			t.Errorf("startup event order[%d] = %q, want %q (full order: %v)", i, order[i], want[i], order)
		}
	}

	cancel()
	if err := <-watchErr; err != nil && err != context.Canceled {
		t.Errorf("Watch returned unexpected error: %v", err)
	}
}

// TestWatch_NewTopLevelDir_DiscoveredDuringBacklogScan is a regression test
// for issue #998's core defect: a brand-new top-level directory created
// while the historical backlog scan is still in progress must be discovered
// promptly, not only once the entire backlog finishes. It builds a backlog
// large enough (15,000 pre-existing transcript files, calibrated against
// this package's real addExistingDirs — see the issue for the derivation)
// that a full sequential scan measurably takes several hundred milliseconds
// on typical hardware, to make a regression to the old "scan everything,
// then arm the root watch and start draining events" order fail: under that
// order the new directory's event cannot be delivered until the whole
// backlog scan completes and Watch's main select loop starts running. The
// deadline below is deliberately generous relative to that measured scan
// cost (favoring determinism over a tight bound, to avoid CI flakiness)
// while still being short enough that only prompt, backlog-size-independent
// delivery can satisfy it.
func TestWatch_NewTopLevelDir_DiscoveredDuringBacklogScan(t *testing.T) {
	root := setupFakeProjects(t)

	const backlogDirs = 300
	const filesPerDir = 50
	old := time.Now().Add(-48 * time.Hour)
	for i := 0; i < backlogDirs; i++ {
		dir := filepath.Join(root, fmt.Sprintf("backlog-%03d", i))
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		for j := 0; j < filesPerDir; j++ {
			p := filepath.Join(dir, fmt.Sprintf("sess-%02d.jsonl", j))
			if err := os.WriteFile(p, []byte(`{}`+"\n"), 0644); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.Chtimes(dir, old, old); err != nil {
			t.Fatal(err)
		}
	}

	w := NewWithRoot(root, testAdapter, 0)
	ch := w.Subscribe()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	watchErr := make(chan error, 1)
	go func() { watchErr <- w.Watch(ctx) }()

	// Deliberately NOT synchronized on Ready(): this test's whole point is
	// to catch a regression to Ready() (or the reactive-catch mechanism
	// behind it) firing only after the backlog scan completes, so waiting
	// on Ready() here would make such a regression invisible — by the time
	// a late Ready() fires, the slow part would already be over. A short
	// fixed sleep instead lets the watcher's goroutine start without
	// assuming anything about when (or whether) it signals readiness.
	time.Sleep(50 * time.Millisecond)

	// Create a brand-new top-level directory right away, while the backlog
	// scan above is very likely still in progress.
	newDir := filepath.Join(root, "brand-new-session")
	if err := os.MkdirAll(newDir, 0755); err != nil {
		t.Fatal(err)
	}
	newFile := filepath.Join(newDir, "fresh.jsonl")
	if err := os.WriteFile(newFile, []byte(`{"type":"start"}`+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	deadline := time.After(400 * time.Millisecond)
	for {
		select {
		case ev := <-ch:
			if ev.Type == agent.EventNewSession && ev.ProjectDir == "brand-new-session" {
				cancel()
				if err := <-watchErr; err != nil && err != context.Canceled {
					t.Errorf("Watch returned unexpected error: %v", err)
				}
				return
			}
		case <-deadline:
			t.Fatal("timed out waiting for the new top-level directory to be discovered — discovery appears to be waiting on the full backlog scan")
		}
	}
}
