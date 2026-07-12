package processlifecycle

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/permission"
	"irrlicht/core/internal/contracttesting"
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
	if got != "font_size 12\n\n"+kittyManagedBlock {
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

func TestEnsureKittyConfigPatchedDanglingStartErrors(t *testing.T) {
	path := withTempKittyConfig(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	// User hand-deleted the bottom half of the block: appending a fresh
	// block here would make the dangling start pair with the new block's
	// end on the next strip, swallowing user lines in between.
	mangled := "font_size 12\n" + kittyBlockStart + "\nuser_line yes\n"
	if err := os.WriteFile(path, []byte(mangled), 0o644); err != nil {
		t.Fatal(err)
	}

	modified, err := EnsureKittyConfigPatched()
	if err == nil {
		t.Fatal("expected error for dangling start marker")
	}
	if modified {
		t.Error("expected modified=false on malformed block")
	}
	if got := readFile(t, path); got != mangled {
		t.Errorf("file rewritten despite malformed block: %q", got)
	}
}

func TestUninstallKittyConfigDanglingEndErrors(t *testing.T) {
	path := withTempKittyConfig(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	mangled := kittyBlockEnd + "\nfont_size 12\n"
	if err := os.WriteFile(path, []byte(mangled), 0o644); err != nil {
		t.Fatal(err)
	}

	modified, err := UninstallKittyConfig()
	if err == nil {
		t.Fatal("expected error for dangling end marker")
	}
	if modified {
		t.Error("expected modified=false on malformed block")
	}
	if got := readFile(t, path); got != mangled {
		t.Errorf("file rewritten despite malformed block: %q", got)
	}
}

func TestEnsureKittyConfigPatchedCollapsesDuplicateBlocks(t *testing.T) {
	path := withTempKittyConfig(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	double := "font_size 12\n\n" + kittyManagedBlock + "\n" + kittyManagedBlock
	if err := os.WriteFile(path, []byte(double), 0o644); err != nil {
		t.Fatal(err)
	}

	modified, err := EnsureKittyConfigPatched()
	if err != nil {
		t.Fatalf("EnsureKittyConfigPatched: %v", err)
	}
	if !modified {
		t.Fatal("expected modified=true for duplicate blocks")
	}
	got := readFile(t, path)
	if got != "font_size 12\n\n"+kittyManagedBlock {
		t.Errorf("content = %q, want single canonical block", got)
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

// TestKittyPermission_GateContract drives the real Apply/Remove closures
// behind the declared "remote-control" permission through the shared issue
// #797 contract: like the claudecode instructions permission, this
// permission has no in-code live check of its own — its gate is
// PermissionService's generic effect dispatch — so this proves that
// dispatch reaches the real closures KittyPermissionDeclaration exports.
func TestKittyPermission_GateContract(t *testing.T) {
	path := withTempKittyConfig(t)
	decl := findKittyPermission(t, PermissionKeyKittyConfig)

	contracttesting.AssertPermissionGated(t, contracttesting.PermissionGate{
		SetState: func(state permission.State) {
			switch state {
			case permission.StateGranted:
				if err := decl.Apply(); err != nil {
					t.Fatalf("Apply: %v", err)
				}
			case permission.StateDenied:
				if err := decl.Remove(); err != nil {
					t.Fatalf("Remove: %v", err)
				}
			}
		},
		Exercise: func() {}, // the effect IS the Apply/Remove call above
		Observe: func() bool {
			data, err := os.ReadFile(path)
			if err != nil {
				return false
			}
			return strings.Contains(string(data), kittyBlockStart)
		},
	})
}

// findKittyPermission returns the declared permission matching key, failing
// the test if the declaration dropped or renamed it.
func findKittyPermission(t *testing.T, key string) agent.Permission {
	t.Helper()
	for _, p := range KittyPermissionDeclaration().Permissions {
		if p.Key == key {
			return p
		}
	}
	t.Fatalf("no permission %q declared", key)
	return agent.Permission{}
}
