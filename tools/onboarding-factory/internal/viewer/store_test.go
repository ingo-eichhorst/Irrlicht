package viewer

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNewSafeArchiveName(t *testing.T) {
	for _, bad := range []string{"", "..", "../../etc/passwd", "sub/dir", "sub" + string(filepath.Separator) + "dir"} {
		if _, err := NewSafeArchiveName(bad); err == nil {
			t.Errorf("NewSafeArchiveName(%q) = nil error; want rejection", bad)
		}
	}
	name, err := NewSafeArchiveName("2026-05-01_run")
	if err != nil {
		t.Fatalf("NewSafeArchiveName(valid) = %v; want nil error", err)
	}
	if string(name) != "2026-05-01_run" {
		t.Errorf("name = %q; want %q", name, "2026-05-01_run")
	}
}

func TestArchiveFilePath(t *testing.T) {
	var st RecordingStore
	name, err := NewSafeArchiveName("2026-05-01_run")
	if err != nil {
		t.Fatal(err)
	}
	got := st.archiveFilePath("/scenario", name, "manifest.json")
	want := filepath.Join("/scenario", "recordings", "2026-05-01_run", "manifest.json")
	if got != want {
		t.Errorf("archiveFilePath() = %q; want %q", got, want)
	}
	if dir := st.archiveFilePath("/scenario", name, ""); dir != filepath.Join("/scenario", "recordings", "2026-05-01_run") {
		t.Errorf("archiveFilePath(relPath=\"\") = %q; want the archive dir itself", dir)
	}
}

func TestReadFileExistsListArchiveDirsRejectEscapeOutsideAgentsDir(t *testing.T) {
	repoRoot := t.TempDir()
	agentsDir := filepath.Join(repoRoot, "replaydata", "agents")
	scenarioDir := filepath.Join(agentsDir, "claude-code", "scenarios", "1-1_foo")
	if err := os.MkdirAll(scenarioDir, 0o755); err != nil {
		t.Fatal(err)
	}
	inTree := filepath.Join(scenarioDir, "assessment.json")
	if err := os.WriteFile(inTree, []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// A secret living as a sibling of repoRoot — outside replaydata/agents/,
	// what a "../../../secret" style escape would reach for.
	secret := filepath.Join(repoRoot, "secret.txt")
	if err := os.WriteFile(secret, []byte("do not leak"), 0o644); err != nil {
		t.Fatal(err)
	}

	st := RecordingStore{RepoRoot: repoRoot}

	if !st.exists(scenarioDir) {
		t.Error("exists(legitimate in-tree dir) = false; want true")
	}
	if b, ok := st.readFile(inTree); !ok || string(b) != `{}` {
		t.Errorf("readFile(legitimate in-tree file) = (%q, %v); want (`{}`, true)", b, ok)
	}

	if st.exists(secret) {
		t.Error("exists(path outside agentsDir) = true; want false")
	}
	if _, ok := st.readFile(secret); ok {
		t.Error("readFile(path outside agentsDir) = ok; want rejected")
	}
	escaped := filepath.Join(scenarioDir, "..", "..", "..", "..", "secret.txt")
	if _, ok := st.readFile(escaped); ok {
		t.Errorf("readFile(%q) = ok; want the \"..\" escape rejected", escaped)
	}
	if dirs := st.listArchiveDirs(filepath.Join(scenarioDir, "..", "..", "..", "..")); dirs != nil {
		t.Errorf("listArchiveDirs(escaping scenarioDir) = %v; want nil", dirs)
	}
}
