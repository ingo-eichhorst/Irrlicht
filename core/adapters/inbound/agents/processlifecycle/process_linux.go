//go:build linux

package processlifecycle

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
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

func (linuxObserver) ParentPIDOf(ctx context.Context, pid int) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "PPid:") {
			continue
		}
		parent, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "PPid:")))
		if err != nil || parent <= 0 {
			return 0, fmt.Errorf("invalid PPid for pid %d: %q", pid, line)
		}
		return parent, nil
	}
	return 0, fmt.Errorf("missing PPid for pid %d", pid)
}

// FindByName returns PIDs whose process name matches name, the way `pgrep -x`
// does on macOS.
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
		if procNameMatches(pid, name) {
			out = append(out, pid)
		}
	}
	return out, nil
}

// procNameMatches reports whether pid's process name equals name. It checks
// the basename of argv[0] first — that's what was actually invoked, it isn't
// truncated, and it preserves a symlink/wrapper name (a `claude` symlink to a
// versioned binary or to node still reads as "claude"), matching the macOS
// pgrep -x path. It falls back to /proc/<pid>/comm (the kernel's name,
// truncated to 15 chars and reflecting the resolved binary) for processes that
// rewrote their argv via prctl(PR_SET_NAME).
func procNameMatches(pid int, name string) bool {
	if data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid)); err == nil {
		argv0 := string(data)
		if i := strings.IndexByte(argv0, 0); i >= 0 {
			argv0 = argv0[:i] // cmdline is NUL-separated; take argv[0]
		}
		if argv0 != "" && filepath.Base(argv0) == name {
			return true
		}
	}
	if comm, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid)); err == nil {
		if strings.TrimRight(string(comm), "\n") == name {
			return true
		}
	}
	return false
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

// ArgvOf returns the argument vector of pid from /proc/<pid>/cmdline (a
// NUL-separated argv). Per the port contract an unreadable argv (process
// exited, root-owned) is a nil slice, not an error.
func (linuxObserver) ArgvOf(pid int) ([]string, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return nil, nil
	}
	fields := strings.Split(strings.TrimRight(string(data), "\x00"), "\x00")
	if len(fields) == 1 && fields[0] == "" {
		return nil, nil
	}
	return fields, nil
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
// has open is not an error: returns 0, nil; a /proc that could not be scanned
// at all IS one (#1537). The scan is O(procs × fds) but
// early-exits on the first writer and only stats fdinfo for fds that already
// point at the target file.
func (linuxObserver) WriterOf(path string) (int, error) {
	if path == "" {
		return 0, nil
	}
	// An unresolvable path is still scanned for as given: WriterOf answers
	// "nobody" for what it cannot see, unlike HoldsForWriting.
	want, _ := resolveProcTarget(path)
	pids, err := procPIDs()
	if err != nil {
		// #1537: an unreadable /proc is the linux spelling of a killed lsof —
		// it says nothing about who holds the file, and reporting it as
		// "nobody" is the collapse the port contract now forbids.
		return 0, fmt.Errorf("scan /proc for writers of %s: %w", path, err)
	}
	myPID := os.Getpid()
	for _, pid := range pids {
		if pid == myPID {
			continue
		}
		if pidHasFileOpenForWrite(pid, want) {
			return pid, nil
		}
	}
	return 0, nil
}

// pidHasFileOpenForWrite is procFDsHoldForWriting for WriterOf's scan, where a
// pid whose fds cannot be read is one more candidate that is not a writer.
func pidHasFileOpenForWrite(pid int, want string) bool {
	held, err := procFDsHoldForWriting("/proc", pid, want)
	return err == nil && held
}

// resolveProcTarget resolves path the way the kernel resolves the
// /proc/<pid>/fd/* links it is compared with. The caller's transcript path is
// only filepath.Clean'd (fswatcher joins os.UserHomeDir() + a relative dir,
// no symlink resolution), so on a host where $HOME or the data dir traverses a
// symlink an exact compare would never match. A path that does not exist is
// returned as given with no error, since nothing can hold it under that name;
// any other failure is returned with the unresolved path.
func resolveProcTarget(path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		return resolved, nil
	}
	if errors.Is(err, fs.ErrNotExist) {
		return path, nil
	}
	return path, err
}

// HoldsForWriting reports whether pid holds path open for writing, read from
// pid's own /proc/<pid>/fd links and the flags in /proc/<pid>/fdinfo — one
// process's descriptors, where WriterOf walks every process's (#2079).
//
// The three answers of the port contract, as /proc spells them: a missing
// /proc/<pid> under a procfs that is there is a pid that does not exist, so
// (false, nil); an fd that vanishes between two reads was closed under us, so
// it is skipped; any other unreadable entry (EACCES on another user's process,
// an fdinfo with no parseable flags, no procfs at all) means the probe could
// not look, so it is an error.
func (linuxObserver) HoldsForWriting(pid int, path string) (bool, error) {
	return holdsForWritingIn("/proc", pid, path)
}

// holdsForWritingIn is HoldsForWriting reading procfs at procRoot, so a test
// can lay down the unreadable and malformed entries a live /proc cannot be
// arranged into.
//
// A path resolveProcTarget cannot resolve (one in a directory this user may
// not search, say) is still compared as given: a link that matches it is a
// real hold, but no match proves nothing, so that case is an error.
func holdsForWritingIn(procRoot string, pid int, path string) (bool, error) {
	if pid <= 0 || path == "" {
		return false, nil
	}
	want, resolveErr := resolveProcTarget(path)
	held, err := procFDsHoldForWriting(procRoot, pid, want)
	if err != nil || held {
		return held, err
	}
	if resolveErr != nil {
		return false, fmt.Errorf("resolve %s: %w", path, resolveErr)
	}
	return false, nil
}

// procFDsHoldForWriting scans pid's fd links for one that resolves to want and
// was opened for writing. An fd it cannot read does not end the scan, since a
// writable fd later in the table is still a real hold; the first such error
// is returned only when no fd holds want.
func procFDsHoldForWriting(procRoot string, pid int, want string) (bool, error) {
	fdDir := fmt.Sprintf("%s/%d/fd", procRoot, pid)
	fds, err := os.ReadDir(fdDir)
	if errors.Is(err, fs.ErrNotExist) {
		// No such pid, provided there is a procfs to have asked.
		if _, rootErr := os.Stat(procRoot); rootErr != nil {
			return false, fmt.Errorf("read %s: %w", procRoot, rootErr)
		}
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read %s: %w", fdDir, err)
	}
	var firstErr error
	for _, fd := range fds {
		held, err := procFDHoldsForWriting(procRoot, pid, fd.Name(), want)
		if held {
			return true, nil
		}
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return false, firstErr
}

// procFDHoldsForWriting reads one fd link and, when it names want, its open
// flags. An fd closed between the directory read and either of these reads
// holds nothing.
func procFDHoldsForWriting(procRoot string, pid int, fd, want string) (bool, error) {
	link := fmt.Sprintf("%s/%d/fd/%s", procRoot, pid, fd)
	target, err := os.Readlink(link)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("readlink %s: %w", link, err)
	}
	if target != want {
		return false, nil
	}
	writable, err := fdOpenForWrite(procRoot, pid, fd)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return writable, err
}

// EnvOf returns the values of keys retained from pid's env, via
// /proc/<pid>/environ (readProcessEnv, defined in osutil_linux.go) —
// selective RETENTION, not selective reading: the kernel exposes the whole
// environ file, and readProcessEnv keeps only the entries named in keys
// (#2002 §1.2). Per the port contract, an unreadable env (process exited,
// root-owned) is a non-nil error, distinct from a readable env holding none
// of keys (empty map, nil error).
func (linuxObserver) EnvOf(pid int, keys map[string]struct{}) (map[string]string, error) {
	return readProcessEnv(pid, keys)
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

// fdOpenForWrite reports whether the given fd of pid was opened for writing,
// read from the "flags:" line of <procRoot>/<pid>/fdinfo/<fd> (octal open
// flags). The access mode is the low two bits: O_RDONLY(0), O_WRONLY(1),
// O_RDWR(2).
// An unreadable fdinfo returns the read error unchanged, so a caller can tell
// a closed fd (fs.ErrNotExist) from one it may not read; an fdinfo with no
// parseable flags line is an error too, never a "read-only".
func fdOpenForWrite(procRoot string, pid int, fd string) (bool, error) {
	fdinfo := fmt.Sprintf("%s/%d/fdinfo/%s", procRoot, pid, fd)
	data, err := os.ReadFile(fdinfo)
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		rest, ok := strings.CutPrefix(line, "flags:")
		if !ok {
			continue
		}
		flags, err := strconv.ParseInt(strings.TrimSpace(rest), 8, 64)
		if err != nil {
			return false, fmt.Errorf("parse flags of %s: %w", fdinfo, err)
		}
		return flags&3 != 0, nil // O_ACCMODE bits non-zero ⇒ writable
	}
	return false, fmt.Errorf("%s has no flags line", fdinfo)
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
