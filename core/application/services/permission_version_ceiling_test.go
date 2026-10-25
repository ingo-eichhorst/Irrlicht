package services

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	// Test-only adapter import, the same exception permission_version_gate_test.go
	// takes for codex: this is where opencode's REAL declaration and the REAL
	// enforcement meet. core/architecture_test.go walks non-test imports only.
	"irrlicht/core/adapters/inbound/agents/opencode"
	"irrlicht/core/domain/agent"
)

// opencodeHooksAt returns opencode's real hooks permission with the version
// source replaced by a fixed observed version and NO probe.
//
// The probe has to go, not merely be shadowed: shouldConfirmByProbe re-runs the
// probe whenever Permits refuses, so a refusal driven through the real gate
// would exec `opencode --version` on the host. On this machine cliprobe
// resolves /opt/homebrew/bin first (#2105 correction 1), and a unit test must
// not depend on — or run — whatever opencode happens to be installed.
func opencodeHooksAt(t *testing.T, observed string) agent.Permission {
	t.Helper()
	for _, p := range opencode.Agent().Permissions {
		if p.Key != opencode.PermissionKeyHooks {
			continue
		}
		if p.Writes == nil || p.Writes.Version == nil {
			t.Fatal("opencode's hooks permission declares no version gate")
		}
		gate := *p.Writes.Version
		gate.Observed = func() string { return observed }
		gate.Probe = nil
		writes := *p.Writes
		writes.Version = &gate
		p.Writes = &writes
		return p
	}
	t.Fatal("opencode declares no hooks permission")
	return agent.Permission{}
}

// isolateOpenCodeConfig points opencode's config dir at a fresh temp tree and
// returns the plugin path the installer resolves there.
func isolateOpenCodeConfig(t *testing.T) string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path, err := opencode.PluginPath()
	if err != nil {
		t.Fatalf("PluginPath: %v", err)
	}
	return path
}

// TestOpenCodeCeiling_RemovesTheStaleV1Plugin is #2105's red-first end-to-end
// defect test: the real opencode declaration through the real service. A 1.x
// install's plugin file is on disk (written by the real installer), the CLI is
// now 2.1.4, and the daemon re-runs the granted effect — which is what
// PermissionService.Start does on every daemon start, and what the #1372 repair
// does through RepairGrantedHookInstall. On main the gate was Min-only, so the
// effect re-installed the 1.x file instead of removing it.
func TestOpenCodeCeiling_RemovesTheStaleV1Plugin(t *testing.T) {
	path := isolateOpenCodeConfig(t)
	if _, err := opencode.EnsureHooksInstalled(); err != nil {
		t.Fatalf("seed the 1.x plugin: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("seeded plugin is not on disk: %v", err)
	}

	svc, log := newGateService()
	grant(svc, opencodeHooksAt(t, "2.1.4"))

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("the 1.x plugin at %s survived a grant against opencode 2.1.4 (stat err %v); "+
			"opencode 2.x logs \"failed to load plugin\" for it on every location load", path, err)
	}
	if !log.errorMentioning("2.1.4", "2.0.0") {
		t.Errorf("no logged refusal naming 2.1.4 and the 2.0.0 ceiling; errors were %v", log.errors)
	}
}

// TestOpenCodeCeiling_LeavesAForeignFileAlone is a LOCK: hookartifact's
// sentinel check already refused to remove a file irrlicht did not write, and
// routing the ceiling's removal through the adapter's own Uninstall is what
// keeps that true. A user's own plugin/irrlicht.js survives a ceiling refusal
// byte for byte.
func TestOpenCodeCeiling_LeavesAForeignFileAlone(t *testing.T) {
	path := isolateOpenCodeConfig(t)
	foreign := []byte("// the user's own plugin, not irrlicht's\nexport default {}\n")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, foreign, 0o644); err != nil {
		t.Fatalf("seed foreign file: %v", err)
	}

	svc, _ := newGateService()
	grant(svc, opencodeHooksAt(t, "2.1.4"))

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the foreign file is gone after a ceiling refusal: %v", err)
	}
	if !bytes.Equal(got, foreign) {
		t.Errorf("the foreign file was changed by a ceiling refusal:\n got %q\nwant %q", got, foreign)
	}
}

// TestOpenCodeCeiling_OneXStillInstallsByteForByte is a LOCK: below the
// ceiling nothing changes. A grant against the newest 1.x writes exactly the
// bytes the installer writes on its own.
func TestOpenCodeCeiling_OneXStillInstallsByteForByte(t *testing.T) {
	isolateOpenCodeConfig(t)
	if _, err := opencode.EnsureHooksInstalled(); err != nil {
		t.Fatalf("reference install: %v", err)
	}
	refPath, _ := opencode.PluginPath()
	want, err := os.ReadFile(refPath)
	if err != nil {
		t.Fatalf("read reference install: %v", err)
	}

	path := isolateOpenCodeConfig(t)
	svc, log := newGateService()
	grant(svc, opencodeHooksAt(t, "1.19.4"))

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("a grant against opencode 1.19.4 installed nothing: %v (errors %v)", err, log.errors)
	}
	if !bytes.Equal(got, want) {
		t.Error("the plugin a 1.19.4 grant installed differs from the installer's own bytes")
	}
}
