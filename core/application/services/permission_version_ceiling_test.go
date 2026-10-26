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
// probe whenever the gate refuses, so a refusal driven through the real gate
// would exec `opencode --version` on the host. On this machine cliprobe
// resolves /opt/homebrew/bin first (#2105 correction 1), and a unit test must
// not depend on — or run — whatever opencode happens to be installed.
//
// ObservedRequires is cleared with it: the replacement reads nothing, so there
// is no database read to gate, and grant() runs as "test-agent", which holds no
// database consent. The consent rule itself is
// TestVariantGate_ObservedIsNotConsultedWithoutItsPermission.
func opencodeHooksAt(t *testing.T, observed string) agent.Permission {
	t.Helper()
	for _, p := range opencode.Agent().Permissions {
		if p.Key != opencode.PermissionKeyHooks {
			continue
		}
		if p.Writes == nil || p.Writes.Version == nil {
			t.Fatal("opencode's hooks permission declares no version gate")
		}
		// Agent() builds a fresh ManagedUserFile and VersionGate per call, so
		// changing them in place touches nothing the daemon holds.
		p.Writes.Version.Observed = func() string { return observed }
		p.Writes.Version.ObservedRequires = ""
		p.Writes.Version.Probe = nil
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

// TestOpenCodeUpgrade_RewritesTheOneXPluginInPlace is #2108's red-first
// end-to-end defect test, and it replaces #2105's removal at 2.1.4: the real
// opencode declaration through the real service. A 1.x install's plugin file is
// on disk (written by the real installer), the CLI is now 2.1.4, and the daemon
// re-runs the granted effect — what PermissionService.Start does on every daemon
// start, and what the #1372 repair does through RepairGrantedHookInstall. Under
// #2105 the gate refused 2.1.4 and removed the file, so a user who upgraded had
// no waiting detection; now the 2.x artifact replaces it at the same path.
func TestOpenCodeUpgrade_RewritesTheOneXPluginInPlace(t *testing.T) {
	want := v2Rendering(t)
	path := isolateOpenCodeConfig(t)
	if _, err := opencode.EnsureHooksInstalled(); err != nil {
		t.Fatalf("seed the 1.x plugin: %v", err)
	}

	svc, log := newGateService()
	grant(svc, opencodeHooksAt(t, "2.1.4"))

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("no plugin at %s after a grant against opencode 2.1.4: %v (errors %v)", path, err, log.errors)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("the plugin at %s after a grant against opencode 2.1.4 is not the 2.x artifact "+
			"(%d bytes, want %d) — opencode 2.x rejects the 1.x one on every load", path, len(got), len(want))
	}
}

// TestOpenCodeCeiling_RemovesTheStaleV1Plugin keeps #2105's removal where it
// still applies: at the next major (3.0.0, the 2.x artifact's ceiling), whose
// plugin format nobody has seen. A LOCK here — on #2105's code 3.0.0 was already
// at or above the 1.x ceiling and removed the file — and the test
// tools/lib/opencode-v2-harmless-mutations_test.sh's ceiling-removal mutation
// turns red.
func TestOpenCodeCeiling_RemovesTheStaleV1Plugin(t *testing.T) {
	path := isolateOpenCodeConfig(t)
	if _, err := opencode.EnsureHooksInstalled(); err != nil {
		t.Fatalf("seed the 1.x plugin: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("seeded plugin is not on disk: %v", err)
	}

	svc, log := newGateService()
	grant(svc, opencodeHooksAt(t, "3.0.0"))

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("the 1.x plugin at %s survived a grant against opencode 3.0.0 (stat err %v); "+
			"no artifact this daemon ships is known to load there", path, err)
	}
	if !log.errorMentioning("3.0.0") {
		t.Errorf("no logged refusal naming 3.0.0; errors were %v", log.errors)
	}
}

// v2Rendering is what the 2.x installer writes (see rendering).
func v2Rendering(t *testing.T) []byte { return rendering(t, opencode.EnsureHooksInstalledV2) }

// rendering is what install writes, read back from a reference install in a
// tree of its own. It relocates the config dir, so call it before isolating
// the tree under test.
func rendering(t *testing.T, install func() (bool, error)) []byte {
	t.Helper()
	isolateOpenCodeConfig(t)
	if _, err := install(); err != nil {
		t.Fatalf("reference install: %v", err)
	}
	ref, _ := opencode.PluginPath()
	want, err := os.ReadFile(ref)
	if err != nil {
		t.Fatalf("read the reference install: %v", err)
	}
	return want
}

// TestOpenCodeCeiling_LeavesAForeignFileAlone is a LOCK: hookartifact's
// sentinel check already refused to touch a file irrlicht did not write, and
// routing both the ceiling's removal and the 2.x rewrite through the adapter's
// own installer is what keeps that true. A user's own plugin/irrlicht.js
// survives a ceiling refusal (3.0.0) and a 2.x install (2.1.4) byte for byte.
func TestOpenCodeCeiling_LeavesAForeignFileAlone(t *testing.T) {
	for _, version := range []string{"3.0.0", "2.1.4"} {
		t.Run(version, func(t *testing.T) {
			path := isolateOpenCodeConfig(t)
			foreign := []byte("// the user's own plugin, not irrlicht's\nexport default {}\n")
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			if err := os.WriteFile(path, foreign, 0o644); err != nil {
				t.Fatalf("seed foreign file: %v", err)
			}

			svc, _ := newGateService()
			grant(svc, opencodeHooksAt(t, version))

			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("the foreign file is gone after a grant at %s: %v", version, err)
			}
			if !bytes.Equal(got, foreign) {
				t.Errorf("the foreign file was changed by a grant at %s:\n got %q\nwant %q", version, got, foreign)
			}
		})
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
