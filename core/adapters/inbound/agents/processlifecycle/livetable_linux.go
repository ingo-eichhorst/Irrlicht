//go:build linux

package processlifecycle

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"irrlicht/core/domain/agent"
)

// liveTableTTL bounds how long one /proc walk answers HasLiveProcess. The
// consent wizard's detection pass asks once per agent adapter, and the
// launcher/endpoint/account probes ask again for the same agents — on linux
// each of those used to be its own full /proc walk with one or two file reads
// per process, every detectionPollInterval, for as long as any permission
// stays pending (forever on a headless host that only grants transcripts).
// One walk per pass reads each process once; a second is far below the 5s
// cadence, so a pass sees one consistent table and the next pass a fresh one.
const liveTableTTL = time.Second

// procEntry is what the HasLiveProcess matchers read of one process — the
// same argv[0], comm and cmdline FindByName and FindByCmdline read.
type procEntry struct {
	pid       int
	argv0     string // basename of argv[0]
	comm      string
	cmdline   string // argv joined with spaces, as FindByCmdline matches it
	cmdlineOK bool   // cmdline was readable; FindByCmdline skips it otherwise
}

var (
	liveTableMu   sync.Mutex
	liveTableAt   time.Time
	liveTableRows []procEntry
	liveTableErr  error

	// Seams for tests.
	liveTableNow  = time.Now
	readLiveTable = func() ([]procEntry, error) { return readProcTable("/proc") }
)

// hasLiveFromTable answers HasLiveProcess from a shared /proc walk. ok is
// false when the observer is not the real /proc one (tests swap osProc), so
// the caller falls back to FindByName/FindByCmdline.
func hasLiveFromTable(m agent.ProcessMatcher) (live, ok bool) {
	if _, real := osProc.(linuxObserver); !real {
		return false, false
	}
	switch v := m.(type) {
	case agent.ExactName:
		if v.Name == "" {
			return false, true
		}
		rows, err := liveTable()
		if err != nil {
			return false, true
		}
		for _, e := range rows {
			if (e.argv0 != "" && e.argv0 == v.Name) || e.comm == v.Name {
				return true, true
			}
		}
		return false, true
	case agent.CommandPattern:
		pattern := v.Regex.String()
		if pattern == "" {
			return false, true
		}
		re, err := compileCmdlinePattern(pattern)
		if err != nil {
			return false, true
		}
		rows, err := liveTable()
		if err != nil {
			return false, true
		}
		myPID := os.Getpid()
		for _, e := range rows {
			if e.pid != myPID && e.cmdlineOK && re.MatchString(e.cmdline) {
				return true, true
			}
		}
		return false, true
	default:
		return false, true
	}
}

// liveTable returns the current /proc table, walking /proc again once the
// cached one is older than liveTableTTL.
func liveTable() ([]procEntry, error) {
	liveTableMu.Lock()
	defer liveTableMu.Unlock()
	now := liveTableNow()
	if !liveTableAt.IsZero() && now.Sub(liveTableAt) < liveTableTTL {
		return liveTableRows, liveTableErr
	}
	liveTableRows, liveTableErr = readLiveTable()
	liveTableAt = now
	return liveTableRows, liveTableErr
}

// readProcTable walks procRoot once, reading cmdline and comm of every pid.
// A process that exits mid-walk simply has unreadable files, exactly as it
// would for FindByName/FindByCmdline.
func readProcTable(procRoot string) ([]procEntry, error) {
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return nil, fmt.Errorf("scan %s: %w", procRoot, err)
	}
	rows := make([]procEntry, 0, len(entries))
	for _, d := range entries {
		pid, err := strconv.Atoi(d.Name())
		if err != nil {
			continue // non-numeric /proc entry
		}
		e := procEntry{pid: pid}
		if data, err := os.ReadFile(fmt.Sprintf("%s/%d/cmdline", procRoot, pid)); err == nil {
			e.cmdlineOK = true
			raw := string(data)
			argv0 := raw
			if i := strings.IndexByte(argv0, 0); i >= 0 {
				argv0 = argv0[:i]
			}
			if argv0 != "" {
				e.argv0 = filepath.Base(argv0)
			}
			e.cmdline = strings.ReplaceAll(strings.TrimRight(raw, "\x00"), "\x00", " ")
		}
		if comm, err := os.ReadFile(fmt.Sprintf("%s/%d/comm", procRoot, pid)); err == nil {
			e.comm = strings.TrimRight(string(comm), "\n")
		}
		rows = append(rows, e)
	}
	return rows, nil
}
