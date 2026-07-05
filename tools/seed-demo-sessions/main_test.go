package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureSessionCWD(t *testing.T) {
	t.Run("creates missing absolute dir", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "projects", "demo-app")
		if err := ensureSessionCWD(dir); err != nil {
			t.Fatalf("ensureSessionCWD: %v", err)
		}
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			t.Fatalf("expected %s to exist as a dir, err=%v", dir, err)
		}
	})

	t.Run("leaves existing dir alone", func(t *testing.T) {
		dir := t.TempDir()
		if err := ensureSessionCWD(dir); err != nil {
			t.Fatalf("ensureSessionCWD: %v", err)
		}
	})

	t.Run("skips empty path", func(t *testing.T) {
		if err := ensureSessionCWD(""); err != nil {
			t.Fatalf("ensureSessionCWD(\"\"): %v", err)
		}
	})

	t.Run("skips relative path", func(t *testing.T) {
		defer os.RemoveAll("relative")
		if err := ensureSessionCWD("relative/path"); err != nil {
			t.Fatalf("ensureSessionCWD(relative): %v", err)
		}
		if _, err := os.Stat("relative/path"); !os.IsNotExist(err) {
			t.Fatalf("relative path should not have been created, err=%v", err)
		}
	})
}
