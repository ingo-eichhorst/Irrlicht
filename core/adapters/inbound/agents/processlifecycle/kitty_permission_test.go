package processlifecycle

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withTempKittyConfig points kitty config resolution at a temp dir via
// XDG_CONFIG_HOME and returns the kitty.conf path inside it.
func withTempKittyConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	return filepath.Join(dir, "kitty", "kitty.conf")
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func TestEnsureKittyConfigPatchedCreatesFile(t *testing.T) {
	path := withTempKittyConfig(t)

	modified, err := EnsureKittyConfigPatched()
	if err != nil {
		t.Fatalf("EnsureKittyConfigPatched: %v", err)
	}
	if !modified {
		t.Fatal("expected modified=true on first install")
	}
	if got := readFile(t, path); got != kittyManagedBlock {
		t.Errorf("content = %q, want managed block only", got)
	}
}

func TestEnsureKittyConfigPatchedIsIdempotent(t *testing.T) {
	path := withTempKittyConfig(t)

	if _, err := EnsureKittyConfigPatched(); err != nil {
		t.Fatalf("first install: %v", err)
	}
	before := readFile(t, path)

	modified, err := EnsureKittyConfigPatched()
	if err != nil {
		t.Fatalf("second install: %v", err)
	}
	if modified {
		t.Error("expected modified=false on re-install")
	}
	if got := readFile(t, path); got != before {
		t.Errorf("content changed on re-install: %q -> %q", before, got)
	}
}

func TestEnsureKittyConfigPatchedPreservesUserContent(t *testing.T) {
	path := withTempKittyConfig(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	user := "font_size 12\nallow_remote_control no\n"
	if err := os.WriteFile(path, []byte(user), 0o644); err != nil {
		t.Fatal(err)
	}

	modified, err := EnsureKittyConfigPatched()
	if err != nil {
		t.Fatalf("EnsureKittyConfigPatched: %v", err)
	}
	if !modified {
		t.Fatal("expected modified=true")
	}
	got := readFile(t, path)
	want := strings.TrimRight(user, "\n") + "\n\n" + kittyManagedBlock
	if got != want {
		t.Errorf("content = %q, want user lines + managed block", got)
	}
}

func TestEnsureKittyConfigPatchedUpgradesStaleBlock(t *testing.T) {
	path := withTempKittyConfig(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	stale := "font_size 12\n\n" + kittyBlockStart + "\n" +
		"allow_remote_control socket-only\n" +
		kittyBlockEnd + "\n"
	if err := os.WriteFile(path, []byte(stale), 0o644); err != nil {
		t.Fatal(err)
	}

	modified, err := EnsureKittyConfigPatched()
	if err != nil {
		t.Fatalf("EnsureKittyConfigPatched: %v", err)
	}
	if !modified {
		t.Fatal("expected modified=true for stale block")
	}
	got := readFile(t, path)
	if want := "font_size 12\n\n" + kittyManagedBlock; got != want {
		t.Errorf("content = %q, want canonical block after user lines", got)
	}
	if strings.Count(got, kittyBlockStart) != 1 {
		t.Errorf("expected exactly one managed block, got %q", got)
	}
}

func TestUninstallKittyConfigRestoresOriginal(t *testing.T) {
	path := withTempKittyConfig(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	user := "font_size 12\nscrollback_lines 4000\n"
	if err := os.WriteFile(path, []byte(user), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := EnsureKittyConfigPatched(); err != nil {
		t.Fatalf("install: %v", err)
	}
	modified, err := UninstallKittyConfig()
	if err != nil {
		t.Fatalf("UninstallKittyConfig: %v", err)
	}
	if !modified {
		t.Fatal("expected modified=true on uninstall")
	}
	if got := readFile(t, path); got != user {
		t.Errorf("content = %q, want original %q", got, user)
	}
}

func TestUninstallKittyConfigBlockOnlyFileEmpties(t *testing.T) {
	path := withTempKittyConfig(t)

	if _, err := EnsureKittyConfigPatched(); err != nil {
		t.Fatalf("install: %v", err)
	}
	if _, err := UninstallKittyConfig(); err != nil {
		t.Fatalf("UninstallKittyConfig: %v", err)
	}
	if got := readFile(t, path); got != "" {
		t.Errorf("content = %q, want empty file", got)
	}
}

func TestUninstallKittyConfigNoBlockIsNoop(t *testing.T) {
	path := withTempKittyConfig(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	user := "font_size 12\n"
	if err := os.WriteFile(path, []byte(user), 0o644); err != nil {
		t.Fatal(err)
	}

	modified, err := UninstallKittyConfig()
	if err != nil {
		t.Fatalf("UninstallKittyConfig: %v", err)
	}
	if modified {
		t.Error("expected modified=false without a managed block")
	}
	if got := readFile(t, path); got != user {
		t.Errorf("content = %q, want untouched %q", got, user)
	}
}

func TestUninstallKittyConfigMissingFileIsNoop(t *testing.T) {
	withTempKittyConfig(t)

	modified, err := UninstallKittyConfig()
	if err != nil {
		t.Fatalf("UninstallKittyConfig: %v", err)
	}
	if modified {
		t.Error("expected modified=false for missing file")
	}
}

func TestKittyDetectedByConfigDir(t *testing.T) {
	path := withTempKittyConfig(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if !KittyDetected() {
		t.Error("expected KittyDetected=true with existing config dir")
	}
}

func TestKittyPermissionDeclarationShape(t *testing.T) {
	decl := KittyPermissionDeclaration()
	if decl.Identity.Name != KittyName {
		t.Errorf("Name = %q, want %q", decl.Identity.Name, KittyName)
	}
	if len(decl.Permissions) != 1 {
		t.Fatalf("expected 1 permission, got %d", len(decl.Permissions))
	}
	p := decl.Permissions[0]
	if p.Key != PermissionKeyKittyConfig {
		t.Errorf("Key = %q, want %q", p.Key, PermissionKeyKittyConfig)
	}
	if p.Apply == nil || p.Remove == nil {
		t.Error("modify-kind permission must carry Apply and Remove closures")
	}
}
