package services

import (
	"os"
	"path/filepath"
	"testing"
)

// Workflow-tool fan-out (issue #565): agents write transcripts one level
// deeper than plain Task subagents — .../<parent>/subagents/workflows/<run>/
// — alongside a journal.jsonl bookkeeping file that is not a session.

func TestDeriveParentSessionID(t *testing.T) {
	cases := []struct {
		name string
		path string
		want string
	}{
		{"plain subagent", "/p/-Users-x/parent-123/subagents/agent-abc.jsonl", "parent-123"},
		{"workflow agent", "/p/-Users-x/parent-123/subagents/workflows/wf_854deede-0ff/agent-abc.jsonl", "parent-123"},
		{"top-level session", "/p/-Users-x/parent-123.jsonl", ""},
		{"workflows dir without subagents above", "/p/-Users-x/parent-123/workflows/wf_1/agent-abc.jsonl", ""},
		{"nested below the run dir", "/p/parent/subagents/workflows/wf_1/nested/agent-a.jsonl", ""},
		{"empty path", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := deriveParentSessionID(tc.path); got != tc.want {
				t.Errorf("deriveParentSessionID(%q) = %q, want %q", tc.path, got, tc.want)
			}
		})
	}
}

// Gemini CLI subagents (issue #663). Real on-disk layout, established from a
// live ~/.gemini/tmp/<project>/chats/ recording of the #663 fixture session:
//
//	chats/session-2026-06-12T12-38-ee437ac7.jsonl   (parent, header UUID ee437ac7-…)
//	chats/ee437ac7-9888-4d95-8a1a-9c93c07abf67/      (dir = parent's FULL UUID)
//	  └─ 1af72c9f-38bd-450a-87f9-750790f9cd82.jsonl  (child, kind:subagent)
//
// The gemini parser skips the session header, so each session registers under
// its FILENAME stem, not its header UUID. The parent's registered SessionID is
// thus "session-2026-06-12T12-38-ee437ac7" — the nested dir's UUID prefix
// (ee437ac7) tags the parent file but the timestamp is not derivable, so the
// rule must resolve the sibling parent transcript on disk and return its stem.
func TestDeriveParentSessionGeminiNested(t *testing.T) {
	const (
		parentStem = "session-2026-06-12T12-38-ee437ac7"
		parentUUID = "ee437ac7-9888-4d95-8a1a-9c93c07abf67"
		childUUID  = "1af72c9f-38bd-450a-87f9-750790f9cd82"
	)
	chats := filepath.Join(t.TempDir(), ".gemini", "tmp", "proj", "chats")
	nested := filepath.Join(chats, parentUUID)
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	parentPath := filepath.Join(chats, parentStem+".jsonl")
	if err := os.WriteFile(parentPath, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	childPath := filepath.Join(nested, childUUID+".jsonl")
	if err := os.WriteFile(childPath, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		path string
		want string
	}{
		// The nested child resolves to the parent's REGISTERED SessionID
		// (the filename stem), which is what the daemon keys sessions on.
		{"nested subagent resolves to registered parent stem", childPath, parentStem},
		// The parent itself is top-level under chats/ — no parent link.
		{"top-level parent session", parentPath, ""},
		// A nested child whose sibling parent transcript is absent must NOT
		// fabricate a link to the bare UUID — genuine gemini false-positive
		// guard that actually reaches the gemini rule.
		{"nested child without sibling parent", filepath.Join(chats, "deadbeef-0000-4000-8000-000000000000", childUUID+".jsonl"), ""},
		// A non-UUID dir under chats/ (not a gemini parent) yields no tag.
		{"non-uuid nested dir", filepath.Join(chats, "not-a-uuid", childUUID+".jsonl"), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := deriveParentSession(tc.path); got != tc.want {
				t.Errorf("deriveParentSession(%q) = %q, want %q", tc.path, got, tc.want)
			}
		})
	}
}

// Tag collision (issue #663 review): two top-level sibling parent transcripts
// share the same 8-hex UUID tag (session-<ts1>-<tag>.jsonl and
// session-<ts2>-<tag>.jsonl). The nested child's dir UUID resolves to that
// ambiguous tag, so the rule cannot tell which sibling is the real parent and
// must return "" rather than guess — no mislink.
func TestDeriveParentSessionGeminiNestedTagCollision(t *testing.T) {
	const (
		tag        = "ee437ac7"
		parentUUID = "ee437ac7-9888-4d95-8a1a-9c93c07abf67"
		childUUID  = "1af72c9f-38bd-450a-87f9-750790f9cd82"
	)
	chats := filepath.Join(t.TempDir(), ".gemini", "tmp", "proj", "chats")
	nested := filepath.Join(chats, parentUUID)
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	// Two sibling parent transcripts whose stems collide on the 8-hex tag.
	for _, stem := range []string{
		"session-2026-06-12T12-38-" + tag,
		"session-2026-06-12T13-00-" + tag,
	} {
		if err := os.WriteFile(filepath.Join(chats, stem+".jsonl"), []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	childPath := filepath.Join(nested, childUUID+".jsonl")
	if err := os.WriteFile(childPath, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if got := deriveParentSession(childPath); got != "" {
		t.Errorf("deriveParentSession(%q) = %q, want %q (ambiguous tag must not mislink)", childPath, got, "")
	}
}

// geminiUUIDTag guards against treating a non-UUID directory under chats/ as a
// gemini parent (e.g. a Claude "subagents" layout sharing the path shape).
func TestGeminiUUIDTag(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"canonical uuid", "ee437ac7-9888-4d95-8a1a-9c93c07abf67", "ee437ac7"},
		{"non-hex tag", "zzzzzzzz-9888-4d95-8a1a-9c93c07abf67", ""},
		{"wrong length", "ee437ac7", ""},
		{"missing hyphens", "ee437ac799884d958a1a9c93c07abf67aaaa", ""},
		{"claude subagents dirname", "subagents", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := geminiUUIDTag(tc.in); got != tc.want {
				t.Errorf("geminiUUIDTag(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestIsWorkflowBookkeepingFile(t *testing.T) {
	cases := []struct {
		name string
		path string
		want bool
	}{
		{"journal in run dir", "/p/parent/subagents/workflows/wf_1/journal.jsonl", true},
		{"agent transcript in run dir", "/p/parent/subagents/workflows/wf_1/agent-a.jsonl", false},
		{"journal-named session at top level", "/p/-Users-x/journal.jsonl", false},
		{"journal in plain subagents dir", "/p/parent/subagents/journal.jsonl", false},
		{"empty path", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isWorkflowBookkeepingFile(tc.path); got != tc.want {
				t.Errorf("isWorkflowBookkeepingFile(%q) = %v, want %v", tc.path, got, tc.want)
			}
		})
	}
}
