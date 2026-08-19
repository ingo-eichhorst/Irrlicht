package services_test

import (
	"testing"

	"irrlicht/core/domain/session"
)

// This file is #1501's half of the periodic host refresh #1405 built for herdr.
// A tmux pane's host is exactly as dynamic — detach here, re-attach there is
// what a persistent multiplexer is FOR, and tmux re-attaches are at least as
// common as herdr's — so a host resolved once and never revisited goes on
// naming the terminal the user walked away from.
//
// The sweep is shared with herdr rather than duplicated: one snapshot flag, one
// need, one identity check. pid_manager_herdr_refresh_test.go carries the herdr
// side, and every assertion there is now also a lock on the generalization.

// tmuxSession builds a live tmux-hosted session, reusing herdrSession's shape
// (own PID for the liveness probe, fresh UpdatedAt to stay out of the readyTTL
// reap) so the only thing these tests observe is the launcher.
func tmuxSession(host *session.Launcher) *session.SessionState {
	return herdrSession(host)
}

// TestCheckPIDLiveness_TmuxHostRefreshedOnReattach is the defect: the same one
// #1405 fixed for herdr, arriving through the multiplexer this repo's users
// are far more likely to be running.
func TestCheckPIDLiveness_TmuxHostRefreshedOnReattach(t *testing.T) {
	repo := newMockRepo()
	repo.states["s"] = tmuxSession(&session.Launcher{
		TmuxPane:       "%17",
		TmuxSocket:     "/private/tmp/tmux-501/default",
		TermProgram:    "iTerm.app",
		ITermSessionID: "w0t0p0-OLD",
		TTY:            "/dev/ttys012",
	})

	pm := newPIDManagerForTest(repo)
	// A fresh read now resolves the client the user re-attached in. The pane
	// address survives because AdoptHostIdentity puts it back — which is what
	// makes the identity check below expressible at all.
	pm.SetLauncherEnvReader(func(int) (*session.Launcher, bool) {
		return &session.Launcher{
			TmuxPane:    "%17",
			TmuxSocket:  "/private/tmp/tmux-501/default",
			TermProgram: "ghostty",
			TTY:         "/dev/ttys077",
		}, true
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
	if got.TmuxPane != "%17" || got.TmuxSocket != "/private/tmp/tmux-501/default" {
		t.Errorf("the pane address must survive: %+v", got)
	}
}

// TestCheckPIDLiveness_TmuxHostClearedOnDetach is the other half. Nothing is
// displaying the session, and a stale host is worse than none: the app raises
// some unrelated window instead of doing nothing.
func TestCheckPIDLiveness_TmuxHostClearedOnDetach(t *testing.T) {
	repo := newMockRepo()
	repo.states["s"] = tmuxSession(&session.Launcher{
		TmuxPane:       "%17",
		TmuxSocket:     "/private/tmp/tmux-501/default",
		TermProgram:    "iTerm.app",
		ITermSessionID: "w0t0p0-OLD",
		TTY:            "/dev/ttys012",
	})

	pm := newPIDManagerForTest(repo)
	pm.SetLauncherEnvReader(func(int) (*session.Launcher, bool) {
		return &session.Launcher{
			TmuxPane:   "%17",
			TmuxSocket: "/private/tmp/tmux-501/default",
			TTY:        "/dev/ttys012",
		}, true
	})

	pm.CheckPIDLiveness()

	got := repo.states["s"].Launcher
	if got.TermProgram != "" || got.ITermSessionID != "" {
		t.Errorf("no client is attached, so no host may be reported: %+v", got)
	}
	if got.TmuxPane != "%17" {
		t.Errorf("the pane address must survive a detach: %+v", got)
	}
}

// TestCheckPIDLiveness_TmuxHostSurvivesAnUnrunnableProbe is the polarity that
// matters most, and the one a `tmux list-clients` makes easy to get wrong: exit
// 1 means "no server running" / "error connecting", which says nothing about
// who is attached. hostKnown=false has to leave the stored host alone
// (#1485/#1492); adopting the empties would clear a live host on a probe that
// never reached the server.
func TestCheckPIDLiveness_TmuxHostSurvivesAnUnrunnableProbe(t *testing.T) {
	repo := newMockRepo()
	repo.states["s"] = tmuxSession(&session.Launcher{
		TmuxPane:       "%17",
		TmuxSocket:     "/private/tmp/tmux-501/default",
		TermProgram:    "iTerm.app",
		ITermSessionID: "w0t0p0-LIVE",
		TTY:            "/dev/ttys012",
	})

	pm := newPIDManagerForTest(repo)
	pm.SetLauncherEnvReader(func(int) (*session.Launcher, bool) {
		return &session.Launcher{TmuxPane: "%17", TmuxSocket: "/private/tmp/tmux-501/default"}, false
	})

	pm.CheckPIDLiveness()

	got := repo.states["s"].Launcher
	if got.TermProgram != "iTerm.app" || got.ITermSessionID != "w0t0p0-LIVE" {
		t.Errorf("a probe that did not run cleared a live host: %+v", got)
	}
}

// TestCheckPIDLiveness_TmuxIgnoresAReadThatIsNoLongerThePane is the PID-reuse
// guard. A PID can be recycled, and assignPIDLocked re-binds a session to a new
// PID while its Launcher is set-once — so a fresh read can describe a
// completely different process. Adopting it wholesale would put a stranger's
// window on this session, on a timer.
func TestCheckPIDLiveness_TmuxIgnoresAReadThatIsNoLongerThePane(t *testing.T) {
	stored := &session.Launcher{
		TmuxPane:       "%17",
		TmuxSocket:     "/private/tmp/tmux-501/default",
		TermProgram:    "iTerm.app",
		ITermSessionID: "w0t0p0-LIVE",
	}
	for name, fresh := range map[string]*session.Launcher{
		"the pid is in a different pane now": {TmuxPane: "%99", TermProgram: "ghostty"},
		"the pid is in no pane at all now":   {TermProgram: "ghostty", ITermSessionID: "STRANGER"},
		"the pid is in a herdr pane now":     {HerdrPaneID: "w1:p1", TermProgram: "ghostty"},
	} {
		repo := newMockRepo()
		snapshot := *stored
		repo.states["s"] = tmuxSession(&snapshot)

		pm := newPIDManagerForTest(repo)
		pm.SetLauncherEnvReader(func(int) (*session.Launcher, bool) { return fresh, true })
		pm.CheckPIDLiveness()

		if got := repo.states["s"].Launcher; *got != *stored {
			t.Errorf("%s: a read of a different pane was adopted: %+v", name, got)
		}
	}
}

// TestCheckPIDLiveness_TmuxSteadyStateDoesNotChurn is the cost half of #1501's
// third item: a client that has not moved must produce no Save, no UpdatedAt
// bump and no push, however often the 5s sweep runs.
//
// It also asserts the session was actually VISITED, because "refreshed and
// correctly declined to write" and "never looked at" are otherwise
// indistinguishable and only the first is what this claims.
func TestCheckPIDLiveness_TmuxSteadyStateDoesNotChurn(t *testing.T) {
	repo := newMockRepo()
	stored := &session.Launcher{
		TmuxPane:    "%17",
		TmuxSocket:  "/private/tmp/tmux-501/default",
		TermProgram: "ghostty",
		TTY:         "/dev/ttys077",
	}
	repo.states["s"] = tmuxSession(stored)

	reads := 0
	pm := newPIDManagerForTest(repo)
	pm.SetLauncherEnvReader(func(int) (*session.Launcher, bool) {
		reads++
		copied := *stored
		return &copied, true
	})

	before := repo.saves
	pm.CheckPIDLiveness()
	pm.CheckPIDLiveness()

	if reads == 0 {
		t.Fatal("the sweep never read this session, so it pins nothing about churn")
	}
	if repo.saves != before {
		t.Errorf("an unmoved client was written back %d times", repo.saves-before)
	}
}

// TestCheckPIDLiveness_InheritedTmuxPaneKeepsItsOwnHost is the guard that stops
// the sweep from re-opening #1499. The snapshot gate is deliberately WIDE — a
// stored launcher cannot tell an adopted host from an inherited pane address —
// so a GUI terminal launched from inside a pane IS read every sweep. What must
// not happen is its own identity being replaced by a fresh read that resolved
// less: the reader answers hostKnown=false for that shape, and this pins that
// the sweep honours it.
func TestCheckPIDLiveness_InheritedTmuxPaneKeepsItsOwnHost(t *testing.T) {
	repo := newMockRepo()
	repo.states["s"] = tmuxSession(&session.Launcher{
		TmuxPane:       "%17",
		TmuxSocket:     "/private/tmp/tmux-501/default",
		TermProgram:    "iTerm.app",
		ITermSessionID: "w0t0p0-OWN",
		TTY:            "/dev/ttys012",
	})

	pm := newPIDManagerForTest(repo)
	// What ReadLauncherEnv returns for an inherited pane address whose own
	// ancestry probe came back short: the identity is there but the PANE's host
	// was never looked up.
	pm.SetLauncherEnvReader(func(int) (*session.Launcher, bool) {
		return &session.Launcher{
			TmuxPane:   "%17",
			TmuxSocket: "/private/tmp/tmux-501/default",
			TTY:        "/dev/ttys012",
		}, false
	})

	pm.CheckPIDLiveness()

	got := repo.states["s"].Launcher
	if got.TermProgram != "iTerm.app" || got.ITermSessionID != "w0t0p0-OWN" {
		t.Errorf("a session that reported its own host had it cleared by the sweep: %+v", got)
	}
}
