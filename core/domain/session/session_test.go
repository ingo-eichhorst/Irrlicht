package session

import (
	"encoding/json"
	"testing"
	"time"
)

func TestIsStale(t *testing.T) {
	now := time.Now().Unix()

	tests := []struct {
		name      string
		updatedAt int64
		maxAge    time.Duration
		want      bool
	}{
		{"fresh session", now - 60, 5 * 24 * time.Hour, false},
		{"stale session", now - 6*24*60*60, 5 * 24 * time.Hour, true},
		{"exactly at boundary", now - 5*24*60*60 - 1, 5 * 24 * time.Hour, true},
		{"zero maxAge disables", now - 999*24*60*60, 0, false},
		{"negative maxAge disables", now - 999*24*60*60, -1, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &SessionState{UpdatedAt: tt.updatedAt}
			if got := s.IsStale(tt.maxAge); got != tt.want {
				t.Errorf("IsStale(%v) = %v, want %v", tt.maxAge, got, tt.want)
			}
		})
	}
}

func TestSessionState_LauncherJSONRoundTrip(t *testing.T) {
	// With Launcher present.
	in := &SessionState{
		SessionID: "abc",
		State:     StateWorking,
		PID:       1234,
		Launcher: &Launcher{
			TermProgram:    "iTerm.app",
			ITermSessionID: "w0t0p0",
		},
	}
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out SessionState
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Launcher == nil {
		t.Fatal("Launcher lost in round-trip")
	}
	if out.Launcher.TermProgram != "iTerm.app" || out.Launcher.ITermSessionID != "w0t0p0" {
		t.Errorf("launcher round-trip mismatch: %+v", out.Launcher)
	}

	// Generic host fallback: only HostBundleID set (e.g. an in-Obsidian
	// terminal where no curated TermProgram matched). IsEmpty must keep it.
	generic := &Launcher{HostBundleID: "md.obsidian"}
	if generic.IsEmpty() {
		t.Error("launcher with only HostBundleID should not be empty")
	}
	gdata, err := json.Marshal(&SessionState{SessionID: "obs", State: StateWorking, Launcher: generic})
	if err != nil {
		t.Fatalf("marshal generic: %v", err)
	}
	var gout SessionState
	if err := json.Unmarshal(gdata, &gout); err != nil {
		t.Fatalf("unmarshal generic: %v", err)
	}
	if gout.Launcher == nil || gout.Launcher.HostBundleID != "md.obsidian" {
		t.Errorf("host_bundle_id lost in round-trip: %+v", gout.Launcher)
	}

	// A herdr pane carries only its own address — every other identity field
	// is deliberately dropped at capture time, so this is the shape that
	// reaches the wire for such a session (#1348).
	herdr := &Launcher{HerdrPaneID: "w1:p1", HerdrSocketPath: "/tmp/herdr/h.sock"}
	if herdr.IsEmpty() {
		t.Error("launcher with only herdr fields should not be empty")
	}
	hdata, err := json.Marshal(&SessionState{SessionID: "hrd", State: StateWorking, Launcher: herdr})
	if err != nil {
		t.Fatalf("marshal herdr: %v", err)
	}
	var hout SessionState
	if err := json.Unmarshal(hdata, &hout); err != nil {
		t.Fatalf("unmarshal herdr: %v", err)
	}
	if hout.Launcher == nil || hout.Launcher.HerdrPaneID != "w1:p1" ||
		hout.Launcher.HerdrSocketPath != "/tmp/herdr/h.sock" {
		t.Errorf("herdr fields lost in round-trip: %+v", hout.Launcher)
	}

	// Without Launcher — backwards compat with pre-170 session JSON files.
	legacy := []byte(`{"session_id":"xyz","state":"ready","pid":99}`)
	var legacyOut SessionState
	if err := json.Unmarshal(legacy, &legacyOut); err != nil {
		t.Fatalf("unmarshal legacy: %v", err)
	}
	if legacyOut.Launcher != nil {
		t.Errorf("legacy session should have nil Launcher, got %+v", legacyOut.Launcher)
	}
}

// TestLauncher_AdoptHostIdentity covers how a herdr pane acquires the window
// it is displayed in (#1350): the pane keeps its own address, and every
// host-window field comes from the attached client.
func TestLauncher_AdoptHostIdentity(t *testing.T) {
	pane := &Launcher{
		HerdrPaneID:     "w1:p2",
		HerdrSocketPath: "/cfg/herdr/sessions/probe/herdr.sock",
		TTY:             "/dev/ttys900", // the pane's own pty, captured first
	}
	client := &Launcher{
		TermProgram:    "iTerm.app",
		ITermSessionID: "w0t0p0-CLIENT",
		TTY:            "/dev/ttys012",
		HostBundleID:   "com.googlecode.iterm2",
	}
	if !pane.AdoptHostIdentity(client) {
		t.Fatal("adopting a populated client identity must report a change")
	}
	if pane.TermProgram != "iTerm.app" || pane.ITermSessionID != "w0t0p0-CLIENT" {
		t.Errorf("host identity not adopted: %+v", pane)
	}
	if pane.TTY != "/dev/ttys012" {
		t.Errorf("TTY: want the client's tab, got %q", pane.TTY)
	}
	if pane.HerdrPaneID != "w1:p2" || pane.HerdrSocketPath != "/cfg/herdr/sessions/probe/herdr.sock" {
		t.Errorf("the pane's own address must survive: %+v", pane)
	}
}

// TestLauncher_AdoptHostIdentity_NoClientIsNoOp is the honest-failure lock: a
// detached herdr session has no window anywhere, and must keep its pane-only
// launcher rather than end up half-populated.
func TestLauncher_AdoptHostIdentity_NoClientIsNoOp(t *testing.T) {
	for name, from := range map[string]*Launcher{
		"nil":   nil,
		"empty": {},
	} {
		pane := &Launcher{HerdrPaneID: "w1:p1", TTY: "/dev/ttys900"}
		if pane.AdoptHostIdentity(from) {
			t.Errorf("%s: must report no change", name)
		}
		if pane.TTY != "/dev/ttys900" || pane.HerdrPaneID != "w1:p1" {
			t.Errorf("%s: launcher was mutated: %+v", name, pane)
		}
	}
}

// TestLauncher_AdoptHostIdentity_KeepsOwnTTYWhenClientHasNone pins the reason
// the TTY copy is guarded: BackgroundAgent.Detached is computed from this
// field (#744), so a client resolved without a controlling terminal must not
// erase the pane's own pty and make a live session look detached.
func TestLauncher_AdoptHostIdentity_KeepsOwnTTYWhenClientHasNone(t *testing.T) {
	pane := &Launcher{HerdrPaneID: "w1:p1", TTY: "/dev/ttys900"}
	pane.AdoptHostIdentity(&Launcher{TermProgram: "ghostty"})
	if pane.TTY != "/dev/ttys900" {
		t.Errorf("TTY: want the pane's own pty preserved, got %q", pane.TTY)
	}
	if pane.TermProgram != "ghostty" {
		t.Errorf("TermProgram: want ghostty, got %q", pane.TermProgram)
	}
}

// TestLauncher_AdoptHostIdentity_TTYlessAgentStaysTTYless is the other half of
// the TTY guard, and the one the first cut of #1350 got wrong: a background
// agent detached into a pool inherits the pane's herdr env but genuinely has no
// controlling terminal. Handing it the client's tty would make
// BackgroundAgent.Detached compute false and hide the "detached" badge #744
// exists to show — for an agent with no window the user can ever reach.
func TestLauncher_AdoptHostIdentity_TTYlessAgentStaysTTYless(t *testing.T) {
	detachedAgent := &Launcher{HerdrPaneID: "w1:p1", HerdrSocketPath: "/cfg/herdr/herdr.sock"}
	detachedAgent.AdoptHostIdentity(&Launcher{TermProgram: "iTerm.app", TTY: "/dev/ttys012"})
	if detachedAgent.TTY != "" {
		t.Errorf("an agent with no controlling terminal must not adopt the client's: got %q", detachedAgent.TTY)
	}
	// The rest of the host identity is still adopted — the window exists even
	// though this process has no terminal of its own.
	if detachedAgent.TermProgram != "iTerm.app" {
		t.Errorf("TermProgram: want iTerm.app, got %q", detachedAgent.TermProgram)
	}
}

// TestLauncher_AdoptHostIdentity_TmuxPaneKeepsItsAddress is #1501's defect
// test, and the riskiest line of that change: AdoptHostIdentity's "put back"
// set was closed at the two Herdr* fields, so a tmux pane adopting its
// client's identity would have been handed a launcher with the client's window
// and NO pane address — the client is a GUI terminal and carries none.
//
// Losing it is worse than the nil it replaced. TmuxPane/TmuxSocket are what
// the macOS TmuxActivator selects with, so the result would be a click that
// raises the right window and selects nothing, and a lookup that falls through to
// whatever backend the remaining fields happen to match.
//
// Seen RED against origin/main, where the put-back names only the herdr
// address.
func TestLauncher_AdoptHostIdentity_TmuxPaneKeepsItsAddress(t *testing.T) {
	pane := &Launcher{
		TmuxPane:   "%17",
		TmuxSocket: "/private/tmp/tmux-501/default",
		TTY:        "/dev/ttys900", // the pane's own pty
	}
	client := &Launcher{
		TermProgram:    "iTerm.app",
		ITermSessionID: "w0t0p0-CLIENT",
		TTY:            "/dev/ttys012",
		HostBundleID:   "com.googlecode.iterm2",
	}
	if !pane.AdoptHostIdentity(client) {
		t.Fatal("adopting a populated client identity must report a change")
	}
	if pane.TermProgram != "iTerm.app" || pane.ITermSessionID != "w0t0p0-CLIENT" {
		t.Errorf("host identity not adopted: %+v", pane)
	}
	if pane.TmuxPane != "%17" || pane.TmuxSocket != "/private/tmp/tmux-501/default" {
		t.Errorf("the pane lost the address TmuxActivator selects on: %+v", pane)
	}
	if pane.TTY != "/dev/ttys012" {
		t.Errorf("TTY: want the client's tab, got %q", pane.TTY)
	}
}

// TestLauncher_AdoptHostIdentity_HerdrPaneKeepsTheClientsTmuxAddress is the
// lock the #1501 put-back had to not break. A herdr client can itself be
// running inside a tmux pane, in which case its TmuxPane/TmuxSocket are its
// OWN and describe the window displaying the herdr pane — pane()'s
// herdr-before-tmux ordering exists precisely because both addresses can be
// present and mean different things.
//
// So the put-back is the pane's own address only, chosen by the same
// herdr-then-tmux precedence processlifecycle.launcherFromEnv captures with. A
// put-back that unconditionally cleared both address pairs would erase this.
func TestLauncher_AdoptHostIdentity_HerdrPaneKeepsTheClientsTmuxAddress(t *testing.T) {
	pane := &Launcher{HerdrPaneID: "w1:p2", HerdrSocketPath: "/cfg/herdr/herdr.sock"}
	pane.AdoptHostIdentity(&Launcher{
		TermProgram: "iTerm.app",
		TmuxPane:    "%4",
		TmuxSocket:  "/private/tmp/tmux-501/default",
		TTY:         "/dev/ttys012",
	})
	if pane.HerdrPaneID != "w1:p2" || pane.HerdrSocketPath != "/cfg/herdr/herdr.sock" {
		t.Errorf("the herdr pane's own address must survive: %+v", pane)
	}
	if pane.TmuxPane != "%4" || pane.TmuxSocket != "/private/tmp/tmux-501/default" {
		t.Errorf("the client's own tmux address must pass through: %+v", pane)
	}
}

// TestLauncher_AdoptHostIdentity_NoPaneKeepsNothing pins the third branch.
// A launcher in no pane at all has no own address to put back, so from stands
// whole. Unreachable from either production call site — both resolve a client
// only for a launcher that IS a pane — and pinned rather than left to be
// inferred from the absence of a branch.
func TestLauncher_AdoptHostIdentity_NoPaneKeepsNothing(t *testing.T) {
	plain := &Launcher{TermProgram: "ghostty"}
	plain.AdoptHostIdentity(&Launcher{TermProgram: "iTerm.app", TmuxPane: "%9", HerdrPaneID: "w1:p1"})
	if plain.TmuxPane != "%9" || plain.HerdrPaneID != "w1:p1" {
		t.Errorf("with no own pane address there is nothing to put back: %+v", plain)
	}
}

// TestLauncher_SamePaneAs is what stands between the periodic host refresh and
// a PID-reuse misroute (services.applyMultiplexerHostRefresh).
func TestLauncher_SamePaneAs(t *testing.T) {
	cases := map[string]struct {
		stored, fresh *Launcher
		want          bool
	}{
		"same herdr pane": {
			&Launcher{HerdrPaneID: "w1:p1"}, &Launcher{HerdrPaneID: "w1:p1"}, true,
		},
		"same tmux pane": {
			&Launcher{TmuxPane: "%17"}, &Launcher{TmuxPane: "%17"}, true,
		},
		"a herdr pane whose client moved into tmux is still the same pane": {
			&Launcher{HerdrPaneID: "w1:p1", TmuxPane: "%4"},
			&Launcher{HerdrPaneID: "w1:p1", TmuxPane: "%9"},
			true,
		},
		"tmux pane rebound to another pane": {
			&Launcher{TmuxPane: "%17"}, &Launcher{TmuxPane: "%18"}, false,
		},
		"the pid is no longer in any pane": {
			&Launcher{TmuxPane: "%17"}, &Launcher{TermProgram: "ghostty"}, false,
		},
		"the pid moved from tmux into herdr": {
			&Launcher{TmuxPane: "%17"}, &Launcher{HerdrPaneID: "w1:p1"}, false,
		},
		"neither is in a pane": {
			&Launcher{TermProgram: "ghostty"}, &Launcher{TermProgram: "ghostty"}, false,
		},
	}
	for name, tc := range cases {
		if got := tc.stored.SamePaneAs(tc.fresh); got != tc.want {
			t.Errorf("%s: SamePaneAs = %v, want %v", name, got, tc.want)
		}
	}
}
