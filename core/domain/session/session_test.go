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
