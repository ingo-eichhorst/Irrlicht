package workerlist

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"irrlicht/core/domain/orchestrator"
	"irrlicht/core/pkg/atomicfile"
)

// captureLog records log lines so a test can count them.
type captureLog struct {
	mu          sync.Mutex
	errs, infos []string
}

func (l *captureLog) LogInfo(_, _, msg string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.infos = append(l.infos, msg)
}

func (l *captureLog) LogError(_, _, msg string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.errs = append(l.errs, msg)
}

func (l *captureLog) LogProcessingTime(_, _ string, _ int64, _ int, _ string) {}
func (l *captureLog) Close() error                                            { return nil }

func (l *captureLog) counts() (errs, infos int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.errs), len(l.infos)
}

// testAdapter returns an adapter over dir/workerlist.json whose clock reads
// the returned value.
func testAdapter(t *testing.T) (*Adapter, *captureLog, *atomic.Int64) {
	t.Helper()
	log := &captureLog{}
	a := NewAdapter(filepath.Join(t.TempDir(), DefaultFileName), log)
	clock := &atomic.Int64{}
	clock.Store(t0.UnixNano())
	a.now = func() time.Time { return time.Unix(0, clock.Load()).UTC() }
	return a, log, clock
}

// writeAtomic replaces path the way the reporter's writer does: write a temp
// file in the same directory, then rename it over path.
func writeAtomic(t *testing.T, path, content string) {
	t.Helper()
	if err := atomicfile.WriteFile(path, []byte(content)); err != nil {
		t.Fatal(err)
	}
}

// wantTeamStatus requires st to hold exactly one team, with status want.
func wantTeamStatus(t *testing.T, when string, st *orchestrator.State, want string) {
	t.Helper()
	if got := teamStatuses(st); len(got) != 1 || got[0] != want {
		t.Errorf("%s: team statuses %v, want [%s]", when, got, want)
	}
}

// waitState polls a.State() until cond holds, failing with the elapsed time.
func waitState(t *testing.T, a *Adapter, what string, cond func(*orchestrator.State) bool) {
	t.Helper()
	start := time.Now()
	for time.Since(start) < 5*time.Second {
		if st := a.State(); st != nil && cond(st) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("%s: not observed after %v (state %+v)", what, time.Since(start), a.State())
}

func slotID(st *orchestrator.State) string { return st.Codebases[0].Worktrees[0].Workers[0].ID }

// LOCK: a missing file is "no pool" — not running, nothing logged. The
// default path with no file is the normal case for a user without a pool.
func TestAdapter_MissingFileIsNotRunningAndSilent(t *testing.T) {
	a, log, _ := testAdapter(t)
	a.evaluate()
	if st := a.State(); st == nil || st.Running {
		t.Fatalf("state = %+v, want Running=false", st)
	}
	if e, i := log.counts(); e+i != 0 {
		t.Errorf("logged %d errors, %d infos for a missing file; want none", e, i)
	}
}

// An unreadable document after a good one keeps the good snapshot, marks
// every team "unreadable", and logs one error however often it is re-read;
// a good document afterwards clears the mark. Seen red by deleting the
// keep-last-good branch in read (the pool vanished: Running=false), and by
// accepting schema 2 in validate (the "schema 2" row read as healthy).
func TestAdapter_UnreadableKeepsLastGoodSnapshot(t *testing.T) {
	bad := map[string]string{
		"parse error":         `{"schema": 1, "teams": [`,
		"missing team update": `{"schema": 1, "teams": [{"name": "team-a", "workers": []}]}`,
		"schema 2":            strings.Replace(oneSlot(t0, rfc(t0)), `"schema": 1`, `"schema": 2`, 1),
		"unknown state":       strings.Replace(oneSlot(t0, rfc(t0)), `"busy": true`, `"busy": true, "state": "queued"`, 1),
	}
	for name, content := range bad {
		t.Run(name, func(t *testing.T) {
			a, log, _ := testAdapter(t)
			writeAtomic(t, a.path, oneSlot(t0, rfc(t0)))
			a.evaluate()
			wantTeamStatus(t, "good document", a.State(), statusOperational)

			writeAtomic(t, a.path, content)
			a.evaluate()
			a.evaluate()
			st := a.State()
			if !st.Running || slotID(st) != "p-1" {
				t.Fatalf("unreadable document: state %+v, want the last good snapshot", st)
			}
			wantTeamStatus(t, "unreadable document", st, statusUnreadable)
			if e, _ := log.counts(); e != 1 {
				t.Errorf("logged %d errors over two reads of one unreadable file, want 1: %v", e, log.errs)
			}

			writeAtomic(t, a.path, oneSlot(t0, rfc(t0)))
			a.evaluate()
			wantTeamStatus(t, "good again", a.State(), statusOperational)
		})
	}
}

// An unreadable document with no good one before it shows no pool, loudly.
func TestAdapter_UnreadableWithoutGoodSnapshotIsNotRunning(t *testing.T) {
	a, log, _ := testAdapter(t)
	writeAtomic(t, a.path, `{"schema": 1, "teams": [`)
	a.evaluate()
	if st := a.State(); st == nil || st.Running {
		t.Fatalf("state = %+v, want Running=false", st)
	}
	if e, _ := log.counts(); e != 1 {
		t.Errorf("logged %d errors, want 1", e)
	}
}

// A status file that first appears after the watcher started — the normal
// case at the default path — reaches State() with no re-evaluation tick (an
// hour long here). Seen red by watching the file instead of its parent
// directory (watchDir adding a.path): the watch cannot be added while the file
// is missing, so its creation is never seen.
func TestAdapter_WatchSeesFileCreatedAfterStart(t *testing.T) {
	a, _, _ := testAdapter(t)
	a.reevaluate = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = a.Watch(ctx) }()
	waitState(t, a, "initial read of the missing file", func(st *orchestrator.State) bool { return !st.Running })

	writeAtomic(t, a.path, oneSlot(t0, rfc(t0)))
	waitState(t, a, "created file", func(st *orchestrator.State) bool { return st.Running && slotID(st) == "p-1" })
}

// The default path lives in the daemon's own data directory, beside its
// listening unix socket, its addr file and its state files and directories
// (11 entries in this developer's ~/.local/share/irrlicht when measured with
// `ls -A ~/.local/share/irrlicht | wc -l`). A status file created there is
// still seen with no tick: the directory watch tolerates a socket among the
// entries it arms (fsnotify's kqueue addWatch returns early for a socket or a
// pipe — backend_kqueue.go, read for #2148) and
// filters the other entries' events by name.
func TestAdapter_WatchSeesFileCreatedInLiveDataDir(t *testing.T) {
	// os.MkdirTemp rather than t.TempDir: a unix socket path must stay under
	// macOS's 104-byte sun_path limit, which t.TempDir's test-named path
	// overruns.
	dir, err := os.MkdirTemp("", "wl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	ln, err := net.Listen("unix", filepath.Join(dir, "irrlichd.sock"))
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	for _, name := range []string{"irrlichd.addr", "permissions.json", "history.json"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"recordings", "sessions"} {
		if err := os.Mkdir(filepath.Join(dir, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	a := NewAdapter(filepath.Join(dir, DefaultFileName), &captureLog{})
	a.now = func() time.Time { return t0 }
	a.reevaluate = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = a.Watch(ctx) }()
	waitState(t, a, "initial read of the missing file", func(st *orchestrator.State) bool { return !st.Running })

	// Churn in a sibling first: it must not be mistaken for the status file.
	if err := os.WriteFile(filepath.Join(dir, "history.json"), []byte(`{"x":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	writeAtomic(t, a.path, oneSlot(t0, rfc(t0)))
	waitState(t, a, "status file created in the data directory", func(st *orchestrator.State) bool {
		return st.Running && slotID(st) == "p-1"
	})
}

// Review of #2148, red-first on 085088aa8: a Watch whose context was
// cancelled before it ran — a grant toggled straight back off — still read
// and parsed the status file once. No read after a revoke.
func TestAdapter_WatchAfterCancellationReadsNothing(t *testing.T) {
	a, _, _ := testAdapter(t)
	writeAtomic(t, a.path, oneSlot(t0, rfc(t0)))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := a.Watch(ctx); err == nil {
		t.Error("Watch on a cancelled context returned nil")
	}
	if st := a.State(); st != nil {
		t.Errorf("Watch on a cancelled context read the file: state %+v", st)
	}
}

// An atomic replace of an existing file reaches State() with no tick. LOCK on
// macOS: kqueue fsnotify re-arms a replaced file by itself (sendCreateIfNew in
// its backend_kqueue.go), so watching the file instead of the directory was
// run against this test and it stayed green. One replace only: the file is
// laid down before Watch starts so its watch is armed by Add, never racing a
// write.
func TestAdapter_WatchSeesAtomicReplace(t *testing.T) {
	a, _, _ := testAdapter(t)
	a.reevaluate = time.Hour
	writeAtomic(t, a.path, oneSlot(t0, rfc(t0)))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = a.Watch(ctx) }()
	waitState(t, a, "initial read", func(st *orchestrator.State) bool { return st.Running && slotID(st) == "p-1" })

	writeAtomic(t, a.path, strings.Replace(oneSlot(t0, rfc(t0)), `"p-1"`, `"p-2"`, 1))
	waitState(t, a, "replace", func(st *orchestrator.State) bool { return st.Running && slotID(st) == "p-2" })
}

// With the file unchanged, the re-evaluation tick alone turns a busy slot
// stale once the clock passes the threshold. Seen red by making Watch's
// ticker case stop asking for a re-read (tools/lib/workerlist-mutations_test.sh).
func TestAdapter_TickTurnsSlotStaleWithoutFileChange(t *testing.T) {
	a, _, clock := testAdapter(t)
	a.reevaluate = 10 * time.Millisecond
	writeAtomic(t, a.path, oneSlot(t0, rfc(t0)))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = a.Watch(ctx) }()
	stale := func(st *orchestrator.State) bool { return st.Codebases[0].Worktrees[0].Workers[0].Stale }
	waitState(t, a, "fresh slot", func(st *orchestrator.State) bool { return st.Running && !stale(st) })

	clock.Store(t0.Add(181 * time.Second).UnixNano())
	waitState(t, a, "slot stale after the threshold", func(st *orchestrator.State) bool { return st.Running && stale(st) })
}

// A subscriber that has not taken a snapshot yet gets the newest one, not the
// first: evaluate broadcasts only on change, so a dropped newest snapshot
// would leave the monitor behind until the next change. Seen red by removing
// the drain of the pending snapshot in broadcast.
func TestAdapter_SlowSubscriberGetsNewestSnapshot(t *testing.T) {
	a, _, _ := testAdapter(t)
	ch := a.Subscribe()
	writeAtomic(t, a.path, oneSlot(t0, rfc(t0)))
	a.evaluate()
	writeAtomic(t, a.path, strings.Replace(oneSlot(t0, rfc(t0)), `"p-1"`, `"p-2"`, 1))
	a.evaluate()
	select {
	case st := <-ch:
		if slotID(&st) != "p-2" {
			t.Errorf("pending snapshot has work item %q, want the newest (p-2)", slotID(&st))
		}
	default:
		t.Fatal("no snapshot pending")
	}
}
