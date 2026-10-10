//go:build darwin || linux

package services_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"irrlicht/core/adapters/inbound/agents"
	"irrlicht/core/adapters/inbound/agents/codex"
	"irrlicht/core/adapters/inbound/agents/processlifecycle"
	"irrlicht/core/application/services"
	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/session"
)

// Issue #2080: a codex root hosted by the shared managed app-server daemon
// carries the daemon's PID, and the daemon outlives every TUI, so closing the
// TUI never produces process_exited. #2077's same-PID sweep only retires a
// released root that is NOT the newest on the PID; the newest (or only) one
// was never probed and stayed in the dashboard until the daemon exited.
//
// These tests drive the real codex declaration's probes (projected the way
// startup.go projects them) against a real helper process holding rollouts,
// so "released" is what HoldsForWriting answered, never a stub's say-so —
// except the streak, scope and discovery tests, whose subject is
// PIDManager's own bookkeeping and which script the probe to isolate it.

// codexReleasedPIDs projects the released-transcript probe exactly as
// startup.go does, so a codex without a ReleasedPID yields no probe.
func codexReleasedPIDs() map[string]agent.ReleasedPIDFunc {
	return agents.ReleasedPIDs([]agent.Agent{codex.Agent()})
}

// codexPIDDiscoverers projects codex's own PID discovery (the whole-table
// transcript-writer scan) exactly as startup.go does.
func codexPIDDiscoverers() map[string]agent.PIDDiscoverFunc {
	return agents.PIDDiscoverers([]agent.Agent{codex.Agent()})
}

// noOwner is a PID discovery that ran and found nobody, for the tests that
// script the release probe and are not about the last-moment discovery check.
func noOwner(string, string, func([]int) int) (int, error) { return 0, nil }

// newCodexLivenessPIDManager wires the shared-PID owner and the given release
// probes and PID discovery. Pass codexReleasedPIDs and codexPIDDiscoverers
// for production's wiring.
func newCodexLivenessPIDManager(repo *mockRepo, released map[string]agent.ReleasedPIDFunc, discovers map[string]agent.PIDDiscoverFunc) *services.PIDManager {
	return services.NewPIDManager(services.PIDManagerDeps{
		Repo: repo, Log: &mockLogger{}, ReadyTTL: 10 * time.Minute,
		PIDDiscovers:     discovers,
		SharedPIDOwners:  codexSharedPIDOwners(),
		ReleasedPIDs:     released,
		OnSessionDeleted: func(string) {},
	})
}

// sweep runs n liveness sweeps back to back.
func sweep(pm *services.PIDManager, n int) {
	for range n {
		pm.CheckPIDLiveness()
	}
}

// Red-first (#2080): the only codex root on the daemon's PID — the last TUI
// closed — ends once the daemon releases its rollout, and the ending is
// recorded as one transcript_removed for that root.
func TestCheckPIDLiveness_LoneCodexRootEndsWhenItsRolloutIsReleased(t *testing.T) {
	f := newCodexSharedPIDFixture(t)
	repo, first, _ := f.seed(f.holderPID)
	_ = repo.Delete("codex-second")
	f.releaseFirst(t)
	rec := &mockRecorder{}
	pm := newCodexLivenessPIDManager(repo, codexReleasedPIDs(), codexPIDDiscoverers())
	pm.SetRecorder(rec, nil)

	sweep(pm, 2)

	if present, _ := codexRootsPresent(repo); present {
		t.Fatal("a lone codex root whose rollout the daemon released survived two liveness sweeps")
	}
	want := []removedRecord{{first.SessionID, codex.AdapterName, f.first}}
	if got := recordedRemovals(rec); !slices.Equal(got, want) {
		t.Fatalf("transcript_removed events = %+v, want %+v", got, want)
	}
}

// Red-first (#2080): the NEWEST of two roots on the daemon's PID — the one the
// same-PID sweep never asks about — ends once only its rollout is released,
// and the older root, whose rollout is still held, stays.
func TestCheckPIDLiveness_NewestCodexRootEndsWhenOnlyItsRolloutIsReleased(t *testing.T) {
	f := newCodexSharedPIDFixture(t)
	repo, _, second := f.seed(f.holderPID)
	f.releaseSecond(t)
	rec := &mockRecorder{}
	pm := newCodexLivenessPIDManager(repo, codexReleasedPIDs(), codexPIDDiscoverers())
	pm.SetRecorder(rec, nil)

	sweep(pm, 2)

	first, stillSecond := codexRootsPresent(repo)
	if stillSecond {
		t.Fatal("the newest codex root on the daemon pid survived two liveness sweeps after its rollout was released")
	}
	if !first {
		t.Fatal("the liveness sweep removed the older codex root, whose rollout is still held")
	}
	want := []removedRecord{{second.SessionID, codex.AdapterName, f.second}}
	if got := recordedRemovals(rec); !slices.Equal(got, want) {
		t.Fatalf("transcript_removed events = %+v, want %+v", got, want)
	}
}

// Lock (#2080): a probe that cannot answer never ends a root — "could not ask"
// is not "released". The rollout sits in a directory this user may not
// search, so the real codex probe gets an error from HoldsForWriting on both
// platforms (darwin's lsof cannot stat it; linux cannot resolve it), which
// processlifecycle's TestHoldsForWritingUnstattablePathIsNeverANo pins.
func TestCheckPIDLiveness_InconclusiveReleaseProbeNeverEndsCodexRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root, which searches a mode-000 directory anyway, so the probe cannot be made to fail")
	}
	f := newCodexSharedPIDFixture(t)
	locked := filepath.Join(t.TempDir(), "locked")
	if err := os.MkdirAll(locked, 0o755); err != nil {
		t.Fatal(err)
	}
	rollout := filepath.Join(locked, "rollout-2026-10-10T13-42-00-01a1219f-0000-7000-8000-000000000003.jsonl")
	if err := os.WriteFile(rollout, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	// Registered after t.TempDir, so it runs first and TempDir can remove it.
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	if _, err := os.Stat(rollout); !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("stat %s after chmod 000 of its directory = %v, want a permission error — without it this case tests nothing", rollout, err)
	}
	// Fail loudly when the probe answers after all: an answer here would turn
	// this lock into a test of "released", not of "could not ask".
	if held, err := processlifecycle.HoldsForWriting(f.holderPID, rollout); err == nil {
		t.Fatalf("HoldsForWriting(%d, %s) = (%v, nil), want an error — this case needs a probe that cannot answer", f.holderPID, rollout, held)
	}

	now := time.Now().Unix()
	repo := newMockRepo()
	repo.states["codex-unknown"] = &session.SessionState{
		SessionID: "codex-unknown", Adapter: codex.AdapterName, State: session.StateReady,
		PID: f.holderPID, TranscriptPath: rollout, FirstSeen: now, UpdatedAt: now,
	}
	pm := newCodexLivenessPIDManager(repo, codexReleasedPIDs(), codexPIDDiscoverers())

	sweep(pm, 3)

	if _, err := repo.Load("codex-unknown"); err != nil {
		t.Fatal("a codex root whose release probe could not answer was ended by the liveness sweep")
	}
}

// scriptedReleaseProbe answers from a script, one answer per call, and counts
// its calls. The streak and scope tests use it because their subject is
// PIDManager's bookkeeping, not the probe.
type scriptedReleaseProbe struct {
	answers []bool
	calls   int
}

func (p *scriptedReleaseProbe) probe(_, _ string, _ int) bool {
	p.calls++
	return p.answers[min(p.calls, len(p.answers))-1]
}

// liveRoot is a codex root bound to this test's own PID, which is alive for
// the whole test. Its transcript does not exist, which isStaleTranscript
// reads as "not stale", so no other reaper in the sweep removes it.
func liveRoot(id string) *session.SessionState {
	now := time.Now().Unix()
	return &session.SessionState{
		SessionID: id, Adapter: codex.AdapterName, State: session.StateReady,
		PID: os.Getpid(), TranscriptPath: "/nonexistent/" + id + ".jsonl", FirstSeen: now, UpdatedAt: now,
	}
}

// Lock (#2080): one "released" answer is not enough, and any other answer
// resets the count. Released, held, released keeps the root through three
// sweeps; a fourth "released" — the second in a row — ends it. One probe per
// root per sweep.
func TestCheckPIDLiveness_ReleaseStreakResetsOnAnyOtherAnswer(t *testing.T) {
	repo := newMockRepo()
	repo.states["codex-live"] = liveRoot("codex-live")
	p := &scriptedReleaseProbe{answers: []bool{true, false, true, true}}
	pm := newCodexLivenessPIDManager(repo, map[string]agent.ReleasedPIDFunc{codex.AdapterName: p.probe},
		map[string]agent.PIDDiscoverFunc{codex.AdapterName: noOwner})

	for i, answer := range p.answers[:3] {
		pm.CheckPIDLiveness()
		if p.calls != i+1 {
			t.Fatalf("after sweep %d the release probe ran %d times, want %d", i+1, p.calls, i+1)
		}
		if _, err := repo.Load("codex-live"); err != nil {
			t.Fatalf("sweep %d (probe answered released=%v) ended the root; the streak so far never reached two in a row", i+1, answer)
		}
	}
	pm.CheckPIDLiveness()
	if _, err := repo.Load("codex-live"); err == nil {
		t.Fatal("the root survived a second consecutive released answer")
	}
}

// Lock (#2080): the release probe asks only about root sessions with a
// transcript and a live PID, of an adapter that declares it, whose observe
// consent is granted. Every other session is left to the paths that own it,
// and the probe is never run for it.
func TestCheckPIDLiveness_ReleaseProbeScope(t *testing.T) {
	for _, tc := range []struct {
		name    string
		state   func() *session.SessionState
		consent bool
	}{
		{name: "subagent", consent: true, state: func() *session.SessionState {
			s := liveRoot("codex-child")
			s.ParentSessionID, s.State = "codex-parent", session.StateWorking
			return s
		}},
		{name: "pre-session without a transcript", consent: true, state: func() *session.SessionState {
			s := liveRoot("proc-4242")
			s.TranscriptPath = ""
			return s
		}},
		{name: "adapter without a release probe", consent: true, state: func() *session.SessionState {
			s := liveRoot("claude-root")
			s.Adapter = "claude-code"
			return s
		}},
		{name: "observe consent withheld", consent: false, state: func() *session.SessionState {
			return liveRoot("codex-unconsented")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := tc.state()
			repo := newMockRepo()
			repo.states[s.SessionID] = s
			p := &scriptedReleaseProbe{answers: []bool{true}}
			pm := newCodexLivenessPIDManager(repo, map[string]agent.ReleasedPIDFunc{codex.AdapterName: p.probe},
				map[string]agent.PIDDiscoverFunc{codex.AdapterName: noOwner})
			pm.SetConsentGate(func(string) bool { return tc.consent })

			sweep(pm, 3)

			if p.calls != 0 {
				t.Fatalf("case %s: the release probe ran %d times, want 0", tc.name, p.calls)
			}
			if _, err := repo.Load(s.SessionID); err != nil {
				t.Fatalf("case %s: the liveness sweep ended the session", tc.name)
			}
		})
	}
}

// Red-first (#2080 review): the release probe asks about the BOUND pid only,
// so a root bound to a live process that never held its rollout reads
// "released" every sweep — the shape of a codex root that inherited a TUI's
// pre-session PID (#2042) while the app-server daemon writes its rollout.
// Before ending a root, PIDManager asks the adapter's own discovery who
// writes the transcript, and a writer anywhere keeps the root. Here the root
// is bound to this test's process, which holds nothing, while the helper
// holds the rollout.
func TestCheckPIDLiveness_RootBoundToANonHolderIsNotEnded(t *testing.T) {
	f := newCodexSharedPIDFixture(t)
	now := time.Now().Unix()
	repo := newMockRepo()
	repo.states["codex-misbound"] = &session.SessionState{
		SessionID: "codex-misbound", Adapter: codex.AdapterName, State: session.StateReady,
		PID: os.Getpid(), TranscriptPath: f.first, FirstSeen: now, UpdatedAt: now,
	}
	if !codex.ReleasedPID("", f.first, os.Getpid()) {
		t.Fatal("precondition: the release probe does not read this root as released, so this case tests nothing")
	}
	pm := newCodexLivenessPIDManager(repo, codexReleasedPIDs(), codexPIDDiscoverers())

	sweep(pm, 4)

	if _, err := repo.Load("codex-misbound"); err != nil {
		t.Fatalf("a codex root bound to pid %d was ended although pid %d still writes its rollout", os.Getpid(), f.holderPID)
	}
}

// Red-first (#2080 review): at the end of a release streak only a discovery
// that ran and found no owner at all ends the root. An owner, a discovery
// that could not run, or an adapter with no discovery keeps it. A root whose
// discovery named another owner is not asked again — neither the release
// probe nor the whole-table discovery — while its binding stays the same;
// one that could not be asked is, after a new streak.
func TestCheckPIDLiveness_ReleasedRootEndsOnlyWhenDiscoveryFindsNoOwner(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		owner                 int
		err                   error
		undeclared            bool
		ends                  bool
		probeCalls, discCalls int
	}{
		{name: "no owner", ends: true, probeCalls: 2, discCalls: 1},
		{name: "another live owner", owner: 4242, probeCalls: 2, discCalls: 1},
		{name: "discovery could not run", err: errors.New("lsof timed out"), probeCalls: 4, discCalls: 2},
		{name: "no discovery declared", undeclared: true, probeCalls: 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := newMockRepo()
			repo.states["codex-live"] = liveRoot("codex-live")
			p := &scriptedReleaseProbe{answers: []bool{true}}
			discCalls := 0
			discovers := map[string]agent.PIDDiscoverFunc{}
			if !tc.undeclared {
				discovers[codex.AdapterName] = func(string, string, func([]int) int) (int, error) {
					discCalls++
					return tc.owner, tc.err
				}
			}
			pm := newCodexLivenessPIDManager(repo, map[string]agent.ReleasedPIDFunc{codex.AdapterName: p.probe}, discovers)

			sweep(pm, 4)

			_, err := repo.Load("codex-live")
			if ended := err != nil; ended != tc.ends {
				t.Fatalf("case %s: root ended = %v after four sweeps, want %v", tc.name, ended, tc.ends)
			}
			if p.calls != tc.probeCalls || discCalls != tc.discCalls {
				t.Fatalf("case %s: four sweeps ran the release probe %d times and discovery %d times, want %d and %d",
					tc.name, p.calls, discCalls, tc.probeCalls, tc.discCalls)
			}
		})
	}
}
