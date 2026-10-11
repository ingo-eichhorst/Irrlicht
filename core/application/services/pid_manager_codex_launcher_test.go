package services_test

import (
	"testing"
	"time"

	"irrlicht/core/adapters/inbound/agents"
	"irrlicht/core/adapters/inbound/agents/codex"
	"irrlicht/core/application/services"
	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/session"
)

// Issue #2083: every daemon-hosted codex root is bound to the shared
// app-server daemon's PID (#2077), and the daemon inherits the environment of
// whichever TUI spawned it, so every root's launcher — and click-to-focus —
// pointed at that one terminal. Process.LauncherPID names the process whose
// env and tty should feed the launcher instead; the session's PID stays the
// daemon's.
//
// These tests drive the PID manager's seam with a stand-in hook. The codex
// rule behind the real hook (exactly one TUI in the root's cwd) is
// table-tested in the codex package, where its process reads can be faked.

// codexLauncherFixture is a codex root bound to a daemon PID, with a launcher
// reader that answers per PID and a hook whose answer the test sets.
type codexLauncherFixture struct {
	t         *testing.T
	repo      *mockRepo
	daemon    int
	tui       int
	launchers map[int]*session.Launcher
	argv      map[int][]string
	hookPID   int // the stand-in hook's answer; 0 = undecidable
	hookCalls int
	reads     []int // PIDs the launcher reader was asked about, in order
	consent   func(adapter string) bool
}

func newCodexLauncherFixture(t *testing.T) *codexLauncherFixture {
	f := &codexLauncherFixture{
		t: t, repo: newMockRepo(),
		// Live, because the seed path ends a session bound to a dead PID
		// before it reaches the launcher backfill.
		daemon: liveProcessForTest(t),
		tui:    liveProcessForTest(t),
	}
	// The daemon's launcher is the env of the TUI that first spawned it — a
	// different terminal from this root's — and it has no controlling tty
	// (`ps -o tty=` reads `??` for the managed daemon on the dev machine).
	f.launchers = map[int]*session.Launcher{
		f.daemon: {TermProgram: "zed"},
		f.tui:    {TermProgram: "ghostty", TTY: "/dev/ttys007"},
	}
	f.argv = map[int][]string{f.daemon: codexManagedDaemonArgv, f.tui: codexTUIArgv}
	return f
}

// root seeds a codex root with no PID yet, in a cwd of its own.
func (f *codexLauncherFixture) root(id string) {
	f.repo.states[id] = &session.SessionState{
		SessionID: id, Adapter: codex.AdapterName, State: session.StateWorking,
		CWD: f.t.TempDir(), TranscriptPath: "/rollouts/rollout-" + id + ".jsonl",
		UpdatedAt: time.Now().Unix(),
	}
}

// boundRoot seeds a codex root already bound to the daemon, carrying launcher.
func (f *codexLauncherFixture) boundRoot(id string, launcher *session.Launcher) *session.SessionState {
	f.root(id)
	s := f.repo.states[id]
	s.PID = f.daemon
	s.Launcher = launcher
	return s
}

func (f *codexLauncherFixture) pidManager() *services.PIDManager {
	pm := newPIDManagerForTest(f.repo)
	pm.SetLauncherEnvReader(func(pid int) (*session.Launcher, bool) {
		f.reads = append(f.reads, pid)
		if l := f.launchers[pid]; l != nil {
			cp := *l
			return &cp, true
		}
		return nil, true
	})
	pm.SetLauncherPIDs(map[string]agent.LauncherPIDFunc{
		codex.AdapterName: func(cwd, transcriptPath string, pid int) int {
			f.hookCalls++
			if pid != f.daemon {
				f.t.Errorf("hook asked about pid %d, want the bound daemon %d", pid, f.daemon)
			}
			return f.hookPID
		},
	})
	pm.SetSessionHosts(agents.SessionHosts([]agent.Agent{codex.Agent()}),
		func(pid int) []string { return f.argv[pid] })
	if f.consent != nil {
		pm.SetConsentGate(f.consent)
	}
	return pm
}

func (f *codexLauncherFixture) state(id string) *session.SessionState {
	f.t.Helper()
	s, err := f.repo.Load(id)
	if err != nil {
		f.t.Fatalf("load %s: %v", id, err)
	}
	return s
}

// assertBoundToDaemon is the PID lock: whatever feeds the launcher, liveness
// and the same-PID policy stay on the daemon.
func (f *codexLauncherFixture) assertBoundToDaemon(id string) {
	f.t.Helper()
	if got := f.state(id).PID; got != f.daemon {
		f.t.Errorf("session PID = %d, want the daemon's %d", got, f.daemon)
	}
}

func launcherOf(s *session.SessionState) session.Launcher {
	if s.Launcher == nil {
		return session.Launcher{}
	}
	return *s.Launcher
}

// Red-first (#2083): a codex root bound to the app-server daemon, whose hook
// names its lone TUI, takes that TUI's launcher — terminal and tty — and keeps
// the daemon's PID.
func TestHandlePIDAssigned_HostedCodexRootTakesItsTUIsLauncher(t *testing.T) {
	f := newCodexLauncherFixture(t)
	f.root("codex-root")
	f.hookPID = f.tui
	pm := f.pidManager()

	pm.HandlePIDAssigned(f.daemon, "codex-root")

	if got, want := launcherOf(f.state("codex-root")), *f.launchers[f.tui]; got != want {
		t.Errorf("launcher = %+v, want the TUI's %+v", got, want)
	}
	f.assertBoundToDaemon("codex-root")
}

// Lock: a hook that cannot decide (two TUIs in the cwd, none, or a root that
// is not daemon-hosted) leaves today's daemon-level attribution.
func TestHandlePIDAssigned_UndecidedLauncherPIDKeepsTheDaemonsLauncher(t *testing.T) {
	f := newCodexLauncherFixture(t)
	f.root("codex-root")
	pm := f.pidManager()

	pm.HandlePIDAssigned(f.daemon, "codex-root")

	if got, want := launcherOf(f.state("codex-root")), *f.launchers[f.daemon]; got != want {
		t.Errorf("launcher = %+v, want the daemon's %+v", got, want)
	}
	if f.hookCalls != 1 {
		t.Errorf("hook calls = %d, want 1", f.hookCalls)
	}
	f.assertBoundToDaemon("codex-root")
}

// Lock: the hook reads codex processes' argv and cwd, so it runs only behind
// codex's observe consent, like SharedPIDOwner and ReleasedPID.
func TestHandlePIDAssigned_LauncherPIDHookIsConsentGated(t *testing.T) {
	f := newCodexLauncherFixture(t)
	f.root("codex-root")
	f.hookPID = f.tui
	f.consent = func(string) bool { return false }
	pm := f.pidManager()

	pm.HandlePIDAssigned(f.daemon, "codex-root")

	if f.hookCalls != 0 {
		t.Errorf("hook ran %d times with observe consent withheld", f.hookCalls)
	}
	if got, want := launcherOf(f.state("codex-root")), *f.launchers[f.daemon]; got != want {
		t.Errorf("launcher = %+v, want the daemon's %+v", got, want)
	}
	f.assertBoundToDaemon("codex-root")
}

// Lock: the hook is keyed by adapter; a session of an adapter that declares
// none never consults it.
func TestHandlePIDAssigned_LauncherPIDHookIgnoredForOtherAdapters(t *testing.T) {
	f := newCodexLauncherFixture(t)
	f.root("claude-root")
	f.repo.states["claude-root"].Adapter = "claude-code"
	f.hookPID = f.tui
	pm := f.pidManager()

	pm.HandlePIDAssigned(f.daemon, "claude-root")

	if f.hookCalls != 0 {
		t.Errorf("codex's hook ran %d times for a claude-code session", f.hookCalls)
	}
	if got, want := launcherOf(f.state("claude-root")), *f.launchers[f.daemon]; got != want {
		t.Errorf("launcher = %+v, want its own PID's %+v", got, want)
	}
}

// The startup backfill re-evaluates a hosted root's attribution (#2083): a
// unique answer is applied, an undecidable one keeps what is stored.
func TestSeedPIDs_HostedCodexRootLauncherReevaluation(t *testing.T) {
	zed := session.Launcher{TermProgram: "zed"} // the daemon's: no tty
	ghostty := session.Launcher{TermProgram: "ghostty", TTY: "/dev/ttys007"}
	// A kitty launcher missing its kitty fields is one the ordinary backfill
	// completes from whatever PID it reads (#326), so the KittyPID it ends up
	// with shows which PID was read.
	kittyPartial := session.Launcher{TermProgram: "kitty", TTY: "/dev/ttys013"}
	kittyTUI := session.Launcher{TermProgram: "kitty", TTY: "/dev/ttys013", KittyPID: 4242}
	kittyDaemon := session.Launcher{TermProgram: "kitty", KittyPID: 1111}
	for _, tc := range []struct {
		name                string
		stored              session.Launcher
		daemonRead, tuiRead session.Launcher
		unique              bool     // the hook names the TUI; else it cannot decide
		boundArgv           []string // the bound PID's argv; nil keeps the managed daemon's
		want                session.Launcher
		noReads             bool // the launcher reader must not be asked at all
		noConsent           bool // codex's observe consent is withheld
	}{
		{
			// A launcher captured from the daemon (before this change, or
			// while the cwd was ambiguous) moves to the TUI now alone there.
			name: "daemon launcher, lone TUI", stored: zed,
			daemonRead: zed, tuiRead: ghostty, unique: true, want: ghostty,
		},
		{
			// The last unique attribution survives a restart into an
			// ambiguous cwd, and nothing is merged in from the daemon.
			name: "TUI launcher, ambiguous cwd", stored: kittyPartial,
			daemonRead: kittyDaemon, tuiRead: kittyTUI, want: kittyPartial, noReads: true,
		},
		{
			// A daemon attribution in a still-ambiguous cwd stays put.
			name: "daemon launcher, ambiguous cwd", stored: zed,
			daemonRead: zed, tuiRead: ghostty, want: zed, noReads: true,
		},
		{
			// The same TUI answering again is an ordinary backfill: missing
			// fields are filled from it, present ones kept.
			name: "TUI launcher, same TUI alone", stored: kittyPartial,
			daemonRead: kittyDaemon, tuiRead: kittyTUI, unique: true, want: kittyTUI,
		},
		{
			// A root not bound to a session host keeps today's backfill from
			// its own PID, whatever the hook would say.
			name: "root bound to a TUI", stored: kittyPartial, boundArgv: codexTUIArgv,
			daemonRead: kittyDaemon, tuiRead: kittyTUI, unique: true,
			want: session.Launcher{TermProgram: "kitty", TTY: "/dev/ttys013", KittyPID: 1111},
		},
		{
			// Without codex's observe consent neither the bound PID's argv nor
			// the TUIs are read, so the root gets today's backfill from its
			// own PID, which only the launcher consent gates.
			name: "observe consent withheld", stored: kittyPartial, noConsent: true,
			daemonRead: kittyDaemon, tuiRead: kittyTUI, unique: true,
			want: session.Launcher{TermProgram: "kitty", TTY: "/dev/ttys013", KittyPID: 1111},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newCodexLauncherFixture(t)
			f.launchers[f.daemon] = &tc.daemonRead
			f.launchers[f.tui] = &tc.tuiRead
			if tc.boundArgv != nil {
				f.argv[f.daemon] = tc.boundArgv
			}
			if tc.unique {
				f.hookPID = f.tui
			}
			if tc.noConsent {
				f.consent = func(string) bool { return false }
			}
			stored := tc.stored
			s := f.boundRoot("codex-root", &stored)
			pm := f.pidManager()

			pm.SeedPIDs([]*session.SessionState{s})

			if got := launcherOf(f.state("codex-root")); got != tc.want {
				t.Errorf("launcher = %+v, want %+v", got, tc.want)
			}
			if tc.noReads && len(f.reads) != 0 {
				t.Errorf("launcher reader asked about pids %v, want no read", f.reads)
			}
			if tc.noConsent && f.hookCalls != 0 {
				t.Errorf("hook ran %d times with observe consent withheld", f.hookCalls)
			}
			f.assertBoundToDaemon("codex-root")
		})
	}
}
