//go:build linux

package processlifecycle

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"irrlicht/core/ports/outbound"
)

// linuxObserver implements outbound.ProcessObserver entirely through /proc —
// no subprocess, no pgrep/lsof. This is both faster and more robust than the
// macOS shell-out path: discovery is plain file reads of the procfs the
// kernel already maintains.
type linuxObserver struct{}

func newObserver() outbound.ProcessObserver { return linuxObserver{} }

// FindByName returns PIDs whose /proc/<pid>/comm exactly matches name.
//
// Linux truncates comm to 15 characters (TASK_COMM_LEN-1); agent names we
// match on ("claude", "codex", "opencode", ...) are well under that, and
// pgrep -x compares against the same truncated comm, so behaviour matches the
// darwin path.
func (linuxObserver) FindByName(name string) ([]int, error) {
	if name == "" {
		return nil, nil
	}
	pids, err := procPIDs()
	if err != nil {
		return nil, fmt.Errorf("scan /proc: %w", err)
	}
	var out []int
	for _, pid := range pids {
		comm, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid))
		if err != nil {
			continue // process exited or unreadable
		}
		if strings.TrimRight(string(comm), "\n") == name {
			out = append(out, pid)
		}
	}
	return out, nil
}

// FindByCmdline returns PIDs whose full command line matches the regex
// pattern, mirroring `pgrep -f`. The daemon's own PID is excluded so a pattern
// that matches the daemon's argv can't match the daemon itself.
func (linuxObserver) FindByCmdline(pattern string) ([]int, error) {
	if pattern == "" {
		return nil, nil
	}
	re, err := compileCmdlinePattern(pattern)
	if err != nil {
		return nil, fmt.Errorf("compile cmdline pattern %q: %w", pattern, err)
	}
	pids, err := procPIDs()
	if err != nil {
		return nil, fmt.Errorf("scan /proc: %w", err)
	}
	myPID := os.Getpid()
	var out []int
	for _, pid := range pids {
		if pid == myPID {
			continue
		}
		data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
		if err != nil {
			continue
		}
		// cmdline is NUL-separated argv; join with spaces so the pattern
		// matches across argument boundaries the way pgrep -f does.
		cmdline := strings.ReplaceAll(strings.TrimRight(string(data), "\x00"), "\x00", " ")
		if re.MatchString(cmdline) {
			out = append(out, pid)
		}
	}
	return out, nil
}

// CWDOf returns the working directory of pid via the /proc/<pid>/cwd symlink.
func (linuxObserver) CWDOf(pid int) (string, error) {
	cwd, err := os.Readlink(fmt.Sprintf("/proc/%d/cwd", pid))
	if err != nil {
		return "", fmt.Errorf("readlink /proc/%d/cwd: %w", pid, err)
	}
	return cwd, nil
}

// WriterOf returns the first PID that has path open for writing, found by
// scanning every process's /proc/<pid>/fd/* symlinks for one resolving to
// path and confirming write access via /proc/<pid>/fdinfo. A file no process
// has open is not an error: returns 0, nil. The scan is O(procs × fds) but
// early-exits on the first writer and only stats fdinfo for fds that already
// point at the target file.
func (linuxObserver) WriterOf(path string) (int, error) {
	if path == "" {
		return 0, nil
	}
	pids, err := procPIDs()
	if err != nil {
		return 0, nil
	}
	myPID := os.Getpid()
	for _, pid := range pids {
		if pid == myPID {
			continue
		}
		fdDir := fmt.Sprintf("/proc/%d/fd", pid)
		fds, err := os.ReadDir(fdDir)
		if err != nil {
			continue // process exited or fds unreadable (not ours)
		}
		for _, fd := range fds {
			target, err := os.Readlink(fdDir + "/" + fd.Name())
			if err != nil || target != path {
				continue
			}
			if fdWritable(pid, fd.Name()) {
				return pid, nil
			}
		}
	}
	return 0, nil
}

// EnvOf returns the whitelisted launcher env of pid via /proc/<pid>/environ
// (readProcessEnv, defined in osutil_linux.go).
func (linuxObserver) EnvOf(pid int) (map[string]string, error) {
	return readProcessEnv(pid)
}

// procPIDs returns the PIDs of every process currently in /proc.
func procPIDs() ([]int, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	pids := make([]int, 0, len(entries))
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue // non-numeric /proc entry
		}
		pids = append(pids, pid)
	}
	return pids, nil
}

// fdWritable reports whether the given fd of pid was opened for writing,
// read from the "flags:" line of /proc/<pid>/fdinfo/<fd> (octal open flags).
// The access mode is the low two bits: O_RDONLY(0), O_WRONLY(1), O_RDWR(2).
func fdWritable(pid int, fd string) bool {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/fdinfo/%s", pid, fd))
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		rest, ok := strings.CutPrefix(line, "flags:")
		if !ok {
			continue
		}
		flags, err := strconv.ParseInt(strings.TrimSpace(rest), 8, 64)
		if err != nil {
			return false
		}
		return flags&3 != 0 // O_ACCMODE bits non-zero ⇒ writable
	}
	return false
}

// cmdlineRECache memoizes compiled FindByCmdline patterns. The pattern set is
// tiny and fixed (one per CommandPattern adapter), but the scanner re-queries
// every poll, so caching avoids recompiling the same regex on a timer.
var cmdlineRECache sync.Map // pattern string → *regexp.Regexp

func compileCmdlinePattern(pattern string) (*regexp.Regexp, error) {
	if v, ok := cmdlineRECache.Load(pattern); ok {
		return v.(*regexp.Regexp), nil
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, err
	}
	cmdlineRECache.Store(pattern, re)
	return re, nil
}
