package vibe

import (
	"testing"

	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/backchannel"
)

// TestSessionsDir pins the $VIBE_HOME seam. Upstream honors the override in
// source (v2.19.1, vibe/core/paths/_vibe_home.py) though not in its docs, so
// a hardcoded root would make every session of a $VIBE_HOME user invisible.
// Absolute-only, matching the sibling adapters: irrlicht performs no shell
// expansion, so a relative or "~"-prefixed value is logged and ignored.
func TestSessionsDir(t *testing.T) {
	tests := []struct {
		name string
		env  string
		want string
	}{
		{"empty falls back to default", "", defaultRootDir},
		{"absolute override produces $VIBE_HOME/logs/session", "/tmp/vibe-home", "/tmp/vibe-home/logs/session"},
		{"trailing slash is cleaned", "/tmp/vibe-home/", "/tmp/vibe-home/logs/session"},
		{"relative override is rejected (falls back to default)", "relative/home", defaultRootDir},
		{"tilde-prefixed override is rejected (no shell expansion)", "~/custom", defaultRootDir},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(vibeHomeEnvVar, tc.env)
			if got := sessionsDir(); got != tc.want {
				t.Errorf("sessionsDir() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAgent_Identity(t *testing.T) {
	a := Agent()
	if a.Identity.Name != AdapterName {
		t.Errorf("Name = %q, want %q", a.Identity.Name, AdapterName)
	}
	if a.Identity.DisplayName != "Mistral Vibe" {
		t.Errorf("DisplayName = %q", a.Identity.DisplayName)
	}
	if a.Identity.IconSVGLight == "" || a.Identity.IconSVGDark == "" {
		t.Error("expected both light and dark icons")
	}
}

func TestAgent_Source_FilesUnderRoot(t *testing.T) {
	// Pin the default root explicitly: Agent() resolves Dir through
	// sessionsDir(), so a $VIBE_HOME set in the developer's own shell would
	// otherwise leak into this assertion.
	t.Setenv(vibeHomeEnvVar, "")

	src, ok := Agent().Source.(agent.FilesUnderRoot)
	if !ok {
		t.Fatalf("Source is %T, want FilesUnderRoot", Agent().Source)
	}
	if src.Dir != defaultRootDir {
		t.Errorf("Dir = %q, want %q", src.Dir, defaultRootDir)
	}
	if src.SessionIDFromPath == nil {
		t.Error("expected SessionIDFromPath (filename is the constant messages.jsonl)")
	}
	if _, ok := src.Parser.(agent.JSONLineParser); !ok {
		t.Errorf("Parser is %T, want JSONLineParser", src.Parser)
	}
}

func TestAgent_Process_CommandPattern(t *testing.T) {
	// Vibe is a Python console-script, so it must match on the command line,
	// not an exact process name.
	if _, ok := Agent().Process.Match.(agent.CommandPattern); !ok {
		t.Errorf("Match is %T, want CommandPattern", Agent().Process.Match)
	}
	if Agent().Process.PIDForSession == nil {
		t.Error("expected PIDForSession")
	}
}

func TestAgent_Control_Backchannel(t *testing.T) {
	a := Agent()
	if !a.Control.SupportsInput {
		t.Error("expected Control.SupportsInput (vibe is an interactive REPL)")
	}
	if a.Control.Interrupt != agent.InterruptCtrlC {
		t.Errorf("Interrupt = %v, want InterruptCtrlC", a.Control.Interrupt)
	}
	if got := a.Control.Presets[backchannel.PresetCompact]; got != "/compact" {
		t.Errorf("compact preset = %q, want /compact", got)
	}
	// An adapter that forwards input MUST declare the shared control consent gate.
	var hasControl bool
	for _, p := range a.Permissions {
		if p.Key == agent.ControlPermissionKey {
			hasControl = true
		}
	}
	if !hasControl {
		t.Error("expected agent.ControlPermission() among Permissions")
	}
}

func TestSessionIDFromPath(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{"/Users/x/.vibe/logs/session/session_20260706_101952_48e134e3/messages.jsonl", "session_20260706_101952_48e134e3"},
		{"/Users/x/.vibe/logs/session/session_abc/meta.json", ""}, // sidecar rejected
		{"/Users/x/.vibe/logs/session/session_abc/other.jsonl", ""},
		{"messages.jsonl", ""}, // no parent dir
	}
	for _, c := range cases {
		if got := sessionIDFromPath(c.path); got != c.want {
			t.Errorf("sessionIDFromPath(%q) = %q, want %q", c.path, got, c.want)
		}
	}
}

func TestProcessCmdRegex(t *testing.T) {
	assertCmdRegexMatches(t, true, []string{
		"/Users/x/.local/share/uv/tools/mistral-vibe/bin/python3 /Users/x/.local/bin/vibe --yolo",
		"/Users/x/.local/bin/vibe",
		"/usr/bin/vibe --model devstral",
	})
	assertCmdRegexMatches(t, false, []string{
		"irrlichd --watch /Users/x/.vibe/logs/session", // the watcher must not self-trip
		"vim /Users/x/notes-about-vibe.md",
		"grep vibe README.md",
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
