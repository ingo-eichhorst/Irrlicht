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

// The declaration's four axes are pinned by the four tests below — one each,
// so a failure names the axis that broke instead of one omnibus assertion.

// TestAgentRegistration pins the identity axis.
func TestAgentRegistration(t *testing.T) {
	id := Agent().Identity

	if id.Name != AdapterName {
		t.Errorf("Identity.Name = %q, want %q", id.Name, AdapterName)
	}
	if id.DisplayName != "Hermes Agent" {
		t.Errorf("Identity.DisplayName = %q", id.DisplayName)
	}
	if id.IconSVGLight == "" || id.IconSVGDark == "" {
		t.Error("both icon variants must be set")
	}
}

// TestAgentRegistration_Process pins the process axis. Hermes ships as a Python
// console script: the OS process is the venv interpreter, so an ExactName
// matcher would never fire.
func TestAgentRegistration_Process(t *testing.T) {
	p := Agent().Process

	if _, ok := p.Match.(agent.CommandPattern); !ok {
		t.Errorf("Process.Match = %T, want agent.CommandPattern", p.Match)
	}
	if p.PIDForSession == nil {
		t.Error("Process.PIDForSession must be set")
	}
	if p.ExcludeArgv == nil {
		t.Error("Process.ExcludeArgv must be set to drop the long-lived gateway")
	}
}

// TestAgentRegistration_Source pins the source axis: one shared store, not a per-process
// transcript.
func TestAgentRegistration_Source(t *testing.T) {
	src, ok := Agent().Source.(agent.ProcessOwnedStore)
	if !ok {
		t.Fatalf("Source = %T, want agent.ProcessOwnedStore", Agent().Source)
	}
	if src.PathForPID == nil {
		t.Fatal("Source.PathForPID must be set")
	}
	if src.Reader == nil {
		t.Error("Source.Reader must be set")
	}
	// The store is one shared database; the PID is not part of its path.
	if src.PathForPID(1) != src.PathForPID(2) {
		t.Error("PathForPID must ignore the pid — the store is shared, not per-process")
	}
}

// TestAgentRegistration_Permissions pins the consent axis.
func TestAgentRegistration_Permissions(t *testing.T) {
	perms := Agent().Permissions

	if len(perms) != 1 {
		t.Fatalf("len(Permissions) = %d, want 1", len(perms))
	}
	p := perms[0]
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
	tests := []struct {
		name string
		cmd  string
		want bool
	}{
		{
			name: "one-shot via the console script (verified live)",
			cmd:  "/Users/x/hermes-local/.hermes/hermes-agent/venv/bin/python3 /Users/x/hermes-local/.hermes/hermes-agent/venv/bin/hermes -z Count to 10",
			want: true,
		},
		{
			name: "the module form (verified live)",
			cmd:  "/Users/x/hermes-local/.hermes/hermes-agent/venv/bin/python -m hermes_cli.main gateway run --replace",
			want: true,
		},
		{name: "the user-facing shim", cmd: "/Users/x/.local/bin/hermes", want: true},
		{name: "shim with a subcommand", cmd: "/opt/homebrew/bin/hermes chat", want: true},

		{
			name: "the daemon watching the store must not look like Hermes",
			cmd:  "irrlichd --watch /Users/x/.hermes/state.db",
			want: false,
		},
		{name: "the daemon itself", cmd: "/usr/local/bin/irrlichd", want: false},
		{
			name: `the install dir: "hermes" followed by "-", not space/end`,
			cmd:  "/Users/x/hermes-local/.hermes/hermes-agent/venv/bin/python -m something_else",
			want: false,
		},
		{
			name: "a different binary whose name merely starts with hermes",
			cmd:  "/usr/bin/hermesd --serve",
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := processCmdRegex.MatchString(tt.cmd); got != tt.want {
				t.Errorf("MatchString(%q) = %v, want %v", tt.cmd, got, tt.want)
			}
		})
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
			// A flag VALUE that happens to name a service must not be read as
			// the subcommand.
			name: "a service name as a flag value is still a session",
			argv: []string{venv + "/python3", venv + "/hermes", "--model", "gateway", "chat"},
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
