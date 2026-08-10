package main

import (
	"bytes"
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestUnknownFlagMessageNamesTheFlag pins scope item 2 of #1417: an unknown flag
// must fail "with a message naming it". A bare usage dump would satisfy the exit
// code and none of the point — the whole defect was a typo that produced no
// evidence of itself.
func TestUnknownFlagMessageNamesTheFlag(t *testing.T) {
	msg := unknownFlagMessage("--recrod")
	if !strings.Contains(msg, "--recrod") {
		t.Errorf("unknownFlagMessage does not name the offending flag: %q", msg)
	}
	// It also has to show the right spelling, or the user learns only that they
	// were wrong.
	if !strings.Contains(msg, "--record") {
		t.Errorf("unknownFlagMessage does not list the known flags: %q", msg)
	}
}

// TestKnownFlagsCoversEveryFlagTheDaemonReads is the drift tripwire for the
// allow-list, and it guards the one direction that is a silent feature outage
// rather than a loud one.
//
// knownFlags is now checked BEFORE dispatch, so a flag added to the daemon and
// read by a bare hasFlag() call — the shape --record uses, far from selectAction
// at the bottom of runDaemon — but never added to knownFlags would be rejected
// with exit 2 before its reader ever ran. The new feature would simply not
// exist, and its author would have no reason to look at this file. So the test
// scans the source for the flags the daemon actually reads rather than trusting
// a second hand-kept list, the same way TestAllHookEvents_CoversEveryConstant
// does for hook events.
//
// It parses the WHOLE PACKAGE, not main.go, and that is the point rather than
// thoroughness for its own sake: hasFlag is an unexported package-level
// function, so any of these files can call it — startup.go alone is over a
// thousand lines and holds the phases runDaemon() calls. A single-file scan
// leaves exactly the hole the test claims to close, and it fails OPEN. Reading
// the AST rather than matching text closes the second half of the same hole: a
// regexp has to hard-code the argument spelling, so hasFlagIn(argv, "--x")
// would slip past a pattern written for hasFlagIn(args, "--x"). Same technique,
// and for the same reason, as TestPermissionWizardIsBuiltFromConsentCatalog.
func TestKnownFlagsCoversEveryFlagTheDaemonReads(t *testing.T) {
	read := flagsTheDaemonReads(t)
	if len(read) == 0 {
		t.Fatal("parsed the package and found no hasFlag call with a literal flag at all; the scanner has drifted from the source, so this test is passing vacuously")
	}
	for _, name := range read {
		if !hasFlagIn(knownFlags, name) {
			t.Errorf("the daemon reads flag %q but knownFlags does not list it: selectAction will reject it with exit 2 before its reader ever runs", name)
		}
	}
}

// flagsTheDaemonReads parses every non-test file in this package and returns
// the flag names passed as literals to hasFlag / hasFlagIn.
func flagsTheDaemonReads(t *testing.T) []string {
	t.Helper()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse package source: %v", err)
	}

	var read []string
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			ast.Inspect(file, func(n ast.Node) bool {
				read = append(read, literalFlagArgs(n)...)
				return true
			})
		}
	}
	return read
}

// literalFlagArgs returns the string literals passed to a hasFlag / hasFlagIn
// call, and nil for any other node.
//
// Only literals count, which is what keeps firstUnknownFlag's own
// hasFlagIn(knownFlags, arg) out of the result: that call is the lookup, not a
// read, and it passes no flag name.
func literalFlagArgs(n ast.Node) []string {
	call, ok := n.(*ast.CallExpr)
	if !ok {
		return nil
	}
	fn, ok := call.Fun.(*ast.Ident)
	if !ok || (fn.Name != "hasFlag" && fn.Name != "hasFlagIn") {
		return nil
	}
	var names []string
	for _, arg := range call.Args {
		lit, ok := arg.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			continue
		}
		if name, err := strconv.Unquote(lit.Value); err == nil {
			names = append(names, name)
		}
	}
	return names
}

// runIrrlichd runs the real binary with args under a timeout and returns its
// exit code, stdout and stderr.
//
// Two things keep a regression from hanging this test, and they are layered
// deliberately, because the defect being fixed is "an unknown flag starts a
// daemon" and a daemon does not exit. The dead bind address below is the
// first: a regression that reaches runDaemon fails to listen and exits 1, so
// the assertion goes red in the ordinary way. The timeout is the backstop for
// a future daemon that survives a failed bind — the same trap #1357 and #1416
// had to put watchdogs around in the shell.
func runIrrlichd(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	bin := buildIrrlichd(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, args...)
	// sanitizedChildEnv, not append(os.Environ(), …): #1429 found that a
	// subprocess test inheriting IRRLICHT_BIND_ADDR / IRRLICHT_PERMISSION_MODE
	// from the developer's machine can reach the REAL daemon on port 7837. The
	// beacon case below actually POSTs, so that is not hypothetical here.
	cmd.Env = append(sanitizedChildEnv(t.TempDir(), t.TempDir()),
		// A port nothing can bind, so the beacon has no daemon to reach and a
		// regression that does get to runDaemon fails fast rather than
		// competing with the developer's real one.
		"IRRLICHT_BIND_ADDR=127.0.0.1:1",
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if ctx.Err() != nil {
		t.Fatalf("irrlichd %q did not exit within the timeout; it most likely started a daemon", args)
	}
	code := 0
	if err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			t.Fatalf("run irrlichd %q: %v", args, err)
		}
		code = ee.ExitCode()
	}
	return code, stdout.String(), stderr.String()
}

// TestUnknownFlagExitsTwoWithoutStartingADaemon is the process-level half of
// #1417 — selectAction being a pure function is only half the claim, since the
// user's complaint is about what the binary does.
func TestUnknownFlagExitsTwoWithoutStartingADaemon(t *testing.T) {
	code, stdout, stderr := runIrrlichd(t, "--recrod")

	if code != 2 {
		t.Errorf("exit code = %d, want 2 (stderr: %q)", code, stderr)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty: a non-empty stdout on a non-zero exit can read as a deny decision to a fail-closed hook", stdout)
	}
	if !strings.Contains(stderr, "--recrod") {
		t.Errorf("stderr does not name the offending flag: %q", stderr)
	}
}

// TestVersionStillWorksAsTheBeaconGuardToken is a LOCK, not a defect test: it
// passes on main by construction and exists so the allow-list cannot quietly
// break the two shapes #1373's beacon depends on.
//
// Both forms are real installed command lines, not hypotheticals. `--version`
// alone is what promote-recording.sh, CONTRIBUTING.md and the bug-report
// template run; `--version hook-post <adapter>` is what hookbeacon.Command
// writes into an agent's config, where the guard LEADS because releases
// v0.2.0–v0.3.2 dispatch on os.Args[1] only.
func TestVersionStillWorksAsTheBeaconGuardToken(t *testing.T) {
	t.Run("--version alone prints the banner", func(t *testing.T) {
		code, stdout, stderr := runIrrlichd(t, "--version")
		if code != 0 {
			t.Errorf("exit code = %d, want 0 (stderr: %q)", code, stderr)
		}
		if !strings.Contains(stdout, "irrlichd version") {
			t.Errorf("stdout = %q, want the version banner", stdout)
		}
	})

	t.Run("--version before the verb still selects the beacon", func(t *testing.T) {
		// Exit 0 with an empty stdout is the beacon's contract (#1373): the
		// version branch would have printed a banner here, which is precisely
		// the silent failure the leading guard exists to avoid.
		code, stdout, _ := runIrrlichd(t, "--version", "hook-post", "gemini-cli")
		if code != 0 {
			t.Errorf("exit code = %d, want 0", code)
		}
		if stdout != "" {
			t.Errorf("stdout = %q, want empty; the version branch won over the beacon", stdout)
		}
	})
}
