package services_test

import (
	"os"
	"testing"
	"time"

	"irrlicht/core/application/services"
	"irrlicht/core/domain/session"
)

// paneLessSession is the shape #1936 exists for: a pi session whose launcher
// was captured from ancestry alone, so it names a terminal but no pane. The
// daemon could not read $HERDR_PANE_ID from the process, because a Node agent
// that sets process.title hides its own environment (#1934).
func paneLessSession(l *session.Launcher) *session.SessionState {
	return &session.SessionState{
		SessionID: "s",
		Adapter:   "pi",
		State:     session.StateWorking,
		PID:       os.Getpid(),
		UpdatedAt: time.Now().Unix(),
		Launcher:  l,
	}
}

// recorderSpy captures what was handed to the launcher reader's self-report
// seam, and how often the reader itself then ran.
type recorderSpy struct {
	pids    []int
	panes   []string
	sockets []string
	reads   int
}

func (r *recorderSpy) record(pid int, paneID, socketPath string) {
	r.pids = append(r.pids, pid)
	r.panes = append(r.panes, paneID)
	r.sockets = append(r.sockets, socketPath)
}

// reportedSocket is the socket path the tests below report and expect back.
const reportedSocket = "/cfg/herdr/sessions/f/herdr.sock"

// repairSetup builds the #1936 defect scenario: a session whose launcher names
// a terminal and no pane, with a reader that — once the report is recorded —
// resolves both the pane and the client displaying it.
//
// Shared by the two tests below rather than inlined in each, because the two
// halves of the repair (what is handed to the reader, and what lands on the
// session) are one scenario asserted from two ends.
func repairSetup(t *testing.T) (*mockRepo, *services.PIDManager, *recorderSpy) {
	t.Helper()
	repo := newMockRepo()
	repo.states["s"] = paneLessSession(&session.Launcher{
		TermProgram: "Apple_Terminal", // the herdr SERVER's terminal — #1348's misroute
		TTY:         "/dev/ttys012",
	})

	pm := newPIDManagerForTest(repo)
	spy := &recorderSpy{}
	pm.SetHerdrPaneRecorder(spy.record)
	pm.SetLauncherEnvReader(func(int) (*session.Launcher, bool) {
		spy.reads++
		return &session.Launcher{
			HerdrPaneID:     "w1:p2",
			HerdrSocketPath: reportedSocket,
			TermProgram:     "ghostty",
			TTY:             "/dev/ttys077",
		}, true
	})
	return repo, pm, spy
}

// TestAdoptSelfReportedHerdrPane_HandsTheReportToTheReader covers the first
// half: the report reaches the seam the launcher read consults, keyed by this
// session's own pid.
func TestAdoptSelfReportedHerdrPane_HandsTheReportToTheReader(t *testing.T) {
	_, pm, spy := repairSetup(t)

	pm.AdoptSelfReportedHerdrPane("s", "w1:p2", reportedSocket)

	if len(spy.pids) != 1 {
		t.Fatalf("recorded %d reports, want 1", len(spy.pids))
	}
	if spy.pids[0] != os.Getpid() {
		t.Errorf("recorded pid %d, want this session's %d", spy.pids[0], os.Getpid())
	}
	if spy.panes[0] != "w1:p2" {
		t.Errorf("recorded pane %q, want w1:p2", spy.panes[0])
	}
	if spy.sockets[0] != reportedSocket {
		t.Errorf("recorded socket %q, want %q", spy.sockets[0], reportedSocket)
	}
}

// TestAdoptSelfReportedHerdrPane_RepairsAPaneLessLauncher is the defect test.
// Without this path the session keeps its ancestry-derived host forever:
// launcherBackfillNeedsFor asks for nothing (it has a tty and no pane) and
// refreshMultiplexerHosts skips it (hostedInAMultiplexerPane is false), so
// nothing in a daemon's lifetime looks at it again.
func TestAdoptSelfReportedHerdrPane_RepairsAPaneLessLauncher(t *testing.T) {
	repo, pm, _ := repairSetup(t)

	pm.AdoptSelfReportedHerdrPane("s", "w1:p2", reportedSocket)

	got := repo.states["s"].Launcher
	if got.HerdrPaneID != "w1:p2" {
		t.Fatalf("pane not adopted: %+v", got)
	}
	if got.HerdrSocketPath != reportedSocket {
		t.Errorf("socket = %q, want %q — the pane and its server are one fact",
			got.HerdrSocketPath, reportedSocket)
	}
	if got.TermProgram != "ghostty" {
		t.Errorf("TermProgram = %q, want the attached client's — the pane's window "+
			"belongs to the client, not to the ancestry walk", got.TermProgram)
	}
	if got.TTY != "/dev/ttys077" {
		t.Errorf("TTY = %q, want the client's", got.TTY)
	}
}

// TestAdoptSelfReportedHerdrPane_KnownPaneCostsNoRead pins the cost rule. A
// report arrives at every turn end, so a session whose pane is already
// resolved must not pay a launcher read for each one — it records the report,
// which is what makes a LATER read (after a restart, or a PID re-bind) the
// cheap kind, and stops.
func TestAdoptSelfReportedHerdrPane_KnownPaneCostsNoRead(t *testing.T) {
	repo := newMockRepo()
	repo.states["s"] = paneLessSession(&session.Launcher{
		HerdrPaneID:     "w1:p2",
		HerdrSocketPath: "/cfg/herdr/sessions/f/herdr.sock",
		TermProgram:     "ghostty",
	})

	pm := newPIDManagerForTest(repo)
	spy := &recorderSpy{}
	pm.SetHerdrPaneRecorder(spy.record)
	pm.SetLauncherEnvReader(func(int) (*session.Launcher, bool) {
		spy.reads++
		return nil, false
	})

	pm.AdoptSelfReportedHerdrPane("s", "w1:p2", "/cfg/herdr/sessions/f/herdr.sock")

	if len(spy.pids) != 1 {
		t.Errorf("recorded %d reports, want 1 — recording is the cheap half and "+
			"always happens", len(spy.pids))
	}
	if spy.reads != 0 {
		t.Errorf("read the launcher %d times for a session that already has its pane", spy.reads)
	}
}

// TestAdoptSelfReportedHerdrPane_UnconfirmedReportChangesNothing covers the
// report that does not survive confirmation inside the reader — pid reuse, or
// no herdr server answering. The session keeps exactly what it had.
func TestAdoptSelfReportedHerdrPane_UnconfirmedReportChangesNothing(t *testing.T) {
	repo := newMockRepo()
	repo.states["s"] = paneLessSession(&session.Launcher{
		TermProgram: "Apple_Terminal",
		TTY:         "/dev/ttys012",
	})

	pm := newPIDManagerForTest(repo)
	pm.SetHerdrPaneRecorder(func(int, string, string) {})
	// The reader ran and resolved no pane: the report was not confirmed.
	pm.SetLauncherEnvReader(func(int) (*session.Launcher, bool) {
		return &session.Launcher{TermProgram: "Apple_Terminal", TTY: "/dev/ttys012"}, true
	})

	pm.AdoptSelfReportedHerdrPane("s", "w1:p9", "/cfg/herdr/sessions/f/herdr.sock")

	got := repo.states["s"].Launcher
	if got.HerdrPaneID != "" || got.HerdrSocketPath != "" {
		t.Errorf("stored a pane the reader did not confirm: %+v", got)
	}
	if got.TermProgram != "Apple_Terminal" {
		t.Errorf("host disturbed by a report that led nowhere: %+v", got)
	}
}

// TestAdoptSelfReportedHerdrPane_UnprobedHostIsNotAdopted applies #1485's rule
// on this path too: empty host fields from a read whose client probe did not
// run mean "not looked up", not "detached". Adopting them would erase the host
// the session already has, on the strength of a probe that never happened.
func TestAdoptSelfReportedHerdrPane_UnprobedHostIsNotAdopted(t *testing.T) {
	repo := newMockRepo()
	repo.states["s"] = paneLessSession(&session.Launcher{
		TermProgram: "Apple_Terminal",
		TTY:         "/dev/ttys012",
	})

	pm := newPIDManagerForTest(repo)
	pm.SetHerdrPaneRecorder(func(int, string, string) {})
	pm.SetLauncherEnvReader(func(int) (*session.Launcher, bool) {
		return &session.Launcher{
			HerdrPaneID:     "w1:p2",
			HerdrSocketPath: "/cfg/herdr/sessions/f/herdr.sock",
		}, false // the client probe did not run
	})

	pm.AdoptSelfReportedHerdrPane("s", "w1:p2", "/cfg/herdr/sessions/f/herdr.sock")

	got := repo.states["s"].Launcher
	if got.HerdrPaneID != "w1:p2" {
		t.Errorf("the pane came from the process, not the probe, and must be kept: %+v", got)
	}
	if got.TermProgram != "Apple_Terminal" {
		t.Errorf("TermProgram = %q — a probe that did not run cleared a known host", got.TermProgram)
	}
}

// TestAdoptSelfReportedHerdrPane_NoPIDIsNotStashed pins the deliberate absence
// of a stash. A report arriving before PID discovery is dropped, because the
// extension sends one at every turn end and the next lands on a bound session
// — an entry with no natural end is the cost that buys one turn of latency.
func TestAdoptSelfReportedHerdrPane_NoPIDIsNotStashed(t *testing.T) {
	repo := newMockRepo()
	unbound := paneLessSession(&session.Launcher{TermProgram: "Apple_Terminal"})
	unbound.PID = 0
	repo.states["s"] = unbound

	pm := newPIDManagerForTest(repo)
	spy := &recorderSpy{}
	pm.SetHerdrPaneRecorder(spy.record)
	pm.SetLauncherEnvReader(func(int) (*session.Launcher, bool) {
		spy.reads++
		return nil, false
	})

	pm.AdoptSelfReportedHerdrPane("s", "w1:p2", "/cfg/herdr/sessions/f/herdr.sock")

	if len(spy.pids) != 0 {
		t.Errorf("recorded %v against a session with no PID; the map is keyed by pid, "+
			"so there is nothing to key it under", spy.pids)
	}
	if spy.reads != 0 {
		t.Errorf("read the launcher %d times with no PID to read", spy.reads)
	}
}

// TestAdoptSelfReportedHerdrPane_WithoutARecorderDoesNothing pins the disabled
// path — demo mode, tests, and a revoked "Terminal focus" consent all leave
// the seam nil, and none of them may pay a read.
func TestAdoptSelfReportedHerdrPane_WithoutARecorderDoesNothing(t *testing.T) {
	repo := newMockRepo()
	repo.states["s"] = paneLessSession(&session.Launcher{TermProgram: "Apple_Terminal"})

	pm := newPIDManagerForTest(repo)
	reads := 0
	pm.SetLauncherEnvReader(func(int) (*session.Launcher, bool) {
		reads++
		return nil, false
	})

	pm.AdoptSelfReportedHerdrPane("s", "w1:p2", "/cfg/herdr/sessions/f/herdr.sock")

	if reads != 0 {
		t.Errorf("read the launcher %d times with the self-report path disabled", reads)
	}
	if got := repo.states["s"].Launcher; got.HerdrPaneID != "" {
		t.Errorf("adopted a pane with no recorder wired: %+v", got)
	}
}

// TestAdoptSelfReportedHerdrPane_UnknownSessionIsQuiet covers the report for a
// session the daemon has already reaped — the hook races teardown the same way
// HandleStopHook does.
func TestAdoptSelfReportedHerdrPane_UnknownSessionIsQuiet(t *testing.T) {
	pm := newPIDManagerForTest(newMockRepo())
	spy := &recorderSpy{}
	pm.SetHerdrPaneRecorder(spy.record)
	pm.SetLauncherEnvReader(func(int) (*session.Launcher, bool) {
		spy.reads++
		return nil, false
	})

	pm.AdoptSelfReportedHerdrPane("gone", "w1:p2", "/cfg/herdr/sessions/f/herdr.sock")

	if len(spy.pids) != 0 || spy.reads != 0 {
		t.Errorf("acted on a session that is not there: recorded %v, read %d times",
			spy.pids, spy.reads)
	}
}
