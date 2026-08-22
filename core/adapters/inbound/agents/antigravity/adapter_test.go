package antigravity

import (
	"testing"

	"irrlicht/core/domain/agent"
)

// TestAgentRegistration pins the four-axis declaration: native ExactName CLI
// matcher, optional PID binding, and a multi-root transcript Source with a
// custom session-ID extractor.
func TestAgentRegistration(t *testing.T) {
	a := Agent()
	if a.Identity.Name != AdapterName {
		t.Errorf("adapter name: want %q, got %q", AdapterName, a.Identity.Name)
	}
	if e, ok := a.Process.Match.(agent.ExactName); !ok || e.Name != ProcessName {
		t.Errorf("Process.Match: want ExactName{%q}, got %#v", ProcessName, a.Process.Match)
	}
	if a.Process.PIDForSession == nil {
		t.Error("PIDForSession must be set (CLI liveness enrichment)")
	}
	src, ok := a.Source.(agent.FilesUnderRoot)
	if !ok {
		t.Fatalf("Source: want FilesUnderRoot, got %T", a.Source)
	}
	if src.Dir != cliBrainDir {
		t.Errorf("Source.Dir = %q, want CLI brain %q", src.Dir, cliBrainDir)
	}
	if len(src.ExtraDirs) != 1 || src.ExtraDirs[0] != ideBrainDir {
		t.Errorf("Source.ExtraDirs = %v, want [%q] (the IDE brain store)", src.ExtraDirs, ideBrainDir)
	}
	if src.SessionIDFromPath == nil {
		t.Fatal("Source.SessionIDFromPath must be set (constant transcript filename)")
	}
	// Two permissions since #1723: the observe-kind transcripts read, and the
	// modify-kind hooks install.
	assertDeclaresExactly(t, a, PermissionKeyTranscripts, PermissionKeyHooks)
}

// assertDeclaresExactly pins the permission set by KEY, not by count alone: a
// count is satisfied by any two permissions, and the PAIR is what three
// separate projections narrow on (agents.HookConfigs, agents.ManagedUserFiles,
// and the #1740 contract-wiring tripwire).
func assertDeclaresExactly(t *testing.T, a agent.Agent, keys ...string) {
	t.Helper()
	want := make(map[string]bool, len(keys))
	for _, key := range keys {
		want[key] = false
	}
	for _, p := range a.Permissions {
		if _, known := want[p.Key]; !known {
			t.Errorf("unexpected permission %q", p.Key)
			continue
		}
		want[p.Key] = true
	}
	for key, seen := range want {
		if !seen {
			t.Errorf("permission %q is not declared", key)
		}
	}
	if len(a.Permissions) != len(want) {
		t.Errorf("declared %d permissions, want %d", len(a.Permissions), len(want))
	}
}

// TestSessionIDFromPath proves the ID is the <conv-id> directory, that only the
// filtered transcript.jsonl is accepted, and that malformed layouts are skipped.
func TestSessionIDFromPath(t *testing.T) {
	const conv = "9ea5200c-3d8a-4075-8f85-2609cef25e78"
	base := "/Users/x/.gemini/antigravity-cli/brain/" + conv + "/.system_generated/logs/"

	cases := []struct {
		name string
		path string
		want string
	}{
		{"cli filtered transcript", base + "transcript.jsonl", conv},
		{"ide filtered transcript",
			"/Users/x/.gemini/antigravity/brain/" + conv + "/.system_generated/logs/transcript.jsonl", conv},
		{"unfiltered sibling is skipped", base + "transcript_full.jsonl", ""},
		{"wrong leaf dir is skipped", "/Users/x/.gemini/antigravity-cli/brain/" + conv + "/x/transcript.jsonl", ""},
		{"flat uuid filename is skipped", "/Users/x/.gemini/antigravity-cli/brain/" + conv + ".jsonl", ""},
		{"other file is skipped", base + "notes.jsonl", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sessionIDFromPath(tc.path); got != tc.want {
				t.Errorf("sessionIDFromPath(%q) = %q, want %q", tc.path, got, tc.want)
			}
		})
	}
}
