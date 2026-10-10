//go:build darwin || linux

package services_test

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"irrlicht/core/adapters/inbound/agents"
	"irrlicht/core/adapters/inbound/agents/muse"
	"irrlicht/core/application/services"
	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/session"
)

// Issue #2084: one `muse serve` process (Muse's MSP host mode) holds the
// .session.lock of every session it hosts — format-spec §2 measured 11 at
// once — so muse.DiscoverPID resolves every concurrent root to that one PID,
// and the same-PID supersession of #169 deleted all but one of them. #2077
// found the same failure for codex; this is its muse twin.
//
// These tests reproduce that shape with a real helper process holding two
// session locks open, and wire the PIDManager's ownership probes from the real
// muse declaration through the same projection startup.go uses
// (agents.SharedPIDOwners) — no fake probe. They are built for darwin and
// linux only: those are the two process observers whose WriterOf can see the
// helper's handles (process_other.go's stub reports no writer).

// museLockDeadline bounds every wait on the helper's file handles. Each poll
// is up to two real WriterOf probes (the lock, then the transcript fallback),
// and a single lsof is allowed processlifecycle's 2s shelloutTimeout, so the
// deadline leaves room for several slow probes on a loaded runner.
const museLockDeadline = 10 * time.Second

// museSharedPIDFixture is two muse session directories whose .session.lock
// files are held open for writing by ONE helper process — the `muse serve`
// shape. first and second are the sessions' session.jsonl transcripts, which
// nothing holds; first is the older root. The helper has to be a child
// process: WriterOf never reports the calling process.
type museSharedPIDFixture struct {
	day, first, second string
	holderPID          int
	holderStdin        io.WriteCloser
}

// newMuseSharedPIDFixture lays both session directories down and starts the
// holder, which opens first's lock on fd 3 and second's on fd 4 and then
// blocks in `read`. read is a shell builtin, so the holder never forks: a
// forked child would inherit both descriptors and show up as a second writer.
//
// It returns only once muse.DiscoverPID — which derives each lock path from
// the transcript itself — reports the helper for both sessions, polled to
// museLockDeadline. So a fixture that held the wrong file fails here, loudly.
func newMuseSharedPIDFixture(t *testing.T) *museSharedPIDFixture {
	t.Helper()
	f := &museSharedPIDFixture{day: filepath.Join(t.TempDir(), "sessions", "2026", "10", "10")}
	f.first = writeMuseSession(t, filepath.Join(f.day, "01a1181a-0000-7000-8000-000000000001"))
	f.second = writeMuseSession(t, filepath.Join(f.day, "01a12197-0000-7000-8000-000000000002"))
	cmd := exec.Command("/bin/sh", "-c",
		`exec 3>>"$1" 4>>"$2"; read line; exec 3>&-; read line`, "sh",
		filepath.Join(filepath.Dir(f.first), ".session.lock"),
		filepath.Join(filepath.Dir(f.second), ".session.lock"))
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start lock holder: %v", err)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	f.holderPID, f.holderStdin = cmd.Process.Pid, stdin
	awaitMuseOwner(t, f.first, f.holderPID)
	awaitMuseOwner(t, f.second, f.holderPID)
	return f
}

// writeMuseSession creates dir with a session.jsonl and a .session.lock — the
// lock carrying muse's stale-text shape, which DiscoverPID never reads — and
// returns the transcript path.
func writeMuseSession(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	transcript := filepath.Join(dir, "session.jsonl")
	if err := os.WriteFile(transcript, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".session.lock"), []byte("pid=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return transcript
}

// releaseFirst sends the holder one line, which closes its handle on the first
// session's lock (the shape of muse ending that session) while the second
// stays held, and waits until the probe sees exactly that.
func (f *museSharedPIDFixture) releaseFirst(t *testing.T) {
	t.Helper()
	if _, err := io.WriteString(f.holderStdin, "\n"); err != nil {
		t.Fatalf("signal lock holder: %v", err)
	}
	awaitMuseOwner(t, f.first, 0)
	awaitMuseOwner(t, f.second, f.holderPID)
}

// seed stores both roots in a fresh repo and returns them. pidOfSecond lets the
// assignment test leave the newer root unbound, as it is just before its PID
// is discovered.
func (f *museSharedPIDFixture) seed(pidOfSecond int) (repo *mockRepo, first, second *session.SessionState) {
	now := time.Now().Unix()
	first = &session.SessionState{
		SessionID: "muse-first", Adapter: muse.AdapterName, State: session.StateReady,
		PID: f.holderPID, TranscriptPath: f.first, FirstSeen: now - 1, UpdatedAt: now,
	}
	second = &session.SessionState{
		SessionID: "muse-second", Adapter: muse.AdapterName, State: session.StateReady,
		PID: pidOfSecond, TranscriptPath: f.second, FirstSeen: now, UpdatedAt: now,
	}
	repo = newMockRepo()
	repo.states[first.SessionID] = first
	repo.states[second.SessionID] = second
	return repo, first, second
}

// awaitMuseOwner polls muse.DiscoverPID — the production lock-writer probe —
// until it answers want without error, and fails loudly with the elapsed time
// otherwise. An erroring probe is a retry, not an answer.
func awaitMuseOwner(t *testing.T, transcript string, want int) {
	t.Helper()
	start := time.Now()
	var got int
	var err error
	if !pollUntil(museLockDeadline, func() bool {
		got, err = muse.DiscoverPID("", transcript, nil)
		return err == nil && got == want
	}) {
		t.Fatalf("after %v the muse owner of %s is pid %d (err %v), want %d",
			time.Since(start).Round(time.Millisecond), transcript, got, err, want)
	}
}

// museSharedPIDOwners wires ownership probes exactly as startup.go does:
// projected from the real muse declaration, so a muse without a
// SharedPIDOwner yields no probe and the exclusive same-PID policy applies.
func museSharedPIDOwners() map[string]agent.SharedPIDOwnerFunc {
	return agents.SharedPIDOwners([]agent.Agent{muse.Agent()})
}

func museSessionsPresent(repo *mockRepo, ids ...string) []bool {
	present := make([]bool, len(ids))
	for i, id := range ids {
		_, err := repo.Load(id)
		present[i] = err == nil
	}
	return present
}

func requireBothMuseRoots(t *testing.T, repo *mockRepo, path string) {
	t.Helper()
	if p := museSessionsPresent(repo, "muse-first", "muse-second"); !p[0] || !p[1] {
		t.Fatalf("%s removed a live muse root sharing the muse serve host's pid "+
			"(muse-first present=%v, muse-second present=%v)", path, p[0], p[1])
	}
}

// Assignment path: the newer root's PID resolves to the host the older root
// already carries. Both locks are held, so both roots stay.
func TestHandlePIDAssigned_ConcurrentMuseRootsKeepServePID(t *testing.T) {
	f := newMuseSharedPIDFixture(t)
	repo, _, second := f.seed(0)

	newPIDManagerWithSharedPIDOwners(repo, museSharedPIDOwners()).HandlePIDAssigned(f.holderPID, second.SessionID)

	requireBothMuseRoots(t, repo, "assigning the host pid to a second muse root")
}

// Periodic path (CheckPIDLiveness → dedupeByPIDPeriodic): two roots already on
// the host's PID, both locks held, survive a sweep.
func TestCheckPIDLiveness_ConcurrentMuseRootsKeepServePID(t *testing.T) {
	f := newMuseSharedPIDFixture(t)
	repo, _, _ := f.seed(f.holderPID)

	newPIDManagerWithSharedPIDOwners(repo, museSharedPIDOwners()).CheckPIDLiveness()

	requireBothMuseRoots(t, repo, "the periodic same-PID sweep")
}

// Startup path (SeedPIDs → dedupeByPID): a daemon restart finding two roots on
// the host's PID keeps both.
func TestSeedPIDs_ConcurrentMuseRootsKeepServePID(t *testing.T) {
	f := newMuseSharedPIDFixture(t)
	repo, first, second := f.seed(f.holderPID)

	newPIDManagerWithSharedPIDOwners(repo, museSharedPIDOwners()).SeedPIDs([]*session.SessionState{first, second})

	requireBothMuseRoots(t, repo, "the startup same-PID dedup")
}

// Retirement: once the host releases the older root's lock, every same-PID
// path retires that root, keeps the newer one, and records the retirement as
// transcript_removed — the event offline replay and the ghost view read as
// "this session ended". Shared ownership must not mean "kept forever". The
// assignment case is also the dedicated-mode /clear shape: one process, the
// old session's lock released, a new root binding the same PID.
func TestSamePIDRetirement_ReleasedMuseRootIsRetiredAndRecorded(t *testing.T) {
	t.Run("assignment", func(t *testing.T) {
		requireReleasedMuseRootRetired(t, "assignment", false,
			func(pm *services.PIDManager, f *museSharedPIDFixture, _, second *session.SessionState) {
				pm.HandlePIDAssigned(f.holderPID, second.SessionID)
			})
	})
	t.Run("periodic", func(t *testing.T) {
		requireReleasedMuseRootRetired(t, "periodic", true,
			func(pm *services.PIDManager, _ *museSharedPIDFixture, _, _ *session.SessionState) {
				pm.CheckPIDLiveness()
			})
	})
	t.Run("seed", func(t *testing.T) {
		requireReleasedMuseRootRetired(t, "seed", true,
			func(pm *services.PIDManager, _ *museSharedPIDFixture, first, second *session.SessionState) {
				pm.SeedPIDs([]*session.SessionState{first, second})
			})
	})
}

// requireReleasedMuseRootRetired releases the older root's lock, runs act
// against a recording PIDManager wired from the real muse declaration, and
// requires the older root gone, the newer one kept, and exactly one
// transcript_removed — for the older root. secondBound puts the newer root on
// the holder's PID already (the startup and periodic shapes); the assignment
// path binds it itself.
func requireReleasedMuseRootRetired(t *testing.T, path string, secondBound bool,
	act func(pm *services.PIDManager, f *museSharedPIDFixture, first, second *session.SessionState)) {
	t.Helper()
	f := newMuseSharedPIDFixture(t)
	pidOfSecond := 0
	if secondBound {
		pidOfSecond = f.holderPID
	}
	repo, first, second := f.seed(pidOfSecond)
	f.releaseFirst(t)
	rec := &mockRecorder{}
	pm := newPIDManagerWithSharedPIDOwners(repo, museSharedPIDOwners())
	pm.SetRecorder(rec, nil)

	act(pm, f, first, second)

	p := museSessionsPresent(repo, first.SessionID, second.SessionID)
	if p[0] {
		t.Fatalf("%s path: a muse root whose lock the host released survived the same-PID reconciliation", path)
	}
	if !p[1] {
		t.Fatalf("%s path: removed the muse root whose lock is still held", path)
	}
	want := []removedRecord{{first.SessionID, muse.AdapterName, f.first}}
	if got := recordedRemovals(rec); !slices.Equal(got, want) {
		t.Fatalf("%s path: transcript_removed events = %+v, want %+v", path, got, want)
	}
}

// Lock: a muse subagent — including the goal/verify/skill reminder
// micro-agents muse spawns for every prompt — shares its parent's PID and
// holds no lock of its own here, so its probe would answer false. It survives
// every same-PID path anyway, because isDedupDeleteCandidate never makes a
// subagent a victim (pid_manager.go), and that exemption is checked before
// any ownership probe runs.
func TestSamePIDReconciliation_MuseSubagentOnServePIDIsNeverAVictim(t *testing.T) {
	for _, tc := range []struct {
		name        string
		secondBound bool
		act         func(pm *services.PIDManager, f *museSharedPIDFixture, states []*session.SessionState)
	}{
		{name: "assignment", act: func(pm *services.PIDManager, f *museSharedPIDFixture, _ []*session.SessionState) {
			pm.HandlePIDAssigned(f.holderPID, "muse-second")
		}},
		{name: "periodic", secondBound: true, act: func(pm *services.PIDManager, _ *museSharedPIDFixture, _ []*session.SessionState) {
			pm.CheckPIDLiveness()
		}},
		{name: "seed", secondBound: true, act: func(pm *services.PIDManager, _ *museSharedPIDFixture, states []*session.SessionState) {
			pm.SeedPIDs(states)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newMuseSharedPIDFixture(t)
			pidOfSecond := 0
			if tc.secondBound {
				pidOfSecond = f.holderPID
			}
			repo, first, second := f.seed(pidOfSecond)
			childTranscript := writeMuseSession(t,
				filepath.Join(filepath.Dir(f.first), "subagent", "01a1181b-0000-7000-8000-000000000003"))
			// The lock file exists but nothing holds it: confirm the probe
			// would decline the child before relying on the exemption.
			awaitMuseOwner(t, childTranscript, 0)
			child := &session.SessionState{
				SessionID: "muse-child", Adapter: muse.AdapterName, State: session.StateWorking,
				ParentSessionID: first.SessionID, PID: f.holderPID, TranscriptPath: childTranscript,
				FirstSeen: second.FirstSeen, UpdatedAt: second.UpdatedAt,
			}
			repo.states[child.SessionID] = child

			tc.act(newPIDManagerWithSharedPIDOwners(repo, museSharedPIDOwners()), f,
				[]*session.SessionState{first, second, child})

			// Only the child is asserted: the roots' survival is the
			// concurrency tests' claim, and this lock must not depend on it.
			if _, err := repo.Load(child.SessionID); err != nil {
				t.Fatalf("%s path removed a muse subagent sharing its parent's pid", tc.name)
			}
		})
	}
}
