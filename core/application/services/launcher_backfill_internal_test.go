package services

import (
	"testing"

	"irrlicht/core/domain/session"
)

// TestLauncherBackfillNeedsFor_HerdrHost covers the gap #1350 leaves open at
// capture time: a herdr session whose PID was bound while nothing was attached
// has no host to record, so a later re-read has to be the path that picks up a
// client attached since then.
//
// The two "already resolved" cases below wanted false until #1405 and now want
// true. That was the second half of the same defect: a host that did resolve
// was never re-checked, so a session detached in one terminal and re-attached
// in another kept naming the first one for the rest of its life. Eligibility is
// now unconditional for a herdr pane, which is what lets the liveness sweep
// re-resolve it (refreshHerdrHosts).
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
			want:     true,
		},
		"herdr pane resolved only to a bundle id": {
			launcher: &session.Launcher{HerdrPaneID: "w1:p1", HostBundleID: "md.obsidian"},
			want:     true,
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

// TestLauncherBackfillNeedsFor_HerdrClientInKittyIsHerdrOnly pins the
// exclusivity that used to be a coincidence. The kitty needs and the herdr need
// could never both fire while the herdr one required TermProgram == "" and the
// kitty ones required TermProgram == "kitty". Dropping that precondition
// (#1405) removes the coincidence, and a herdr pane whose *client* runs in
// kitty is exactly the shape that would satisfy both — so the herdr branch now
// returns before the kitty ones are computed. It has to: a pane owns none of
// those fields, they are adopted wholesale from the client, and letting the
// per-field kitty copies run first would report an update for a client that
// never moved.
func TestLauncherBackfillNeedsFor_HerdrClientInKittyIsHerdrOnly(t *testing.T) {
	needs := launcherBackfillNeedsFor(&session.Launcher{
		HerdrPaneID: "w1:p1",
		TermProgram: "kitty", // the attached client's, adopted at capture
	})
	if want := (launcherBackfillNeeds{herdrHost: true}); needs != want {
		t.Errorf("herdr pane must have exactly the herdr need: got %+v, want %+v", needs, want)
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
