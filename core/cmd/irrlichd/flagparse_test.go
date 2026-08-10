package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"regexp"
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
// scans main.go's source for the flags the daemon actually reads rather than
// trusting a second hand-kept list, the same way
// TestAllHookEvents_CoversEveryConstant does for hook events.
func TestKnownFlagsCoversEveryFlagTheDaemonReads(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	// Matches hasFlag("--x") and hasFlagIn(args, "--x"). The one hasFlagIn call
	// with a non-literal list — firstUnknownFlag's hasFlagIn(knownFlags, arg) —
	// deliberately does not match: it is the lookup, not a read.
	re := regexp.MustCompile(`hasFlag(?:In)?\((?:args, )?"([^"]*)"\)`)
	matches := re.FindAllStringSubmatch(string(src), -1)
	if len(matches) == 0 {
		t.Fatal("scanned main.go and found no hasFlag call at all; the scanner has drifted from the source, so this test is passing vacuously")
	}
	for _, m := range matches {
		if !hasFlagIn(knownFlags, m[1]) {
			t.Errorf("main.go reads flag %q but knownFlags does not list it: selectAction will reject it with exit 2 before its reader ever runs", m[1])
		}
	}
}

// runIrrlichd runs the real binary with args under a timeout and returns its
// exit code, stdout and stderr.
//
// The timeout is load-bearing rather than hygiene. The defect being fixed is
// "an unknown flag starts a daemon", and a daemon does not exit — so on a
// regression this test HANGS rather than failing, which is the same trap #1357
// and #1416 had to put watchdogs around in the shell. Bounding it here turns
// that back into an ordinary red.
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
