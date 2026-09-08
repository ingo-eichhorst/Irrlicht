package processlifecycle

import (
	"context"
	"path/filepath"
	"testing"

	"irrlicht/core/domain/session"
)

// resetHerdrSelfReports empties the package-level map around one test, both
// before and after. Before as well as after, because a test that fails partway
// leaves entries behind and the next one would then be reading state it did
// not write — the failure mode a shared map introduces, and the only reason
// this helper exists.
func resetHerdrSelfReports(t *testing.T) {
	t.Helper()
	clear := func() {
		rememberedHerdrPanes.mu.Lock()
		defer rememberedHerdrPanes.mu.Unlock()
		rememberedHerdrPanes.entries = map[int]herdrSelfReport{}
	}
	clear()
	t.Cleanup(clear)
}

// deadPID is above darwin's default pid ceiling (kern.maxproc caps pids at
// 99999), so IsAlive's signal-0 probe answers ESRCH for it without the test
// having to spawn and reap anything.
const deadPID = 4_000_000

// TestSelfReportedPaneSkipsTheScan is #1936's point, stated as cost rather
// than as correctness: both routes reach the same pane, and only one of them
// enumerates every pane on the server to get there.
func TestSelfReportedPaneSkipsTheScan(t *testing.T) {
	resetHerdrSelfReports(t)
	dir := shortTempDir(t)
	f := &fakeHerdr{t: t, panes: map[string]fakePane{
		"w1:p1": {cwd: "/work/a", foregroundCWD: "/work/a", shellPID: 100, foreground: []int{101}},
		"w1:p2": {cwd: "/work/b", foregroundCWD: "/work/b", shellPID: 200, foreground: []int{201}},
	}}
	sock := f.start(dir, "factory")
	useSessionsDir(t, dir)

	RememberHerdrPane(201, "w1:p2", sock)

	l := &session.Launcher{}
	adoptHerdrPane(l, 201)

	if l.HerdrPaneID != "w1:p2" || l.HerdrSocketPath != sock {
		t.Fatalf("adopted pane %q on %q, want w1:p2 on %q", l.HerdrPaneID, l.HerdrSocketPath, sock)
	}
	if n := f.methodCalls("pane.list"); n != 0 {
		t.Errorf("pane.list called %d times — a confirmed self-report names the pane, "+
			"so enumerating the server's panes is exactly the work it exists to avoid", n)
	}
	if n := f.methodCalls("pane.process_info"); n != 1 {
		t.Errorf("pane.process_info called %d times, want exactly 1: the report is "+
			"confirmed on the pane it names and no other", n)
	}
}

// TestSelfReportedPaneIsConfirmedNotBelieved is the invariant herdrpane.go
// established — cwd narrows, pid decides — applied to a source that could
// otherwise bypass it. A report whose pane no longer holds the process must
// not be used, however authoritative its origin was when it was made.
func TestSelfReportedPaneIsConfirmedNotBelieved(t *testing.T) {
	resetHerdrSelfReports(t)
	dir := shortTempDir(t)
	f := &fakeHerdr{t: t, panes: map[string]fakePane{
		"w1:p1": {cwd: "/work/a", foregroundCWD: "/work/a", shellPID: 100, foreground: []int{101}},
		"w1:p2": {cwd: "/work/b", foregroundCWD: "/work/b", shellPID: 200, foreground: []int{201}},
	}}
	sock := f.start(dir, "factory")
	useSessionsDir(t, dir)

	// pid 101 lives in w1:p1; the stale entry claims w1:p2. This is the shape
	// pid reuse produces: the report was true for whoever held 101 before.
	RememberHerdrPane(101, "w1:p2", sock)

	pane, gotSock, ok := selfReportedPane(context.Background(), 101)
	if ok {
		t.Fatalf("used a contradicted report (%q on %q); the pane's process list does "+
			"not name this pid", pane, gotSock)
	}
	if _, still := rememberedHerdrPane(101); still {
		t.Error("a report the server actively contradicted is still remembered — it " +
			"would be re-offered, and re-rejected, on every read for this pid")
	}
}

// TestSelfReportedPaneKeepsAReportItCouldNotCheck is the other half of the
// same tri-state, and the half that is easy to get wrong: a socket that does
// not answer is not evidence against the report. Dropping the entry here would
// discard good information on the strength of a probe that never ran — #1485's
// defect, in a different map.
func TestSelfReportedPaneKeepsAReportItCouldNotCheck(t *testing.T) {
	resetHerdrSelfReports(t)
	dir := shortTempDir(t)
	useSessionsDir(t, dir)

	// A path with no listener behind it: dialing fails, so no server answers.
	sock := filepath.Join(dir, "gone", "herdr.sock")
	RememberHerdrPane(201, "w1:p2", sock)

	if _, _, ok := selfReportedPane(context.Background(), 201); ok {
		t.Fatal("reported a pane on the strength of a probe that could not run")
	}
	report, still := rememberedHerdrPane(201)
	if !still {
		t.Fatal("dropped the report because the server was unreachable; the next " +
			"read, when it is reachable again, now has nothing to confirm")
	}
	if report.paneID != "w1:p2" || report.socketPath != sock {
		t.Errorf("kept %+v, want the report as made", report)
	}
}

// TestSelfReportedPaneFallsBackToTheScan pins that a contradicted report costs
// the session nothing: adoptHerdrPane still resolves the pane the ordinary
// way, so the self-report can only ever add an answer, never remove one.
func TestSelfReportedPaneFallsBackToTheScan(t *testing.T) {
	resetHerdrSelfReports(t)
	dir := shortTempDir(t)
	f := &fakeHerdr{t: t, panes: map[string]fakePane{
		"w1:p1": {cwd: "/work/a", foregroundCWD: "/work/a", shellPID: 100, foreground: []int{101}},
		"w1:p2": {cwd: "/work/b", foregroundCWD: "/work/b", shellPID: 200, foreground: []int{201}},
	}}
	sock := f.start(dir, "factory")
	useSessionsDir(t, dir)

	RememberHerdrPane(101, "w1:p2", sock) // wrong pane for this pid

	l := &session.Launcher{}
	adoptHerdrPane(l, 101)

	if l.HerdrPaneID != "w1:p1" {
		t.Fatalf("adopted %q, want w1:p1 — the scan should have found the pane the "+
			"discarded report failed to name", l.HerdrPaneID)
	}
}

func TestRememberHerdrPaneRejectsHalfAReport(t *testing.T) {
	resetHerdrSelfReports(t)
	for _, tc := range []struct {
		name               string
		pid                int
		paneID, socketPath string
	}{
		{"no pid", 0, "w1:p1", "/tmp/herdr.sock"},
		{"negative pid", -1, "w1:p1", "/tmp/herdr.sock"},
		{"no pane", 42, "", "/tmp/herdr.sock"},
		{"no socket", 42, "w1:p1", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			RememberHerdrPane(tc.pid, tc.paneID, tc.socketPath)
			if _, ok := rememberedHerdrPane(tc.pid); ok {
				t.Errorf("stored %+v; a pane and its socket are one fact and half of "+
					"it addresses nothing", tc)
			}
		})
	}
}

// TestRememberHerdrPaneEvictsDeadProcessesAtTheCap pins the one path on which
// the map is allowed to spend syscalls, and that reaching the cap does not
// wedge it: entries whose process is gone make room for a live report.
func TestRememberHerdrPaneEvictsDeadProcessesAtTheCap(t *testing.T) {
	resetHerdrSelfReports(t)
	for i := 0; i < maxRememberedPanes; i++ {
		RememberHerdrPane(deadPID+i, "w1:p1", "/tmp/herdr.sock")
	}
	if got := rememberedCount(); got != maxRememberedPanes {
		t.Fatalf("filled to %d entries, want %d", got, maxRememberedPanes)
	}

	RememberHerdrPane(1, "w1:p9", "/tmp/other.sock")

	report, ok := rememberedHerdrPane(1)
	if !ok {
		t.Fatal("a full map refused a new report even though every entry in it names " +
			"a process that no longer exists")
	}
	if report.paneID != "w1:p9" {
		t.Errorf("stored %+v, want the report as made", report)
	}
	if got := rememberedCount(); got >= maxRememberedPanes {
		t.Errorf("%d entries after eviction — the dead ones were not dropped", got)
	}
}

// TestRememberHerdrPaneUpdatesInPlace covers the report arriving again for a
// pid already known, which is the ordinary case: the extension re-reports at
// every turn end. It must not be treated as a new entry, or a busy session
// would walk the map toward its cap on its own.
func TestRememberHerdrPaneUpdatesInPlace(t *testing.T) {
	resetHerdrSelfReports(t)
	RememberHerdrPane(42, "w1:p1", "/tmp/a/herdr.sock")
	RememberHerdrPane(42, "w2:p3", "/tmp/b/herdr.sock")

	if got := rememberedCount(); got != 1 {
		t.Fatalf("%d entries for one pid, want 1", got)
	}
	report, _ := rememberedHerdrPane(42)
	if report.paneID != "w2:p3" || report.socketPath != "/tmp/b/herdr.sock" {
		t.Errorf("kept %+v, want the later report — a pane can move between turns", report)
	}
}

func rememberedCount() int {
	rememberedHerdrPanes.mu.Lock()
	defer rememberedHerdrPanes.mu.Unlock()
	return len(rememberedHerdrPanes.entries)
}
