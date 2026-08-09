package hookjson

import (
	"os"
	"path/filepath"
	"testing"

	"irrlicht/core/domain/agent"
)

// staticRoots is a PathConfiner over one fixed absolute root.
func staticRoots(root string) *PathConfiner {
	return NewPathConfiner(func() []string { return []string{root} }, ".jsonl")
}

func writeFile(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

// TestConfine_RejectionReasons pins the reason taxonomy. The reasons are what
// gets logged and counted, so a path refused for the wrong stated reason is a
// misleading operator signal even though the security outcome is the same.
func TestConfine_RejectionReasons(t *testing.T) {
	root := t.TempDir()
	inTree := writeFile(t, filepath.Join(root, "sub", "a.jsonl"))
	outside := writeFile(t, filepath.Join(t.TempDir(), "b.jsonl"))

	tests := map[string]struct {
		confiner *PathConfiner
		path     string
		want     RejectReason
	}{
		"in tree accepted":          {staticRoots(root), inTree, RejectNone},
		"empty path":                {staticRoots(root), "", RejectEmptyPath},
		"relative path":             {staticRoots(root), "sub/a.jsonl", RejectRelativePath},
		"wrong suffix":              {staticRoots(root), filepath.Join(root, "sub", "a.txt"), RejectWrongSuffix},
		"no declared roots":         {NewPathConfiner(func() []string { return nil }, ".jsonl"), inTree, RejectNoRoots},
		"outside every root":        {staticRoots(root), outside, RejectEscapesRoot},
		"missing parent directory":  {staticRoots(root), filepath.Join(root, "nope", "c.jsonl"), RejectUnresolvable},
		"missing leaf inside tree":  {staticRoots(root), filepath.Join(root, "sub", "later.jsonl"), RejectNone},
		"root itself is not a file": {staticRoots(root), root, RejectWrongSuffix},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			got, reason := tt.confiner.Confine(tt.path)
			if reason != tt.want {
				t.Fatalf("Confine(%q) reason = %q, want %q", tt.path, reason, tt.want)
			}
			if reason == RejectNone && got == "" {
				t.Error("accepted path came back empty")
			}
			if reason != RejectNone && got != "" {
				t.Errorf("rejected path returned %q, want empty", got)
			}
		})
	}
}

// TestConfine_MissingLeafIsAcceptedNotAnEscape is the race the "resolve one
// level up" rule exists for: the hook fires around the transcript write, so a
// file that is not on disk yet must be accepted when its directory is in the
// tree — refusing it would 400 a legitimate hook.
func TestConfine_MissingLeafIsAcceptedNotAnEscape(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	unflushed := filepath.Join(root, "sub", "not-yet.jsonl")

	got, reason := staticRoots(root).Confine(unflushed)
	if reason != RejectNone {
		t.Fatalf("Confine(%q) = %q, want it accepted", unflushed, reason)
	}
	if got != unflushed {
		t.Errorf("confined path = %q, want %q", got, unflushed)
	}
}

// TestConfine_MissingLeafCannotSmuggleParentRefs guards the one case the
// missing-leaf allowance could have opened: ".." cannot be resolved without the
// filesystem, so a non-existent path carrying one is refused rather than
// collapsed lexically.
func TestConfine_MissingLeafCannotSmuggleParentRefs(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	smuggled := filepath.Join(root, "sub") + "/../../escaped.jsonl"

	if got, reason := staticRoots(root).Confine(smuggled); reason == RejectNone {
		t.Errorf("Confine(%q) accepted it as %q — a missing path with %q must not be resolved lexically", smuggled, got, "..")
	}
}

// TestConfine_SymlinkEscapeResolvedBeforeContainment is the ordering property
// at the unit level: lexically inside, really outside.
func TestConfine_SymlinkEscapeResolvedBeforeContainment(t *testing.T) {
	root := t.TempDir()
	outside := writeFile(t, filepath.Join(t.TempDir(), "secret.jsonl"))
	link := filepath.Join(root, "sub", "innocent.jsonl")
	if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Symlink(outside, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	if got, reason := staticRoots(root).Confine(link); reason != RejectEscapesRoot {
		t.Errorf("Confine(%q) = (%q, %q), want %q", link, got, reason, RejectEscapesRoot)
	}
}

// TestConfine_CountsEveryRejectionByReason checks the counters the receivers
// report. Nothing publishes them yet, which is exactly why they need a test:
// an uncounted rejection is indistinguishable from no rejection.
func TestConfine_CountsEveryRejectionByReason(t *testing.T) {
	c := staticRoots(t.TempDir())
	c.Confine("")
	c.Confine("")
	c.Confine("relative.jsonl")

	if n := c.RejectionCount(); n != 3 {
		t.Errorf("RejectionCount = %d, want 3", n)
	}
	counts := c.Rejections()
	if counts[RejectEmptyPath] != 2 {
		t.Errorf("empty_path count = %d, want 2", counts[RejectEmptyPath])
	}
	if counts[RejectRelativePath] != 1 {
		t.Errorf("relative_path count = %d, want 1", counts[RejectRelativePath])
	}
	// The snapshot must be a copy, not the live map.
	counts[RejectEmptyPath] = 99
	if c.Rejections()[RejectEmptyPath] != 2 {
		t.Error("Rejections() handed out the live map")
	}
}

// TestConfinerForSource_NonFileSourceConfinesNothing pins the fail-closed
// choice: an adapter whose Source declares no directory tree gets a confiner
// that refuses everything, not one that waves paths through.
func TestConfinerForSource_NonFileSourceConfinesNothing(t *testing.T) {
	c := ConfinerForSource(func() agent.Source { return agent.FilesUnderCWD{Filename: "x.md"} }, "darwin", ".jsonl")

	if _, reason := c.Confine(writeFile(t, filepath.Join(t.TempDir(), "a.jsonl"))); reason != RejectNoRoots {
		t.Errorf("reason = %q, want %q", reason, RejectNoRoots)
	}
}

// TestConfinerForSource_UsesDeclaredRoots checks the roots really come from the
// adapter's own declaration, including ExtraDirs — the point of deriving them
// from agent.Source rather than a second hardcoded list.
func TestConfinerForSource_UsesDeclaredRoots(t *testing.T) {
	primary := t.TempDir()
	extra := t.TempDir()
	src := agent.FilesUnderRoot{Dir: primary, ExtraDirs: []string{extra}}
	c := ConfinerForSource(func() agent.Source { return src }, "darwin", ".jsonl")

	for _, root := range []string{primary, extra} {
		path := writeFile(t, filepath.Join(root, "sub", "a.jsonl"))
		if _, reason := c.Confine(path); reason != RejectNone {
			t.Errorf("declared root %s: Confine(%q) = %q, want accepted", root, path, reason)
		}
	}
	outside := writeFile(t, filepath.Join(t.TempDir(), "a.jsonl"))
	if _, reason := c.Confine(outside); reason != RejectEscapesRoot {
		t.Errorf("undeclared root: reason = %q, want %q", reason, RejectEscapesRoot)
	}
}
