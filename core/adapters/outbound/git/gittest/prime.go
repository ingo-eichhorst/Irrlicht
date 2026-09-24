// Package gittest warms the git binary the outbound git adapter runs, for the
// test packages that drive the real adapter (#2047).
//
// Why a test needs this. pathutil.MustResolve("git") resolves to /usr/bin/git
// on macOS, and that is Apple's xcrun stub rather than git itself: it looks
// the real git up through xcrun's lookup cache (xcrun_db under
// $(getconf DARWIN_USER_TEMP_DIR)). #2047 measured the stub locally at 0.01s
// with a warm cache and 5.00s cold under CPU stress — exactly the adapter's
// per-call gitTimeout — and reproduced both CI failures that way
// (TestGetGitRoot_DeletedSubdir, TestConcurrencyProject_WorktreeFoldsIntoRealRepo)
// with the cache deleted and a non-stub git first on PATH. That the cache is
// cold at every CI job start is ASSUMED, not measured: the runner installs
// Homebrew git, so neither checkout nor the tests' own `git init` would go
// through the stub.
//
// PrimeGit runs `<that binary> --version` once, under a deadline far above the
// adapter's, so the first adapter call in the package no longer pays for the
// lookup. It changes nothing in production: gitTimeout and pathutil's
// trustedDirs are untouched.
package gittest

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"irrlicht/core/pkg/pathutil"
	"irrlicht/core/pkg/shellout"
)

// PrimeTimeout bounds the one warm-up call. The slowest cold prime measured so
// far is 9.4s, by TestColdXcrunCacheRepro's green half at its default stress
// (4 busy loops per CPU, 10-core machine); 60s leaves six times that. A primer
// that needs more is reported as a failure rather than waited out.
const PrimeTimeout = 60 * time.Second

// SkipPrimeEnv disables the primer. It exists for the cold-cache
// reproduction (coldcache_repro_darwin_test.go), which must run the packages
// WITHOUT it to show the failure the primer prevents. A skipped primer says so
// on stderr.
const SkipPrimeEnv = "IRRLICHT_SKIP_GIT_PRIME"

// binary is resolved the same way the adapter resolves its own package var
// gitPath (adapter.go: `var gitPath = pathutil.MustResolve("git")`).
// TestPrimerWarmsTheAdaptersBinary in package git pins the two as equal, so a
// change on either side is a test failure rather than a primer that warms the
// wrong file.
var binary = pathutil.MustResolve("git")

// Binary returns the path PrimeGit runs.
func Binary() string { return binary }

// PrimeGit runs Binary() --version once under PrimeTimeout and returns how long
// it took. The error carries the elapsed time and git's output.
func PrimeGit() (time.Duration, error) {
	return prime(binary, PrimeTimeout)
}

func prime(bin string, timeout time.Duration) (time.Duration, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	start := time.Now()
	cmd := exec.CommandContext(ctx, bin, "--version")
	// Without it, a child that inherits the output pipe keeps CombinedOutput
	// blocked after the kill: TestPrimeIsBoundedWhenAChildHoldsThePipe waited
	// 30.05s for a 200ms deadline before this line existed.
	cmd.WaitDelay = shellout.WaitDelay
	out, err := cmd.CombinedOutput()
	elapsed := time.Since(start)
	if ctx.Err() != nil {
		return elapsed, fmt.Errorf("gittest: %s --version did not answer within %s (killed after %s)", bin, timeout, elapsed.Round(time.Millisecond))
	}
	if err != nil {
		msg := fmt.Sprintf("gittest: %s --version failed after %s: %v", bin, elapsed.Round(time.Millisecond), err)
		if o := strings.TrimSpace(string(out)); o != "" {
			msg += ": " + o
		}
		return elapsed, errors.New(msg)
	}
	return elapsed, nil
}

// PrimeOrExit is the call a TestMain makes before m.Run(). It exits the test
// binary with status 2 when the primer fails, so a package whose git cannot be
// warmed fails loudly by name instead of skipping into the flake #2047 is
// about. The elapsed time is printed either way (visible under -v, and on any
// failing package), which is how a slow runner shows up in CI logs.
func PrimeOrExit() {
	if os.Getenv(SkipPrimeEnv) != "" {
		fmt.Fprintf(os.Stderr, "gittest: primer SKIPPED by %s — this run is the cold-cache reproduction, not a gate\n", SkipPrimeEnv)
		return
	}
	elapsed, err := PrimeGit()
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(2)
	}
	fmt.Fprintf(os.Stderr, "gittest: primed %s in %s\n", binary, elapsed.Round(time.Millisecond))
}
