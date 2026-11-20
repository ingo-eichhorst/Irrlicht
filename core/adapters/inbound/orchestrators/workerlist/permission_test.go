package workerlist

import (
	"os"
	"path/filepath"
	"testing"

	"irrlicht/core/domain/permission"
)

// The detection probe: the variable unset, the file missing, the file
// present — at the default path and at the overriding one. Seen red by
// returning true whenever IRRLICHT_WORKERLIST_FILE is set (the "variable
// names a missing file" row).
func TestFileDetected(t *testing.T) {
	dataDir := t.TempDir()
	other := filepath.Join(t.TempDir(), "pool.json")
	writeFile := func(t *testing.T, p string) {
		t.Helper()
		if err := os.WriteFile(p, []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("unset, default missing", func(t *testing.T) {
		t.Setenv(EnvFile, "")
		if FileDetected(dataDir) {
			t.Error("detected with no file at the default path")
		}
	})
	t.Run("unset, default present", func(t *testing.T) {
		t.Setenv(EnvFile, "")
		writeFile(t, filepath.Join(dataDir, DefaultFileName))
		if !FileDetected(dataDir) {
			t.Error("not detected with a file at the default path")
		}
		if got := ResolvePath(dataDir); got != filepath.Join(dataDir, DefaultFileName) {
			t.Errorf("ResolvePath = %q, want the default path", got)
		}
	})
	t.Run("variable names a missing file", func(t *testing.T) {
		t.Setenv(EnvFile, other)
		// The default file exists from the row above: the variable overrides
		// it rather than adding a second place to look.
		if FileDetected(dataDir) {
			t.Error("detected although the file the variable names is missing")
		}
	})
	t.Run("variable names a present file", func(t *testing.T) {
		t.Setenv(EnvFile, other)
		writeFile(t, other)
		if !FileDetected(dataDir) {
			t.Error("not detected with the file the variable names present")
		}
		if got := ResolvePath(dataDir); got != other {
			t.Errorf("ResolvePath = %q, want %q", got, other)
		}
	})
}

// LOCK: one observe permission, keyed "state", with the daemon's effects.
func TestPermissionDeclarationShape(t *testing.T) {
	calls := 0
	effect := func() error { calls++; return nil }
	decl := PermissionDeclaration(effect, effect)
	if decl.Identity.Name != Name || decl.Identity.DisplayName == "" {
		t.Errorf("identity = %+v", decl.Identity)
	}
	if len(decl.Permissions) != 1 {
		t.Fatalf("%d permissions, want 1", len(decl.Permissions))
	}
	p := decl.Permissions[0]
	if p.Key != PermissionKeyState || p.Kind != permission.KindObserve {
		t.Errorf("permission = %s/%s, want %s/%s", p.Key, p.Kind, PermissionKeyState, permission.KindObserve)
	}
	if p.Apply == nil || p.Remove == nil {
		t.Fatal("Apply/Remove not wired")
	}
	_ = p.Apply()
	_ = p.Remove()
	if calls != 2 {
		t.Errorf("effects ran %d times, want the two passed in", calls)
	}
}
