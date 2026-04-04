package processlifecycle

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// DiscoverPIDByCWD finds a "claude" process whose CWD matches the given
// directory. When multiple processes match, disambiguate selects one.
// Returns 0, nil when no matching process is found.
func DiscoverPIDByCWD(cwd string, disambiguate func([]int) int) (int, error) {
	if cwd == "" {
		return 0, nil
	}
	pids, err := FindProcesses("claude")
	if err != nil {
		return 0, fmt.Errorf("find claude processes: %w", err)
	}

	myPID := os.Getpid()
	var matches []int
	for _, pid := range pids {
		if pid == myPID {
			continue
		}
		dir, err := ProcessCWD(pid)
		if err != nil {
			continue
		}
		if dir == cwd {
			matches = append(matches, pid)
		}
	}

	switch len(matches) {
	case 0:
		return 0, nil
	case 1:
		return matches[0], nil
	default:
		if disambiguate != nil {
			return disambiguate(matches), nil
		}
		// Default: highest PID (most recently started on macOS).
		best := 0
		for _, p := range matches {
			if p > best {
				best = p
			}
		}
		return best, nil
	}
}

// DiscoverPID uses lsof to find the PID that has filePath open.
// Returns 0, nil when no process has the file open.
func DiscoverPID(filePath string) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, "lsof", "-t", filePath).Output()
	if err != nil {
		// Exit status 1 means no matches — not an error.
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return 0, nil
		}
		return 0, fmt.Errorf("lsof %s: %w", filePath, err)
	}

	// Skip our own PID — the daemon reads transcript files for metrics,
	// so lsof often returns the daemon itself. We want the external
	// process (e.g. Claude Code CLI) that owns the session.
	myPID := os.Getpid()
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSpace(line))
		if err != nil {
			continue
		}
		if pid != myPID {
			return pid, nil
		}
	}
	return 0, nil
}
