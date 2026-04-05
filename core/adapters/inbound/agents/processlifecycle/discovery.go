package processlifecycle

import (
	"fmt"
	"os"
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

