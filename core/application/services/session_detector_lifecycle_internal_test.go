package services

import (
	"os"
	"path/filepath"
	"testing"
)

// relocatedTranscript's unit-level contract, including the subagent limitation
// assessed in issue #1088.
//
// The subagent case is pinned as a KNOWN, benign limitation rather than a bug:
// onRemoved is never reached for a relocating subagent, because Claude Code
// relocates a parent by renaming the whole <parent-id>/ subtree and renaming a
// directory leaves the child files' vnodes untouched — fswatcher only emits
// events for paths ending in .jsonl, so no Remove is ever delivered for the
// child. See relocatedTranscript's doc comment for the full reasoning and the
// on-disk evidence. If fswatcher ever starts emitting Removes for files inside
// a renamed directory, this expectation is the thing to revisit.
func TestRelocatedTranscript(t *testing.T) {
	root := t.TempDir()
	const (
		mainSlug = "-Users-test"
		wtSlug   = "-Users-test--claude-worktrees-1088"
		parentID = "11111111-2222-3333-4444-555555555555"
	)

	// A top-level session transcript that moved main → worktree slug: the
	// source is absent, only the destination exists.
	if err := os.MkdirAll(filepath.Join(root, wtSlug), 0o755); err != nil {
		t.Fatal(err)
	}
	parentGone := filepath.Join(root, mainSlug, parentID+".jsonl")
	parentLive := filepath.Join(root, wtSlug, parentID+".jsonl")
	if err := os.WriteFile(parentLive, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// A subagent transcript that moved with it, one level deeper.
	subLiveDir := filepath.Join(root, wtSlug, parentID, "subagents")
	if err := os.MkdirAll(subLiveDir, 0o755); err != nil {
		t.Fatal(err)
	}
	subGone := filepath.Join(root, mainSlug, parentID, "subagents", "agent-abc.jsonl")
	if err := os.WriteFile(filepath.Join(subLiveDir, "agent-abc.jsonl"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// A genuinely deleted transcript: no surviving copy anywhere.
	reallyGone := filepath.Join(root, mainSlug, "deleted.jsonl")

	tests := []struct {
		name    string
		removed string
		want    string
	}{
		{name: "top-level relocation is detected", removed: parentGone, want: parentLive},
		{name: "subagent relocation is not detected (known, benign)", removed: subGone, want: ""},
		{name: "genuine deletion", removed: reallyGone, want: ""},
		{name: "empty path", removed: "", want: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := relocatedTranscript(tc.removed); got != tc.want {
				t.Errorf("relocatedTranscript(%q) = %q, want %q", tc.removed, got, tc.want)
			}
		})
	}
}
