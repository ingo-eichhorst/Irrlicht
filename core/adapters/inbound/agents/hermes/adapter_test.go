package hermes

import (
	"path/filepath"
	"testing"

	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/permission"
)

// TestStorePath pins the $HERMES_HOME seam. Hermes resolves its home as
// context-override → HERMES_HOME → platform default, and only the env var is
// observable from outside the process. A relative value is a
// misconfiguration Hermes itself would not expand, so it must fall back
// rather than be reinterpreted.
func TestStorePath(t *testing.T) {
	tests := []struct {
		name string
		env  string
		want string // "" ⇒ expect $HOME/.hermes/state.db
	}{
		{"empty falls back to default", "", ""},
		{"absolute override", "/tmp/hermes-home", "/tmp/hermes-home/state.db"},
		{"trailing slash is cleaned", "/tmp/hermes-home/", "/tmp/hermes-home/state.db"},
		{"relative override is rejected", "relative/home", ""},
		{"tilde override is rejected (no shell expansion)", "~/custom", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv(homeEnvVar, tt.env)

			want := tt.want
			if want == "" {
				want = filepath.Join(home, defaultHomeRelPath, storeRelPath)
			}
			if got := StorePath(); got != want {
				t.Errorf("StorePath() = %q, want %q", got, want)
			}
		})
	}
}

// TestAgentRegistration pins the declaration's four axes.
func TestAgentRegistration(t *testing.T) {
	a := Agent()

	if a.Identity.Name != AdapterName {
		t.Errorf("Identity.Name = %q, want %q", a.Identity.Name, AdapterName)
	}
	if a.Identity.DisplayName != "Hermes Agent" {
		t.Errorf("Identity.DisplayName = %q", a.Identity.DisplayName)
	}
	if a.Identity.IconSVGLight == "" || a.Identity.IconSVGDark == "" {
		t.Error("both icon variants must be set")
	}

	// Hermes ships as a Python console script: the OS process is the venv
	// interpreter, so an ExactName matcher would never fire.
	if _, ok := a.Process.Match.(agent.CommandPattern); !ok {
		t.Errorf("Process.Match = %T, want agent.CommandPattern", a.Process.Match)
	}
	if a.Process.PIDForSession == nil {
		t.Error("Process.PIDForSession must be set")
	}
	if a.Process.ExcludeArgv == nil {
		t.Error("Process.ExcludeArgv must be set to drop the long-lived gateway")
	}

	src, ok := a.Source.(agent.ProcessOwnedStore)
	if !ok {
		t.Fatalf("Source = %T, want agent.ProcessOwnedStore", a.Source)
	}
	if src.PathForPID == nil {
		t.Error("Source.PathForPID must be set")
	}
	if src.Reader == nil {
		t.Error("Source.Reader must be set")
	}
	// The store is one shared database; the PID is not part of its path.
	if src.PathForPID(1) != src.PathForPID(2) {
		t.Error("PathForPID must ignore the pid — the store is shared, not per-process")
	}

	if len(a.Permissions) != 1 {
		t.Fatalf("len(Permissions) = %d, want 1", len(a.Permissions))
	}
	p := a.Permissions[0]
	if p.Key != PermissionKeyStore {
		t.Errorf("Permission.Key = %q, want %q", p.Key, PermissionKeyStore)
	}
	if p.Kind != permission.KindObserve {
		t.Errorf("Permission.Kind = %q, want observe", p.Kind)
	}
	// An observe permission's effect is owned by the daemon wiring (start /
	// stop watchers), so it must carry no closures of its own.
	if p.Apply != nil || p.Remove != nil {
		t.Error("observe permission must have nil Apply/Remove")
	}
}

// TestProcessCmdRegex pins both real invocation shapes and — more
// importantly — the paths that must NOT match. The Hermes home and install
// directory both contain the substring "hermes", so a looser pattern would
// let the daemon's own argv self-trip the matcher.
func TestProcessCmdRegex(t *testing.T) {
	shouldMatch := []string{
		// Verified live: one-shot session via the console script.
		"/Users/x/hermes-local/.hermes/hermes-agent/venv/bin/python3 /Users/x/hermes-local/.hermes/hermes-agent/venv/bin/hermes -z Count to 10",
		// Verified live: the module form.
		"/Users/x/hermes-local/.hermes/hermes-agent/venv/bin/python -m hermes_cli.main gateway run --replace",
		// The user-facing shim.
		"/Users/x/.local/bin/hermes",
		"/opt/homebrew/bin/hermes chat",
	}
	for _, cmd := range shouldMatch {
		if !processCmdRegex.MatchString(cmd) {
			t.Errorf("expected match: %q", cmd)
		}
	}

	shouldNotMatch := []string{
		// The daemon watching the Hermes store must not look like Hermes.
		"irrlichd --watch /Users/x/.hermes/state.db",
		"/usr/local/bin/irrlichd",
		// The install directory: "hermes" followed by "-", not space/end.
		"/Users/x/hermes-local/.hermes/hermes-agent/venv/bin/python -m something_else",
		// A different binary whose name merely starts with hermes.
		"/usr/bin/hermesd --serve",
	}
	for _, cmd := range shouldNotMatch {
		if processCmdRegex.MatchString(cmd) {
			t.Errorf("expected NO match: %q", cmd)
		}
	}
}

// TestIsServiceArgv pins the session-vs-service split. Binding a session to
// the long-lived gateway would reproduce the claude-code --bg-spare ghost
// (#727): a pool process that outlives every session and never reaps.
func TestIsServiceArgv(t *testing.T) {
	venv := "/Users/x/hermes-local/.hermes/hermes-agent/venv/bin"

	tests := []struct {
		name string
		argv []string
		want bool
	}{
		{
			name: "gateway is a service",
			argv: []string{venv + "/python", "-m", "hermes_cli.main", "gateway", "run", "--replace"},
			want: true,
		},
		{
			name: "serve is a service",
			argv: []string{venv + "/python3", venv + "/hermes", "serve"},
			want: true,
		},
		{
			name: "one-shot is a session",
			argv: []string{venv + "/python3", venv + "/hermes", "-z", "Count to 10"},
			want: false,
		},
		{
			name: "chat is a session",
			argv: []string{venv + "/python3", venv + "/hermes", "chat"},
			want: false,
		},
		{
			// The regression this guards: -z carries arbitrary user text, so
			// a prompt that happens to start with a service word would
			// otherwise read as that subcommand and hide a real session.
			name: "one-shot whose PROMPT names a service is still a session",
			argv: []string{venv + "/python3", venv + "/hermes", "-z", "gateway is down, debug it"},
			want: false,
		},
		{
			// Per the ExcludeArgv contract: an unreadable argv must not exclude.
			name: "nil argv is not excluded",
			argv: nil,
			want: false,
		},
		{
			name: "empty argv is not excluded",
			argv: []string{},
			want: false,
		},
		{
			name: "argv with no hermes entry is not excluded",
			argv: []string{"/bin/zsh", "-c", "echo hi"},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isServiceArgv(tt.argv); got != tt.want {
				t.Errorf("isServiceArgv(%q) = %v, want %v", tt.argv, got, tt.want)
			}
		})
	}
}
