//go:build darwin || linux

package services_test

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"irrlicht/core/adapters/inbound/agents"
	"irrlicht/core/adapters/inbound/agents/codex"
	"irrlicht/core/application/services"
	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/lifecycle"
	"irrlicht/core/domain/session"
)

// Issue #2077: codex ≥0.162 runs every TUI's threads inside ONE shared managed
// app-server daemon, which holds every live rollout open for writing. The
// transcript-writer probe therefore resolves every codex root to the daemon's
// PID, and the same-PID supersession of #169 deleted all but one of them.
//
// These tests reproduce that shape with a real helper process holding two
// rollouts open, and wire the PIDManager's ownership probes from the real
// codex declaration through the same projection startup.go uses
// (agents.SharedPIDOwners) — no fake probe. They are built for darwin and
// linux only: those are the two process observers whose WriterOf can see the
// helper's handles (process_other.go's stub reports no writer).

// codexWriterDeadline bounds every wait on the helper's file handles. Each
// poll is one real WriterOf probe (lsof on darwin, a /proc scan on linux), and
// a single lsof is allowed processlifecycle's 2s shelloutTimeout, so the
// deadline leaves room for several slow probes on a loaded runner.
const codexWriterDeadline = 10 * time.Second

// codexSharedPIDFixture is two codex rollouts held open for writing by ONE
// helper process — the managed app-server daemon's shape. first is the older
// root. The helper has to be a child process: WriterOf never reports the
// calling process (darwin's writerPIDFromLsof drops self, linux's WriterOf
// skips os.Getpid()).
type codexSharedPIDFixture struct {
	first, second string
	holderPID     int
	holderStdin   io.WriteCloser
}

// newCodexSharedPIDFixture lays the rollouts down and starts the holder, which
// opens first on fd 3 and second on fd 4 and then blocks in `read`. read is a
// shell builtin, so the holder never forks: a forked child would inherit both
// descriptors and show up as a second writer of the same files.
//
// It returns only once the real codex probe reports the helper as the writer
// of both files, polled to codexWriterDeadline — never after a sleep.
func newCodexSharedPIDFixture(t *testing.T) *codexSharedPIDFixture {
	t.Helper()
	day := filepath.Join(t.TempDir(), "sessions", "2026", "10", "10")
	if err := os.MkdirAll(day, 0o755); err != nil {
		t.Fatal(err)
	}
	f := &codexSharedPIDFixture{
		first:  filepath.Join(day, "rollout-2026-10-10T13-40-00-01a1181a-0000-7000-8000-000000000001.jsonl"),
		second: filepath.Join(day, "rollout-2026-10-10T13-41-00-01a12197-0000-7000-8000-000000000002.jsonl"),
	}
	for _, p := range []string{f.first, f.second} {
		if err := os.WriteFile(p, []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("/bin/sh", "-c",
		`exec 3>>"$1" 4>>"$2"; read line; exec 3>&-; read line`, "sh", f.first, f.second)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start rollout holder: %v", err)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	f.holderPID, f.holderStdin = cmd.Process.Pid, stdin
	awaitCodexWriter(t, f.first, f.holderPID)
	awaitCodexWriter(t, f.second, f.holderPID)
	return f
}

// releaseFirst sends the holder one line, which closes its handle on the first
// rollout (the shape of the daemon unloading that thread) while the second
// stays held, and waits until the probe sees exactly that.
func (f *codexSharedPIDFixture) releaseFirst(t *testing.T) {
	t.Helper()
	if _, err := io.WriteString(f.holderStdin, "\n"); err != nil {
		t.Fatalf("signal rollout holder: %v", err)
	}
	awaitCodexWriter(t, f.first, 0)
	awaitCodexWriter(t, f.second, f.holderPID)
}

// seed stores both roots in a fresh repo and returns them. pidOfSecond lets the
// assignment test leave the newer root unbound, as it is just before its PID
// is discovered.
func (f *codexSharedPIDFixture) seed(pidOfSecond int) (repo *mockRepo, first, second *session.SessionState) {
	now := time.Now().Unix()
	first = &session.SessionState{
		SessionID: "codex-first", Adapter: codex.AdapterName, State: session.StateReady,
		PID: f.holderPID, TranscriptPath: f.first, FirstSeen: now - 1, UpdatedAt: now,
	}
	second = &session.SessionState{
		SessionID: "codex-second", Adapter: codex.AdapterName, State: session.StateReady,
		PID: pidOfSecond, TranscriptPath: f.second, FirstSeen: now, UpdatedAt: now,
	}
	repo = newMockRepo()
	repo.states[first.SessionID] = first
	repo.states[second.SessionID] = second
	return repo, first, second
}

// awaitCodexWriter polls codex.DiscoverPID — the production transcript-writer
// probe — until it answers want without error, and fails loudly with the
// elapsed time otherwise. An erroring probe is a retry, not an answer.
func awaitCodexWriter(t *testing.T, path string, want int) {
	t.Helper()
	start := time.Now()
	var got int
	var err error
	if !pollUntil(codexWriterDeadline, func() bool {
		got, err = codex.DiscoverPID("", path, nil)
		return err == nil && got == want
	}) {
		t.Fatalf("after %v the writer of %s is pid %d (err %v), want %d",
			time.Since(start).Round(time.Millisecond), path, got, err, want)
	}
}

// codexSharedPIDOwners wires ownership probes exactly as startup.go does:
// projected from the real codex declaration, so a codex without a
// SharedPIDOwner yields no probe and the exclusive same-PID policy applies.
func codexSharedPIDOwners() map[string]agent.SharedPIDOwnerFunc {
	return agents.SharedPIDOwners([]agent.Agent{codex.Agent()})
}

func codexRootsPresent(repo *mockRepo) (first, second bool) {
	_, errFirst := repo.Load("codex-first")
	_, errSecond := repo.Load("codex-second")
	return errFirst == nil, errSecond == nil
}

func requireBothCodexRoots(t *testing.T, repo *mockRepo, path string) {
	t.Helper()
	if first, second := codexRootsPresent(repo); !first || !second {
		t.Fatalf("%s removed a live codex root sharing the app-server daemon's pid "+
			"(codex-first present=%v, codex-second present=%v)", path, first, second)
	}
}

// Assignment path: the newer root's PID resolves to the daemon the older root
// already carries. Both rollouts are held, so both roots stay.
func TestHandlePIDAssigned_ConcurrentCodexRootsKeepDaemonPID(t *testing.T) {
	f := newCodexSharedPIDFixture(t)
	repo, _, second := f.seed(0)

	newPIDManagerWithSharedPIDOwners(repo, codexSharedPIDOwners()).HandlePIDAssigned(f.holderPID, second.SessionID)

	requireBothCodexRoots(t, repo, "assigning the daemon pid to a second codex root")
}

// Periodic path (CheckPIDLiveness → dedupeByPIDPeriodic): two roots already on
// the daemon's PID, both rollouts held, survive a sweep.
func TestCheckPIDLiveness_ConcurrentCodexRootsKeepDaemonPID(t *testing.T) {
	f := newCodexSharedPIDFixture(t)
	repo, _, _ := f.seed(f.holderPID)

	newPIDManagerWithSharedPIDOwners(repo, codexSharedPIDOwners()).CheckPIDLiveness()

	requireBothCodexRoots(t, repo, "the periodic same-PID sweep")
}

// Startup path (SeedPIDs → dedupeByPID): a daemon restart finding two roots on
// the codex daemon's PID keeps both.
func TestSeedPIDs_ConcurrentCodexRootsKeepDaemonPID(t *testing.T) {
	f := newCodexSharedPIDFixture(t)
	repo, first, second := f.seed(f.holderPID)

	newPIDManagerWithSharedPIDOwners(repo, codexSharedPIDOwners()).SeedPIDs([]*session.SessionState{first, second})

	requireBothCodexRoots(t, repo, "the startup same-PID dedup")
}

// Retirement: once the daemon releases the older root's rollout (the shape of
// codex unloading a thread after /new), the periodic sweep retires that root
// and keeps the newer one. Shared ownership must not mean "kept forever".
func TestCheckPIDLiveness_ReleasedCodexRootIsRetired(t *testing.T) {
	f := newCodexSharedPIDFixture(t)
	repo, _, _ := f.seed(f.holderPID)
	f.releaseFirst(t)

	newPIDManagerWithSharedPIDOwners(repo, codexSharedPIDOwners()).CheckPIDLiveness()

	first, second := codexRootsPresent(repo)
	if first {
		t.Fatal("a codex root whose rollout the daemon released survived the same-PID sweep")
	}
	if !second {
		t.Fatal("the periodic sweep removed the codex root whose rollout is still held")
	}
}

// A released codex root's retirement is a real teardown, so every same-PID
// path records it as transcript_removed — the event offline replay and the
// ghost view read as "this session ended". Before #2077 the /new supersession
// always went through the assignment path (cleanupStalePIDHolders, which
// records); with the shared-PID probe it now usually ends on the periodic
// sweep, after codex unloads the old thread, so that path must record too.
func TestSamePIDRetirement_ReleasedCodexRootRecordsTranscriptRemoved(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(f *codexSharedPIDFixture, repo *mockRepo, rec *mockRecorder, first, second *session.SessionState)
		// pidOfSecond: the assignment path binds the newer root's PID itself.
		pidOfSecond func(f *codexSharedPIDFixture) int
	}{
		{
			name:        "assignment",
			pidOfSecond: func(*codexSharedPIDFixture) int { return 0 },
			run: func(f *codexSharedPIDFixture, repo *mockRepo, rec *mockRecorder, _, second *session.SessionState) {
				newRecordingCodexPIDManager(repo, rec).HandlePIDAssigned(f.holderPID, second.SessionID)
			},
		},
		{
			name:        "periodic",
			pidOfSecond: func(f *codexSharedPIDFixture) int { return f.holderPID },
			run: func(_ *codexSharedPIDFixture, repo *mockRepo, rec *mockRecorder, _, _ *session.SessionState) {
				newRecordingCodexPIDManager(repo, rec).CheckPIDLiveness()
			},
		},
		{
			name:        "seed",
			pidOfSecond: func(f *codexSharedPIDFixture) int { return f.holderPID },
			run: func(_ *codexSharedPIDFixture, repo *mockRepo, rec *mockRecorder, first, second *session.SessionState) {
				newRecordingCodexPIDManager(repo, rec).SeedPIDs([]*session.SessionState{first, second})
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newCodexSharedPIDFixture(t)
			repo, first, second := f.seed(tc.pidOfSecond(f))
			f.releaseFirst(t)
			rec := &mockRecorder{}

			tc.run(f, repo, rec, first, second)

			if present, _ := codexRootsPresent(repo); present {
				t.Fatal("precondition: the released codex root was not retired")
			}
			var removed []lifecycle.Event
			for _, ev := range rec.snapshot() {
				if ev.Kind == lifecycle.KindTranscriptRemoved {
					removed = append(removed, ev)
				}
			}
			want := lifecycle.Event{Kind: lifecycle.KindTranscriptRemoved,
				SessionID: first.SessionID, Adapter: codex.AdapterName, TranscriptPath: f.first}
			if len(removed) != 1 || removed[0].SessionID != want.SessionID ||
				removed[0].Adapter != want.Adapter || removed[0].TranscriptPath != want.TranscriptPath {
				t.Fatalf("%s path: transcript_removed events = %+v, want exactly one for %s (%s, %s)",
					tc.name, removed, want.SessionID, want.Adapter, want.TranscriptPath)
			}
		})
	}
}

func newRecordingCodexPIDManager(repo *mockRepo, rec *mockRecorder) *services.PIDManager {
	pm := newPIDManagerWithSharedPIDOwners(repo, codexSharedPIDOwners())
	pm.SetRecorder(rec, nil)
	return pm
}
