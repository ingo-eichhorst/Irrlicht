package copilot

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"irrlicht/core/adapters/inbound/agents/fswatcher"
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
	w := fswatcher.NewWithRoot(root, AdapterName, 0).WithSessionID(sessionIDFromPath)

	ch := w.Subscribe()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	watchErr := make(chan error, 1)
	go func() { watchErr <- w.Watch(ctx) }()

	select {
	case <-w.Ready():
	case <-time.After(2 * time.Second):
		t.Fatal("watcher did not signal Ready")
	}

	const sid = "51ffced3-5bba-416f-b898-7338f39e69e8"
	dir := filepath.Join(root, sid)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	line := `{"type":"session.start","timestamp":"2026-08-02T20:39:00.000Z","data":{"sessionId":"` +
		sid + `","copilotVersion":"1.0.77","context":{"cwd":"/tmp/proj"}}}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, transcriptFilename), []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}

	deadline := time.After(10 * time.Second)
	for {
		select {
		case ev := <-ch:
			if ev.SessionID == sid {
				cancel()
				if err := <-watchErr; err != nil && err != context.Canceled {
					t.Errorf("Watch returned unexpected error: %v", err)
				}
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
