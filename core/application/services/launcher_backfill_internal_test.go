package services

import (
	"testing"

	"irrlicht/core/domain/session"
)

// TestLauncherBackfillNeedsFor_HerdrHost covers the gap #1350 leaves open at
// capture time: a herdr session whose PID was bound while nothing was attached
// has no host to record, so the seed-time backfill has to be the path that
// picks up a client attached since then.
func TestLauncherBackfillNeedsFor_HerdrHost(t *testing.T) {
	tests := map[string]struct {
		launcher *session.Launcher
		want     bool
	}{
		"herdr pane with no host": {
			launcher: &session.Launcher{HerdrPaneID: "w1:p1", TTY: "/dev/ttys900"},
			want:     true,
		},
		"herdr pane whose client already resolved": {
			launcher: &session.Launcher{HerdrPaneID: "w1:p1", TermProgram: "ghostty"},
			want:     false,
		},
		"herdr pane resolved only to a bundle id": {
			launcher: &session.Launcher{HerdrPaneID: "w1:p1", HostBundleID: "md.obsidian"},
			want:     false,
		},
		"a non-herdr session is never eligible": {
			launcher: &session.Launcher{TTY: "/dev/ttys012"},
			want:     false,
		},
	}
	for name, tc := range tests {
		if got := launcherBackfillNeedsFor(tc.launcher).herdrHost; got != tc.want {
			t.Errorf("%s: herdrHost = %v, want %v", name, got, tc.want)
		}
	}
}

// TestApplyLauncherBackfill_HerdrHost pins that the backfill adopts the freshly
// resolved client identity, and reports no change when the session is still
// detached — writing empties over empties would churn UpdatedAt on every seed.
func TestApplyLauncherBackfill_HerdrHost(t *testing.T) {
	stored := &session.Launcher{
		HerdrPaneID:     "w1:p1",
		HerdrSocketPath: "/cfg/herdr/herdr.sock",
		TTY:             "/dev/ttys900",
	}
	needs := launcherBackfillNeedsFor(stored)

	// Still detached: the re-read carries the herdr address and nothing else.
	stillDetached := &session.Launcher{HerdrPaneID: "w1:p1", HerdrSocketPath: "/cfg/herdr/herdr.sock"}
	if applyLauncherBackfill(stored, needs, stillDetached) {
		t.Error("a still-detached session must report no update")
	}
	if stored.TermProgram != "" {
		t.Errorf("no host may be invented: %+v", stored)
	}

	// A client attached since capture.
	fresh := &session.Launcher{
		HerdrPaneID:     "w1:p1",
		HerdrSocketPath: "/cfg/herdr/herdr.sock",
		TermProgram:     "iTerm.app",
		ITermSessionID:  "w0t0p0-CLIENT",
		TTY:             "/dev/ttys012",
	}
	if !applyLauncherBackfill(stored, needs, fresh) {
		t.Fatal("a newly attached client must report an update")
	}
	if stored.TermProgram != "iTerm.app" || stored.ITermSessionID != "w0t0p0-CLIENT" {
		t.Errorf("host identity not adopted: %+v", stored)
	}
	if stored.HerdrPaneID != "w1:p1" {
		t.Errorf("the pane address must survive: %+v", stored)
	}
}
