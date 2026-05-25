package processlifecycle

import (
	"fmt"
	"os"
)

// DiscoverPIDByCWD finds a process by exact name whose CWD matches the given
// directory. When multiple processes match, disambiguate selects one.
// Returns 0, nil when no matching process is found.
func DiscoverPIDByCWD(processName, cwd string, disambiguate func([]int) int) (int, error) {
	if cwd == "" || processName == "" {
		return 0, nil
	}
	pids, err := osProc.FindByName(processName)
	if err != nil {
		return 0, fmt.Errorf("find %s processes: %w", processName, err)
	}
	return narrowByCWD(pids, cwd, disambiguate), nil
}

// DiscoverPIDByCWDAndCmdLine finds a process whose full command line matches
// the given regex pattern (via the observer's FindByCmdline) and whose CWD
// matches cwd. Use this for agents whose OS process name doesn't match their
// CLI name — e.g. Python tools where the OS process is `python` and the agent
// script is in argv[1]. Mirrors DiscoverPIDByCWD's contract: returns 0, nil
// when no match.
func DiscoverPIDByCWDAndCmdLine(cmdLinePattern, cwd string, disambiguate func([]int) int) (int, error) {
	if cwd == "" || cmdLinePattern == "" {
		return 0, nil
	}
	pids, err := osProc.FindByCmdline(cmdLinePattern)
	if err != nil {
		return 0, fmt.Errorf("find processes matching %q: %w", cmdLinePattern, err)
	}
	return narrowByCWD(pids, cwd, disambiguate), nil
}

// LiveCWDs returns the set of working directories currently held by live
// processes whose binary name matches processName. Excludes the daemon's own
// PID. PIDs whose CWD cannot be read (race against process exit, restricted
// permissions) are skipped silently — this is a best-effort snapshot, not a
// guarantee.
//
// Used by the OpenCode adapter to gate EventNewSession on a live process: a
// session row in the DB is only surfaced if some opencode process currently
// owns its CWD.
func LiveCWDs(processName string) (map[string]struct{}, error) {
	if processName == "" {
		return nil, nil
	}
	pids, err := osProc.FindByName(processName)
	if err != nil {
		return nil, fmt.Errorf("find %s processes: %w", processName, err)
	}
	myPID := os.Getpid()
	set := make(map[string]struct{}, len(pids))
	for _, pid := range pids {
		if pid == myPID {
			continue
		}
		dir, err := osProc.CWDOf(pid)
		if err != nil {
			continue
		}
		set[dir] = struct{}{}
	}
	return set, nil
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
		dir, err := osProc.CWDOf(pid)
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
	return osProc.WriterOf(transcriptPath)
}
