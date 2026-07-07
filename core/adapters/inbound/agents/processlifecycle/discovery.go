package processlifecycle

import (
	"fmt"
	"os"
	"path/filepath"

	"irrlicht/core/domain/agent"
)

// HasLiveProcess reports whether at least one running process matches m.
// It is the always-on detection primitive behind the consent wizard
// (issue #570): pure observation through the ProcessObserver seam, with
// no session side-effects — unlike the Scanner, which emits proc-<pid>
// pre-sessions and is therefore permission-gated.
func HasLiveProcess(m agent.ProcessMatcher) bool {
	var pids []int
	var err error
	switch v := m.(type) {
	case agent.ExactName:
		pids, err = osProc.FindByName(v.Name)
	case agent.CommandPattern:
		pids, err = osProc.FindByCmdline(v.Regex.String())
	default:
		return false
	}
	return err == nil && len(pids) > 0
}

// DiscoverPIDByCWD finds a process by exact name whose CWD matches the given
// directory. When multiple processes match, disambiguate selects one.
// Returns 0, nil when no matching process is found.
func DiscoverPIDByCWD(processName, cwd string, disambiguate func([]int) int) (int, error) {
	return DiscoverPIDByCWDExcludingArgv(processName, cwd, disambiguate, nil)
}

// DiscoverPIDByCWDExcludingArgv is DiscoverPIDByCWD with an extra per-PID argv
// filter applied before disambiguation. excludeArgv mirrors the adapter's
// Process.ExcludeArgv predicate (the same one the Scanner runs via argvExcluded):
// when it returns true the PID is dropped from the candidate set, so a same-name
// infrastructure process — e.g. Claude Code's `--bg-spare` pre-warmed pool helper,
// which runs the `claude` binary in the session's cwd — never reaches the
// disambiguator and is never bound as the session PID (the ghost in #727).
//
// argv is read through the same ProcessObserver seam (osProc.ArgvOf); a
// nil/unreadable argv is passed through to the predicate, which per the
// ExcludeArgv contract must not exclude on it. A nil excludeArgv disables
// filtering — the legacy DiscoverPIDByCWD behaviour.
func DiscoverPIDByCWDExcludingArgv(processName, cwd string, disambiguate func([]int) int, excludeArgv func([]string) bool) (int, error) {
	if cwd == "" || processName == "" {
		return 0, nil
	}
	pids, err := osProc.FindByName(processName)
	if err != nil {
		return 0, fmt.Errorf("find %s processes: %w", processName, err)
	}
	if excludeArgv != nil {
		kept := make([]int, 0, len(pids))
		for _, pid := range pids {
			argv, _ := osProc.ArgvOf(pid)
			if excludeArgv(argv) {
				continue
			}
			kept = append(kept, pid)
		}
		pids = kept
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
	return DiscoverPIDByCWDAndCmdLineExcludingArgv(cmdLinePattern, cwd, disambiguate, nil)
}

// DiscoverPIDByCWDAndCmdLineExcludingArgv is DiscoverPIDByCWDAndCmdLine with an
// extra per-PID argv filter applied before disambiguation. excludeArgv mirrors
// the adapter's Process.ExcludeArgv predicate (the same one the Scanner runs
// via argvExcluded): when it returns true the PID is dropped from the candidate
// set, so a same-cmdline infrastructure process — e.g. Gemini's heap-bump Node
// worker, which shares the launcher's cwd — never reaches the disambiguator.
// Without this the disambiguator's "highest unclaimed PID" could pick the
// higher-PID worker, the very process the scanner treats as a ghost (#664).
//
// argv is read through the same ProcessObserver seam (osProc.ArgvOf); a
// nil/unreadable argv is passed through to the predicate, which per the
// ExcludeArgv contract must not exclude on it. A nil excludeArgv disables
// filtering — the legacy DiscoverPIDByCWDAndCmdLine behaviour.
func DiscoverPIDByCWDAndCmdLineExcludingArgv(cmdLinePattern, cwd string, disambiguate func([]int) int, excludeArgv func([]string) bool) (int, error) {
	if cwd == "" || cmdLinePattern == "" {
		return 0, nil
	}
	pids, err := osProc.FindByCmdline(cmdLinePattern)
	if err != nil {
		return 0, fmt.Errorf("find processes matching %q: %w", cmdLinePattern, err)
	}
	if excludeArgv != nil {
		kept := make([]int, 0, len(pids))
		for _, pid := range pids {
			argv, _ := osProc.ArgvOf(pid)
			if excludeArgv(argv) {
				continue
			}
			kept = append(kept, pid)
		}
		pids = kept
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
	// CWDOf returns the OS-canonical working directory (e.g. on Linux
	// /proc/<pid>/cwd is fully symlink-resolved). The caller's cwd may carry
	// symlink components, so canonicalise it before the equality check or a
	// symlinked $HOME would never match. EvalSymlinks needs the dir to exist;
	// it does (the process is live), and on failure we keep the original.
	if resolved, err := filepath.EvalSymlinks(cwd); err == nil {
		cwd = resolved
	}
	matches := matchingCWDPids(pids, cwd)
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
		return highestPID(matches)
	}
}

// matchingCWDPids filters pids to those whose CWD equals cwd exactly,
// excluding the daemon's own PID. PIDs whose CWD cannot be read (race
// against process exit, restricted permissions) are skipped silently.
func matchingCWDPids(pids []int, cwd string) []int {
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
	return matches
}

// highestPID returns the largest PID in pids (most recently started on
// macOS), or 0 for an empty slice.
func highestPID(pids []int) int {
	best := 0
	for _, p := range pids {
		if p > best {
			best = p
		}
	}
	return best
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
