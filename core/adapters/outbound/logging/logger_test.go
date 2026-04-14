package logging

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLogger_FilePermissions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "logs")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	logPath := filepath.Join(dir, "events.log")
	logger, err := NewWithPath(logPath)
	if err != nil {
		t.Fatalf("NewWithPath: %v", err)
	}
	defer logger.Close()
	logger.LogInfo("test", "", "hello")

	info, err := os.Stat(logPath)
	if err != nil {
		t.Fatalf("stat log: %v", err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Errorf("log perm: got %o, want 0600", got)
	}
}
