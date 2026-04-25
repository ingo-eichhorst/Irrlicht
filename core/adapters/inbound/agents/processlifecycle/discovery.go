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

// DiscoverPIDByCWD finds a process by exact name whose CWD matches the given
// directory. When multiple processes match, disambiguate selects one.
// Returns 0, nil when no matching process is found.
func DiscoverPIDByCWD(processName, cwd string, disambiguate func([]int) int) (int, error) {
	if cwd == "" || processName == "" {
		return 0, nil
	}
	pids, err := findProcesses(processName)
	if err != nil {
		return 0, fmt.Errorf("find %s processes: %w", processName, err)
	}
	return narrowByCWD(pids, cwd, disambiguate), nil
}

// DiscoverPIDByCWDAndCmdLine finds a process whose full command line matches
// the given regex pattern (via pgrep -f) and whose CWD matches cwd. Use this
// for agents whose OS process name doesn't match their CLI name — e.g. Python
// tools where the OS process is `python` and the agent script is in argv[1].
// Mirrors DiscoverPIDByCWD's contract: returns 0, nil when no match.
func DiscoverPIDByCWDAndCmdLine(cmdLinePattern, cwd string, disambiguate func([]int) int) (int, error) {
	if cwd == "" || cmdLinePattern == "" {
		return 0, nil
	}
	pids, err := findProcessesByCmdLine(cmdLinePattern)
	if err != nil {
		return 0, fmt.Errorf("find processes matching %q: %w", cmdLinePattern, err)
	}
	return narrowByCWD(pids, cwd, disambiguate), nil
}

// narrowByCWD filters pids to those whose CWD equals the given path, then
// resolves to a single PID via disambiguate (falling back to highest PID).
// Excludes the daemon's own PID. Returns 0 when no match.
func narrowByCWD(pids []int, cwd string, disambiguate func([]int) int) int {
	myPID := os.Getpid()
	var matches []int
	for _, pid := range pids {
		if pid == myPID {
			continue
		}
		dir, err := processCWD(pid)
		if err != nil {
			continue
		}
		if dir == cwd {
			matches = append(matches, pid)
		}
	}
	switch len(matches) {
	case 0:
		return 0
	case 1:
		return matches[0]
	default:
		if disambiguate != nil {
			return disambiguate(matches)
		}
		// Default: highest PID (most recently started on macOS).
		best := 0
		for _, p := range matches {
			if p > best {
				best = p
			}
		}
		return best
	}
}

// DiscoverPIDByTranscriptWriter finds the process that has a transcript file
// open for writing. This is used for agents (Codex, Pi) that keep transcript
// files open during their lifetime — unlike Claude Code which opens, writes,
// and closes. Returns 0, nil when no writer is found.
func DiscoverPIDByTranscriptWriter(transcriptPath string) (int, error) {
	if transcriptPath == "" {
		return 0, nil
	}

	// lsof <path> lists all processes with the file open.
	// Output format:
	//   COMMAND  PID USER  FD   TYPE DEVICE SIZE/OFF NODE NAME
	//   codex  24454 ingo  14w  REG  1,18   3330     ...  /path/to/transcript.jsonl
	//
	// The FD column ends with 'w' for write mode, 'r' for read.
	out, err := lsofFile(transcriptPath)
	if err != nil {
		return 0, nil // file not open by any process
	}

	myPID := os.Getpid()
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		// Skip header row.
		if fields[0] == "COMMAND" {
			continue
		}
		pid, err := strconv.Atoi(fields[1])
		if err != nil || pid <= 0 || pid == myPID {
			continue
		}
		// FD column (e.g. "14w", "8299r") — writer ends with 'w'.
		fd := fields[3]
		if len(fd) > 0 && fd[len(fd)-1] == 'w' {
			return pid, nil
		}
	}
	return 0, nil
}

// lsofFile runs lsof on a single file path and returns the output.
func lsofFile(path string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "lsof", path).Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}
