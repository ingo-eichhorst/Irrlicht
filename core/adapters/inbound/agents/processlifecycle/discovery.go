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
	return narrowByCWD(withoutExcludedArgv(pids, excludeArgv), cwd, disambiguate), nil
}

// PIDsByCWDExcludingArgv returns every process named processName whose cwd is
// cwd and whose argv excludeArgv does not reject — all of them, where
// DiscoverPIDByCWDExcludingArgv narrows to one. It is for a caller that counts
// matches (codex's LauncherPID, #2083), so it differs from that function in
// what it does when it cannot look: a candidate whose cwd cannot be read is an
// error rather than a silent skip, because a skipped match and a non-match
// would otherwise produce the same count. The daemon's own PID is excluded.
//
// argv is read first (a sysctl on darwin) and cwd only for the PIDs it keeps
// (an lsof on darwin), so a rejected process costs no cwd read.
func PIDsByCWDExcludingArgv(processName, cwd string, excludeArgv func([]string) bool) ([]int, error) {
	if cwd == "" || processName == "" {
		return nil, nil
	}
	pids, err := osProc.FindByName(processName)
	if err != nil {
		return nil, fmt.Errorf("find %s processes: %w", processName, err)
	}
	cwd = canonicalCWD(cwd)
	myPID := os.Getpid()
	var matches []int
	for _, pid := range withoutExcludedArgv(pids, excludeArgv) {
		if pid == myPID {
			continue
		}
		dir, err := osProc.CWDOf(pid)
		if err != nil {
			return nil, fmt.Errorf("cwd of %s pid %d: %w", processName, pid, err)
		}
		if dir == cwd {
			matches = append(matches, pid)
		}
	}
	return matches, nil
}

// withoutExcludedArgv drops the PIDs whose argv excludeArgv rejects. A nil
// excludeArgv keeps every PID; an unreadable argv is passed to the predicate
// as nil, which per the ExcludeArgv contract must not exclude on it.
func withoutExcludedArgv(pids []int, excludeArgv func([]string) bool) []int {
	if excludeArgv == nil {
		return pids
	}
	kept := make([]int, 0, len(pids))
	for _, pid := range pids {
		argv, _ := osProc.ArgvOf(pid)
		if excludeArgv(argv) {
			continue
		}
		kept = append(kept, pid)
	}
	return kept
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
	return narrowByCWD(withoutExcludedArgv(pids, excludeArgv), cwd, disambiguate), nil
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

// LiveCWDsByCmdline is the CommandPattern counterpart of LiveCWDs: it
// returns the set of working directories held by live processes whose FULL
// COMMAND LINE matches cmdLinePattern, optionally filtered by excludeArgv.
//
// It exists because LiveCWDs matches on the binary name via pgrep -x, which
// never fires for an agent that ships as a Python console script — the OS
// process is the interpreter. Hermes is such an agent, and needs the CWD
// snapshot for the same reason opencode does: its store rows carry no
// working directory of their own for CLI sessions.
//
// Per the ExcludeArgv contract a nil/unreadable argv is passed through to
// the predicate, which must not exclude on it. Same best-effort semantics as
// LiveCWDs: an unreadable CWD is skipped, not an error.
func LiveCWDsByCmdline(cmdLinePattern string, excludeArgv func([]string) bool) (map[string]struct{}, error) {
	if cmdLinePattern == "" {
		return nil, nil
	}
	pids, err := osProc.FindByCmdline(cmdLinePattern)
	if err != nil {
		return nil, fmt.Errorf("find processes matching %q: %w", cmdLinePattern, err)
	}
	myPID := os.Getpid()
	set := make(map[string]struct{}, len(pids))
	for _, pid := range pids {
		if pid == myPID {
			continue
		}
		if excludeArgv != nil {
			argv, _ := osProc.ArgvOf(pid)
			if excludeArgv(argv) {
				continue
			}
		}
		dir, err := osProc.CWDOf(pid)
		if err != nil || dir == "" {
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
	matches := matchingCWDPids(pids, canonicalCWD(cwd))
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

// canonicalCWD resolves symlinks in cwd. CWDOf returns the OS-canonical
// working directory (e.g. on Linux /proc/<pid>/cwd is fully symlink-resolved),
// while the caller's cwd may carry symlink components, so it is canonicalised
// before an equality check or a symlinked $HOME would never match.
// EvalSymlinks needs the dir to exist; it does when a process runs in it, and
// on failure the original is kept.
func canonicalCWD(cwd string) string {
	if resolved, err := filepath.EvalSymlinks(cwd); err == nil {
		return resolved
	}
	return cwd
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

// HoldsForWriting asks the platform observer whether pid holds path open for
// writing: DiscoverPIDByTranscriptWriter's question asked of one pid, for the
// SharedPIDOwner probes that already know which pid they mean (#2079). See
// outbound.ProcessObserver.HoldsForWriting for its three answers.
func HoldsForWriting(pid int, path string) (bool, error) {
	return osProc.HoldsForWriting(pid, path)
}
