package claudecode

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// assertBackgroundMeta asserts pid reads back as a background agent with the
// given name.
func assertBackgroundMeta(t *testing.T, pid int, wantName string) {
	t.Helper()
	if name, ok := ReadBackgroundMeta(pid); !ok || name != wantName {
		t.Errorf("got (%q, %v), want (%q, true)", name, ok, wantName)
	}
}

// assertNotBackgroundMeta asserts pid does not read back as a background
// agent, using msg as the failure message.
func assertNotBackgroundMeta(t *testing.T, pid int, msg string) {
	t.Helper()
	if _, ok := ReadBackgroundMeta(pid); ok {
		t.Error(msg)
	}
}

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
		assertBackgroundMeta(t, 100, "Add guiding colors")
	})
	t.Run("interactive is not background", func(t *testing.T) {
		assertNotBackgroundMeta(t, 101, "interactive session reported as background")
	})
	t.Run("bg without a name is still background", func(t *testing.T) {
		assertBackgroundMeta(t, 102, "")
	})
	t.Run("garbage file is ignored", func(t *testing.T) {
		assertNotBackgroundMeta(t, 103, "garbage file reported as background")
	})
	t.Run("missing file", func(t *testing.T) {
		assertNotBackgroundMeta(t, 999, "missing file reported as background")
	})
	t.Run("non-positive pid", func(t *testing.T) {
		assertNotBackgroundMeta(t, 0, "pid<=0 reported as background")
	})
}
