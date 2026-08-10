package copilot

import (
	"path/filepath"
	"strings"
	"testing"

	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/permission"
)

func TestSessionsDir(t *testing.T) {
	t.Run("default when COPILOT_HOME is unset", func(t *testing.T) {
		t.Setenv(copilotHomeEnvVar, "")
		if got := sessionsDir(); got != defaultRootDir {
			t.Errorf("sessionsDir() = %q, want %q", got, defaultRootDir)
		}
	})

	t.Run("absolute COPILOT_HOME relocates the store", func(t *testing.T) {
		t.Setenv(copilotHomeEnvVar, "/tmp/copilot-alt")
		want := filepath.Join("/tmp/copilot-alt", "session-state")
		if got := sessionsDir(); got != want {
			t.Errorf("sessionsDir() = %q, want %q", got, want)
		}
	})

	t.Run("relative COPILOT_HOME is ignored", func(t *testing.T) {
		// agentpaths.FromEnv logs and ignores non-absolute values — nothing
		// here expands a shell metacharacter, so "~/x" is a misconfiguration.
		t.Setenv(copilotHomeEnvVar, "~/custom")
		if got := sessionsDir(); got != defaultRootDir {
			t.Errorf("sessionsDir() = %q, want the default %q", got, defaultRootDir)
		}
	})
}

func TestSessionIDFromPath(t *testing.T) {
	const id = "f447df2a-06bf-441b-bc33-970693513198"
	tests := []struct {
		name string
		path string
		want string
	}{
		{
			name: "transcript yields its parent directory",
			path: "/home/u/.copilot/session-state/" + id + "/events.jsonl",
			want: id,
		},
		{
			// The sibling files in every session directory must not mint a
			// second session for the same conversation. workspace.yaml is not
			// .jsonl so the watcher filters it before we see it, but the
			// contract is asserted here regardless.
			name: "workspace.yaml is not ours",
			path: "/home/u/.copilot/session-state/" + id + "/workspace.yaml",
			want: "",
		},
		{
			name: "an unrelated .jsonl in the session dir is not ours",
			path: "/home/u/.copilot/session-state/" + id + "/other.jsonl",
			want: "",
		},
		{
			name: "a transcript at the filesystem root yields nothing",
			path: "/events.jsonl",
			want: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := sessionIDFromPath(tc.path); got != tc.want {
				t.Errorf("sessionIDFromPath(%q) = %q, want %q", tc.path, got, tc.want)
			}
		})
	}
}

func TestAgent_Identity(t *testing.T) {
	a := Agent()
	if a.Identity.Name != AdapterName {
		t.Errorf("Name = %q, want %q", a.Identity.Name, AdapterName)
	}
	if a.Identity.DisplayName != "GitHub Copilot" {
		t.Errorf("DisplayName = %q, want %q", a.Identity.DisplayName, "GitHub Copilot")
	}
	// Both icon variants are served through GET /api/v1/agents, which is how
	// the web dashboard and the macOS app render a new adapter without any
	// per-adapter code of their own. An empty one ships a blank tile.
	for name, svg := range map[string]string{"light": a.Identity.IconSVGLight, "dark": a.Identity.IconSVGDark} {
		if !strings.HasPrefix(svg, "<svg") || !strings.HasSuffix(svg, "</svg>") {
			t.Errorf("%s icon is not a complete <svg> element", name)
		}
	}
	if a.Identity.IconSVGLight == a.Identity.IconSVGDark {
		t.Error("light and dark icons are identical — the mark must invert for dark chrome")
	}
}

func TestAgent_Source_FilesUnderRoot(t *testing.T) {
	src, ok := Agent().Source.(agent.FilesUnderRoot)
	if !ok {
		t.Fatalf("Source = %T, want agent.FilesUnderRoot", Agent().Source)
	}
	if src.SessionIDFromPath == nil {
		t.Fatal("SessionIDFromPath is nil — the session id would be taken from " +
			"the constant filename events.jsonl, collapsing every session into one")
	}
	if got := src.SessionIDFromPath("/r/session-state/abc/events.jsonl"); got != "abc" {
		t.Errorf("wired SessionIDFromPath returned %q, want %q", got, "abc")
	}
	if _, ok := src.Parser.(agent.JSONLineParser); !ok {
		t.Errorf("Parser = %T, want agent.JSONLineParser", src.Parser)
	}
}

func TestAgent_Process_ExactName(t *testing.T) {
	m, ok := Agent().Process.Match.(agent.ExactName)
	if !ok {
		t.Fatalf("Process.Match = %T, want agent.ExactName", Agent().Process.Match)
	}
	// The npm install runs the agent as a native child of a node loader.
	// pgrep -x matches the kernel accounting name — the executable basename —
	// so this binds the child and never the "node" parent.
	if m.Name != "copilot" {
		t.Errorf("ExactName = %q, want %q", m.Name, "copilot")
	}
	if Agent().Process.PIDForSession == nil {
		t.Error("PIDForSession is nil — sessions would never bind to a live process")
	}
}

// TestAgent_Permissions pins the consent surface: one observe permission for
// reading transcripts, and — since #1378 — one modify permission for the hook
// install.
//
// The ONE-observe count matters beyond tidiness: PermissionService resolves an
// adapter to its FIRST observe-kind entry and stops, so a second observe entry
// would be silently unreachable. The hooks entry must therefore be
// modify-kind, which is asserted rather than assumed.
//
// Both entries are checked for the four prose fields because the consent
// wizard renders all of them; the hooks entry's copy is additionally bound to
// what the installer actually writes by AssertHookDisclosureMatchesInstalled
// (hookdisclosure_test.go), which is the check that would catch a stale event
// list here.
// permissionsByKey indexes the real registration once for the subtests below.
func permissionsByKey(t *testing.T) map[string]agent.Permission {
	t.Helper()
	perms := Agent().Permissions
	if len(perms) != 2 {
		t.Fatalf("got %d permissions, want exactly 2 (transcripts + hooks)", len(perms))
	}
	byKey := make(map[string]agent.Permission, len(perms))
	for _, p := range perms {
		byKey[p.Key] = p
	}
	return byKey
}

// Split into four top-level tests rather than subtests: a t.Run closure's body
// counts toward its ENCLOSING function's cognitive complexity, so grouping
// them made one function measurably harder to read, not easier.

func TestAgent_Permissions_ExactlyOneObserveEntry(t *testing.T) {
	observes := 0
	for _, p := range permissionsByKey(t) {
		if p.Kind == permission.KindObserve {
			observes++
		}
	}
	if observes != 1 {
		t.Errorf("got %d observe-kind permissions, want exactly 1 — PermissionService "+
			"resolves an adapter to its first observe entry and stops, so any second "+
			"one is unreachable", observes)
	}
}

func TestAgent_Permissions_TranscriptsIsObserveOnly(t *testing.T) {
	p, ok := permissionsByKey(t)[PermissionKeyTranscripts]
	if !ok {
		t.Fatalf("no %q permission declared", PermissionKeyTranscripts)
	}
	if p.Kind != permission.KindObserve {
		t.Errorf("Kind = %v, want %v", p.Kind, permission.KindObserve)
	}
	if p.Apply != nil || p.Remove != nil {
		t.Error("observe-kind permission must not carry Apply/Remove effects")
	}
	if p.Writes != nil {
		t.Error("an observe-kind permission must not declare a ManagedUserFile")
	}
}

// TestAgent_Permissions_HooksIsModifyKind checks only what is adapter-specific.
// Writes' sub-fields (absolute Path, Uninstall, Verify, Version) are asserted
// registry-wide for every adapter by cmd/irrlichd/managedfiles_test.go,
// agents/hookverify_test.go and agents/hookversion_test.go; copilot is in
// All() and inherits all three.
func TestAgent_Permissions_HooksIsModifyKind(t *testing.T) {
	p, ok := permissionsByKey(t)[PermissionKeyHooks]
	if !ok {
		t.Fatalf("no %q permission declared", PermissionKeyHooks)
	}
	if p.Kind != permission.KindModify {
		t.Errorf("Kind = %v, want %v — an install writes a file", p.Kind, permission.KindModify)
	}
	if p.Apply == nil || p.Remove == nil {
		t.Error("the hooks permission must carry both Apply and Remove effects")
	}
	if p.Writes == nil {
		t.Error("the hooks permission must declare the file it writes")
	}
}

func TestAgent_Permissions_ConsentProseIsComplete(t *testing.T) {
	for _, p := range permissionsByKey(t) {
		for name, field := range map[string]string{
			"Title": p.Title, "FeatureUnlocked": p.FeatureUnlocked,
			"Touches": p.Touches, "Detail": p.Detail,
		} {
			if strings.TrimSpace(field) == "" {
				t.Errorf("%s/%s is empty — the consent wizard renders this to the user", p.Key, name)
			}
		}
	}
}

// TestAgent_NoControlDeclared pins the deliberate omission: backchannel input
// is not declared until the factory's control scenarios verify it live. A
// Control block without a matching ControlPermission would advertise input the
// daemon refuses to forward.
func TestAgent_NoControlDeclared(t *testing.T) {
	if Agent().Control.SupportsInput {
		t.Error("SupportsInput is true but no ControlPermission is declared — " +
			"add agent.ControlPermission() alongside it, and verify the presets live")
	}
}
