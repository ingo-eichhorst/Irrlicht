package pi

import (
	"os"
	"path/filepath"
	"testing"

	"irrlicht/core/domain/permission"
	"irrlicht/core/internal/contracttesting"
	"irrlicht/core/pkg/atomicfile"
	"irrlicht/core/pkg/hookbeacon"
)

// hooksPermission returns the real declared hooks permission's closures.
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

// TestHooksPermission_IsGated wires the install-type flavour of the #797
// contract: nothing is written while the permission is pending, granting
// installs the extension, and denying removes it.
//
// The stake here is higher than for a config-entry adapter and worth naming.
// What this permission's Apply writes is EXECUTABLE CODE that pi loads into
// every session, so "nothing is exercised while pending or denied" is not
// only a consent rule — a pre-consent daemon that ran Apply would have put
// code inside the user's agent before the wizard was ever answered.
func TestHooksPermission_IsGated(t *testing.T) {
	piHome(t)
	apply, remove := hooksPermission(t)

	state := permission.StatePending
	contracttesting.AssertPermissionGated(t, contracttesting.PermissionGate{
		Key: PermissionKeyHooks,
		// transcripts is the adapter's only other declared permission and is
		// observe-kind, so it has no closure to drive: the key-isolation arm
		// is INERT here and repeats the revoked arm exactly, the same
		// situation copilot's, geminicli's and vibe's equivalent tests
		// document. Install-type wirings hold their own permission's
		// closures, so a wrong key is not representable — the arm is
		// load-bearing at the live receiver (hooks_test.go), not here.
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
		Observe: func() bool {
			status, err := VerifyHooksInstalled()
			if err != nil {
				t.Fatalf("VerifyHooksInstalled: %v", err)
			}
			return status.Intact()
		},
	})
}

// piHome relocates $HOME and clears both pi overrides, returning the temp
// home. Every test in this file WRITES, so isolation is not optional.
func piHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(agentDirEnvVar, "")
	t.Setenv(sessionDirEnvVar, "")
	return home
}

func extensionPath(t *testing.T) string {
	t.Helper()
	p, err := ExtensionPath()
	if err != nil {
		t.Fatalf("ExtensionPath: %v", err)
	}
	return p
}

// TestEnsureInstalled_CreatesTheExtensionAndIsIdempotent pins the base case:
// the file lands in pi's own auto-discovery directory, and a second call is
// a no-op rather than a rewrite (a daemon restart must not churn the file
// under a running pi session).
func TestEnsureInstalled_CreatesTheExtensionAndIsIdempotent(t *testing.T) {
	home := piHome(t)

	modified, err := EnsureHooksInstalled()
	if err != nil {
		t.Fatalf("EnsureHooksInstalled: %v", err)
	}
	if !modified {
		t.Error("first install reported no modification")
	}

	want := filepath.Join(home, defaultAgentDirName, extensionsDirName, extensionFilename)
	if got := extensionPath(t); got != want {
		t.Errorf("ExtensionPath() = %q, want %q — pi auto-discovers only this directory", got, want)
	}
	data, err := os.ReadFile(want)
	if err != nil {
		t.Fatalf("reading the installed extension: %v", err)
	}
	if !isOurs(data) {
		t.Error("the installed file does not carry the beacon sentinel")
	}

	modified, err = EnsureHooksInstalled()
	if err != nil {
		t.Fatalf("second EnsureHooksInstalled: %v", err)
	}
	if modified {
		t.Error("second install reported a modification; the install is not idempotent")
	}
}

// TestEnsureInstalled_RefusesToOverwriteAFileItDidNotWrite is the guard on
// the one genuinely destructive thing this adapter could do. Our path is a
// name we chose inside a directory the USER owns and pi shares with every
// other extension; overwriting whatever is there for the sake of monitoring
// is never the right trade, and the refusal surfaces as #1362's "granted but
// NOT applied" with this message rather than as silent data loss.
func TestEnsureInstalled_RefusesToOverwriteAFileItDidNotWrite(t *testing.T) {
	piHome(t)
	path := extensionPath(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	const theirs = "export default function () { /* someone else's extension */ }\n"
	if err := os.WriteFile(path, []byte(theirs), 0o600); err != nil {
		t.Fatal(err)
	}

	modified, err := EnsureHooksInstalled()
	if err == nil {
		t.Error("EnsureHooksInstalled overwrote a foreign file without complaint")
	}
	if modified {
		t.Error("EnsureHooksInstalled reported a modification while refusing")
	}
	got, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != theirs {
		t.Error("the foreign file was modified")
	}
}

// TestUninstall_RemovesOursAndLeavesForeignAlone pins both directions of the
// "leave a foreign entry alone" rule every hook uninstall in this codebase
// follows, applied to a whole file rather than to an entry inside one.
func TestUninstall_RemovesOursAndLeavesForeignAlone(t *testing.T) {
	piHome(t)
	if _, err := EnsureHooksInstalled(); err != nil {
		t.Fatalf("EnsureHooksInstalled: %v", err)
	}
	path := extensionPath(t)

	modified, err := UninstallHooks()
	if err != nil {
		t.Fatalf("UninstallHooks: %v", err)
	}
	if !modified {
		t.Error("uninstall of an installed extension reported no modification")
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Errorf("the extension file survived uninstall (stat err = %v)", statErr)
	}

	// A second uninstall is a no-op, not an error.
	if modified, err = UninstallHooks(); err != nil || modified {
		t.Errorf("second uninstall = (%v, %v), want (false, nil)", modified, err)
	}

	// And a foreign file at our path is left alone.
	const theirs = "export default function () {}\n"
	if err := os.WriteFile(path, []byte(theirs), 0o600); err != nil {
		t.Fatal(err)
	}
	if modified, err = UninstallHooks(); err != nil || modified {
		t.Errorf("uninstall over a foreign file = (%v, %v), want (false, nil)", modified, err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != theirs {
		t.Error("uninstall removed or altered a file it did not write")
	}
}

// TestVerifyAgreesWithEnsureInstalled is the #1372 obligation as a property
// rather than as three hand-picked cases: for every state the file can be
// in, VerifyHooksInstalled reports work outstanding exactly when
// EnsureHooksInstalled would do some.
//
// It exists because those are two separate readers of the same file, and the
// failure mode of a disagreement is invisible in both directions — a verifier
// that under-reports leaves a dead channel green forever, and one that
// over-reports makes the repair timer rewrite a correct file on every tick.
func TestVerifyAgreesWithEnsureInstalled(t *testing.T) {
	foreign, err := hookbeacon.Command(foreignBinaryPath, AdapterName)
	if err != nil {
		t.Fatal(err)
	}
	stale, err := renderExtension(foreign)
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name            string
		seed            func(t *testing.T, path string)
		wantOutstanding bool
	}{
		{name: "absent", seed: func(*testing.T, string) {}, wantOutstanding: true},
		{
			name: "ours_but_naming_another_binary",
			seed: func(t *testing.T, path string) {
				if err := atomicfile.WriteFile(path, stale); err != nil {
					t.Fatal(err)
				}
			},
			wantOutstanding: true,
		},
		{
			name: "current",
			seed: func(t *testing.T, path string) {
				if _, err := EnsureHooksInstalled(); err != nil {
					t.Fatal(err)
				}
			},
			wantOutstanding: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			piHome(t)
			tc.seed(t, extensionPath(t))

			status, err := VerifyHooksInstalled()
			if err != nil {
				t.Fatalf("VerifyHooksInstalled: %v", err)
			}
			reported := len(status.Missing) > 0 || len(status.Stale) > 0
			if reported != tc.wantOutstanding {
				t.Errorf("verify reported outstanding=%v (missing=%v stale=%v), want %v",
					reported, status.Missing, status.Stale, tc.wantOutstanding)
			}

			modified, err := EnsureHooksInstalled()
			if err != nil {
				t.Fatalf("EnsureHooksInstalled: %v", err)
			}
			if modified != reported {
				t.Errorf("verify says outstanding=%v but ensure modified=%v — the two "+
					"readers of the same file disagree", reported, modified)
			}
		})
	}
}

// TestVerify_DoesNotWrite pins that the verifier is read-only by
// construction (#1372): it runs on a timer against a granted install, and a
// verifier that materialized what it was checking for would report health it
// had just created.
func TestVerify_DoesNotWrite(t *testing.T) {
	piHome(t)
	if _, err := VerifyHooksInstalled(); err != nil {
		t.Fatalf("VerifyHooksInstalled: %v", err)
	}
	if _, err := os.Stat(extensionPath(t)); !os.IsNotExist(err) {
		t.Errorf("verify created the extension file it was asked to check (stat err = %v)", err)
	}
}

// TestInstallLeavesAPiWrittenSettingsFileUntouched is issue #1756's
// obligation for this adapter: grade the install against a config pi itself
// wrote, not one the test constructed.
//
// The fixture is a verbatim ~/.pi/agent/settings.json from pi 0.83.0
// (testdata/README.md carries its provenance), and its `packages` array is
// the load-bearing part — that array is what `pi install` writes, so this is
// a home where pi's OTHER extension mechanism is already in use. The claim
// under test is the one the consent copy makes: irrlicht's install adds a
// file and touches nothing else.
//
// The assertion is a whole-tree diff rather than a check on settings.json
// alone, because "the file I remembered to look at is unchanged" is exactly
// the shape of the blind spot #1756 was filed about.
func TestInstallLeavesAPiWrittenSettingsFileUntouched(t *testing.T) {
	home := piHome(t)
	agentDir := filepath.Join(home, defaultAgentDirName)
	if err := os.MkdirAll(filepath.Join(agentDir, extensionsDirName), 0o755); err != nil {
		t.Fatal(err)
	}
	real, err := os.ReadFile(filepath.Join("testdata", "real-pi-settings.json"))
	if err != nil {
		t.Fatalf("reading the captured pi settings fixture: %v", err)
	}
	settings := filepath.Join(agentDir, "settings.json")
	if err := os.WriteFile(settings, real, 0o600); err != nil {
		t.Fatal(err)
	}
	// A second extension in the same directory, as a `pi install`-populated
	// home would have.
	neighbour := filepath.Join(agentDir, extensionsDirName, "someone-elses.ts")
	if err := os.WriteFile(neighbour, []byte("export default function () {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	before := treeSnapshot(t, home)
	if _, err := EnsureHooksInstalled(); err != nil {
		t.Fatalf("EnsureHooksInstalled: %v", err)
	}
	after := treeSnapshot(t, home)

	ours, err := filepath.Rel(home, extensionPath(t))
	if err != nil {
		t.Fatal(err)
	}
	for path, content := range after {
		if path == ours {
			continue
		}
		if content != before[path] {
			t.Errorf("install changed %s, which is not the file it declares it writes", path)
		}
	}
	for path := range before {
		if _, still := after[path]; !still {
			t.Errorf("install removed %s", path)
		}
	}
	if _, installed := after[ours]; !installed {
		t.Fatalf("install wrote nothing at %s; the diff above proved nothing", ours)
	}

	// And uninstall gives the tree back exactly.
	if _, err := UninstallHooks(); err != nil {
		t.Fatalf("UninstallHooks: %v", err)
	}
	restored := treeSnapshot(t, home)
	if len(restored) != len(before) {
		t.Errorf("after uninstall the tree holds %d files, before install it held %d",
			len(restored), len(before))
	}
	for path, content := range before {
		if restored[path] != content {
			t.Errorf("after uninstall %s no longer matches what pi wrote", path)
		}
	}
}

// treeSnapshot maps every regular file under root to its content, keyed by
// its root-relative path.
func treeSnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		out[rel] = string(data)
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}
	return out
}
