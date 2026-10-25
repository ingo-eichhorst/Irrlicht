package processlifecycle_test

import (
	"path/filepath"
	"testing"

	"irrlicht/core/adapters/inbound/agents/opencode"
	"irrlicht/core/adapters/inbound/agents/processlifecycle"
	"irrlicht/core/domain/agent"
)

// TestOpenCodeServiceGetsNoPlaceholderRow is #2105's scanner-level test for the
// ghost row: opencode's REAL declaration through the REAL scanner, wired the
// way core/cmd/irrlichd/wiring.go wires it (WithArgvFilter(SkipsPreSession)).
//
// The rows mirror the sandbox measurement in #2105: opencode 2.x's background
// service is comm `opencode`, argv `<exe> serve --service`, cwd $HOME; a TUI
// client is comm `opencode` too, in its project. On main the service was
// minted as `proc-<pid>` in $HOME. The TUI row is the lock: the predicate must
// not swallow a client.
func TestOpenCodeServiceGetsNoPlaceholderRow(t *testing.T) {
	home := canonicalTempDir(t)
	project := canonicalTempDir(t)
	const (
		service = 41001
		tui     = 41002
	)
	processlifecycle.SetFakeProcessesForTest(t, map[int]processlifecycle.FakeProcess{
		service: {Name: "opencode", CWD: home, Argv: []string{"/opt/homebrew/Cellar/opencode/2.1.4/bin/opencode", "serve", "--service"}},
		tui:     {Name: "opencode", CWD: project, Argv: []string{"/opt/homebrew/Cellar/opencode/2.1.4/bin/opencode"}},
	})

	s := processlifecycle.NewScanner(opencode.ProcessName, opencode.AdapterName, 0).
		WithArgvFilter(opencode.Agent().Process.SkipsPreSession)
	ch := s.Subscribe()
	defer s.Unsubscribe(ch)

	s.PollForTest()

	minted := map[string]bool{}
	for drained := false; !drained; {
		select {
		case ev := <-ch:
			if ev.Type == agent.EventNewSession {
				minted[ev.CWD] = true
			}
		default:
			drained = true
		}
	}
	// maybeTrackNewPID tracks a PID and emits its EventNewSession under one
	// lock, so a minted cwd is a tracked PID.
	if !minted[project] {
		t.Fatalf("the TUI client (pid %d, cwd %s) got no row — the scan did not run, or the "+
			"service predicate swallowed a client; minted cwds = %v", tui, project, minted)
	}
	if minted[home] {
		t.Errorf("opencode 2.x's background service (pid %d, `serve --service`, cwd %s) was "+
			"minted a placeholder row; minted cwds = %v", service, home, minted)
	}
}

func canonicalTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		return resolved // CWDOf reports the canonical path
	}
	return dir
}
