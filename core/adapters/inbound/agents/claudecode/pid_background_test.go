package claudecode

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// TestReadBackgroundMeta verifies the #744 registry read: a kind:"bg" entry is
// reported as a background agent (carrying Claude's job name), while interactive
// sessions, missing files, and garbage are not.
func TestReadBackgroundMeta(t *testing.T) {
	dir := withTestDeps(t, map[int]bool{}, nil) // overrides sessionsDir to a temp dir
	write := func(pid int, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, strconv.Itoa(pid)+".json"), []byte(body), 0o644); err != nil {
			t.Fatalf("write meta: %v", err)
		}
	}
	write(100, `{"pid":100,"sessionId":"s-bg","kind":"bg","name":"Add guiding colors"}`)
	write(101, `{"pid":101,"sessionId":"s-int","kind":"interactive"}`)
	write(102, `{"pid":102,"sessionId":"s-bg-noname","kind":"bg"}`)
	write(103, `not json`)

	t.Run("bg with name", func(t *testing.T) {
		if name, ok := ReadBackgroundMeta(100); !ok || name != "Add guiding colors" {
			t.Errorf("got (%q, %v), want (\"Add guiding colors\", true)", name, ok)
		}
	})
	t.Run("interactive is not background", func(t *testing.T) {
		if _, ok := ReadBackgroundMeta(101); ok {
			t.Error("interactive session reported as background")
		}
	})
	t.Run("bg without a name is still background", func(t *testing.T) {
		if name, ok := ReadBackgroundMeta(102); !ok || name != "" {
			t.Errorf("got (%q, %v), want (\"\", true)", name, ok)
		}
	})
	t.Run("garbage file is ignored", func(t *testing.T) {
		if _, ok := ReadBackgroundMeta(103); ok {
			t.Error("garbage file reported as background")
		}
	})
	t.Run("missing file", func(t *testing.T) {
		if _, ok := ReadBackgroundMeta(999); ok {
			t.Error("missing file reported as background")
		}
	})
	t.Run("non-positive pid", func(t *testing.T) {
		if _, ok := ReadBackgroundMeta(0); ok {
			t.Error("pid<=0 reported as background")
		}
	})
}
