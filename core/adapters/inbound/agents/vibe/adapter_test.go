package vibe

import (
	"testing"

	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/backchannel"
)

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
	src, ok := Agent().Source.(agent.FilesUnderRoot)
	if !ok {
		t.Fatalf("Source is %T, want FilesUnderRoot", Agent().Source)
	}
	if src.Dir != ".vibe/logs/session" {
		t.Errorf("Dir = %q", src.Dir)
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
	matches := []string{
		"/Users/x/.local/share/uv/tools/mistral-vibe/bin/python3 /Users/x/.local/bin/vibe --yolo",
		"/Users/x/.local/bin/vibe",
		"/usr/bin/vibe --model devstral",
	}
	for _, cmd := range matches {
		if !processCmdRegex.MatchString(cmd) {
			t.Errorf("expected match for %q", cmd)
		}
	}
	nonMatches := []string{
		"irrlichd --watch /Users/x/.vibe/logs/session", // the watcher must not self-trip
		"vim /Users/x/notes-about-vibe.md",
		"grep vibe README.md",
	}
	for _, cmd := range nonMatches {
		if processCmdRegex.MatchString(cmd) {
			t.Errorf("expected NO match for %q", cmd)
		}
	}
}
