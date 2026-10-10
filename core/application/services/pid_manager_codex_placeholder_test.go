package services_test

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"irrlicht/core/adapters/inbound/agents"
	"irrlicht/core/adapters/inbound/agents/codex"
	"irrlicht/core/application/services"
	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/session"
)

// Issue #2082: a codex TUI's real root binds to the shared app-server daemon's
// PID (#2077), never to the TUI's own PID, so nothing retires the TUI's
// proc-<pid> placeholder by PID. Only the cwd-guarded sweep did, after
// preSessionSweepGrace (90s), and the dashboard showed the real root beside a
// `ready` placeholder for the same TUI for that long.
//
// The argv rows were captured on the dev machine on 2026-10-11 with
// `ps -o args= -p <pid>` (pids 29284, 3766, 70874), paths shortened to their
// last element. The release step is not the subject here, so every rollout
// reads as still held.
var (
	codexManagedDaemonArgv   = []string{"codex", "app-server", "--listen", "unix://", "--analytics-default-enabled", "--managed-daemon"}
	codexVSCodeAppServerArgv = []string{"codex", "-c", "features.code_mode_host=true", "app-server", "--analytics-default-enabled"}
	codexTUIArgv             = []string{"codex", "--yolo"}
)

type codexPlaceholderFixture struct {
	t        *testing.T
	repo     *mockRepo
	cwd      string
	argv     map[int][]string
	released map[string]agent.ReleasedPIDFunc
	consent  func(adapter string) bool
}

func newCodexPlaceholderFixture(t *testing.T) *codexPlaceholderFixture {
	return &codexPlaceholderFixture{
		t: t, repo: newMockRepo(), cwd: t.TempDir(), argv: map[int][]string{},
		released: map[string]agent.ReleasedPIDFunc{
			codex.AdapterName: func(string, string, int) bool { return false },
		},
	}
}

// process starts a live stand-in process whose argv, as the PID manager reads
// it, is argv.
func (f *codexPlaceholderFixture) process(argv []string) int {
	pid := liveProcessForTest(f.t)
	f.argv[pid] = argv
	return pid
}

// placeholder seeds the scanner's proc-<pid> row for a live TUI in f.cwd,
// minted age ago, and returns its id.
func (f *codexPlaceholderFixture) placeholder(age time.Duration) string {
	return f.placeholderFor(f.process(codexTUIArgv), age)
}

// placeholderFor seeds the proc-<pid> row for pid in f.cwd, minted age ago.
func (f *codexPlaceholderFixture) placeholderFor(pid int, age time.Duration) string {
	id := fmt.Sprintf("proc-%d", pid)
	now := time.Now()
	f.repo.states[id] = &session.SessionState{
		SessionID: id, Adapter: codex.AdapterName, State: session.StateReady,
		PID: pid, CWD: f.cwd, FirstSeen: now.Add(-age).Unix(), UpdatedAt: now.Unix(),
	}
	return id
}

// root seeds a codex root in cwd, bound to pid and first seen age ago, whose
// rollout was written just now.
func (f *codexPlaceholderFixture) root(id string, pid int, cwd string, age time.Duration) {
	transcript := filepath.Join(f.t.TempDir(), "rollout-"+id+".jsonl")
	writeTranscript(f.t, transcript, time.Now())
	now := time.Now()
	f.repo.states[id] = &session.SessionState{
		SessionID: id, Adapter: codex.AdapterName, State: session.StateReady,
		PID: pid, CWD: cwd, TranscriptPath: transcript,
		FirstSeen: now.Add(-age).Unix(), UpdatedAt: now.Unix(),
	}
}

// pidManager wires the codex excluder as startup.go does
// (agents.ArgvExcluders → SetInfraReaper), with f.argv as the argv reader.
func (f *codexPlaceholderFixture) pidManager() *services.PIDManager {
	pm := services.NewPIDManager(services.PIDManagerDeps{
		Repo: f.repo, Log: &mockLogger{}, ReadyTTL: 10 * time.Minute,
		ReleasedPIDs:     f.released,
		OnSessionDeleted: func(string) {},
	})
	pm.SetInfraReaper(agents.ArgvExcluders([]agent.Agent{codex.Agent()}),
		func(pid int) []string { return f.argv[pid] })
	if f.consent != nil {
		pm.SetConsentGate(f.consent)
	}
	return pm
}

func (f *codexPlaceholderFixture) present(id string) bool {
	_, err := f.repo.Load(id)
	return err == nil
}

// Red-first (#2082): a TUI placeholder goes within one sweep once a codex root
// in its cwd, newer than it, is bound to the app-server daemon — and the
// retirement hands the placeholder's per-session state to that root.
func TestCheckPIDLiveness_CodexPlaceholderRetiredOnceItsRootBindsToAppServer(t *testing.T) {
	f := newCodexPlaceholderFixture(t)
	tui := f.placeholder(30 * time.Second)
	f.root("codex-root", f.process(codexManagedDaemonArgv), f.cwd, 10*time.Second)
	pm := f.pidManager()
	var superseded [][2]string
	pm.SetSessionSupersededHandler(func(oldID, newID string) {
		superseded = append(superseded, [2]string{oldID, newID})
	})

	pm.CheckPIDLiveness()

	if f.present(tui) {
		t.Fatalf("placeholder %s survived a sweep after a newer codex root in its cwd bound to the app-server daemon", tui)
	}
	if !f.present("codex-root") {
		t.Fatal("the codex root itself was removed")
	}
	if want := [][2]string{{tui, "codex-root"}}; fmt.Sprint(superseded) != fmt.Sprint(want) {
		t.Errorf("superseded hook calls = %v, want %v", superseded, want)
	}
}

// Lock (#2082): with N placeholders and M app-server-bound roots in one cwd,
// the M oldest placeholders go and the rest stay — also on the next sweep,
// which must not let the same roots take more.
func TestCheckPIDLiveness_CodexPlaceholdersRetiredAtMostOnePerRoot(t *testing.T) {
	for _, tc := range []struct {
		name         string
		placeholders int
		roots        int
	}{
		{"two TUIs, one root", 2, 1},
		{"three TUIs, two roots", 3, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newCodexPlaceholderFixture(t)
			var ids []string // oldest first
			for i := range tc.placeholders {
				ids = append(ids, f.placeholder(time.Duration(60-10*i)*time.Second))
			}
			hosts := [][]string{codexManagedDaemonArgv, codexVSCodeAppServerArgv}
			for i := range tc.roots {
				f.root(fmt.Sprintf("codex-root-%d", i), f.process(hosts[i]), f.cwd, time.Duration(10-5*i)*time.Second)
			}
			pm := f.pidManager()

			for sweepN := 1; sweepN <= 2; sweepN++ {
				pm.CheckPIDLiveness()
				for i, id := range ids {
					if want := i >= tc.roots; f.present(id) != want {
						t.Fatalf("after sweep %d placeholder #%d (oldest first) present = %t, want %t",
							sweepN, i, f.present(id), want)
					}
				}
			}
		})
	}
}

// Lock (#2082): a placeholder on the root's own PID — one a daemon from before
// this change minted for the app-server itself — is the PID-match path's to
// retire, and must not use up the root's pairing, which belongs to its TUI's
// placeholder.
func TestCheckPIDLiveness_CodexPlaceholderOnTheRootsOwnPIDLeavesItsPairing(t *testing.T) {
	f := newCodexPlaceholderFixture(t)
	daemon := f.process(codexManagedDaemonArgv)
	ghost := f.placeholderFor(daemon, 60*time.Second)
	tui := f.placeholder(30 * time.Second)
	f.root("codex-root", daemon, f.cwd, 10*time.Second)

	f.pidManager().CheckPIDLiveness()

	if f.present(ghost) {
		t.Errorf("placeholder %s on the root's own pid survived the sweep", ghost)
	}
	if f.present(tui) {
		t.Errorf("TUI placeholder %s survived: the root's pairing went to the placeholder on its own pid", tui)
	}
}

// Locks (#2082): each case leaves the placeholder alone for the sweep, so it
// keeps the grace-period path it had before.
func TestCheckPIDLiveness_CodexPlaceholderPromptRetirementScope(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(f *codexPlaceholderFixture) (placeholder string)
	}{
		{"no root in its cwd", func(f *codexPlaceholderFixture) string {
			id := f.placeholder(30 * time.Second)
			f.root("codex-root", f.process(codexManagedDaemonArgv), f.t.TempDir(), 10*time.Second)
			return id
		}},
		// A TUI opened after the root's thread started is a fresh TUI with no
		// thread yet (the issue's non-goal), not the TUI that root came from.
		{"placeholder newer than the root", func(f *codexPlaceholderFixture) string {
			f.root("codex-root", f.process(codexManagedDaemonArgv), f.cwd, 30*time.Second)
			return f.placeholder(10 * time.Second)
		}},
		// For example a root that inherited a TUI's pid (#2042).
		{"root bound to a TUI, not an app-server", func(f *codexPlaceholderFixture) string {
			id := f.placeholder(30 * time.Second)
			f.root("codex-root", f.process(codexTUIArgv), f.cwd, 10*time.Second)
			return id
		}},
		// The ExcludeArgv contract: no exclusion on the absence of evidence.
		{"root's argv unreadable", func(f *codexPlaceholderFixture) string {
			id := f.placeholder(30 * time.Second)
			f.root("codex-root", f.process(nil), f.cwd, 10*time.Second)
			return id
		}},
		{"observe consent withheld", func(f *codexPlaceholderFixture) string {
			f.consent = func(adapter string) bool { return adapter != codex.AdapterName }
			id := f.placeholder(30 * time.Second)
			f.root("codex-root", f.process(codexManagedDaemonArgv), f.cwd, 10*time.Second)
			return id
		}},
		// Only an adapter whose sessions are hosted by a process that outlives
		// them (it declares ReleasedPID) binds a root away from its client.
		// Another adapter declares one, so the check is per adapter.
		{"adapter declares no release probe", func(f *codexPlaceholderFixture) string {
			f.released = map[string]agent.ReleasedPIDFunc{
				"another-adapter": func(string, string, int) bool { return false },
			}
			id := f.placeholder(30 * time.Second)
			f.root("codex-root", f.process(codexManagedDaemonArgv), f.cwd, 10*time.Second)
			return id
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newCodexPlaceholderFixture(t)
			id := tc.setup(f)

			f.pidManager().CheckPIDLiveness()

			if !f.present(id) {
				t.Fatalf("placeholder %s was retired within its grace period", id)
			}
		})
	}
}

// Lock (#2082): naming the codex app-server in Process.ExcludeArgv must not let
// the #727 infra reaper end an idle codex root the app-server still hosts. That
// reaper ends a root whose bound PID's argv the adapter excludes once its
// transcript and UpdatedAt are stale, and every codex root is bound to an
// app-server (#2077).
func TestCheckPIDLiveness_IdleCodexRootOnAppServerIsNotReapedAsInfra(t *testing.T) {
	f := newCodexPlaceholderFixture(t)
	transcript := filepath.Join(t.TempDir(), "rollout-idle.jsonl")
	writeTranscript(t, transcript, time.Now().Add(-time.Hour))
	f.repo.states["codex-idle"] = &session.SessionState{
		SessionID: "codex-idle", Adapter: codex.AdapterName, State: session.StateReady,
		PID: f.process(codexManagedDaemonArgv), CWD: f.cwd, TranscriptPath: transcript,
		FirstSeen: time.Now().Add(-2 * time.Hour).Unix(),
		UpdatedAt: time.Now().Add(-time.Hour).Unix(), // > the 10m readyTTL
	}

	f.pidManager().CheckPIDLiveness()

	if !f.present("codex-idle") {
		t.Fatal("an idle codex root hosted by the app-server daemon was reaped as an infra-bound ghost")
	}
}
