package fswatcher

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"

	"irrlicht/core/domain/agent"
)

// newFsnotify returns a real fsnotify watcher for the direct reconcile tests,
// closed when the test ends. reconcile only uses it to arm directories.
func newFsnotify(t *testing.T) *fsnotify.Watcher {
	t.Helper()
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fsw.Close() })
	return fsw
}

// writeTranscript writes content to path, failing the test on error.
func writeTranscript(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

// mkdir creates dir and any parents, failing the test on error, and returns it.
func mkdir(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// expectEvent asserts the next event on ch has the wanted type and session ID.
func expectEvent(t *testing.T, ch <-chan agent.Event, want agent.EventType, sessionID string) agent.Event {
	t.Helper()
	select {
	case ev := <-ch:
		if ev.Type != want {
			t.Errorf("event type = %q, want %q", ev.Type, want)
		}
		if ev.SessionID != sessionID {
			t.Errorf("session ID = %q, want %q", ev.SessionID, sessionID)
		}
		return ev
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %q event for %q", want, sessionID)
		return agent.Event{}
	}
}

// expectNoEvent asserts nothing arrives on ch within d. A negative assertion
// needs some bound; d is kept short because every path that would emit here is
// synchronous with the call under test.
func expectNoEvent(t *testing.T, ch <-chan agent.Event, d time.Duration) {
	t.Helper()
	select {
	case ev := <-ch:
		t.Fatalf("unexpected %q event for %q", ev.Type, ev.TranscriptPath)
	case <-time.After(d):
	}
}

func TestEffectiveReconcileInterval(t *testing.T) {
	tests := []struct {
		name string
		set  time.Duration
		want time.Duration
	}{
		{"zero uses the default", 0, defaultReconcileInterval},
		{"negative disables the sweep", -1, 0},
		{"positive is honored", 250 * time.Millisecond, 250 * time.Millisecond},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := NewWithRoot("/tmp/fake", testAdapter, 0).WithReconcileInterval(tc.set)
			if got := w.effectiveReconcileInterval(); got != tc.want {
				t.Errorf("effectiveReconcileInterval() = %v, want %v", got, tc.want)
			}
		})
	}
}

// A transcript file that fsnotify never reported is surfaced by the sweep —
// the dropped-notification failure mode behind issue #1248, where the symptom
// is an event that never arrives rather than one that arrives late.
func TestReconcile_EmitsNewSessionForFileFsnotifyMissed(t *testing.T) {
	root := setupFakeProjects(t)
	w := NewWithRoot(root, testAdapter, 0)
	ch := w.Subscribe()

	path := filepath.Join(root, "-Users-test-myproject", "ghost.jsonl")
	writeTranscript(t, path, `{"type":"init"}`+"\n")

	// No fsnotify event is ever delivered for ghost.jsonl: the sweep is the
	// only thing that can find it.
	w.reconcile(context.Background(), newFsnotify(t))

	ev := expectEvent(t, ch, agent.EventNewSession, "ghost")
	if ev.ProjectDir != "-Users-test-myproject" {
		t.Errorf("project dir = %q, want %q", ev.ProjectDir, "-Users-test-myproject")
	}
	if ev.Size == 0 {
		t.Error("size = 0, want the file's actual size")
	}
}

// The sweep must be silent when fsnotify is doing its job, or every interval
// would replay the whole tree as new sessions.
func TestReconcile_DoesNotReEmitWhatWasAlreadyReported(t *testing.T) {
	root := setupFakeProjects(t)
	w := NewWithRoot(root, testAdapter, 0)
	ch := w.Subscribe()
	fsw := newFsnotify(t)

	path := filepath.Join(root, "-Users-test-myproject", "abc-123.jsonl")
	writeTranscript(t, path, `{"type":"init"}`+"\n")

	w.reconcile(context.Background(), fsw)
	expectEvent(t, ch, agent.EventNewSession, "abc-123")

	// Nothing changed on disk, so two further sweeps must emit nothing.
	w.reconcile(context.Background(), fsw)
	w.reconcile(context.Background(), fsw)
	expectNoEvent(t, ch, 100*time.Millisecond)
}

// A write fsnotify missed shows up as activity, not as a second new session.
func TestReconcile_EmitsActivityWhenFileGrewUnobserved(t *testing.T) {
	root := setupFakeProjects(t)
	w := NewWithRoot(root, testAdapter, 0)
	ch := w.Subscribe()
	fsw := newFsnotify(t)

	path := filepath.Join(root, "-Users-test-myproject", "abc-123.jsonl")
	writeTranscript(t, path, `{"type":"init"}`+"\n")
	w.reconcile(context.Background(), fsw)
	expectEvent(t, ch, agent.EventNewSession, "abc-123")

	writeTranscript(t, path, `{"type":"init"}`+"\n"+`{"type":"user"}`+"\n")
	w.reconcile(context.Background(), fsw)
	expectEvent(t, ch, agent.EventActivity, "abc-123")
}

// A file the normal event path already emitted must not be re-reported by the
// following sweep — the guard against duplicate events in the healthy case.
func TestReconcile_SilentAfterNormalEventPathEmitted(t *testing.T) {
	root := setupFakeProjects(t)
	w := NewWithRoot(root, testAdapter, 0)
	ch := w.Subscribe()
	fsw := newFsnotify(t)

	path := filepath.Join(root, "-Users-test-myproject", "abc-123.jsonl")
	writeTranscript(t, path, `{"type":"init"}`+"\n")
	// Drive the ordinary create path, as Watch's select loop would.
	w.handleEvent(fsw, fsnotify.Event{Name: path, Op: fsnotify.Create})
	expectEvent(t, ch, agent.EventNewSession, "abc-123")

	w.reconcile(context.Background(), fsw)
	expectNoEvent(t, ch, 100*time.Millisecond)
}

// A recreated path is a new session again, not activity on the old one.
func TestReconcile_RecreatedFileIsANewSessionAgain(t *testing.T) {
	root := setupFakeProjects(t)
	w := NewWithRoot(root, testAdapter, 0)
	ch := w.Subscribe()
	fsw := newFsnotify(t)

	path := filepath.Join(root, "-Users-test-myproject", "abc-123.jsonl")
	writeTranscript(t, path, `{"a":1}`+"\n")
	w.reconcile(context.Background(), fsw)
	expectEvent(t, ch, agent.EventNewSession, "abc-123")

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	w.handleEvent(fsw, fsnotify.Event{Name: path, Op: fsnotify.Remove})
	expectEvent(t, ch, agent.EventRemoved, "abc-123")

	writeTranscript(t, path, `{"a":1}`+"\n")
	w.reconcile(context.Background(), fsw)
	expectEvent(t, ch, agent.EventNewSession, "abc-123")
}

// For header-linked adapters the sweep defers a zero-byte file exactly like a
// zero-byte Create does, so a child never surfaces before its parent link is
// readable.
func TestReconcile_DefersZeroByteFileForHeaderLinkedAdapter(t *testing.T) {
	root := setupFakeProjects(t)
	w := NewWithRoot(root, testAdapter, 0).WithParentSessionID(func(path string) string {
		data, err := os.ReadFile(path)
		if err != nil || len(data) == 0 {
			return ""
		}
		return strings.TrimSuffix(string(data), "\n")
	})
	ch := w.Subscribe()
	fsw := newFsnotify(t)

	path := filepath.Join(root, "-Users-test-myproject", "child.jsonl")
	writeTranscript(t, path, "")
	w.reconcile(context.Background(), fsw)
	expectNoEvent(t, ch, 100*time.Millisecond)

	writeTranscript(t, path, "parent-thread\n")
	w.reconcile(context.Background(), fsw)
	ev := expectEvent(t, ch, agent.EventNewSession, "child")
	if ev.ParentSessionID != "parent-thread" {
		t.Errorf("ParentSessionID = %q, want %q", ev.ParentSessionID, "parent-thread")
	}
}

// The sweep honors the same staleness cutoff as the normal event path.
func TestReconcile_SkipsStaleFile(t *testing.T) {
	root := setupFakeProjects(t)
	w := NewWithRoot(root, testAdapter, 1*time.Hour)
	ch := w.Subscribe()

	path := filepath.Join(root, "-Users-test-myproject", "old.jsonl")
	writeTranscript(t, path, `{"a":1}`+"\n")
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}

	w.reconcile(context.Background(), newFsnotify(t))
	expectNoEvent(t, ch, 100*time.Millisecond)
}

// A custom session-ID extractor returning "" still means "skip this file".
func TestReconcile_SkipsFileWithoutSessionID(t *testing.T) {
	root := setupFakeProjects(t)
	w := NewWithRoot(root, testAdapter, 0).WithSessionID(func(string) string { return "" })
	ch := w.Subscribe()

	path := filepath.Join(root, "-Users-test-myproject", "ignored.jsonl")
	writeTranscript(t, path, `{"a":1}`+"\n")

	w.reconcile(context.Background(), newFsnotify(t))
	expectNoEvent(t, ch, 100*time.Millisecond)
}

// Adapters with a flat layout keep transcripts directly under the root with no
// project directory at all (kiro-cli: sessions/cli/<uuid>.jsonl). Walking only
// the subdirectories would make the sweep inert for them — the fix would ship
// while doing nothing for that adapter.
func TestReconcile_EmitsForFlatLayoutFileDirectlyUnderRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "cli")
	mkdir(t, root)
	w := NewWithRoot(root, testAdapter, 0)
	ch := w.Subscribe()

	writeTranscript(t, filepath.Join(root, "flat-session.jsonl"), `{"a":1}`+"\n")

	w.reconcile(context.Background(), newFsnotify(t))
	expectEvent(t, ch, agent.EventNewSession, "flat-session")
}

// The startup scan has to cover the flat layout too, not just the sweep.
// Ready() promises "every pre-existing transcript file has already been
// emitted"; walking only subdirectories made that false for kiro-cli, whose
// whole backlog sits directly under the root — its sessions were invisible
// after a daemon restart until something wrote to them. The sweep is disabled
// here so this measures the startup scan alone.
func TestWatch_StartupScanEmitsFlatLayoutBacklog(t *testing.T) {
	root := filepath.Join(t.TempDir(), "cli")
	mkdir(t, root)
	writeTranscript(t, filepath.Join(root, "pre-existing.jsonl"), `{"a":1}`+"\n")

	w := NewWithRoot(root, testAdapter, 0).WithReconcileInterval(-1)
	ch := w.Subscribe()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = w.Watch(ctx) }()

	select {
	case <-w.Ready():
	case <-time.After(2 * time.Second):
		t.Fatal("watcher did not signal Ready")
	}
	expectEvent(t, ch, agent.EventNewSession, "pre-existing")
}

// The dedup has to work in both directions. fsnotify's event channel is
// unbuffered, so a Create can sit blocked in the producer while a sweep walks
// the same file and reports it, and only then be delivered to the ordinary
// create path — which would emit a second new-session event for one file.
func TestReconcile_ThenFsnotifyCreateDoesNotDuplicateNewSession(t *testing.T) {
	root := setupFakeProjects(t)
	w := NewWithRoot(root, testAdapter, 0)
	ch := w.Subscribe()
	fsw := newFsnotify(t)

	path := filepath.Join(root, "-Users-test-myproject", "abc-123.jsonl")
	writeTranscript(t, path, `{"type":"init"}`+"\n")

	// Sweep reports it first...
	w.reconcile(context.Background(), fsw)
	expectEvent(t, ch, agent.EventNewSession, "abc-123")

	// ...then the create event that was blocked in the producer arrives.
	w.handleEvent(fsw, fsnotify.Event{Name: path, Op: fsnotify.Create})
	expectNoEvent(t, ch, 100*time.Millisecond)
}

// A directory deleted and recreated at the same path must be armed again.
// fsnotify drops the watch when the directory goes away, so a watcher that
// still believes it is watched would leave that whole subtree event-blind.
func TestHandleEvent_RemovedDirIsRearmedWhenRecreated(t *testing.T) {
	root := setupFakeProjects(t)
	w := NewWithRoot(root, testAdapter, 0)
	w.Subscribe()
	fsw := newFsnotify(t)

	proj := filepath.Join(root, "-Users-test-myproject")
	w.addWatch(fsw, proj)
	if _, armed := w.watched[proj]; !armed {
		t.Fatal("precondition: directory should be armed")
	}

	// The directory goes away; fsnotify reports it and drops its own watch.
	if err := os.RemoveAll(proj); err != nil {
		t.Fatal(err)
	}
	w.handleEvent(fsw, fsnotify.Event{Name: proj, Op: fsnotify.Remove})
	if _, armed := w.watched[proj]; armed {
		t.Fatal("a removed directory must not stay recorded as watched, or it is never re-armed")
	}

	// Recreated at the same path: addWatch must actually attempt the
	// registration again instead of short-circuiting on a stale entry.
	//
	// The retry goes through a fresh fsnotify watcher deliberately. Re-adding
	// the path through the same one races with that watcher's own teardown of
	// the deleted directory's file descriptor and intermittently returns
	// EBADF — observed 1 run in ~150. Production already copes, because a
	// rejected registration is not recorded and the next sweep retries it;
	// asserting on a single immediate attempt would just make this test flaky,
	// which is the opposite of the point of this PR.
	mkdir(t, proj)
	w.addWatch(newFsnotify(t), proj)
	if _, armed := w.watched[proj]; !armed {
		t.Error("a recreated directory should be armed again")
	}
}

// A directory the sweep already armed is not re-registered on the next sweep,
// and one that is still rejected is reported once rather than once per sweep —
// otherwise #1255's log line would repeat every interval forever.
func TestAddWatch_RetriesRejectedDirWithoutRepeatingTheLog(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root bypasses the directory permissions this test relies on")
	}
	root := setupFakeProjects(t)
	log := &recordingLogger{}
	w := NewWithRoot(root, testAdapter, 0).WithLogger(log)
	ch := w.Subscribe()
	fsw := newFsnotify(t)

	blind := filepath.Join(root, "-Users-test-blind")
	mkdir(t, blind)
	if err := os.Chmod(blind, 0000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(blind, 0755) })

	// Three sweeps over a directory that keeps failing: one report, not three.
	for i := 0; i < 3; i++ {
		w.reconcile(context.Background(), fsw)
	}
	if got := log.matching(blind); len(got) != 1 {
		t.Fatalf("logged %d errors naming %q across 3 sweeps, want 1 (all: %v)", len(got), blind, got)
	}
	if _, armed := w.watched[blind]; armed {
		t.Error("a rejected directory must not be recorded as watched, or it is never retried")
	}

	// Once the condition clears, the retry arms it and its contents surface —
	// the blind spot closes without a restart.
	if err := os.Chmod(blind, 0755); err != nil {
		t.Fatal(err)
	}
	writeTranscript(t, filepath.Join(blind, "late.jsonl"), `{"a":1}`+"\n")
	w.reconcile(context.Background(), fsw)
	expectEvent(t, ch, agent.EventNewSession, "late")
	if _, armed := w.watched[blind]; !armed {
		t.Error("a recovered directory should be recorded as watched")
	}
}

// End-to-end through Watch, with a genuinely unobservable file.
//
// A directory whose watch registration failed is one fsnotify structurally
// cannot report on: no event for anything created inside it will ever be
// delivered. That is the same observable situation as a dropped kqueue
// notification, and unlike a real drop it can be produced on demand — which is
// what makes this the deterministic stand-in for issue #1248's failure mode.
//
// The disabled-sweep subtest is the control: it pins that the file really is
// invisible to fsnotify, so the recovered case is evidence about the sweep
// rather than about fsnotify quietly delivering the event anyway.
func TestWatch_ReconcileRecoversFileUnderAnUnwatchableDir(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root bypasses the directory permissions this test relies on")
	}

	run := func(t *testing.T, interval time.Duration) <-chan agent.Event {
		t.Helper()
		root := setupFakeProjects(t)
		blind := filepath.Join(root, "-Users-test-blind")
		mkdir(t, blind)
		// Unreadable at Watch time, so the watch registration for it fails and
		// the directory is never armed.
		if err := os.Chmod(blind, 0000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(blind, 0755) })

		w := NewWithRoot(root, testAdapter, 0).WithReconcileInterval(interval)
		ch := w.Subscribe()
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		go func() { _ = w.Watch(ctx) }()

		select {
		case <-w.Ready():
		case <-time.After(2 * time.Second):
			t.Fatal("watcher did not signal Ready")
		}

		// Now make it readable and drop a transcript in. The root watch cannot
		// see this: creating a file inside blind/ does not change root's own
		// directory entries.
		if err := os.Chmod(blind, 0755); err != nil {
			t.Fatal(err)
		}
		writeTranscript(t, filepath.Join(blind, "unseen.jsonl"), `{"a":1}`+"\n")
		return ch
	}

	t.Run("sweep enabled recovers it", func(t *testing.T) {
		ch := run(t, 50*time.Millisecond)
		ev := expectEvent(t, ch, agent.EventNewSession, "unseen")
		if ev.ProjectDir != "-Users-test-blind" {
			t.Errorf("project dir = %q, want %q", ev.ProjectDir, "-Users-test-blind")
		}
	})

	t.Run("sweep disabled leaves it invisible", func(t *testing.T) {
		ch := run(t, -1)
		expectNoEvent(t, ch, 500*time.Millisecond)
	})
}
