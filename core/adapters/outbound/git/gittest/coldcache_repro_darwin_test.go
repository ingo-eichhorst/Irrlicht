//go:build darwin

package gittest

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// coldcache_repro_darwin_test.go is #2047's red-first evidence, runnable on
// demand:
//
//	IRRLICHT_REPRO_COLD_XCRUN=1 go test ./core/adapters/outbound/git/gittest/ \
//	    -run TestColdXcrunCacheRepro -v -timeout 20m
//
// It rebuilds, on this machine, the conditions #2047 infers for the macOS CI
// runner, then runs the git and filesystem adapter test binaries twice under
// them: once with the primer disabled (SkipPrimeEnv), which must go RED with
// only the known timeout victims failing, and once with it enabled, which must
// go GREEN. Either half coming out the other way fails the test, and so does
// any step it could not perform — a repro that cannot delete the cache or
// cannot confirm the stub is in play has not reproduced anything.
//
// It is opt-in because it deliberately saturates every core for a minute or
// two and deletes a per-user cache. The cache rebuilds on the next xcrun call.
//
// Set IRRLICHT_REPRO_COLD_XCRUN_OUT to a directory to keep both runs' output.

const reproEnv = "IRRLICHT_REPRO_COLD_XCRUN"

// knownTimeoutVictims are the tests #2047 saw fail at the 5s ceiling: the first
// two in CI and in the local repro, the third once in CI (2026-09-15). A red
// half that fails anything else is red for a reason this repro does not model.
var knownTimeoutVictims = map[string]bool{
	"TestGetGitRoot_DeletedSubdir":                     true,
	"TestGetGitRoot_NotARepo":                          true,
	"TestConcurrencyProject_WorktreeFoldsIntoRealRepo": true,
}

func TestColdXcrunCacheRepro(t *testing.T) {
	if os.Getenv(reproEnv) == "" {
		t.Skipf("opt-in: set %s=1 (saturates every core and deletes the xcrun cache)", reproEnv)
	}

	realGit := xcrunFindGit(t)
	if realGit == binary {
		t.Fatalf("the adapter's git %q IS the real git, not the xcrun stub — nothing here can reproduce #2047", binary)
	}
	cache := xcrunCachePath(t)

	// Mimic the runner: a git that is NOT the stub first on PATH, so the
	// tests' own `git init` never warms the cache on the adapter's behalf.
	pathDir := t.TempDir()
	if err := os.Symlink(realGit, filepath.Join(pathDir, "git")); err != nil {
		t.Fatalf("symlink real git onto PATH: %v", err)
	}
	childPath := pathDir + string(os.PathListSeparator) + os.Getenv("PATH")

	bins := buildReproBinaries(t)
	outDir := os.Getenv(reproEnv + "_OUT")

	t.Run("primer disabled goes red", func(t *testing.T) {
		results := runCold(t, cache, childPath, bins, true)
		keep(t, outDir, "red", results)
		var failed []string
		for _, r := range results {
			failed = append(failed, r.failedTests...)
		}
		if len(failed) == 0 {
			t.Fatal("both packages passed with a cold cache and no primer — the repro did not reproduce, so the primer's green proves nothing")
		}
		for _, name := range failed {
			if !knownTimeoutVictims[name] {
				t.Errorf("%s failed, which is not one of #2047's timeout victims — red for another reason", name)
			}
		}
		if !t.Failed() {
			t.Logf("RED as expected; failed: %s", strings.Join(failed, ", "))
		}
	})

	t.Run("primer enabled goes green", func(t *testing.T) {
		results := runCold(t, cache, childPath, bins, false)
		keep(t, outDir, "green", results)
		for _, r := range results {
			if r.exitErr != nil {
				t.Errorf("%s failed with the primer on (%v); failed tests: %v", r.name, r.exitErr, r.failedTests)
			}
			if !strings.Contains(r.output, "gittest: primed ") {
				t.Errorf("%s never printed the primer line — the primer did not run", r.name)
			}
		}
	})
}

type reproBinary struct{ name, path, dir string }

type reproResult struct {
	name        string
	output      string
	exitErr     error
	failedTests []string
}

func xcrunFindGit(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("/usr/bin/xcrun", "--find", "git").Output()
	if err != nil {
		t.Fatalf("xcrun --find git: %v", err)
	}
	p := strings.TrimSpace(string(out))
	if p == "" {
		t.Fatal("xcrun --find git printed nothing")
	}
	return p
}

func xcrunCachePath(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("/usr/bin/getconf", "DARWIN_USER_TEMP_DIR").Output()
	if err != nil {
		t.Fatalf("getconf DARWIN_USER_TEMP_DIR: %v", err)
	}
	dir := strings.TrimSpace(string(out))
	if dir == "" {
		t.Fatal("getconf DARWIN_USER_TEMP_DIR printed nothing")
	}
	return filepath.Join(dir, "xcrun_db")
}

// buildReproBinaries compiles the two packages that failed in CI, once, before
// any cache is deleted, so the build itself cannot be what warms or cools it.
func buildReproBinaries(t *testing.T) []reproBinary {
	t.Helper()
	dir := t.TempDir()
	var bins []reproBinary
	for _, pkg := range []string{"irrlicht/core/adapters/outbound/git", "irrlicht/core/adapters/outbound/filesystem"} {
		src, err := exec.Command("go", "list", "-f", "{{.Dir}}", pkg).Output()
		if err != nil || strings.TrimSpace(string(src)) == "" {
			t.Fatalf("go list %s: %v", pkg, err)
		}
		b := reproBinary{name: pkg, path: filepath.Join(dir, filepath.Base(pkg)+".test"), dir: strings.TrimSpace(string(src))}
		if out, err := exec.Command("go", "test", "-c", "-race", "-o", b.path, pkg).CombinedOutput(); err != nil {
			t.Fatalf("build %s: %v\n%s", pkg, err, out)
		}
		bins = append(bins, b)
	}
	return bins
}

// runCold saturates every core, deletes the xcrun cache, and runs the binaries
// concurrently. The stress is separate `sh` busy loops rather than goroutines:
// goroutines are capped at GOMAXPROCS threads and leave the children a larger
// share of the CPU. Every loop is killed and reaped before this returns.
func runCold(t *testing.T, cache, childPath string, bins []reproBinary, skipPrimer bool) []reproResult {
	t.Helper()
	stopStress := startStress(t, stressFactor(t)*runtime.NumCPU())
	defer stopStress()

	if err := os.Remove(cache); err != nil && !os.IsNotExist(err) {
		t.Fatalf("could not delete the xcrun cache %s: %v", cache, err)
	}
	if _, err := os.Stat(cache); !os.IsNotExist(err) {
		t.Fatalf("the xcrun cache %s still exists after deletion (stat err: %v)", cache, err)
	}

	env := append(os.Environ(), "PATH="+childPath)
	if skipPrimer {
		env = append(env, SkipPrimeEnv+"=1")
	}
	results := make([]reproResult, len(bins))
	var runWG sync.WaitGroup
	for i, b := range bins {
		runWG.Add(1)
		go func(i int, b reproBinary) {
			defer runWG.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			cmd := exec.CommandContext(ctx, b.path, "-test.count=1", "-test.v")
			cmd.Dir = b.dir // several tests read their package's own source
			cmd.Env = env
			var buf bytes.Buffer
			cmd.Stdout, cmd.Stderr = &buf, &buf
			err := cmd.Run()
			results[i] = reproResult{name: b.name, output: buf.String(), exitErr: err, failedTests: topLevelFailures(buf.String())}
		}(i, b)
	}
	runWG.Wait()
	return results
}

// stressFactor is how many busy loops run per CPU, from
// IRRLICHT_REPRO_COLD_XCRUN_STRESS (default 4). #2047's first local repro used
// 2 and measured the cold stub at 5.00s, right at the ceiling; a rerun of this
// fixture at 2 on the same 10-core machine measured the victims at 4.43s and
// 4.54s and passed, so the margin at 2 is too thin to be the default.
func stressFactor(t *testing.T) int {
	t.Helper()
	v := os.Getenv(reproEnv + "_STRESS")
	if v == "" {
		return 4
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		t.Fatalf("%s_STRESS=%q is not a positive integer", reproEnv, v)
	}
	return n
}

// startStress starts n busy loops, each in its own process group, confirms each
// one is alive, and returns the function that kills and reaps them all.
func startStress(t *testing.T, n int) func() {
	t.Helper()
	var loops []*exec.Cmd
	stop := func() {
		for _, c := range loops {
			_ = syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
			_ = c.Wait()
		}
	}
	for i := 0; i < n; i++ {
		c := exec.Command("/bin/sh", "-c", "while :; do :; done")
		c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := c.Start(); err != nil {
			stop()
			t.Fatalf("busy loop %d of %d did not start: %v", i+1, n, err)
		}
		loops = append(loops, c)
	}
	for i, c := range loops {
		if err := c.Process.Signal(syscall.Signal(0)); err != nil {
			stop()
			t.Fatalf("busy loop %d of %d is not running: %v", i+1, n, err)
		}
	}
	return stop
}

var failLine = regexp.MustCompile(`(?m)^--- FAIL: (\S+)`)

func topLevelFailures(out string) []string {
	var names []string
	for _, m := range failLine.FindAllStringSubmatch(out, -1) {
		names = append(names, m[1])
	}
	sort.Strings(names)
	return names
}

func keep(t *testing.T, dir, label string, results []reproResult) {
	t.Helper()
	for _, r := range results {
		for _, line := range strings.Split(r.output, "\n") {
			if strings.HasPrefix(line, "--- FAIL") || strings.HasPrefix(line, "gittest:") || strings.Contains(line, "did not answer") || strings.Contains(line, "want session folded") {
				t.Logf("[%s %s] %s", label, filepath.Base(r.name), line)
			}
		}
		if dir == "" {
			continue
		}
		p := filepath.Join(dir, "coldxcrun-"+label+"-"+filepath.Base(r.name)+".out")
		if err := os.WriteFile(p, []byte(r.output), 0o644); err != nil {
			t.Errorf("write %s: %v", p, err)
		}
	}
}
