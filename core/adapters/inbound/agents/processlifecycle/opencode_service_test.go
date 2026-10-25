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
// way core/cmd/irrlichd/wiring.go wires it — the adapter's name, its
// CommandPattern as WithCommandLineMatch (#2106), WithArgvFilter(SkipsPreSession).
//
// The rows mirror the sandbox measurement in #2105: opencode 2.x's background
// service is comm `opencode`, argv `<exe> serve --service`, cwd $HOME; a TUI
// client is comm `opencode` too, in its project, or `opencode2` (#2106). On
// main the service was minted as `proc-<pid>` in $HOME. The TUI rows are the
// lock: the predicate must not swallow a client.
func TestOpenCodeServiceGetsNoPlaceholderRow(t *testing.T) {
	home := canonicalTempDir(t)
	project := canonicalTempDir(t)
	project2 := canonicalTempDir(t)
	const (
		service = 41001
		tui     = 41002
		tui2    = 41003
	)
	processlifecycle.SetFakeProcessesForTest(t, map[int]processlifecycle.FakeProcess{
		service: {Name: "opencode", CWD: home, Argv: []string{"/opt/homebrew/Cellar/opencode/2.1.4/bin/opencode", "serve", "--service"}},
		tui:     {Name: "opencode", CWD: project, Argv: []string{"/opt/homebrew/Cellar/opencode/2.1.4/bin/opencode"}},
		tui2:    {Name: "opencode2", CWD: project2, Argv: []string{"/Users/x/.local/opencode-v2/bin/opencode2"}},
	})

	decl := opencode.Agent()
	match, ok := decl.Process.Match.(agent.CommandPattern)
	if !ok {
		t.Fatalf("opencode's matcher is %T; this test wires a CommandPattern the way wiring.go does", decl.Process.Match)
	}
	s := processlifecycle.NewScanner(decl.Identity.Name, opencode.AdapterName, 0).
		WithCommandLineMatch(match.Regex.String()).
		WithArgvFilter(decl.Process.SkipsPreSession)
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
	for pid, cwd := range map[int]string{tui: project, tui2: project2} {
		if !minted[cwd] {
			t.Fatalf("the TUI client (pid %d, cwd %s) got no row — the scan did not run, or the "+
				"service predicate swallowed a client; minted cwds = %v", pid, cwd, minted)
		}
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
