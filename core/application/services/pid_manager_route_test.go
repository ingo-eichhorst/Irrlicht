package services_test

import (
	"testing"
	"time"

	"irrlicht/core/domain/session"
)

// TestCaptureRoute_PassesAdapterAndCWD pins the #2010 seam: the route reader
// needs the session's adapter (only a Claude Code session has settings files
// to consult) and its working directory (where the project and local
// settings files are found from).
func TestCaptureRoute_PassesAdapterAndCWD(t *testing.T) {
	repo := newMockRepo()
	repo.states["s"] = &session.SessionState{
		SessionID: "s",
		Adapter:   "claude-code",
		CWD:       "/work/project",
		State:     session.StateWorking,
		UpdatedAt: time.Now().Unix(),
	}
	pm := newPIDManagerForTest(repo)
	var gotAdapter, gotCWD string
	pm.SetRouteReader(func(pid int, adapter, cwd string) *session.RouteObservation {
		gotAdapter, gotCWD = adapter, cwd
		return &session.RouteObservation{Status: session.RouteAbsent}
	})

	pm.HandlePIDAssigned(42, "s")

	if gotAdapter != "claude-code" || gotCWD != "/work/project" {
		t.Fatalf("reader got adapter=%q cwd=%q, want claude-code and /work/project", gotAdapter, gotCWD)
	}
}
