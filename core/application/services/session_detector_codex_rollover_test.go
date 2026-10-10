//go:build darwin || linux

package services_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"irrlicht/core/adapters/inbound/agents/codex"
	"irrlicht/core/adapters/inbound/agents/muse"
	"irrlicht/core/application/services"
	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/session"
	"irrlicht/core/ports/inbound"
)

// Issue #2080's prerequisite, segment rollover. A paginated codex thread
// continues in a NEW rollout file, rollout-<ts>-<thread>_<segment>.jsonl, whose
// session_meta.id is still the thread id, so the watcher maps it to the same
// session. Observed on the dev machine (2026-10-10): thread 01a1181a rolled
// from a 2026/10/07 rollout to a 2026/10/10 `_01a12665…` segment, and
// `lsof -p` of the managed app-server daemon listed only the new file. A
// session left pointing at the old
// segment reads "released" to every probe of its transcript while the thread
// is alive, so the detector must follow the newer segment.

// rolloverDeadline bounds every wait on the detector's event loop.
const rolloverDeadline = 5 * time.Second

// awaitTranscriptPath polls the stored session until its TranscriptPath is
// want, and fails with the elapsed time otherwise.
func awaitTranscriptPath(t *testing.T, repo *mockRepo, sessionID, want string) {
	t.Helper()
	start := time.Now()
	var got string
	if !pollUntil(rolloverDeadline, func() bool {
		s, err := repo.Load(sessionID)
		if err != nil {
			return false
		}
		got = s.TranscriptPath
		return got == want
	}) {
		t.Fatalf("after %v session %s points at %q, want %q",
			time.Since(start).Round(time.Millisecond), sessionID, got, want)
	}
}

// writeRollout lays down a rollout with the given mtime, set explicitly so
// "newer" never depends on write ordering or timestamp granularity.
func writeRollout(t *testing.T, path string, mtime time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

// runDetector starts det and stops it when the test ends, waiting for Run to
// return so no detector goroutine outlives the test's fixtures.
func runDetector(t *testing.T, det *services.SessionDetector) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-done
	})
}

// newProbedDetector wires the codex probes exactly as startup.go does. The
// repo is seeded BEFORE Run, so the startup path sees the sessions and no
// wait on seedFromDisk is needed.
func newProbedDetector(tw *mockAgentWatcher, repo *mockRepo) *services.SessionDetector {
	return services.NewSessionDetector([]inbound.Watcher{tw}, services.SessionDetectorDeps{
		PW: newMockProcessWatcher(), Repo: repo, Log: &mockLogger{}, Git: &mockGit{},
		Metrics: &mockMetrics{}, Version: "test",
		PIDDiscovers:    codexPIDDiscoverers(),
		SharedPIDOwners: codexSharedPIDOwners(),
		ReleasedPIDs:    codexReleasedPIDs(),
	})
}

// Red-first (#2080): a codex root whose thread rolls over to a newer segment
// follows it, on the event that announces the new file (EventNewSession) and
// on the writes after it (EventActivity). Once the daemon holds only the new
// segment, the root is neither ended by the released-transcript probe nor
// retired as a same-PID victim by #2077's sweep — it shares the daemon's PID
// with a newer root, so the old segment would fail that sweep's probe too.
func TestSessionDetector_CodexRolloverFollowsNewerSegment(t *testing.T) {
	for _, tc := range []struct {
		name string
		typ  agent.EventType
	}{
		{"create event", agent.EventNewSession},
		{"activity event", agent.EventActivity},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const thread = "01a1181a-0349-7712-8ec0-c750ef098f2e"
			const other = "01a12197-2519-75f2-a238-022308ff1721"
			root := filepath.Join(t.TempDir(), "sessions", "2026", "10")
			oldSeg := filepath.Join(root, "07", "rollout-2026-10-07T10-00-00-"+thread+".jsonl")
			newSeg := filepath.Join(root, "10", "rollout-2026-10-10T17-19-15-"+thread+"_01a12665-9cc8-7d70-9842-833231c6622e.jsonl")
			otherRollout := filepath.Join(root, "10", "rollout-2026-10-10T17-20-00-"+other+".jsonl")
			now := time.Now()
			writeRollout(t, oldSeg, now.Add(-30*time.Second))
			writeRollout(t, newSeg, now)
			writeRollout(t, otherRollout, now)
			h := startRolloutHolder(t, oldSeg, newSeg, otherRollout)

			repo := newMockRepo()
			cwd := t.TempDir()
			repo.states[thread] = &session.SessionState{
				SessionID: thread, Adapter: codex.AdapterName, State: session.StateReady, CWD: cwd,
				PID: h.pid, TranscriptPath: oldSeg, FirstSeen: now.Unix() - 10, UpdatedAt: now.Unix(),
			}
			repo.states[other] = &session.SessionState{
				SessionID: other, Adapter: codex.AdapterName, State: session.StateReady, CWD: cwd,
				PID: h.pid, TranscriptPath: otherRollout, FirstSeen: now.Unix(), UpdatedAt: now.Unix(),
			}
			tw := newMockAgentWatcher().withIdentity(agent.Identity{Name: codex.AdapterName})
			det := newProbedDetector(tw, repo)
			runDetector(t, det)

			tw.ch <- agent.Event{Type: tc.typ, SessionID: thread, TranscriptPath: newSeg}
			awaitTranscriptPath(t, repo, thread, newSeg)

			h.release(t, 0)
			det.RunPIDLivenessSweepForTest()
			det.RunPIDLivenessSweepForTest()

			for _, id := range []string{thread, other} {
				if _, err := repo.Load(id); err != nil {
					t.Fatalf("codex root %s was removed although the daemon still holds its current rollout", id)
				}
			}
		})
	}
}

// Lock (#2080): an adapter that declares no ReleasedPID keeps the transcript
// path it already has when an event for the same session arrives from a
// different, newer file. Muse is the case that depends on it: its shadow
// session.jsonl maps to the same id as the nested copy and is created later,
// and only the nested copy holds the subagent's stream (muse.sessionIDFromPath).
func TestSessionDetector_NonOptInSessionKeepsItsTranscriptPath(t *testing.T) {
	for _, typ := range []agent.EventType{agent.EventNewSession, agent.EventActivity} {
		t.Run(string(typ), func(t *testing.T) {
			dir := t.TempDir()
			nested := filepath.Join(dir, "parent", "subagent", "child", "session.jsonl")
			shadow := filepath.Join(dir, "child", "session.jsonl")
			now := time.Now()
			writeRollout(t, nested, now.Add(-30*time.Second))
			writeRollout(t, shadow, now)

			repo := newMockRepo()
			repo.states["child"] = &session.SessionState{
				SessionID: "child", Adapter: muse.AdapterName, State: session.StateReady, CWD: dir,
				PID: os.Getpid(), TranscriptPath: nested, FirstSeen: now.Unix(), UpdatedAt: now.Unix(),
			}
			tw := newMockAgentWatcher().withIdentity(agent.Identity{Name: muse.AdapterName})
			runDetector(t, newProbedDetector(tw, repo))

			tw.ch <- agent.Event{Type: typ, SessionID: "child", TranscriptPath: shadow}
			// One drainWatcher goroutine forwards this watcher's events in
			// order, and Run handles them one at a time, so once a session
			// minted by a LATER event exists, the shadow's event has been
			// handled — the observation this lock needs, without a sleep.
			sentinel := filepath.Join(dir, "sentinel.jsonl")
			writeRollout(t, sentinel, now)
			tw.ch <- agent.Event{Type: agent.EventNewSession, SessionID: "sentinel", ParentSessionID: "child", TranscriptPath: sentinel}
			start := time.Now()
			if !pollUntil(rolloverDeadline, func() bool {
				_, err := repo.Load("sentinel")
				return err == nil
			}) {
				t.Fatalf("after %v the sentinel session was never created, so nothing shows the shadow's event was handled",
					time.Since(start).Round(time.Millisecond))
			}

			s, err := repo.Load("child")
			if err != nil {
				t.Fatal("the muse session disappeared")
			}
			if s.TranscriptPath != nested {
				t.Fatalf("muse session points at %q after its shadow's event, want it kept at %q", s.TranscriptPath, nested)
			}
		})
	}
}
