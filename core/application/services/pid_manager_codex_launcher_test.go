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
	// launcherDenied withholds the launcher-read grant the hook's answer
	// feeds (the reader itself returns nil then, as startup.go's does).
	launcherDenied bool
	// hostUnknown makes the TUI's read report that its host could not be
	// determined (LauncherEnvReader's hostKnown false).
	hostUnknown bool
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
		if f.launcherDenied {
			return nil, false
		}
		f.reads = append(f.reads, pid)
		if l := f.launchers[pid]; l != nil {
			cp := *l
			return &cp, !(f.hostUnknown && pid == f.tui)
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
	pm.SetLauncherReadGate(func() bool { return !f.launcherDenied })
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

// Review of #2083: the hook's answer only feeds the launcher read, so while
// that read is not granted the hook's process scan buys nothing and is not
// run — on a bind or at startup.
func TestLauncherPIDHookSkippedWhileLauncherReadIsDenied(t *testing.T) {
	f := newCodexLauncherFixture(t)
	f.root("codex-root")
	f.hookPID = f.tui
	f.launcherDenied = true
	pm := f.pidManager()

	pm.HandlePIDAssigned(f.daemon, "codex-root")
	pm.SeedPIDs([]*session.SessionState{f.state("codex-root")})

	if f.hookCalls != 0 {
		t.Errorf("hook ran %d times while the launcher read was denied", f.hookCalls)
	}
	if s := f.state("codex-root"); s.Launcher != nil {
		t.Errorf("launcher = %+v, want none while the read is denied", *s.Launcher)
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
	// The daemon spawned by a TUI in a plain ghostty tab, and a TUI in a
	// tmux pane shown in ghostty.
	ghosttyBare := session.Launcher{TermProgram: "ghostty"}
	ghosttyTmux := session.Launcher{TermProgram: "ghostty", TTY: "/dev/ttys009", TmuxPane: "%3", TmuxSocket: "/private/tmp/tmux-501/default"}
	// A daemon whose spawner sat in a herdr pane, a tmux pane, an iTerm tab
	// or a Terminal.app tab, against TUIs that set different fields from it.
	ghosttyTTY9 := session.Launcher{TermProgram: "ghostty", TTY: "/dev/ttys009"}
	daemonHerdr := session.Launcher{HerdrPaneID: "w1:p1", HerdrSocketPath: "/cfg/herdr/herdr.sock", TermProgram: "ghostty"}
	daemonTmux := session.Launcher{TmuxPane: "%1", TmuxSocket: "/private/tmp/tmux-501/default", TermProgram: "ghostty"}
	daemonITerm := session.Launcher{TermProgram: "iTerm.app", ITermSessionID: "w0t0p0:AAA"}
	daemonTerminal := session.Launcher{TermProgram: "Apple_Terminal", TermSessionID: "AAA"}
	tmuxUnresolved := session.Launcher{TmuxPane: "%3", TmuxSocket: "/private/tmp/tmux-501/default", TTY: "/dev/ttys009"}
	obsidian := session.Launcher{HostBundleID: "md.obsidian", TTY: "/dev/ttys009"}
	iTermTTY1 := session.Launcher{TermProgram: "iTerm.app", TTY: "/dev/ttys001"}
	for _, tc := range []struct {
		name                string
		stored              session.Launcher
		daemonRead, tuiRead session.Launcher
		unique              bool     // the hook names the TUI; else it cannot decide
		boundArgv           []string // the bound PID's argv; nil keeps the managed daemon's
		want                session.Launcher
		noReads             bool // the launcher reader must not be asked at all
		noConsent           bool // codex's observe consent is withheld
		hostUnknown         bool // the TUI's read could not determine its host
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
			// A tty read that came back empty (darwin's `ps` killed by its
			// 2s ceiling) is unknown, not a different terminal: the same
			// TUI's stored tty survives (review of #2083).
			name:   "TUI launcher, same TUI read without a tty",
			stored: ghostty, daemonRead: zed, unique: true, want: ghostty,
			tuiRead: session.Launcher{TermProgram: "ghostty"},
		},
		{
			// The lone TUI's whole launcher is taken, pane address included,
			// not only the fields the ordinary backfill asks for (second
			// review of #2083).
			name: "daemon launcher, lone TUI in a tmux pane", stored: ghosttyBare,
			daemonRead: ghosttyBare, tuiRead: ghosttyTmux, unique: true, want: ghosttyTmux,
		},
		{
			// The daemon inherits its spawner's herdr pane; the lone TUI in a
			// plain tab must not keep it (third review of #2083).
			name: "daemon launcher in a herdr pane, lone TUI in a tab", stored: daemonHerdr,
			daemonRead: daemonHerdr, tuiRead: ghosttyTTY9, unique: true, want: ghosttyTTY9,
		},
		{
			name: "daemon launcher in a tmux pane, lone TUI in a tab", stored: daemonTmux,
			daemonRead: daemonTmux, tuiRead: ghosttyTTY9, unique: true, want: ghosttyTTY9,
		},
		{
			name: "daemon launcher in iTerm, lone TUI in tmux", stored: daemonITerm,
			daemonRead: daemonITerm, tuiRead: tmuxUnresolved, unique: true, want: tmuxUnresolved,
		},
		{
			name: "daemon launcher in Terminal.app, lone TUI in Obsidian", stored: daemonTerminal,
			daemonRead: daemonTerminal, tuiRead: obsidian, unique: true, want: obsidian,
		},
		{
			// Another terminal's tty is never carried onto a read without one.
			name: "other TUI launcher, fresh read without a tty", stored: iTermTTY1,
			daemonRead: zed, tuiRead: session.Launcher{TermProgram: "ghostty"}, unique: true,
			want: session.Launcher{TermProgram: "ghostty"},
		},
		{
			// A read that could not determine its host moves nothing (#1485).
			name: "host unknown on the TUI read", stored: zed, hostUnknown: true,
			daemonRead: zed, tuiRead: ghostty, unique: true, want: zed,
		},
		{
			// Linux reads no controlling tty at all, so neither launcher has
			// one; the terminal program alone tells the TUI from the daemon.
			name: "no tty on either read", stored: zed, daemonRead: zed, unique: true,
			tuiRead: session.Launcher{TermProgram: "ghostty"},
			want:    session.Launcher{TermProgram: "ghostty"},
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
			f.hostUnknown = tc.hostUnknown
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

// Review of #2083: the periodic multiplexer refresh (#1405) re-reads a pane
// launcher on every sweep. A hosted root's PID is the daemon's, whose env
// carries whichever pane the spawning TUI was in, and asking the hook for the
// right process would cost a process scan every sweep, so a hosted root is
// skipped: what is stored stays, and nothing is read or scanned.
func TestCheckPIDLiveness_HostedCodexRootIsSkippedByThePaneRefresh(t *testing.T) {
	const sock = "/cfg/herdr/herdr.sock"
	stored := session.Launcher{HerdrPaneID: "w1:p2", HerdrSocketPath: sock, TermProgram: "iTerm.app", TTY: "/dev/ttys012"}
	f := newCodexLauncherFixture(t)
	// The daemon was spawned by a TUI in another herdr pane.
	f.launchers[f.daemon] = &session.Launcher{HerdrPaneID: "w1:p1", HerdrSocketPath: sock, TermProgram: "zed"}
	f.launchers[f.tui] = &session.Launcher{HerdrPaneID: "w1:p2", HerdrSocketPath: sock, TermProgram: "ghostty", TTY: "/dev/ttys077"}
	f.hookPID = f.tui
	launcher := stored
	f.boundRoot("codex-root", &launcher)
	pm := f.pidManager()

	pm.CheckPIDLiveness()

	if got := launcherOf(f.state("codex-root")); got != stored {
		t.Errorf("launcher = %+v, want the stored %+v", got, stored)
	}
	if len(f.reads) != 0 || f.hookCalls != 0 {
		t.Errorf("the sweep read pids %v and ran the hook %d times for a hosted root, want neither", f.reads, f.hookCalls)
	}
	f.assertBoundToDaemon("codex-root")
}
