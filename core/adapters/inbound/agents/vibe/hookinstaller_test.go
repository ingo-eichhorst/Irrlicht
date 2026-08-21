// hookinstaller_test.go covers the writing half: the install-type permission
// gate (#797), and the install/verify/uninstall round trip against Vibe's
// own hooks.toml and config.toml.
//
// The two-file shape is the point of several tests here that no prior hook
// adapter needed: this permission's Apply writes hooks.toml (the entry) AND
// config.toml (the gate the entry cannot fire without) — agent.
// ManagedUserFile.Also (#1731), and the file this ticket is the first real
// consumer of.
package vibe

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"irrlicht/core/domain/permission"
	"irrlicht/core/internal/contracttesting"
)

// hooksPermission returns the real declared hooks permission.
func hooksPermission(t *testing.T) (apply, remove func() error) {
	t.Helper()
	for _, p := range Agent().Permissions {
		if p.Key == PermissionKeyHooks {
			return p.Apply, p.Remove
		}
	}
	t.Fatal("no hooks permission declared")
	return nil, nil
}

// hooksAreLiveAndEnabled reports whether hooks.toml carries our entry AND
// config.toml's gate is on — the two facts that together mean the hook can
// actually fire, mirroring VerifyHooksInstalled's own definition of intact.
func hooksAreLiveAndEnabled(t *testing.T) bool {
	t.Helper()
	status, err := VerifyHooksInstalled()
	if err != nil {
		t.Fatalf("VerifyHooksInstalled: %v", err)
	}
	return status.Intact()
}

// TestHooksPermission_IsGated wires the install-type flavour of the #797
// contract: nothing is written while the permission is pending, granting
// writes both files, and denying removes the entry (and, since nothing else
// is installed, clears the flag too).
func TestHooksPermission_IsGated(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv(vibeHomeEnvVar, "")
	apply, remove := hooksPermission(t)

	state := permission.StatePending
	contracttesting.AssertPermissionGated(t, contracttesting.PermissionGate{
		Key: PermissionKeyHooks,
		// transcripts is the adapter's only other declared permission and is
		// observe-kind, so it has no closure to drive: the key-isolation arm
		// is INERT here and repeats the revoked arm exactly, the same
		// situation copilot's and geminicli's equivalent tests document.
		// Install-type wirings hold their own permission's closures, so a
		// wrong key is not representable — the arm is load-bearing at the
		// live receiver (hooks_test.go), not here.
		OtherKeys: []string{PermissionKeyTranscripts},
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
		Observe: func() bool { return hooksAreLiveAndEnabled(t) },
	})
}

// TestInstallVerifyUninstall_RoundTrip pins the three-way agreement between
// what the installer writes, what Verify reports, and what Uninstall
// removes.
func TestInstallVerifyUninstall_RoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv(vibeHomeEnvVar, "")

	status, err := VerifyHooksInstalled()
	if err != nil {
		t.Fatalf("verify before install: %v", err)
	}
	if status.Intact() {
		t.Error("Verify reports intact before anything was installed")
	}

	modified, err := EnsureHooksInstalled()
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if !modified {
		t.Error("first install reported no modification")
	}

	if status, err = VerifyHooksInstalled(); err != nil || !status.Intact() {
		t.Errorf("Verify after install: intact=%v err=%v damage=%s", status.Intact(), err, status.Damage())
	}

	// Idempotent: a second install changes nothing.
	if modified, err = EnsureHooksInstalled(); err != nil || modified {
		t.Errorf("second install modified=%v err=%v, want false/nil", modified, err)
	}

	if modified, err = UninstallHooks(); err != nil || !modified {
		t.Errorf("uninstall modified=%v err=%v, want true/nil", modified, err)
	}
	if hooksAreLiveAndEnabled(t) {
		t.Error("entries survive uninstall")
	}
}

// TestVerifyDoesNotCreateEitherFile is the read-only half of #1372, over
// both files this permission's Apply can write.
func TestVerifyDoesNotCreateEitherFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(vibeHomeEnvVar, "")

	if _, err := VerifyHooksInstalled(); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, defaultHomeDirName)); !os.IsNotExist(err) {
		t.Errorf("Verify created the .vibe directory; it must be read-only")
	}
}

// TestUninstallPreservesUnrelatedContent seeds both files with content
// irrlicht did not write and asserts install+uninstall leaves it byte-for-
// byte alone — the property hooktoml's own tests establish in isolation,
// pinned here at the adapter's actual call sites.
func TestUninstallPreservesUnrelatedContent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(vibeHomeEnvVar, "")
	vibeHome := filepath.Join(home, defaultHomeDirName)
	if err := os.MkdirAll(vibeHome, 0o700); err != nil {
		t.Fatal(err)
	}

	hooksSeed := "# my own lint hook\n[[hooks]]\nname = \"lint\"\ntype = \"post_agent_turn\"\ncommand = \"eslint --quiet .\"\n"
	if err := os.WriteFile(filepath.Join(vibeHome, hooksFilename), []byte(hooksSeed), 0o600); err != nil {
		t.Fatal(err)
	}
	configSeed := "active_model = \"mistral-medium-3.5\"  # my favourite\nbypass_tool_permissions = false\n"
	if err := os.WriteFile(filepath.Join(vibeHome, configFilename), []byte(configSeed), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := EnsureHooksInstalled(); err != nil {
		t.Fatalf("install: %v", err)
	}
	if _, err := UninstallHooks(); err != nil {
		t.Fatalf("uninstall: %v", err)
	}

	hooksGot, err := os.ReadFile(filepath.Join(vibeHome, hooksFilename))
	if err != nil {
		t.Fatalf("hooks.toml gone after install+uninstall: %v", err)
	}
	if !containsAll(string(hooksGot), "# my own lint hook", "name = \"lint\"") {
		t.Errorf("the user's own hook or its comment was lost:\n%s", hooksGot)
	}

	configGot, err := os.ReadFile(filepath.Join(vibeHome, configFilename))
	if err != nil {
		t.Fatalf("config.toml gone after install+uninstall: %v", err)
	}
	if !containsAll(string(configGot), "active_model = \"mistral-medium-3.5\"  # my favourite", "bypass_tool_permissions = false") {
		t.Errorf("unrelated config.toml content was lost:\n%s", configGot)
	}
}

// TestUninstall_ClearsTheFlagOnlyWhenNoOtherHooksRemain is the defect test
// for the two-file coordination this adapter's Uninstall owns: a user's OWN
// hand-written [[hooks]] entry in hooks.toml still needs
// enable_experimental_hooks held true after OUR entry is removed, or their
// hook silently stops firing too.
func TestUninstall_ClearsTheFlagOnlyWhenNoOtherHooksRemain(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(vibeHomeEnvVar, "")
	vibeHome := filepath.Join(home, defaultHomeDirName)
	if err := os.MkdirAll(vibeHome, 0o700); err != nil {
		t.Fatal(err)
	}
	userHook := "[[hooks]]\nname = \"lint\"\ntype = \"post_agent_turn\"\ncommand = \"eslint --quiet .\"\n"
	if err := os.WriteFile(filepath.Join(vibeHome, hooksFilename), []byte(userHook), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := EnsureHooksInstalled(); err != nil {
		t.Fatalf("install: %v", err)
	}
	if _, err := UninstallHooks(); err != nil {
		t.Fatalf("uninstall: %v", err)
	}

	configPath := filepath.Join(vibeHome, configFilename)
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("config.toml: %v", err)
	}
	if !containsAll(string(data), "enable_experimental_hooks = true") {
		t.Errorf("the flag was cleared although the user's own hook is still in hooks.toml — "+
			"their hook can no longer fire:\n%s", data)
	}

	hooksGot, err := os.ReadFile(filepath.Join(vibeHome, hooksFilename))
	if err != nil {
		t.Fatal(err)
	}
	if !containsAll(string(hooksGot), "name = \"lint\"") {
		t.Errorf("the user's own hook was removed:\n%s", hooksGot)
	}
}

// TestUninstall_ClearsTheFlagWhenNoHooksRemain is the vacuity guard for the
// test above: when OUR entry was the only one, the flag IS cleared.
func TestUninstall_ClearsTheFlagWhenNoHooksRemain(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv(vibeHomeEnvVar, "")

	if _, err := EnsureHooksInstalled(); err != nil {
		t.Fatalf("install: %v", err)
	}
	if _, err := UninstallHooks(); err != nil {
		t.Fatalf("uninstall: %v", err)
	}

	configPath, err := vibeConfigTomlPath()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("config.toml: %v", err)
	}
	if containsAll(string(data), "enable_experimental_hooks = true") {
		t.Errorf("the flag survived although no [[hooks]] block remains:\n%s", data)
	}
}

// TestEnsureHooksInstalled_RewritesAStaleCommandInPlace pins that a
// previously-installed entry naming a different (e.g. moved) irrlichd binary
// is reconciled in place rather than left stale or duplicated — the
// adapter-side half of hookbeacon's binary-path drift handling, which
// hooktoml's own IsCanonical hook exists to let an adapter wire at all.
func TestEnsureHooksInstalled_RewritesAStaleCommandInPlace(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(vibeHomeEnvVar, "")
	vibeHome := filepath.Join(home, defaultHomeDirName)
	if err := os.MkdirAll(vibeHome, 0o700); err != nil {
		t.Fatal(err)
	}
	stale := "[[hooks]]\nname = \"irrlicht-turn-done\"\ntype = \"post_agent_turn\"\n" +
		"command = \"/old/nonexistent/irrlichd hook-post mistral-vibe >/dev/null || true\"\n"
	if err := os.WriteFile(filepath.Join(vibeHome, hooksFilename), []byte(stale), 0o600); err != nil {
		t.Fatal(err)
	}

	modified, err := EnsureHooksInstalled()
	if err != nil {
		t.Fatalf("EnsureHooksInstalled: %v", err)
	}
	if !modified {
		t.Error("reconciling a stale entry reported no modification")
	}
	if !hooksAreLiveAndEnabled(t) {
		t.Error("the entry is not canonical after reconciliation")
	}
	got, err := os.ReadFile(filepath.Join(vibeHome, hooksFilename))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(got), "[[hooks]]") != 1 {
		t.Errorf("reconciliation duplicated the block instead of rewriting it in place:\n%s", got)
	}
	if strings.Contains(string(got), "/old/nonexistent/irrlichd") {
		t.Errorf("the stale binary path survived reconciliation:\n%s", got)
	}
}

// TestWrites_DeclaresBothFiles pins the #1731 field this adapter is the
// first real consumer of: Path is hooks.toml (what --uninstall-hooks and
// the disclosure copy mean by "the file this permission writes"), Also
// names config.toml.
func TestWrites_DeclaresBothFiles(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv(vibeHomeEnvVar, "")

	for _, p := range Agent().Permissions {
		if p.Key != PermissionKeyHooks {
			continue
		}
		if p.Writes == nil {
			t.Fatal("hooks permission declares no Writes")
		}
		path, err := p.Writes.Path()
		if err != nil {
			t.Fatalf("Path(): %v", err)
		}
		if filepath.Base(path) != hooksFilename {
			t.Errorf("Writes.Path() = %q, want basename %q", path, hooksFilename)
		}
		if len(p.Writes.Also) != 1 {
			t.Fatalf("Writes.Also has %d entries, want 1", len(p.Writes.Also))
		}
		also, err := p.Writes.Also[0]()
		if err != nil {
			t.Fatalf("Also[0](): %v", err)
		}
		if filepath.Base(also) != configFilename {
			t.Errorf("Writes.Also[0]() = %q, want basename %q", also, configFilename)
		}
		return
	}
	t.Fatal("no hooks permission declared")
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}
