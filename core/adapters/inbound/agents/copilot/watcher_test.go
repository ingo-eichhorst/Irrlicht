package copilot

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"irrlicht/core/adapters/inbound/agents/fswatcher"
	"irrlicht/core/domain/agent"
)

// TestWatcher_EmitsSessionForNewDirectory proves the adapter's Source wiring
// actually yields a session for Copilot's layout, where each session is a NEW
// directory created at runtime:
//
//	<root>/<session-id>/events.jsonl
//
// That shape is the interesting part. Claude Code writes into project
// directories that usually already exist when the daemon starts, so a watcher
// that only ever attached to directories present at startup would still work
// for it — and would silently never see a single Copilot session. This test
// creates the session directory AFTER the watcher is running, which is what
// a real `copilot` launch does.
//
// It also pins the session id: the transcript filename is the constant
// events.jsonl, so without SessionIDFromPath every session would collapse onto
// the id "events".
func TestWatcher_EmitsSessionForNewDirectory(t *testing.T) {
	root := t.TempDir()
	ch, stop := startWatcher(t, root)

	const sid = "51ffced3-5bba-416f-b898-7338f39e69e8"
	writeSessionDir(t, root, sid)

	awaitSessionEvent(t, ch, sid)
	stop(t)
}

// startWatcher runs the adapter's real watcher wiring over root and returns its
// event channel plus a stop func that cancels it and asserts a clean exit.
func startWatcher(t *testing.T, root string) (<-chan agent.Event, func(*testing.T)) {
	t.Helper()
	w := fswatcher.NewWithRoot(root, AdapterName, 0).WithSessionID(sessionIDFromPath)
	ch := w.Subscribe()
	ctx, cancel := context.WithCancel(context.Background())

	watchErr := make(chan error, 1)
	go func() { watchErr <- w.Watch(ctx) }()

	select {
	case <-w.Ready():
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("watcher did not signal Ready")
	}

	return ch, func(t *testing.T) {
		t.Helper()
		cancel()
		if err := <-watchErr; err != nil && err != context.Canceled {
			t.Errorf("Watch returned unexpected error: %v", err)
		}
	}
}

// writeSessionDir creates <root>/<sid>/events.jsonl the way a real `copilot`
// launch does — directory first, AFTER the watcher is already running.
func writeSessionDir(t *testing.T, root, sid string) {
	t.Helper()
	dir := filepath.Join(root, sid)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	line := `{"type":"session.start","timestamp":"2026-08-02T20:39:00.000Z","data":{"sessionId":"` +
		sid + `","copilotVersion":"1.0.77","context":{"cwd":"/tmp/proj"}}}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, transcriptFilename), []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
}

// awaitSessionEvent blocks until the watcher emits an event carrying sid,
// failing if a differently-identified session arrives first (which would mean
// the id came from the constant filename) or if nothing arrives at all.
func awaitSessionEvent(t *testing.T, ch <-chan agent.Event, sid string) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case ev := <-ch:
			if ev.SessionID == sid {
				return
			}
			if ev.SessionID != "" {
				t.Fatalf("event carried SessionID %q, want %q — session id must come "+
					"from the directory, not the constant filename", ev.SessionID, sid)
			}
		case <-deadline:
			t.Fatal("no event for a session directory created after the watcher started — " +
				"every real Copilot session creates its directory at runtime, so this " +
				"would mean the adapter observes nothing in production")
		}
	}
}
