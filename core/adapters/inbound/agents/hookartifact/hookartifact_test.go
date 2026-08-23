// hookartifact_test.go carries forward, as permanent locks against this
// package, both adapters' pre-existing mutation tests for the shape this
// package extracted from pi/hookinstaller.go and opencode/hookinstaller.go
// (issue #1764). Per docs/testing-philosophy.md, a rewrite that replaces an
// existing implementation owes its predecessors' cases on top of its own —
// TestSourceScanCatchesEveryKnownShape (core/application/services/
// construction_test.go) is the cautionary precedent the issue names: a
// rewrite there silently dropped a case the original caught while eighteen
// new probes all passed. Every test below is traceable to a specific
// pre-existing test in pi/hookinstaller_test.go or
// opencode/hookinstaller_test.go, named in its doc comment, so a reader can
// check nothing was dropped in translation.
//
// These fixtures use a synthetic adapter identity rather than pi's or
// opencode's real one, so this package's own test suite proves the shared
// mechanism correct independently of either adapter's continued existence —
// pi's and opencode's own (unmodified) hookinstaller_test.go files are the
// second, end-to-end proof that the real wiring still behaves identically
// after the extraction.
package hookartifact

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"irrlicht/core/pkg/hookbeacon"
)

// fixtureAdapter is a real adapter segment (hookbeacon validates the shape)
// scoped so it can never collide with an actual adapter's own sentinel.
const fixtureAdapter = "hookartifact-fixture"

// foreignFixtureBinary is the irrlichd a differently-situated daemon would
// have named — deliberately nonexistent, the same pattern pi's and
// opencode's own hookport_test.go use for the identical reason: an absolute
// path that has stopped existing is exactly the drift beacon delivery
// admits, and it needs no second executable to arrange.
const foreignFixtureBinary = "/nonexistent-hookartifact-1764/bin/irrlichd"

// fixtureRender stands in for a real adapter's renderExtension/renderPlugin:
// it embeds the beacon command verbatim, which is enough to carry
// hookbeacon.Sentinel(fixtureAdapter) — the same substring isOurs looks for
// in a real installed extension.js/plugin.js — without needing either
// adapter's actual JS template or placeholder-substitution machinery, which
// stays adapter-local (see the package doc).
func fixtureRender(command string) ([]byte, error) {
	if command == "" {
		return nil, errors.New("hookartifact fixture: refusing to render an empty beacon command")
	}
	return []byte("// fixture artifact — DO NOT EDIT\nconst BEACON = " + strconv.Quote(command) + ";\n"), nil
}

// fixtureInstaller builds an Installer against a fresh temp directory.
// Sentinel/ResolveCommand are computed the same way a real adapter's
// hookinstaller.go computes them — hookbeacon stays a TEST-only dependency
// here, never a production import of this package (see the package doc).
func fixtureInstaller(t *testing.T) (in Installer, path string) {
	t.Helper()
	dir := t.TempDir()
	path = filepath.Join(dir, "artifact.js")
	return Installer{
		AdapterName: fixtureAdapter,
		Sentinel:    hookbeacon.Sentinel(fixtureAdapter),
		ResolveCommand: func() (string, error) {
			return hookbeacon.InstalledCommand(fixtureAdapter)
		},
		Path:   func() (string, error) { return path, nil },
		Render: fixtureRender,
		Events: []string{"turn_end"},
	}, path
}

// TestEnsureInstalled_CreatesArtifactAndIsIdempotent carries forward pi's
// TestEnsureInstalled_CreatesTheExtensionAndIsIdempotent and opencode's
// TestEnsureHooksInstalled_IsIdempotent: the base case (a fresh install
// writes a file carrying the sentinel) and the idempotency property (a
// second call changes nothing — the permission service re-runs Apply on
// every daemon start, so a non-idempotent installer would rewrite the
// user's file forever).
func TestEnsureInstalled_CreatesArtifactAndIsIdempotent(t *testing.T) {
	in, path := fixtureInstaller(t)

	modified, err := in.EnsureInstalled()
	if err != nil {
		t.Fatalf("EnsureInstalled: %v", err)
	}
	if !modified {
		t.Error("first install reported no modification")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the installed artifact: %v", err)
	}
	if !in.IsOurs(data) {
		t.Error("the installed file does not carry the beacon sentinel")
	}

	modified, err = in.EnsureInstalled()
	if err != nil {
		t.Fatalf("second EnsureInstalled: %v", err)
	}
	if modified {
		t.Error("second install reported a modification; the install is not idempotent")
	}
}

// TestEnsureInstalled_RefusesToOverwriteAForeignFile carries forward pi's
// TestEnsureInstalled_RefusesToOverwriteAFileItDidNotWrite and opencode's
// TestEnsureHooksInstalled_RefusesToOverwriteAForeignFile, including the
// latter's assertion that the refusal names the file — the wizard surfaces
// that string verbatim as "granted but NOT applied" (#1362).
//
// This is the guard on the one genuinely destructive thing an install of
// this shape could do on the WRITE side: the path is a name irrlicht chose
// inside a directory the user owns, and overwriting whatever is already
// there for the sake of monitoring is never the right trade.
func TestEnsureInstalled_RefusesToOverwriteAForeignFile(t *testing.T) {
	in, path := fixtureInstaller(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	const theirs = "export default function () { /* someone else's file */ }\n"
	if err := os.WriteFile(path, []byte(theirs), 0o600); err != nil {
		t.Fatal(err)
	}

	modified, err := in.EnsureInstalled()
	if err == nil {
		t.Error("EnsureInstalled overwrote a foreign file without complaint")
	}
	if modified {
		t.Error("EnsureInstalled reported a modification while refusing")
	}
	if err != nil && !strings.Contains(err.Error(), path) {
		t.Errorf("the refusal %q does not name the file — the wizard surfaces this string as \"granted but NOT applied\" (#1362)", err)
	}
	got, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != theirs {
		t.Error("the foreign file was modified")
	}
}

// TestUninstall_RemovesOursAndLeavesForeignAlone carries forward pi's
// TestUninstall_RemovesOursAndLeavesForeignAlone and opencode's
// TestUninstallHooks_LeavesAForeignFileAlone — the "foreign-file clobber"
// case issue #1764 names explicitly as opencode's committed mutation for
// exactly this hazard. Getting this wrong in the permissive direction means
// uninstall deletes a user's own file, which is the single worst thing this
// package could do.
func TestUninstall_RemovesOursAndLeavesForeignAlone(t *testing.T) {
	in, path := fixtureInstaller(t)
	if _, err := in.EnsureInstalled(); err != nil {
		t.Fatalf("EnsureInstalled: %v", err)
	}

	modified, err := in.Uninstall()
	if err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if !modified {
		t.Error("uninstall of an installed artifact reported no modification")
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Errorf("the artifact survived uninstall (stat err = %v)", statErr)
	}

	// A second uninstall is a no-op, not an error (opencode's
	// TestUninstallHooks_OnNothingIsQuiet covers the "never installed" leg
	// of the same rule separately, below).
	if modified, err = in.Uninstall(); err != nil || modified {
		t.Errorf("second uninstall = (%v, %v), want (false, nil)", modified, err)
	}

	// And a foreign file written to our path afterward survives Uninstall
	// byte-for-byte — the clobber case.
	const theirs = "export default function () {}\n"
	if err := os.WriteFile(path, []byte(theirs), 0o600); err != nil {
		t.Fatal(err)
	}
	if modified, err = in.Uninstall(); err != nil || modified {
		t.Errorf("uninstall over a foreign file = (%v, %v), want (false, nil)", modified, err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != theirs {
		t.Error("uninstall removed or altered a file it did not write")
	}
}

// TestUninstall_OnNothingIsQuiet carries forward opencode's
// TestUninstallHooks_OnNothingIsQuiet: PermissionService runs Remove
// whenever the answer is "denied", including the first time the wizard is
// ever answered, so a revoke with nothing installed must not be an error.
func TestUninstall_OnNothingIsQuiet(t *testing.T) {
	in, _ := fixtureInstaller(t)
	modified, err := in.Uninstall()
	if err != nil {
		t.Fatalf("Uninstall with nothing installed: %v", err)
	}
	if modified {
		t.Error("Uninstall reported a change with nothing installed")
	}
}

// TestUninstall_LeavesParentDirectoryBehind carries forward opencode's
// TestUninstallHooks_LeavesThePluginDirectoryBehind: an empty parent
// directory is an ordinary state for the upstream agent, and removing a
// directory a user's editor or shell may have open is a worse surprise than
// leaving one behind.
func TestUninstall_LeavesParentDirectoryBehind(t *testing.T) {
	in, path := fixtureInstaller(t)
	if _, err := in.EnsureInstalled(); err != nil {
		t.Fatal(err)
	}
	if _, err := in.Uninstall(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("the artifact survived uninstall: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(path)); err != nil {
		t.Errorf("the parent directory was removed too: %v", err)
	}
}

// TestVerifyInstalled_ReportsTheThreeStates carries forward opencode's
// TestVerifyHooksInstalled_ReportsTheThreeStates: Missing (nothing there, or
// a foreign file at our path — Verify must not report a foreign file as
// "Stale", since Stale promises EnsureInstalled would rewrite it in place,
// which it deliberately will not), Stale (ours, but naming a different
// irrlichd — the moved-binary case), and Intact (current). The companion
// property pi's TestVerifyAgreesWithEnsureInstalled pins — that these two
// readers of the same file never disagree — is
// TestVerifyInstalled_AgreesWithEnsureInstalled, below.
func TestVerifyInstalled_ReportsTheThreeStates(t *testing.T) {
	for _, tc := range []struct {
		name string
		seed func(t *testing.T, in Installer, path string)
		want string // "missing", "stale", or "intact"
	}{
		{
			name: "nothing_installed",
			seed: func(*testing.T, Installer, string) {},
			want: "missing",
		},
		{
			name: "fresh_install",
			seed: func(t *testing.T, in Installer, _ string) {
				if _, err := in.EnsureInstalled(); err != nil {
					t.Fatal(err)
				}
			},
			want: "intact",
		},
		{
			// Ours, but written for a daemon that lived somewhere else.
			name: "ours_but_naming_another_binary",
			seed: func(t *testing.T, in Installer, _ string) {
				foreignCommand, err := hookbeacon.Command(foreignFixtureBinary, fixtureAdapter)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := in.EnsureInstalledWithCommand(foreignCommand); err != nil {
					t.Fatal(err)
				}
			},
			want: "stale",
		},
		{
			// Not ours at all. Missing rather than Stale: Stale promises
			// EnsureInstalled would rewrite it in place, and it deliberately
			// will not.
			name: "foreign_file",
			seed: func(t *testing.T, in Installer, path string) {
				if err := os.WriteFile(path, []byte("export default function () {}\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			want: "missing",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in, path := fixtureInstaller(t)
			tc.seed(t, in, path)

			status, err := in.VerifyInstalled()
			if err != nil {
				t.Fatalf("VerifyInstalled: %v", err)
			}
			switch tc.want {
			case "intact":
				if !status.Intact() {
					t.Errorf("status = %+v, want intact", status)
				}
			case "stale":
				if len(status.Stale) != len(in.Events) {
					t.Errorf("status = %+v, want all %d events Stale", status, len(in.Events))
				}
			case "missing":
				if len(status.Missing) != len(in.Events) {
					t.Errorf("status = %+v, want all %d events Missing", status, len(in.Events))
				}
			}
		})
	}
}

// TestVerifyInstalled_AgreesWithEnsureInstalled carries forward pi's
// TestVerifyAgreesWithEnsureInstalled as a property rather than as three
// hand-picked cases: for every state the file can be in, VerifyInstalled
// reports work outstanding exactly when EnsureInstalled would do some.
//
// It exists because those are two separate readers of the same file (before
// this package's own inspect helper unified them, they were two independent
// implementations), and the failure mode of a disagreement is invisible in
// both directions — a verifier that under-reports leaves a dead channel
// green forever, and one that over-reports makes the repair timer rewrite a
// correct file on every tick. TestVerifyInstalled_ReportsTheThreeStates
// above pins the three states by name; this pins that Verify and Ensure can
// never read one of them differently.
func TestVerifyInstalled_AgreesWithEnsureInstalled(t *testing.T) {
	foreignCommand, err := hookbeacon.Command(foreignFixtureBinary, fixtureAdapter)
	if err != nil {
		t.Fatal(err)
	}
	stale, err := fixtureRender(foreignCommand)
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name            string
		seed            func(t *testing.T, in Installer, path string)
		wantOutstanding bool
	}{
		{name: "absent", seed: func(*testing.T, Installer, string) {}, wantOutstanding: true},
		{
			name: "ours_but_naming_another_binary",
			seed: func(t *testing.T, in Installer, path string) {
				if err := os.WriteFile(path, stale, 0o600); err != nil {
					t.Fatal(err)
				}
			},
			wantOutstanding: true,
		},
		{
			name: "current",
			seed: func(t *testing.T, in Installer, _ string) {
				if _, err := in.EnsureInstalled(); err != nil {
					t.Fatal(err)
				}
			},
			wantOutstanding: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in, path := fixtureInstaller(t)
			tc.seed(t, in, path)

			status, err := in.VerifyInstalled()
			if err != nil {
				t.Fatalf("VerifyInstalled: %v", err)
			}
			reported := len(status.Missing) > 0 || len(status.Stale) > 0
			if reported != tc.wantOutstanding {
				t.Errorf("verify reported outstanding=%v (missing=%v stale=%v), want %v",
					reported, status.Missing, status.Stale, tc.wantOutstanding)
			}

			modified, err := in.EnsureInstalled()
			if err != nil {
				t.Fatalf("EnsureInstalled: %v", err)
			}
			if modified != reported {
				t.Errorf("verify says outstanding=%v but ensure modified=%v — the two "+
					"readers of the same file disagree", reported, modified)
			}
		})
	}
}

// TestVerifyInstalled_ForeignFileIsOutstandingButEnsureRefuses is the fourth
// state pi's original property test predates and never covered: a foreign
// file. There "outstanding" and "modified" cannot be compared as two bools
// the way TestVerifyInstalled_AgreesWithEnsureInstalled's other three states
// can — Ensure legitimately REFUSES (an error, not a silent no-op) rather
// than writing, which is itself the correct way of not reconciling what
// Verify reports outstanding (see EnsureInstalledWithCommand's
// installForeign case). Split into its own test, rather than a branch
// inside the table above, so neither test carries the other's shape.
//
// It is included because the extraction is exactly what makes checking this
// state cheap now — a foreign file is the highest-stakes state #1764's own
// text names.
func TestVerifyInstalled_ForeignFileIsOutstandingButEnsureRefuses(t *testing.T) {
	in, path := fixtureInstaller(t)
	if err := os.WriteFile(path, []byte("export default function () {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	status, err := in.VerifyInstalled()
	if err != nil {
		t.Fatalf("VerifyInstalled: %v", err)
	}
	if status.Intact() {
		t.Error("verify reported a foreign file as intact")
	}

	modified, err := in.EnsureInstalled()
	if err == nil {
		t.Error("EnsureInstalled succeeded against a foreign file; want a refusal")
	}
	if modified {
		t.Error("EnsureInstalled reported a modification while refusing")
	}
}

// TestVerifyInstalled_DoesNotWrite carries forward pi's
// TestVerify_DoesNotWrite: the verifier is read-only by construction
// (#1372) — it runs on a timer against a granted install, and a verifier
// that materialized what it was checking for would report health it had
// just created.
func TestVerifyInstalled_DoesNotWrite(t *testing.T) {
	in, path := fixtureInstaller(t)
	if _, err := in.VerifyInstalled(); err != nil {
		t.Fatalf("VerifyInstalled: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("verify created the artifact it was asked to check (stat err = %v)", err)
	}
}
