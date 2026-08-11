package services_test

import (
	"os"
	"testing"
	"time"

	"irrlicht/core/domain/session"
)

// herdrSession builds a live herdr-hosted session whose stored launcher
// carries the host resolved from whichever client was attached at capture
// time. os.Getpid() keeps the liveness sweep's ESRCH probe satisfied, and a
// fresh UpdatedAt keeps it out of the readyTTL reap, so the only thing these
// tests observe is the launcher.
func herdrSession(host *session.Launcher) *session.SessionState {
	return &session.SessionState{
		SessionID: "s",
		Adapter:   "claude-code",
		State:     session.StateWorking,
		PID:       os.Getpid(),
		UpdatedAt: time.Now().Unix(),
		Launcher:  host,
	}
}

// TestCheckPIDLiveness_HerdrHostRefreshedOnReattach is the #1405 defect: a
// herdr session's host is a *dynamic* property (detach-here-reattach-there is
// the whole point of a persistent multiplexer), but #1350 stores it as a
// capture-time snapshot. Attach in iTerm → bind the session → detach →
// re-attach in Ghostty, and the session keeps pointing at iTerm for the rest
// of its life, across daemon restarts: clicking it raises the wrong window,
// which is the misroute class #1348 was opened to remove.
//
// The periodic liveness sweep is the only thing that runs while a session is
// idle, so it is where the re-resolution has to happen.
func TestCheckPIDLiveness_HerdrHostRefreshedOnReattach(t *testing.T) {
	repo := newMockRepo()
	repo.states["s"] = herdrSession(&session.Launcher{
		HerdrPaneID:     "w1:p1",
		HerdrSocketPath: "/cfg/herdr/herdr.sock",
		TermProgram:     "iTerm.app",
		ITermSessionID:  "w0t0p0-OLD",
		TTY:             "/dev/ttys012",
	})

	pm := newPIDManagerForTest(repo)
	// A fresh read now resolves the client the user re-attached in.
	pm.SetLauncherEnvReader(func(int) *session.Launcher {
		return &session.Launcher{
			HerdrPaneID:     "w1:p1",
			HerdrSocketPath: "/cfg/herdr/herdr.sock",
			TermProgram:     "ghostty",
			TTY:             "/dev/ttys077",
		}
	})

	pm.CheckPIDLiveness()

	got := repo.states["s"].Launcher
	if got == nil {
		t.Fatal("launcher went nil")
	}
	if got.TermProgram != "ghostty" {
		t.Errorf("host not refreshed on the sweep: TermProgram = %q, want ghostty", got.TermProgram)
	}
	if got.ITermSessionID != "" {
		t.Errorf("the previous client's iTerm selector survived the re-attach: %+v", got)
	}
	if got.TTY != "/dev/ttys077" {
		t.Errorf("TTY still names the old client's tab: %q", got.TTY)
	}
	if got.HerdrPaneID != "w1:p1" || got.HerdrSocketPath != "/cfg/herdr/herdr.sock" {
		t.Errorf("the pane address must survive: %+v", got)
	}
}

// TestCheckPIDLiveness_HerdrHostClearedOnDetach is the other half of the same
// defect. Detaching leaves no client, so no window displays the session — and
// #1350's rule is that this is reported honestly rather than by leaving the
// last known host in place. A stale host is worse than none: the app raises
// some unrelated window instead of logging "no attached client".
func TestCheckPIDLiveness_HerdrHostClearedOnDetach(t *testing.T) {
	repo := newMockRepo()
	repo.states["s"] = herdrSession(&session.Launcher{
		HerdrPaneID:     "w1:p1",
		HerdrSocketPath: "/cfg/herdr/herdr.sock",
		TermProgram:     "iTerm.app",
		ITermSessionID:  "w0t0p0-OLD",
		TTY:             "/dev/ttys012",
	})

	pm := newPIDManagerForTest(repo)
	// Nothing attached: the reader resolves no client, so it returns the pane's
	// own address and its own pty and nothing else (ReadLauncherEnv's herdr
	// early-return in launcherFromEnv).
	pm.SetLauncherEnvReader(func(int) *session.Launcher {
		return &session.Launcher{
			HerdrPaneID:     "w1:p1",
			HerdrSocketPath: "/cfg/herdr/herdr.sock",
			TTY:             "/dev/ttys900",
		}
	})

	pm.CheckPIDLiveness()

	got := repo.states["s"].Launcher
	if got.TermProgram != "" || got.ITermSessionID != "" {
		t.Errorf("a detached session must report no host, not the last one: %+v", got)
	}
	if got.HerdrPaneID != "w1:p1" {
		t.Errorf("the pane address must survive: %+v", got)
	}
}

// TestCheckPIDLiveness_NonHerdrLauncherNotRefreshed is a LOCK, not a defect
// test — it passes on main by construction, because the sweep reads no
// launchers at all there. It pins the cost argument: every other Launcher
// field is static for a process's lifetime, so only a herdr session may pay
// the sweep's env read. A regression that widened this to all sessions would
// put a sysctl + `ps` shellout per session on a 5s timer.
func TestCheckPIDLiveness_NonHerdrLauncherNotRefreshed(t *testing.T) {
	repo := newMockRepo()
	repo.states["s"] = herdrSession(&session.Launcher{
		TermProgram: "iTerm.app",
		TTY:         "/dev/ttys012",
	})

	calls := 0
	pm := newPIDManagerForTest(repo)
	pm.SetLauncherEnvReader(func(int) *session.Launcher {
		calls++
		return &session.Launcher{TermProgram: "ghostty"}
	})

	pm.CheckPIDLiveness()

	if calls != 0 {
		t.Errorf("a non-herdr session must not be re-read by the sweep: %d reads", calls)
	}
	if repo.states["s"].Launcher.TermProgram != "iTerm.app" {
		t.Errorf("non-herdr launcher was rewritten: %+v", repo.states["s"].Launcher)
	}
}

// TestCheckPIDLiveness_HerdrSteadyStateDoesNotChurn pins the other half of the
// cost argument: a session whose client has not moved must produce no Save and
// therefore no UpdatedAt bump and no websocket push, however often the sweep
// runs. Without this, a 5s timer would rewrite every herdr session forever.
//
// A LOCK, not a defect test — the no-save half passes on main by construction,
// since the sweep reads no launchers there. So it also asserts the session was
// actually *visited*: "refreshed and correctly declined to write" and "never
// looked at" are otherwise indistinguishable, and only the first is what this
// claims to pin.
func TestCheckPIDLiveness_HerdrSteadyStateDoesNotChurn(t *testing.T) {
	repo := newMockRepo()
	stored := &session.Launcher{
		HerdrPaneID:     "w1:p1",
		HerdrSocketPath: "/cfg/herdr/herdr.sock",
		TermProgram:     "ghostty",
		TTY:             "/dev/ttys077",
	}
	repo.states["s"] = herdrSession(stored)

	calls := 0
	pm := newPIDManagerForTest(repo)
	pm.SetLauncherEnvReader(func(int) *session.Launcher {
		calls++
		cp := *stored
		return &cp
	})

	before := repo.saves
	pm.CheckPIDLiveness()
	pm.CheckPIDLiveness()

	if repo.saves != before {
		t.Errorf("unchanged host still churned the repo: %d saves", repo.saves-before)
	}
	if calls != 2 {
		t.Errorf("the session must be re-read once per sweep: %d reads over 2 sweeps", calls)
	}
}

// TestCheckPIDLiveness_HerdrIgnoresAReadThatIsNoLongerThePane guards the way
// this refresh could re-create the very misroute it removes. It decides "herdr
// pane" from the *stored* launcher, but the PID it then reads may no longer be
// that pane: a PID can be reused by an unrelated process, and assignPIDLocked
// re-binds a session to a new PID while captureLauncher — set-once — keeps the
// old Launcher (the --bg-spare re-bind of #727). Such a read carries no
// HERDR_PANE_ID, so hostIdentity runs the ancestry fallbacks and resolves the
// herdr *server's* terminal; adopting it wholesale would point every sweep at
// the wrong window (#1348).
func TestCheckPIDLiveness_HerdrIgnoresAReadThatIsNoLongerThePane(t *testing.T) {
	repo := newMockRepo()
	repo.states["s"] = herdrSession(&session.Launcher{
		HerdrPaneID:     "w1:p1",
		HerdrSocketPath: "/cfg/herdr/herdr.sock",
		TermProgram:     "ghostty",
		TTY:             "/dev/ttys077",
	})

	pm := newPIDManagerForTest(repo)
	// What the reader returns for a PID that is no longer the pane: the
	// ancestry walk resolved the herdr server's own terminal.
	pm.SetLauncherEnvReader(func(int) *session.Launcher {
		return &session.Launcher{TermProgram: "kitty", KittyPID: 42, TTY: "/dev/ttys001"}
	})

	pm.CheckPIDLiveness()

	got := repo.states["s"].Launcher
	if got.TermProgram != "ghostty" || got.KittyPID != 0 || got.TTY != "/dev/ttys077" {
		t.Errorf("a read that is not this pane was adopted anyway: %+v", got)
	}
}

// TestCheckPIDLiveness_HerdrAcquiresAMissingTTY covers the one host-ish field
// the client adoption does NOT carry. AdoptHostIdentity deliberately refuses to
// move the client's tty onto a pane that has none, because
// BackgroundAgent.Detached is derived from it (#744) — so a herdr launcher
// stored without a tty (processTTY's `ps` timed out at capture) has no other
// route to acquiring one, and Terminal.app's tab-level targeting stays broken
// for that session until it does.
func TestCheckPIDLiveness_HerdrAcquiresAMissingTTY(t *testing.T) {
	repo := newMockRepo()
	repo.states["s"] = herdrSession(&session.Launcher{
		HerdrPaneID:     "w1:p1",
		HerdrSocketPath: "/cfg/herdr/herdr.sock",
		TermProgram:     "iTerm.app",
	})

	pm := newPIDManagerForTest(repo)
	pm.SetLauncherEnvReader(func(int) *session.Launcher {
		return &session.Launcher{
			HerdrPaneID:     "w1:p1",
			HerdrSocketPath: "/cfg/herdr/herdr.sock",
			TermProgram:     "iTerm.app",
			ITermSessionID:  "w0t0p0",
			TTY:             "/dev/ttys077",
		}
	})

	pm.CheckPIDLiveness()

	if got := repo.states["s"].Launcher.TTY; got != "/dev/ttys077" {
		t.Errorf("a missing tty was never backfilled: TTY = %q", got)
	}
}

// TestCheckPIDLiveness_HerdrSubagentsShareOneRead pins the cost fix for the
// multiplier the per-socket memo does not cover: subagent sessions share their
// parent's PID and each carries its own herdr launcher, so without a per-PID
// memo a parent with subagents pays one identical `ps`-backed read per session,
// every sweep, forever.
func TestCheckPIDLiveness_HerdrSubagentsShareOneRead(t *testing.T) {
	repo := newMockRepo()
	pane := &session.Launcher{
		HerdrPaneID:     "w1:p1",
		HerdrSocketPath: "/cfg/herdr/herdr.sock",
		TermProgram:     "ghostty",
		TTY:             "/dev/ttys077",
	}
	parent := herdrSession(pane)
	repo.states["s"] = parent
	for _, id := range []string{"c1", "c2", "c3"} {
		child := herdrSession(pane)
		child.SessionID = id
		child.ParentSessionID = "s"
		repo.states[id] = child
	}

	calls := 0
	pm := newPIDManagerForTest(repo)
	pm.SetLauncherEnvReader(func(int) *session.Launcher {
		calls++
		cp := *pane
		return &cp
	})

	pm.CheckPIDLiveness()

	if calls != 1 {
		t.Errorf("4 sessions on one PID cost %d reads, want 1", calls)
	}
}
