// hookinstaller_test.go grades the Go side of the install: where the file
// lands, what happens when one is already there, and that nothing is written
// until the user has consented.
package opencode

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/permission"
	"irrlicht/core/internal/contracttesting"
	"irrlicht/core/pkg/hookbeacon"
)

// opencodeHome relocates $HOME and clears $XDG_CONFIG_HOME, returning the temp
// home. Every test in this file WRITES, so isolation is not optional.
func opencodeHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(configHomeEnvVar, "")
	return home
}

// hooksPermission returns the declared Apply/Remove closures, read off the real
// registration the daemon consumes rather than a copy.
func hooksPermission(t *testing.T) (apply, remove func() error) {
	t.Helper()
	for _, p := range Agent().Permissions {
		if p.Key == PermissionKeyHooks {
			if p.Apply == nil || p.Remove == nil {
				t.Fatal("hooks permission declares no Apply/Remove")
			}
			return p.Apply, p.Remove
		}
	}
	t.Fatal("no hooks permission")
	return nil, nil
}

// TestHooksPermission_IsGated wires the install-type flavour of the #797
// contract: nothing is written while the permission is pending, granting
// installs the plugin, and denying removes it.
//
// The stake here is higher than for a config-entry adapter and worth naming.
// What this permission's Apply writes is EXECUTABLE CODE that opencode loads
// into every session, so "nothing is exercised while pending or denied" is not
// only a consent rule — a pre-consent daemon that ran Apply would have put code
// inside the user's agent before the wizard was ever answered.
func TestHooksPermission_IsGated(t *testing.T) {
	opencodeHome(t)
	apply, remove := hooksPermission(t)

	state := permission.StatePending
	contracttesting.AssertPermissionGated(t, contracttesting.PermissionGate{
		Key: PermissionKeyHooks,
		// database is the adapter's only other declared permission and is
		// observe-kind, so it has no closure to drive: the key-isolation arm is
		// INERT here and repeats the revoked arm exactly, the same situation
		// copilot's, geminicli's, vibe's and pi's equivalent tests document.
		// Install-type wirings hold their own permission's closures, so a wrong
		// key is not representable — the arm is load-bearing at the live
		// receiver (hooks_test.go), not here.
		OtherKeys: []string{PermissionKeyDatabase},
		SetState:  contracttesting.OnlyKey(PermissionKeyHooks, func(s permission.State) { state = s }),
		Exercise: func() {
			switch state {
			case permission.StateGranted:
				if err := apply(); err != nil {
					t.Fatalf("apply: %v", err)
				}
			case permission.StateDenied:
				if err := remove(); err != nil {
					t.Fatalf("remove: %v", err)
				}
			}
		},
		Observe: func() bool {
			status, err := VerifyHooksInstalled()
			if err != nil {
				t.Fatalf("VerifyHooksInstalled: %v", err)
			}
			return status.Intact()
		},
	})
}

// TestPluginPath_LandsInOpenCodesGlobalConfigDir pins WHERE the file goes, in
// both spellings opencode's own Global.Path.config resolves.
//
// The XDG leg is not decoration: opencode composes its config directory as
// join(process.env.XDG_CONFIG_HOME || join(homedir(), ".config"), "opencode"),
// so a user with that variable set has their plugin directory somewhere else
// entirely — and an installer that ignored it would write a file opencode never
// loads, with the permission still reading granted and nothing to look at.
func TestPluginPath_LandsInOpenCodesGlobalConfigDir(t *testing.T) {
	home := opencodeHome(t)

	got, err := PluginPath()
	if err != nil {
		t.Fatalf("PluginPath: %v", err)
	}
	if want := filepath.Join(home, ".config", "opencode", "plugin", "irrlicht.js"); got != want {
		t.Errorf("PluginPath() = %q, want %q", got, want)
	}

	xdg := t.TempDir()
	t.Setenv(configHomeEnvVar, xdg)
	got, err = PluginPath()
	if err != nil {
		t.Fatalf("PluginPath with %s set: %v", configHomeEnvVar, err)
	}
	if want := filepath.Join(xdg, "opencode", "plugin", "irrlicht.js"); got != want {
		t.Errorf("PluginPath() with %s=%q = %q, want %q", configHomeEnvVar, xdg, got, want)
	}
}

// TestEnsureHooksInstalled_IsIdempotent pins that a second call changes
// nothing. The permission service re-runs Apply on every daemon start, so a
// non-idempotent installer would rewrite the user's plugin file forever.
func TestEnsureHooksInstalled_IsIdempotent(t *testing.T) {
	opencodeHome(t)

	changed, err := EnsureHooksInstalled()
	if err != nil {
		t.Fatalf("first install: %v", err)
	}
	if !changed {
		t.Fatal("first install reported no change")
	}

	changed, err = EnsureHooksInstalled()
	if err != nil {
		t.Fatalf("second install: %v", err)
	}
	if changed {
		t.Error("second install reported a change — Apply runs on every daemon start")
	}
}

// TestEnsureHooksInstalled_RefusesToOverwriteAForeignFile is the rule every
// hook uninstall and install in this codebase follows, and it matters more here
// than anywhere: `plugin/irrlicht.js` is a path a user could plausibly have
// written themselves, and overwriting it would destroy their own plugin for the
// sake of monitoring.
func TestEnsureHooksInstalled_RefusesToOverwriteAForeignFile(t *testing.T) {
	opencodeHome(t)
	path, err := PluginPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	const theirs = "export default async () => ({})\n"
	if err := os.WriteFile(path, []byte(theirs), 0o644); err != nil {
		t.Fatal(err)
	}

	changed, err := EnsureHooksInstalled()
	if err == nil {
		t.Fatal("EnsureHooksInstalled overwrote a file irrlicht did not write")
	}
	if changed {
		t.Error("EnsureHooksInstalled reported a change while refusing")
	}
	got, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != theirs {
		t.Error("the user's own plugin file was modified")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("the refusal %q does not name the file — the wizard surfaces this string as \"granted but NOT applied\" (#1362)", err)
	}
}

// TestUninstallHooks_LeavesAForeignFileAlone is the mirror of the above.
func TestUninstallHooks_LeavesAForeignFileAlone(t *testing.T) {
	opencodeHome(t)
	path, err := PluginPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("export default async () => ({})\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	changed, err := UninstallHooks()
	if err != nil {
		t.Fatalf("UninstallHooks: %v", err)
	}
	if changed {
		t.Error("UninstallHooks reported a change for a file irrlicht did not write")
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("the user's own plugin file was removed: %v", err)
	}
}

// TestUninstallHooks_OnNothingIsQuiet pins that a revoke with no install is not
// an error. PermissionService runs Remove whenever the answer is "denied",
// including the first time the wizard is answered.
func TestUninstallHooks_OnNothingIsQuiet(t *testing.T) {
	opencodeHome(t)
	changed, err := UninstallHooks()
	if err != nil {
		t.Fatalf("UninstallHooks with nothing installed: %v", err)
	}
	if changed {
		t.Error("UninstallHooks reported a change with nothing installed")
	}
}

// TestVerifyHooksInstalled_ReportsTheThreeStates is the #1372 half: the
// verifier re-reads a granted install on a timer, so it has to tell "gone" from
// "stale" from "intact". A file that is not ours is Missing rather than Stale,
// because "stale" means EnsureHooksInstalled would rewrite it and it
// deliberately will not.
func TestVerifyHooksInstalled_ReportsTheThreeStates(t *testing.T) {
	opencodeHome(t)
	path, err := PluginPath()
	if err != nil {
		t.Fatal(err)
	}

	status, err := VerifyHooksInstalled()
	if err != nil {
		t.Fatalf("verify with nothing installed: %v", err)
	}
	if len(status.Missing) != len(installedHookEvents) {
		t.Errorf("nothing installed: Missing = %v, want all %d events", status.Missing, len(installedHookEvents))
	}

	if _, err := EnsureHooksInstalled(); err != nil {
		t.Fatal(err)
	}
	if status, err = VerifyHooksInstalled(); err != nil {
		t.Fatalf("verify after install: %v", err)
	}
	if !status.Intact() {
		t.Errorf("after a fresh install: %+v, want intact", status)
	}

	// Ours, but written by a daemon that lived somewhere else.
	foreign, err := hookbeacon.Command(foreignBinaryPath, AdapterName)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ensureInstalledWithCommand(foreign); err != nil {
		t.Fatal(err)
	}
	if status, err = VerifyHooksInstalled(); err != nil {
		t.Fatalf("verify after a foreign-binary install: %v", err)
	}
	if len(status.Stale) != len(installedHookEvents) {
		t.Errorf("a copy naming another irrlichd: %+v, want all %d events Stale", status, len(installedHookEvents))
	}

	// Not ours at all.
	if err := os.WriteFile(path, []byte("export default async () => ({})\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if status, err = VerifyHooksInstalled(); err != nil {
		t.Fatalf("verify against a foreign file: %v", err)
	}
	if len(status.Missing) != len(installedHookEvents) {
		t.Errorf("a foreign file at our path: %+v, want all %d events Missing (not Stale — Ensure will not rewrite it)", status, len(installedHookEvents))
	}
}

// TestUninstallHooks_LeavesThePluginDirectoryBehind pins the deliberate
// omission recorded on UninstallHooks: an empty plugin/ is an ordinary state
// for an opencode installation, and removing a directory a user's editor or
// shell may have open is a worse surprise than leaving one behind.
func TestUninstallHooks_LeavesThePluginDirectoryBehind(t *testing.T) {
	opencodeHome(t)
	if _, err := EnsureHooksInstalled(); err != nil {
		t.Fatal(err)
	}
	if _, err := UninstallHooks(); err != nil {
		t.Fatal(err)
	}
	path, err := PluginPath()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("the plugin file survived uninstall: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(path)); err != nil {
		t.Errorf("the plugin directory was removed too: %v", err)
	}
}

// variantAt returns the artifact the real declaration selects at version,
// failing when it selects none.
func variantAt(t *testing.T, version string) *agent.HookVariant {
	t.Helper()
	v, bound, why := hooksWrites(t).SelectVariant(version)
	if v == nil {
		t.Fatalf("opencode %s selects no hook artifact (bound %d: %q)", version, bound, why)
	}
	return v
}

// renderedNow is what renders for the irrlichd running this test.
func renderedNow(t *testing.T, render func(string) ([]byte, error)) []byte {
	t.Helper()
	command, err := hookbeacon.InstalledCommand(AdapterName)
	if err != nil {
		t.Fatalf("beacon command: %v", err)
	}
	want, err := render(command)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	return want
}

// TestVariants_TwoXWritesTheTwoXArtifact is #2108's adapter-level test: the
// artifact selected at 2.1.4 writes plugin_v2.js's rendering at PluginPath, and
// the one selected at 1.19.4 writes plugin.js's byte for byte (that half a LOCK:
// it is what 1.x always got).
func TestVariants_TwoXWritesTheTwoXArtifact(t *testing.T) {
	for version, render := range map[string]func(string) ([]byte, error){
		"2.1.4":  renderPluginV2,
		"1.19.4": renderPlugin,
	} {
		t.Run(version, func(t *testing.T) {
			opencodeHome(t)
			if err := variantAt(t, version).Apply(); err != nil {
				t.Fatalf("Apply: %v", err)
			}
			path, _ := PluginPath()
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("nothing at %s: %v", path, err)
			}
			if want := renderedNow(t, render); string(got) != string(want) {
				t.Errorf("opencode %s installed %d bytes at %s, want the %d its artifact renders", version, len(got), path, len(want))
			}
		})
	}
}

// TestVariants_UpgradeReadsStaleThenRewritesInPlace is the read-only half of the
// upgrade: under 2.1.4 the 1.x file reads Stale — ours, not current — which is
// what makes the #1372 loop repair it, and the 2.x Apply then rewrites it in
// place, after which it reads Intact. A 1.x file read as foreign instead would
// be reported Missing and never overwritten.
func TestVariants_UpgradeReadsStaleThenRewritesInPlace(t *testing.T) {
	opencodeHome(t)
	if _, err := EnsureHooksInstalled(); err != nil {
		t.Fatalf("seed the 1.x plugin: %v", err)
	}
	v2 := variantAt(t, "2.1.4")

	status, err := v2.Verify()
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if len(status.Stale) != len(installedHookEventsV2) || len(status.Missing) != 0 {
		t.Fatalf("the 1.x plugin under 2.1.4 reads %+v, want every 2.x event Stale", status)
	}
	if err := v2.Apply(); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if status, err = v2.Verify(); err != nil || !status.Intact() {
		t.Errorf("after the 2.x Apply the install reads %+v (err %v), want intact", status, err)
	}
	path, _ := PluginPath()
	if entries, _ := os.ReadDir(filepath.Dir(path)); len(entries) != 1 {
		t.Errorf("the plugin directory holds %d files after the rewrite, want 1 — an upgrade is a rewrite in place", len(entries))
	}
}
