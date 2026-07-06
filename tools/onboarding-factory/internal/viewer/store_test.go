package viewer

import (
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
