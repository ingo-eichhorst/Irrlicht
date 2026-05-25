//go:build darwin

package processlifecycle

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"irrlicht/core/ports/outbound"
)

// darwinObserver implements outbound.ProcessObserver with the macOS userland:
// pgrep for process discovery, lsof for cwd / open-file ownership, and
// KERN_PROCARGS2 sysctl (via readProcessEnv) for env. These are bounded
// shell-outs with a 2-second ceiling — the same primitives this package has
// always used; this type just gathers them behind the port.
type darwinObserver struct{}

func newObserver() outbound.ProcessObserver { return darwinObserver{} }

// FindByName returns PIDs whose executable name exactly matches name
// (pgrep -x).
func (darwinObserver) FindByName(name string) ([]int, error) {
	if name == "" {
		return nil, nil
	}
	return runPgrep("-x", name)
}

// FindByCmdline returns PIDs whose full command line matches the regex
// pattern (pgrep -f). Used for agents whose process name on disk doesn't
// match their CLI name — e.g. Python tools launched via a wrapper, where the
// OS process is `python` and the agent script is in argv[1]. pgrep interprets
// the pattern as extended regex on macOS. The daemon's own PID is filtered so
// a pattern that matches pgrep's argv can't match the daemon itself.
func (darwinObserver) FindByCmdline(pattern string) ([]int, error) {
	if pattern == "" {
		return nil, nil
	}
	ownPID := os.Getpid()
	pids, err := runPgrep("-f", pattern)
	if err != nil {
		return nil, err
	}
	out := pids[:0]
	for _, p := range pids {
		if p == ownPID {
			continue
		}
		out = append(out, p)
	}
	return out, nil
}

// CWDOf returns the working directory of pid via `lsof -d cwd`.
func (darwinObserver) CWDOf(pid int) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "lsof", "-a", "-p", strconv.Itoa(pid), "-d", "cwd", "-Fn").Output()
	if err != nil {
		return "", fmt.Errorf("lsof cwd pid %d: %w", pid, err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "n") {
			return strings.TrimPrefix(line, "n"), nil
		}
	}
	return "", fmt.Errorf("cwd not found for pid %d", pid)
}

// WriterOf returns the PID that has path open for writing, via `lsof <path>`.
// Used for agents (Codex, Pi) that keep their transcript open for the session
// lifetime — unlike Claude Code which opens, writes, and closes. A file that
// no process has open is not an error: returns 0, nil.
//
// lsof output format:
//
//	COMMAND  PID USER  FD   TYPE DEVICE SIZE/OFF NODE NAME
//	codex  24454 ingo  14w  REG  1,18   3330     ...  /path/to/transcript.jsonl
//
// The FD column ends with 'w' for write mode, 'r' for read.
func (darwinObserver) WriterOf(path string) (int, error) {
	if path == "" {
		return 0, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "lsof", path).Output()
	if err != nil {
		return 0, nil // file not open by any process
	}

	myPID := os.Getpid()
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		if fields[0] == "COMMAND" { // header row
			continue
		}
		pid, err := strconv.Atoi(fields[1])
		if err != nil || pid <= 0 || pid == myPID {
			continue
		}
		fd := fields[3] // e.g. "14w", "8299r" — writer ends with 'w'
		if len(fd) > 0 && fd[len(fd)-1] == 'w' {
			return pid, nil
		}
	}
	return 0, nil
}

// EnvOf returns the whitelisted launcher env of pid via KERN_PROCARGS2 sysctl
// (readProcessEnv, defined in osutil_darwin.go). Per the port contract an
// unreadable env is an empty map, not an error.
func (darwinObserver) EnvOf(pid int) (map[string]string, error) {
	m, _ := readProcessEnv(pid)
	return m, nil
}

// runPgrep invokes pgrep with the given flag and pattern, parses the PIDs from
// stdout, and returns nil for the no-match (exit 1) case.
func runPgrep(flag, pattern string) ([]int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "pgrep", flag, pattern).Output()
	if err != nil {
		// pgrep exits 1 when there are no matches — not an error.
		if exit, ok := err.(*exec.ExitError); ok && exit.ExitCode() == 1 {
			return nil, nil
		}
		return nil, err
	}
	var pids []int
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		pid, err := strconv.Atoi(line)
		if err == nil && pid > 0 {
			pids = append(pids, pid)
		}
	}
	return pids, nil
}
