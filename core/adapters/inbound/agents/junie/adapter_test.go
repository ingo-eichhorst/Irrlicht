package junie

import (
	"testing"

	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/permission"
)

func TestAgent_Identity(t *testing.T) {
	a := Agent()
	if a.Identity.Name != AdapterName {
		t.Errorf("Name = %q, want %q", a.Identity.Name, AdapterName)
	}
	if a.Identity.DisplayName != "Junie" {
		t.Errorf("DisplayName = %q", a.Identity.DisplayName)
	}
	if a.Identity.IconSVGLight == "" || a.Identity.IconSVGDark == "" {
		t.Error("expected both light and dark icons")
	}
}

func TestAgent_Source_FilesUnderRoot(t *testing.T) {
	src, ok := Agent().Source.(agent.FilesUnderRoot)
	if !ok {
		t.Fatalf("Source is %T, want FilesUnderRoot", Agent().Source)
	}
	if src.Dir != defaultRootDir {
		t.Errorf("Dir = %q, want %q", src.Dir, defaultRootDir)
	}
	if src.SessionIDFromPath == nil {
		t.Error("expected SessionIDFromPath (filename is the constant events.jsonl)")
	}
	if _, ok := src.Parser.(agent.JSONLineParser); !ok {
		t.Errorf("Parser is %T, want JSONLineParser", src.Parser)
	}
}

func TestAgent_Process_CommandPattern(t *testing.T) {
	// The junie binary lives inside junie.app/Contents/MacOS/, so an
	// ExactName match on "junie" would be fragile across install layouts;
	// the adapter matches the full command line instead.
	if _, ok := Agent().Process.Match.(agent.CommandPattern); !ok {
		t.Errorf("Match is %T, want CommandPattern", Agent().Process.Match)
	}
	if Agent().Process.PIDForSession == nil {
		t.Error("expected PIDForSession")
	}
}

// TestAgent_ObserveOnly is a lock: Junie exposes no hook system, so the
// adapter must declare exactly one observe permission and no effect closures
// — nothing it does modifies the user's machine, and the consent surface must
// say so (the aider precedent).
func TestAgent_ObserveOnly(t *testing.T) {
	a := Agent()
	if len(a.Permissions) != 1 {
		t.Fatalf("len(Permissions) = %d, want 1 (observe-only adapter)", len(a.Permissions))
	}
	p := a.Permissions[0]
	if p.Key != PermissionKeyTranscripts {
		t.Errorf("Key = %q, want %q", p.Key, PermissionKeyTranscripts)
	}
	if p.Kind != permission.KindObserve {
		t.Errorf("Kind = %v, want KindObserve", p.Kind)
	}
	if p.Apply != nil || p.Remove != nil || p.Writes != nil {
		t.Error("observe permission must carry no Apply/Remove/Writes effects")
	}
}

func TestSessionIDFromPath(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{"/Users/x/.junie/sessions/session-260825-151320-19jp/events.jsonl", "session-260825-151320-19jp"},
		// The root's own index.jsonl must not mint a phantom session.
		{"/Users/x/.junie/sessions/index.jsonl", ""},
		// Sibling files inside a session directory are not transcripts.
		{"/Users/x/.junie/sessions/session-260825-151320-19jp/state.json", ""},
		{"/Users/x/.junie/sessions/session-260825-151320-19jp/transcript.md", ""},
		// Task subdirectories must not mint sessions of their own.
		{"/Users/x/.junie/sessions/session-260825-151320-19jp/task-260825-151518-1tmg/events.jsonl", ""},
		{"events.jsonl", ""}, // no parent dir
	}
	for _, c := range cases {
		if got := sessionIDFromPath(c.path); got != c.want {
			t.Errorf("sessionIDFromPath(%q) = %q, want %q", c.path, got, c.want)
		}
	}
}

func TestProcessCmdRegex(t *testing.T) {
	assertCmdRegexMatches(t, true, []string{
		// CLI install (observed live, 2026-08).
		"/Users/x/.local/share/junie/versions/2929.4/Applications/junie.app/Contents/MacOS/junie",
		"/Users/x/.local/share/junie/versions/2929.4/Applications/junie.app/Contents/MacOS/junie --session-id session-260825-151320-19jp",
		// IDE-spawned ACP instance (observed live, 2026-08).
		"/Users/x/Library/Caches/JetBrains/IntelliJIdea2026.2/acp-agents/junie/2913.6.0/Applications/junie.app/Contents/MacOS/junie --acp=true",
		"/Users/x/.local/bin/junie",
	})
	assertCmdRegexMatches(t, false, []string{
		// The daemon's own watchers mention the ~/.junie tree; the matcher
		// must not self-trip on them ("junie" preceded by "." not "/").
		"irrlichd --watch /Users/x/.junie/sessions",
		"tail -f /Users/x/.junie/sessions/session-260825-151320-19jp/events.jsonl",
		"ls /Users/x/.junie/processes",
		// Intermediate path components named junie are followed by "/" or ".".
		"cat /Users/x/.local/share/junie/versions/manifest.json",
		"vim /Users/x/notes-about-junie.md",
		"grep junie README.md",
	})
}

func assertCmdRegexMatches(t *testing.T, want bool, cmds []string) {
	t.Helper()
	for _, cmd := range cmds {
		if got := processCmdRegex.MatchString(cmd); got != want {
			t.Errorf("processCmdRegex.MatchString(%q) = %v, want %v", cmd, got, want)
		}
	}
}
