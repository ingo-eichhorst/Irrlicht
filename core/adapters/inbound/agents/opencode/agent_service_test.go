package opencode

import "testing"

// TestAgentDeclaresTheV2ServiceAsSessionHost is #2105's red-first table test
// for the ghost row. opencode 2.x runs one background service per user, started
// as `<exe> serve --service` with its cwd changed to $HOME. On main nothing
// declared it, so the process scanner minted a `proc-<pid>` placeholder row in
// $HOME for it (82 samples in the sandbox's qa/main-p1base run).
//
// The argv shape is read out of the opencode 2.1.4 bundle with `strings`, never
// by running it: the service is spawned as `[...oj(), "serve", "--service"]`,
// where oj() is `[process.execPath]` for the compiled binary (comm `opencode`),
// and the fallback command is literally `["opencode","serve","--service"]`.
func TestAgentDeclaresTheV2ServiceAsSessionHost(t *testing.T) {
	isHost := Agent().Process.SessionHostArgv
	if isHost == nil {
		t.Fatal("opencode declares no Process.SessionHostArgv, so the scanner mints a " +
			"placeholder row for opencode 2.x's background service")
	}
	for _, c := range []struct {
		name string
		argv []string
		want bool
	}{
		{"brew service", []string{"/opt/homebrew/Cellar/opencode/2.1.4/bin/opencode", "serve", "--service"}, true},
		{"curl service", []string{"/Users/x/.opencode/bin/opencode", "serve", "--service"}, true},
		{"bundle fallback command", []string{"opencode", "serve", "--service"}, true},
		{"service flag after serve's own options", []string{"opencode", "serve", "--port", "49374", "--service"}, true},
		{"value-taking global option before serve", []string{"opencode", "--log-level", "debug", "serve", "--service"}, true},
		{"boolean global option before serve", []string{"opencode", "--print-logs", "serve", "--service"}, true},
		{"nil argv", nil, false},
		{"empty argv", []string{}, false},
		{"bare TUI", []string{"opencode"}, false},
		{"TUI in a project", []string{"opencode", "/Users/x/project"}, false},
		{"V1 headless server", []string{"opencode", "serve"}, false},
		{"stdio server", []string{"opencode", "serve", "--stdio"}, false},
		{"prompt slot holding the words", []string{"opencode", "run", "serve --service"}, false},
		{"prompt words split into argv", []string{"opencode", "run", "serve", "--service"}, false},
		{"serve as the value of --prompt", []string{"opencode", "--prompt", "serve", "--service"}, false},
		{"serve as the value of --session", []string{"opencode", "--session", "serve", "--service"}, false},
		{"serve as the value of -s", []string{"opencode", "-s", "serve", "--service"}, false},
		{"serve as the value of --server", []string{"opencode", "--server", "serve", "--service"}, false},
		{"serve as the value of --log-level", []string{"opencode", "--log-level", "serve", "--service"}, false},
		{"--service after the operand separator", []string{"opencode", "serve", "--", "--service"}, false},
		{"serve only as argv[0]", []string{"serve", "--service"}, false},
	} {
		if got := isHost(c.argv); got != c.want {
			t.Errorf("%s: SessionHostArgv(%q) = %t, want %t", c.name, c.argv, got, c.want)
		}
	}
}
