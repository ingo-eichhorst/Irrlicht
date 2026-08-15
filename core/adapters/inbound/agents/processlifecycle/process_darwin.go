//go:build darwin

package processlifecycle

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"

	"irrlicht/core/pkg/pathutil"
	"irrlicht/core/ports/outbound"
)

// lsofPath and pgrepPath are resolved once from a fixed set of trusted
// directories rather than trusted PATH, per go:S4036.
var (
	lsofPath  = pathutil.MustResolve("lsof")
	pgrepPath = pathutil.MustResolve("pgrep")
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

// ArgvOf returns the argument vector of pid via KERN_PROCARGS2 sysctl
// (the same raw buffer readProcessEnv parses for env). Per the port
// contract an unreadable argv — hardened-runtime processes strip it — is a
// nil slice, not an error.
func (darwinObserver) ArgvOf(pid int) ([]string, error) {
	buf, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil {
		return nil, nil
	}
	return parseProcargs2Argv(buf), nil
}

// CWDOf returns the working directory of pid via `lsof -d cwd`.
func (darwinObserver) CWDOf(pid int) (string, error) {
	out, err := runProbe(context.Background(), probeLsofCWD, func(ctx context.Context) *exec.Cmd {
		return exec.CommandContext(ctx, lsofPath, "-a", "-p", strconv.Itoa(pid), "-d", "cwd", "-Fn")
	})
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
// An lsof that could not be RUN is a different fact and is returned as an
// error (#1537): the 2s ceiling, a fork failure or a missing binary say
// nothing about who holds the file, and reporting them as "nobody" is the
// #1485 collapse with the same tool. lsofProbeRan draws the line.
//
// lsof output format:
//
//	COMMAND  PID USER  FD   TYPE DEVICE SIZE/OFF NODE NAME
//	codex  24454 ingo  14w  REG  1,18   3330     ...  /path/to/transcript.jsonl
//
// The FD column carries the access mode — 'r' (read), 'w' (write) or 'u'
// (read/write) — optionally followed by a lock character, e.g. "59uW". Both
// 'w' and 'u' are writers; see writerPIDFromLsof.
func (darwinObserver) WriterOf(path string) (int, error) {
	return writerOfVia(path, func(ctx context.Context) *exec.Cmd {
		return exec.CommandContext(ctx, lsofPath, path)
	})
}

// writerOfVia is WriterOf with the shellout injected.
func writerOfVia(path string, build shelloutCmd) (int, error) {
	if path == "" {
		return 0, nil
	}
	out, err := runProbe(context.Background(), probeLsofWriter, build)
	if !lsofProbeRan(err) {
		// #1537: an lsof that could not be ASKED knows nothing about who holds
		// this transcript, and the comment that used to sit on the collapsed
		// return ("file not open by any process") stated one cause for an error
		// with several. Exit 1 IS an answer and still returns (0, nil) below —
		// on this path the file being absent is itself the honest verdict "no
		// process is writing it", which is why WriterOf needs no os.Stat where
		// herdrClientPIDs does.
		return 0, fmt.Errorf("lsof %s: %w", path, err)
	}

	return writerPIDFromLsof(string(out), os.Getpid()), nil
}

// writerPIDFromLsof picks the first PID holding the file open for writing out
// of lsof's table. Split out of WriterOf so the mode predicate — the part that
// actually decides — is testable without shelling out to lsof.
//
// Both 'w' (write-only) and 'u' (read/write) count: a read-write handle IS a
// writer, and it is what Codex 0.147 uses for its rollout ("59u"). The old
// test compared the FD column's LAST BYTE against 'w', which missed 'u'
// entirely and also missed a locked write handle like "59uW", where the last
// byte is lsof's lock character rather than the mode. Reading the mode via
// e.Mode() fixes both, and matches the Linux observer, whose fdWritable has
// always accepted O_WRONLY and O_RDWR alike (flags&3 != 0) — macOS was the
// outlier, and the disagreement was invisible because no codex recording had
// been made since the upstream change (#1388).
func writerPIDFromLsof(out string, self int) int {
	for _, e := range parseLsofFDs(out, self) {
		if m := e.Mode(); m == 'w' || m == 'u' {
			return e.PID
		}
	}
	return 0
}

// lsofFD is one open-file row of lsof's default table: the process holding the
// file and its raw FD column (e.g. "14w", "3r", "9u", "5uW").
type lsofFD struct {
	PID int
	FD  string
}

// Mode returns the access mode letter of the FD column — 'r', 'w' or 'u'
// (read/write) — or 0 when the column has none.
//
// It reads the first non-digit rather than the last byte, because lsof may
// append a lock character after the mode: a file locked for writing shows
// "5uW", whose last byte is the lock, not the mode. Callers that only ever see
// unlocked files are unaffected either way.
func (e lsofFD) Mode() byte {
	for i := 0; i < len(e.FD); i++ {
		if e.FD[i] < '0' || e.FD[i] > '9' {
			return e.FD[i]
		}
	}
	return 0
}

// parseLsofFDs tokenizes lsof's default table, dropping the header row, rows
// too short to carry an FD column, and self. It deliberately applies no mode
// filter, leaving the predicate at each call site. Both current callers —
// WriterOf here and herdr client discovery (osutil_darwin.go) — happen to
// count 'w' and 'u' alike, since a read/write handle is a writer; keeping the
// filter out of the parser is what lets either change without silently
// redefining the other.
func parseLsofFDs(out string, self int) []lsofFD {
	var entries []lsofFD
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 || fields[0] == "COMMAND" { // short row, or the header
			continue
		}
		pid, err := strconv.Atoi(fields[1])
		if err != nil || pid <= 0 {
			continue
		}
		if pid == self {
			continue
		}
		entries = append(entries, lsofFD{PID: pid, FD: fields[3]})
	}
	return entries
}

// EnvOf returns the whitelisted launcher env of pid via KERN_PROCARGS2 sysctl
// (readProcessEnv, defined in osutil_darwin.go). Per the port contract an
// unreadable env is an empty map, not an error.
func (darwinObserver) EnvOf(pid int) (map[string]string, error) {
	m, _ := readProcessEnv(pid)
	return m, nil
}

// pgrepNoMatch is pgrep's "no process matched" exit status — an answer, not a
// failure. The lsof twin is lsofNothingToReport (osutil_darwin.go); both are
// the per-tool half of the shared predicate (#1538).
const pgrepNoMatch = 1

// runPgrep invokes pgrep with the given flag and pattern, parses the PIDs from
// stdout, and returns nil for the no-match (exit 1) case.
func runPgrep(flag, pattern string) ([]int, error) {
	return runPgrepVia(flag, pattern, func(ctx context.Context) *exec.Cmd {
		return exec.CommandContext(ctx, pgrepPath, flag, pattern)
	})
}

// runPgrepVia is runPgrep with the shellout injected.
func runPgrepVia(flag, pattern string, build shelloutCmd) ([]int, error) {
	out, err := runProbe(context.Background(), probePgrepDiscover, build)
	if err != nil {
		// pgrep exits 1 when there are no matches — a real answer, the same
		// shape as lsof's (lsofProbeRan). Anything else is a probe that did
		// not run and must stay an error: collapsing it to (nil, nil) would
		// report "no such process" for a pgrep the 2s ceiling killed.
		if probeAnswered(err, pgrepNoMatch) {
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
