package main

// The census is an ADDED report, so there is no state in which it ever ran red
// on its own. What proves it works is breaking each thing it depends on: the
// artifacts it counts, the tree it walks, and the flags that bound it.
//
// The last test here is the reason the flag exists. A cell with a terminal
// not-runnable Desktop verdict is counted as not-runnable by the census and
// called "pending-record" by the profile view, in one fixture, at the same
// moment. If someone repairs the display state, that test goes red and must be
// updated deliberately — the disagreement may not disappear unnoticed.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"irrlicht/tools/onboarding-factory/internal/desktopresults"
)

func verdictJSON(t *testing.T, root string) verdictReport {
	t.Helper()
	code, stdout, stderr := runOf("status", "--agent", "claudecode",
		"--profile", "desktop-local", "--verdicts", "--json", "--repo-root", root)
	if code != exitOK {
		t.Fatalf("of status --verdicts: exit=%d stderr:\n%s", code, stderr)
	}
	var report verdictReport
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("decode the verdict report: %v\nstdout:\n%s", err, stdout)
	}
	return report
}

func TestStatusVerdictsCountsEveryOutcome(t *testing.T) {
	fixture := desktopResultsRepo(t, false)
	report := verdictJSON(t, fixture.root)

	// The fixture carries exactly one cell per outcome.
	for _, outcome := range desktopresults.Outcomes() {
		if got := report.Census.ByOutcome[string(outcome)]; got != 1 {
			t.Errorf("outcome %s: count=%d, want 1", outcome, got)
		}
	}
	if report.Census.Cells != 5 || report.Census.WithVerdict != 5 {
		t.Fatalf("cells=%d with_verdict=%d, want 5 and 5", report.Census.Cells, report.Census.WithVerdict)
	}
	if len(report.Census.WithoutVerdict) != 0 {
		t.Fatalf("every fixture cell carries a verdict; got %v", report.Census.WithoutVerdict)
	}
}

// TestStatusVerdictsNamesACellWithNoVerdict is the mutation for the undecided
// path: a cell whose result artifact is removed must be NAMED, not folded into
// a smaller total that looks like a tidier campaign.
func TestStatusVerdictsNamesACellWithNoVerdict(t *testing.T) {
	fixture := desktopResultsRepo(t, false)
	cellDir := fixture.cellDirs["not-runnable"]
	if err := os.Remove(filepath.Join(cellDir, desktopResultsFile)); err != nil {
		t.Fatalf("remove the result artifact: %v", err)
	}

	report := verdictJSON(t, fixture.root)
	if report.Census.WithVerdict != 4 || report.Census.Cells != 5 {
		t.Fatalf("with_verdict=%d cells=%d, want 4 and 5", report.Census.WithVerdict, report.Census.Cells)
	}
	if len(report.Census.WithoutVerdict) != 1 || !strings.Contains(report.Census.WithoutVerdict[0], "not-runnable") {
		t.Fatalf("the undecided cell must be named; got %v", report.Census.WithoutVerdict)
	}
}

// TestStatusVerdictsRefusesAnUnreadableArtifact keeps "cannot look" separate
// from "found nothing": an artifact that cannot be parsed is the one case where
// a silent zero reads exactly like a clean campaign.
func TestStatusVerdictsRefusesAnUnreadableArtifact(t *testing.T) {
	fixture := desktopResultsRepo(t, false)
	path := filepath.Join(fixture.cellDirs["unobservable"], desktopResultsFile)
	if err := os.WriteFile(path, []byte("{ this is not JSON"), 0o644); err != nil {
		t.Fatalf("corrupt the result artifact: %v", err)
	}
	code, stdout, stderr := runOf("status", "--agent", "claudecode",
		"--profile", "desktop-local", "--verdicts", "--repo-root", fixture.root)
	if code == exitOK {
		t.Fatalf("an unreadable artifact must fail the census; stdout:\n%s", stdout)
	}
	if !strings.Contains(stderr, desktopResultsFile) {
		t.Fatalf("the failure must name the artifact; stderr:\n%s", stderr)
	}
}

// TestStatusVerdictsRefusesAnEmptyTree is the same rule one level up: a walk
// that finds no cell has not measured a campaign with nothing in it.
func TestStatusVerdictsRefusesAnEmptyTree(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "replaydata", "agents", "scenarios.json"),
		`{"meta":{"min_versions":{"claudecode":"2.0.0"}},"scenarios":[]}`)
	write(t, filepath.Join(root, "replaydata", "agents", "claudecode", "scenarios", ".keep"), "")
	code, _, stderr := runOf("status", "--agent", "claudecode",
		"--profile", "desktop-local", "--verdicts", "--repo-root", root)
	if code == exitOK {
		t.Fatal("a tree with no cell must fail the census, not report zero")
	}
	if !strings.Contains(stderr, "cannot run") {
		t.Fatalf("the failure must say the census could not run; stderr:\n%s", stderr)
	}
}

func TestStatusVerdictsRejectsTheWrongFlags(t *testing.T) {
	fixture := desktopResultsRepo(t, false)
	t.Run("cli-local", func(t *testing.T) {
		code, _, stderr := runOf("status", "--agent", "claudecode", "--verdicts", "--repo-root", fixture.root)
		if code == exitOK {
			t.Fatal("--verdicts under cli-local must be refused")
		}
		if !strings.Contains(stderr, "desktop-local") {
			t.Fatalf("the refusal must name the profile to pass; stderr:\n%s", stderr)
		}
	})
	t.Run("no agent", func(t *testing.T) {
		code, _, stderr := runOf("status", "--profile", "desktop-local", "--verdicts", "--repo-root", fixture.root)
		if code == exitOK {
			t.Fatal("--verdicts without --agent must be refused")
		}
		if !strings.Contains(stderr, "--agent") {
			t.Fatalf("the refusal must name the missing flag; stderr:\n%s", stderr)
		}
	})
}

// TestStatusVerdictsReportsTheDisplayStateDisagreement pins the defect the flag
// exists to expose, in one fixture: the not-runnable cell has a terminal
// Desktop verdict AND reads as pending-record in the profile view.
//
// This is a LOCK, not a red-first defect test. If the display state is ever
// taught to read execution-results.json, this goes red and the expectation must
// be changed on purpose rather than drifting.
func TestStatusVerdictsReportsTheDisplayStateDisagreement(t *testing.T) {
	fixture := desktopResultsRepo(t, false)
	report := verdictJSON(t, fixture.root)

	if report.Census.ByOutcome[string(desktopresults.OutcomeNotRunnable)] != 1 {
		t.Fatalf("the fixture must carry one not-runnable verdict; got %v", report.Census.ByOutcome)
	}
	if report.PendingDisplayState == 0 {
		t.Fatal("the profile view still cannot see a Desktop verdict, so at least one cell must " +
			"read pending-record; a zero here means the display state was fixed — update this test")
	}
}
