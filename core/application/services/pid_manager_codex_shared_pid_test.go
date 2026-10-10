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

// codexRolloutHolder is ONE process holding two codex rollouts open for
// writing — the managed app-server daemon's shape. It has to be a child
// process: WriterOf never reports the calling process (darwin's
// writerPIDFromLsof drops self, linux's WriterOf skips os.Getpid()).
type codexRolloutHolder struct {
	pid   int
	stdin io.WriteCloser
}

// startCodexRolloutHolder opens first on fd 3 and second on fd 4, then blocks
// in `read`. read is a shell builtin, so the holder never forks: a forked
// child would inherit both descriptors and show up as a second writer of the
// same files. One line on stdin closes fd 3 (first's rollout released, the
// shape of the daemon unloading a thread) while fd 4 stays open.
//
// It returns only once the real codex probe reports the helper as the writer
// of both files, polled to codexWriterDeadline — never after a sleep.
func startCodexRolloutHolder(t *testing.T, first, second string) *codexRolloutHolder {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c",
		`exec 3>>"$1" 4>>"$2"; read line; exec 3>&-; read line`, "sh", first, second)
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
	h := &codexRolloutHolder{pid: cmd.Process.Pid, stdin: stdin}
	awaitCodexWriter(t, first, h.pid)
	awaitCodexWriter(t, second, h.pid)
	return h
}

// releaseFirst closes the holder's handle on the first rollout and waits until
// the probe sees it released while the second is still held.
func (h *codexRolloutHolder) releaseFirst(t *testing.T, first, second string) {
	t.Helper()
	if _, err := io.WriteString(h.stdin, "\n"); err != nil {
		t.Fatalf("signal rollout holder: %v", err)
	}
	awaitCodexWriter(t, first, 0)
	awaitCodexWriter(t, second, h.pid)
}

// awaitCodexWriter polls codex.DiscoverPID — the production transcript-writer
// probe — until it answers want without error, and fails loudly with the
// elapsed time otherwise. An erroring probe is a retry, not an answer.
func awaitCodexWriter(t *testing.T, path string, want int) {
	t.Helper()
	start := time.Now()
	var got int
	var err error
	for time.Since(start) < codexWriterDeadline {
		got, err = codex.DiscoverPID("", path, nil)
		if err == nil && got == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("after %v the writer of %s is pid %d (err %v), want %d",
		time.Since(start).Round(time.Millisecond), path, got, err, want)
}

// codexSharedPIDFixture lays down two codex rollouts under a codex-shaped
// sessions tree and starts one holder for both. first is the older root.
type codexSharedPIDFixture struct {
	cwd, first, second string
	holder             *codexRolloutHolder
}

func newCodexSharedPIDFixture(t *testing.T) codexSharedPIDFixture {
	t.Helper()
	root := t.TempDir()
	day := filepath.Join(root, "sessions", "2026", "10", "10")
	if err := os.MkdirAll(day, 0o755); err != nil {
		t.Fatal(err)
	}
	f := codexSharedPIDFixture{
		cwd:    t.TempDir(),
		first:  filepath.Join(day, "rollout-2026-10-10T13-40-00-01a1181a-0000-7000-8000-000000000001.jsonl"),
		second: filepath.Join(day, "rollout-2026-10-10T13-41-00-01a12197-0000-7000-8000-000000000002.jsonl"),
	}
	for _, p := range []string{f.first, f.second} {
		if err := os.WriteFile(p, []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	f.holder = startCodexRolloutHolder(t, f.first, f.second)
	return f
}

// roots returns the two root sessions. pidOfSecond lets the assignment test
// leave the newer root unbound, as it is just before its PID is discovered.
func (f codexSharedPIDFixture) roots(pidOfSecond int) (first, second *session.SessionState) {
	now := time.Now().Unix()
	first = &session.SessionState{
		SessionID: "codex-first", Adapter: codex.AdapterName, State: session.StateReady,
		PID: f.holder.pid, CWD: f.cwd, TranscriptPath: f.first,
		FirstSeen: now - 1, UpdatedAt: now,
	}
	second = &session.SessionState{
		SessionID: "codex-second", Adapter: codex.AdapterName, State: session.StateReady,
		PID: pidOfSecond, CWD: f.cwd, TranscriptPath: f.second,
		FirstSeen: now, UpdatedAt: now,
	}
	return first, second
}

// newCodexSharedPIDManager wires ownership probes exactly as startup.go does:
// projected from the real codex declaration, so a codex without a
// SharedPIDOwner yields no probe and the exclusive same-PID policy applies.
func newCodexSharedPIDManager(repo *mockRepo) *services.PIDManager {
	return services.NewPIDManager(services.PIDManagerDeps{
		Repo: repo, Log: &mockLogger{}, ReadyTTL: 10 * time.Minute,
		SharedPIDOwners:  agents.SharedPIDOwners([]agent.Agent{codex.Agent()}),
		OnSessionDeleted: func(string) {},
	})
}

func requireBothCodexRoots(t *testing.T, repo *mockRepo, path string) {
	t.Helper()
	repo.mu.Lock()
	first, second := repo.states["codex-first"], repo.states["codex-second"]
	repo.mu.Unlock()
	if first == nil || second == nil {
		t.Fatalf("%s removed a live codex root sharing the app-server daemon's pid "+
			"(codex-first present=%v, codex-second present=%v)", path, first != nil, second != nil)
	}
}

// Assignment path: the newer root's PID resolves to the daemon the older root
// already carries. Both rollouts are held, so both roots stay.
func TestHandlePIDAssigned_ConcurrentCodexRootsKeepDaemonPID(t *testing.T) {
	f := newCodexSharedPIDFixture(t)
	first, second := f.roots(0)
	repo := newMockRepo()
	repo.states[first.SessionID] = first
	repo.states[second.SessionID] = second

	newCodexSharedPIDManager(repo).HandlePIDAssigned(f.holder.pid, second.SessionID)

	requireBothCodexRoots(t, repo, "assigning the daemon pid to a second codex root")
}

// Periodic path (CheckPIDLiveness → dedupeByPIDPeriodic): two roots already on
// the daemon's PID, both rollouts held, survive a sweep.
func TestCheckPIDLiveness_ConcurrentCodexRootsKeepDaemonPID(t *testing.T) {
	f := newCodexSharedPIDFixture(t)
	first, second := f.roots(f.holder.pid)
	repo := newMockRepo()
	repo.states[first.SessionID] = first
	repo.states[second.SessionID] = second

	newCodexSharedPIDManager(repo).CheckPIDLiveness()

	requireBothCodexRoots(t, repo, "the periodic same-PID sweep")
}

// Startup path (SeedPIDs → dedupeByPID): a daemon restart finding two roots on
// the codex daemon's PID keeps both.
func TestSeedPIDs_ConcurrentCodexRootsKeepDaemonPID(t *testing.T) {
	f := newCodexSharedPIDFixture(t)
	first, second := f.roots(f.holder.pid)
	repo := newMockRepo()
	repo.states[first.SessionID] = first
	repo.states[second.SessionID] = second

	newCodexSharedPIDManager(repo).SeedPIDs([]*session.SessionState{first, second})

	requireBothCodexRoots(t, repo, "the startup same-PID dedup")
}

// Retirement: once the daemon releases the older root's rollout (the shape of
// codex unloading a thread after /new), the periodic sweep retires that root
// and keeps the newer one. Shared ownership must not mean "kept forever".
func TestCheckPIDLiveness_ReleasedCodexRootIsRetired(t *testing.T) {
	f := newCodexSharedPIDFixture(t)
	first, second := f.roots(f.holder.pid)
	repo := newMockRepo()
	repo.states[first.SessionID] = first
	repo.states[second.SessionID] = second
	f.holder.releaseFirst(t, f.first, f.second)

	newCodexSharedPIDManager(repo).CheckPIDLiveness()

	repo.mu.Lock()
	gotFirst, gotSecond := repo.states["codex-first"], repo.states["codex-second"]
	repo.mu.Unlock()
	if gotFirst != nil {
		t.Fatal("a codex root whose rollout the daemon released survived the same-PID sweep")
	}
	if gotSecond == nil {
		t.Fatal("the periodic sweep removed the codex root whose rollout is still held")
	}
}
