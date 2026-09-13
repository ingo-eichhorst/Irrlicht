package muse

import (
	"os"
	"path/filepath"
	"testing"

	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/permission"
)

func TestAgent_Identity(t *testing.T) {
	a := Agent()
	if a.Identity.Name != AdapterName {
		t.Errorf("Name = %q, want %q", a.Identity.Name, AdapterName)
	}
	if a.Identity.DisplayName != "Muse" {
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
		t.Error("expected SessionIDFromPath (filename is the constant session.jsonl)")
	}
	if src.ParentSessionIDFromPath == nil {
		t.Error("expected ParentSessionIDFromPath (subagent/<child-id>/ nesting)")
	}
	if _, ok := src.Parser.(agent.JSONLineParser); !ok {
		t.Errorf("Parser is %T, want JSONLineParser", src.Parser)
	}
}

func TestAgent_Process_CommandPattern(t *testing.T) {
	// The OS process name embeds Muse's version and build (muse-bin-1.2.1-
	// R2847.1), so an ExactName match would break on every release; the
	// adapter matches the full command line instead.
	if _, ok := Agent().Process.Match.(agent.CommandPattern); !ok {
		t.Errorf("Match is %T, want CommandPattern", Agent().Process.Match)
	}
	if Agent().Process.PIDForSession == nil {
		t.Error("expected PIDForSession")
	}
}

// TestAgent_ObserveOnly is a lock: Muse's research found no documented
// native hook system, so the adapter must declare exactly one observe
// permission and no effect closures — nothing it does modifies the user's
// machine, and the consent surface must say so (the junie/aider precedent).
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
	const root = "/Users/x/.local/share/muse/sessions"
	const topID = "01a09c40-6dd8-7732-8c3f-5a7618ffaee4"
	const childID = "01a09c4b-2a8d-70f1-a8f6-7868d4053349"

	cases := []struct {
		name string
		path string
		want string
	}{
		{
			"top-level session",
			root + "/2026/09/13/" + topID + "/session.jsonl",
			topID,
		},
		{
			"nested subagent session",
			root + "/2026/09/13/" + topID + "/subagent/" + childID + "/session.jsonl",
			childID,
		},
		{
			"date component is not a session id",
			root + "/2026/09/13/session.jsonl",
			"",
		},
		{
			"root itself is not a session id",
			root + "/session.jsonl",
			"",
		},
		{
			"non-uuid directory is not a session id",
			root + "/2026/09/13/not-a-uuid/session.jsonl",
			"",
		},
		{
			"approval-review sidecar is a different filename",
			root + "/2026/09/13/" + topID + "/approval-review/aeb1a485-eae6-48fb-9c06-f7a12fc2c611.jsonl",
			"",
		},
		{
			"tool-outputs spool is not a transcript",
			root + "/2026/09/13/" + topID + "/tool-outputs/.spool",
			"",
		},
		{
			"the lock file itself is not a transcript",
			root + "/2026/09/13/" + topID + "/.session.lock",
			"",
		},
		{
			"cron.db sidecar is not a transcript",
			root + "/2026/09/13/" + topID + "/cron.db",
			"",
		},
		{
			"peer-history sidecar is not a transcript",
			root + "/2026/09/13/" + topID + "/session.peer-history.sqlite3",
			"",
		},
		{
			"cli debug log is not a transcript",
			root + "/2026/09/13/" + topID + "/cli-" + topID + ".log",
			"",
		},
		{"no parent dir", "session.jsonl", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := sessionIDFromPath(c.path); got != c.want {
				t.Errorf("sessionIDFromPath(%q) = %q, want %q", c.path, got, c.want)
			}
		})
	}
}

func TestParentSessionIDFromPath(t *testing.T) {
	const root = "/Users/x/.local/share/muse/sessions"
	const parentID = "01a09c47-506a-7763-b82a-9ab89a9e195a"
	const childID = "01a09c4b-2a8d-70f1-a8f6-7868d4053349"

	cases := []struct {
		name string
		path string
		want string
	}{
		{
			"nested subagent session names its parent",
			root + "/2026/09/13/" + parentID + "/subagent/" + childID + "/session.jsonl",
			parentID,
		},
		{
			"top-level session has no parent",
			root + "/2026/09/13/" + parentID + "/session.jsonl",
			"",
		},
		{
			"non-session sibling has no parent",
			root + "/2026/09/13/" + parentID + "/approval-review/x.jsonl",
			"",
		},
		{
			"a subagent directory whose grandparent isn't a uuid names no parent",
			root + "/2026/09/13/subagent/" + childID + "/session.jsonl",
			"",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := parentSessionIDFromPath(c.path); got != c.want {
				t.Errorf("parentSessionIDFromPath(%q) = %q, want %q", c.path, got, c.want)
			}
		})
	}
}

// TestSessionIDFromPath_SuppressesTopLevelShadow reproduces format-spec
// §1's anomaly on disk: a subagent's child UUID also surfacing as a
// top-level dated directory holding a short, independent, approval-only
// shadow stream. sessionIDFromPath must mint a session from the nested,
// authoritative copy and refuse to mint a second one from the top-level
// shadow sharing its id.
func TestSessionIDFromPath_SuppressesTopLevelShadow(t *testing.T) {
	tmp := t.TempDir()
	const parentID = "01a09c47-506a-7763-b82a-9ab89a9e195a"
	const childID = "01a09c71-ab1c-7611-b0d4-6cb963f16317"

	dateDir := filepath.Join(tmp, "2026", "09", "13")
	nestedDir := filepath.Join(dateDir, parentID, "subagent", childID)
	shadowDir := filepath.Join(dateDir, childID)
	if err := os.MkdirAll(nestedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(shadowDir, 0o755); err != nil {
		t.Fatal(err)
	}
	nestedTranscript := filepath.Join(nestedDir, transcriptFilename)
	shadowTranscript := filepath.Join(shadowDir, transcriptFilename)
	if err := os.WriteFile(nestedTranscript, []byte(`{}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(shadowTranscript, []byte(`{}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if got := sessionIDFromPath(nestedTranscript); got != childID {
		t.Errorf("nested authoritative copy: sessionIDFromPath = %q, want %q", got, childID)
	}
	if got := sessionIDFromPath(shadowTranscript); got != "" {
		t.Errorf("top-level shadow: sessionIDFromPath = %q, want \"\" (suppressed)", got)
	}
	if got := parentSessionIDFromPath(nestedTranscript); got != parentID {
		t.Errorf("nested copy's parent: parentSessionIDFromPath = %q, want %q", got, parentID)
	}
}

// TestSessionIDFromPath_OrdinaryTopLevelSessionIsUnaffected is the vacuity
// guard for the shadow suppression above: a top-level session with no
// colliding nested copy anywhere must mint normally, and an unreadable date
// directory (no on-disk sibling scan possible) must fail open rather than
// silently dropping a real session.
func TestSessionIDFromPath_OrdinaryTopLevelSessionIsUnaffected(t *testing.T) {
	tmp := t.TempDir()
	const id = "01a09c85-58a1-7331-b094-597fec8ea335"
	dateDir := filepath.Join(tmp, "2026", "09", "13")
	sessionDir := filepath.Join(dateDir, id)
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	transcript := filepath.Join(sessionDir, transcriptFilename)
	if err := os.WriteFile(transcript, []byte(`{}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := sessionIDFromPath(transcript); got != id {
		t.Errorf("sessionIDFromPath(%q) = %q, want %q", transcript, got, id)
	}

	// A path whose date directory does not exist on disk at all (the common
	// case in every other table-driven test above, which uses fabricated
	// paths with nothing backing them on the test machine) must still mint
	// normally — isShadowedBySubagentCopy's ReadDir failure fails open.
	fabricated := "/Users/x/.local/share/muse/sessions/2026/09/13/" + id + "/session.jsonl"
	if got := sessionIDFromPath(fabricated); got != id {
		t.Errorf("sessionIDFromPath(%q) = %q, want %q (unreadable date dir must fail open)", fabricated, got, id)
	}
}

func TestProcessCmdRegex(t *testing.T) {
	assertCmdRegexMatches(t, true, []string{
		// Real install, exec'd from the bash launcher (format-spec §2).
		"/Users/ingo/.local/bin/muse-bin-1.2.1-R2847.1",
		"/Users/ingo/.local/bin/muse-bin-1.2.1-R2847.1 --provider echo",
		"/Users/ingo/.local/bin/muse-bin-1.2.1-R2847.1 serve",
		"muse-bin-1.2.1-R2847.1",
	})
	assertCmdRegexMatches(t, false, []string{
		// The wrapper script's own name: exec replaces its process image
		// before a session begins, so this is never observed running
		// (format-spec §2) — and the pattern must not match it anyway.
		"/Users/ingo/.local/bin/muse",
		// The daemon's own commands mention the ~/.local/share/muse tree;
		// the matcher must not self-trip on them.
		"irrlichd --watch /Users/x/.local/share/muse/sessions",
		"tail -f /Users/x/.local/share/muse/sessions/2026/09/13/x/session.jsonl",
		"cat /Users/x/.local/share/muse/session-index.db",
		// Intermediate path components or unrelated tokens containing
		// "muse" must not match.
		"cat /Users/x/.local/share/muse/versions/manifest.json",
		"vim notes-about-muse-bin-1.2.1.txt",
		"grep muse README.md",
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
