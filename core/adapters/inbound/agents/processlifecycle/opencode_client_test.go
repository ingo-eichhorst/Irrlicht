package processlifecycle_test

import (
	"testing"

	"irrlicht/core/adapters/inbound/agents/opencode"
	"irrlicht/core/adapters/inbound/agents/processlifecycle"
)

// opencodeV2Processes is the process table #2104's live verification measured
// for opencode 2.x, plus the `opencode2` and npm `opencode.exe` spellings of a
// client: the background service (`serve --service`, cwd $HOME) and TUI
// clients, each with its project directory as its cwd — and a process that
// only NAMES an opencode binary in its arguments, as #2106's live run's
// tui-detach.py driver did.
func opencodeV2Processes(t *testing.T) (home, tuiProject, tui2Project, exeProject, driverDir string) {
	t.Helper()
	home = canonicalTempDir(t)
	tuiProject = canonicalTempDir(t)
	tui2Project = canonicalTempDir(t)
	exeProject = canonicalTempDir(t)
	driverDir = canonicalTempDir(t)
	processlifecycle.SetFakeProcessesForTest(t, map[int]processlifecycle.FakeProcess{
		42001: {Name: "opencode", CWD: home, Argv: []string{"/x/shim/opencode", "serve", "--service"}},
		42002: {Name: "opencode", CWD: tuiProject, Argv: []string{"/opt/homebrew/bin/opencode"}},
		42003: {Name: "opencode2", CWD: tui2Project, Argv: []string{"/Users/x/.local/opencode-v2/bin/opencode2", "--prompt", "Reply with PONG."}},
		42004: {Name: "opencode.exe", CWD: exeProject, Argv: []string{"/x/node_modules/@opencode/cli/bin/opencode.exe"}},
		42005: {Name: "python3", CWD: driverDir, Argv: []string{"python3", "/x/tui-detach.py", "/x/shim/opencode"}},
	})
	return home, tuiProject, tui2Project, exeProject, driverDir
}

// TestOpenCodeDiscoverPID_BindsClientsNotTheService is #2106's red-first test
// for PID binding. A session binds to the client in its directory, whichever
// spelling the client was started under — and never to the 2.x service,
// whose cwd is $HOME: a session in $HOME has no client here. On main
// DiscoverPID matched by comm `opencode` alone, so it bound a $HOME session to
// the service and found no `opencode2` or `opencode.exe` client at all.
func TestOpenCodeDiscoverPID_BindsClientsNotTheService(t *testing.T) {
	home, tuiProject, tui2Project, exeProject, driverDir := opencodeV2Processes(t)
	for _, c := range []struct {
		name string
		cwd  string
		want int
	}{
		{"service in $HOME is not a client", home, 0},
		{"TUI as opencode", tuiProject, 42002},
		{"TUI as opencode2", tui2Project, 42003},
		{"npm opencode.exe", exeProject, 42004},
		{"a driver naming opencode in its arguments is not a client", driverDir, 0},
	} {
		got, err := opencode.DiscoverPID(c.cwd, "", nil)
		if err != nil || got != c.want {
			t.Errorf("%s: DiscoverPID(%s) = (%d, %v), want (%d, nil)", c.name, c.cwd, got, err, c.want)
		}
	}
}

// TestOpenCodeLiveClientCWDs_ExcludesTheService: the watcher admits a 2.x
// session only while a client owns its directory, so the live set it reads
// must hold every client's cwd and not the service's $HOME — or every
// session the service hosts there would read as client-run (#2106).
func TestOpenCodeLiveClientCWDs_ExcludesTheService(t *testing.T) {
	home, tuiProject, tui2Project, exeProject, driverDir := opencodeV2Processes(t)
	live, err := opencode.LiveClientCWDs()
	if err != nil {
		t.Fatalf("LiveClientCWDs: %v", err)
	}
	for _, cwd := range []string{tuiProject, tui2Project, exeProject} {
		if _, ok := live[cwd]; !ok {
			t.Errorf("client cwd %s missing from the live set %v", cwd, live)
		}
	}
	if _, ok := live[home]; ok {
		t.Errorf("the service's cwd %s (service in $HOME is not a client) is in the live set %v", home, live)
	}
	if _, ok := live[driverDir]; ok {
		t.Errorf("a driver naming opencode only in its arguments put %s in the live set %v", driverDir, live)
	}
}
