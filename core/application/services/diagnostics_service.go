package services

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/config"
	"irrlicht/core/domain/session"
	"irrlicht/core/ports/outbound"
)

// maxBundleLogBytes caps the trimmed event log included in a diagnostics
// bundle. Raw logs reach ~58 MB on a busy machine; this keeps the bundle
// predictable while preserving the newest activity (the tail), which is what a
// fresh bug report needs.
const maxBundleLogBytes = 10 * 1024 * 1024

// defaultAdapterName is the adapter a session with an empty Adapter belongs to
// (Claude Code, for backwards compatibility — see session.SessionState.Adapter).
const defaultAdapterName = "claude-code"

// DiagnosticsPaths are the resolved, IRRLICHT_HOME-aware locations the bundle
// reads from. The daemon computes them once at wiring time so the service stays
// free of path-resolution logic and is trivially testable with t.TempDir().
type DiagnosticsPaths struct {
	Home            string // user home, rewritten to "~" in redacted output
	InstancesDir    string // persisted session files (<id>.json)
	LedgerDir       string // per-session ledgers (*.ledger.json)
	LogsDir         string // event logs (events.log*)
	PermissionsFile string // consent state (permissions.json)
}

// DiagnosticsService assembles a redacted .tar.gz snapshot of daemon state for
// bug reports (#736). It streams to any io.Writer, so the same engine backs the
// GET /debug/bundle endpoint and the irrlichd --diagnose CLI. All file paths
// are injected via DiagnosticsPaths; the service performs no path resolution.
type DiagnosticsService struct {
	repo    outbound.SessionRepository
	obs     outbound.ProcessObserver
	isAlive func(int) bool
	agents  []agent.Agent
	cfg     config.Config
	version string
	paths   DiagnosticsPaths
	now     func() time.Time
}

// NewDiagnosticsService wires the service. isAlive is the per-PID liveness probe
// (processlifecycle.IsAlive in production); agents supply both the process
// matchers (for the full landscape) and the per-adapter infra-argv predicates
// (for the ghost-session diagnosis).
func NewDiagnosticsService(
	repo outbound.SessionRepository,
	obs outbound.ProcessObserver,
	isAlive func(int) bool,
	agents []agent.Agent,
	cfg config.Config,
	version string,
	paths DiagnosticsPaths,
) *DiagnosticsService {
	return &DiagnosticsService{
		repo:    repo,
		obs:     obs,
		isAlive: isAlive,
		agents:  agents,
		cfg:     cfg,
		version: version,
		paths:   paths,
		now:     time.Now,
	}
}

// HandleBundle serves the bundle over HTTP. It must be wrapped in localhostOnly
// by the caller — the bundle carries session paths and (pre-redaction) argv.
// The bundle is bounded, so it is built in memory: a build failure returns a
// clean 500 instead of a truncated download.
func (s *DiagnosticsService) HandleBundle(w http.ResponseWriter, r *http.Request) {
	var buf bytes.Buffer
	if err := s.WriteBundle(&buf); err != nil {
		http.Error(w, "failed to build diagnostics bundle", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition",
		fmt.Sprintf(`attachment; filename="irrlicht-diag-%s.tar.gz"`, fileSafe(s.version)))
	w.Header().Set("Content-Length", strconv.Itoa(buf.Len()))
	_, _ = w.Write(buf.Bytes())
}

// WriteBundle writes the gzip+tar bundle to w. Per-artifact failures are
// recorded into a collection-errors.txt entry rather than aborting the bundle —
// a partial snapshot still helps. Only a failure of the tar/gzip writer itself
// returns an error.
func (s *DiagnosticsService) WriteBundle(w io.Writer) error {
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	b := &bundleBuilder{tw: tw, red: NewRedactor(s.paths.Home), now: s.now()}

	b.addText("version.txt", s.versionText())
	b.addText("system.txt", systemText())
	b.addJSON("config.json", s.configView())
	b.addRawFile("permissions.json", s.paths.PermissionsFile)

	sessions, err := s.repo.ListAll()
	if err != nil {
		b.errf("sessions.json: ListAll: %v", err)
		sessions = nil
	}
	b.addJSON("state.json", stateView(sessions, b.now))
	b.addJSON("sessions.json", sessions)
	s.addInstances(b)
	s.addLedgers(b)
	b.addJSON("liveness.json", s.liveness(sessions, b.red))
	b.addJSON("processes.json", s.processes(b.red))
	s.addLogs(b)

	if errs := b.errs; len(errs) > 0 {
		b.addText("collection-errors.txt", strings.Join(errs, "\n")+"\n")
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}

// bundleBuilder accumulates tar entries, redacting every payload, and collects
// per-artifact errors for an in-band collection-errors.txt.
type bundleBuilder struct {
	tw   *tar.Writer
	red  *Redactor
	now  time.Time
	errs []string
}

func (b *bundleBuilder) errf(format string, a ...any) {
	b.errs = append(b.errs, fmt.Sprintf(format, a...))
}

func (b *bundleBuilder) addBytes(name string, data []byte) {
	hdr := &tar.Header{Name: name, Mode: 0o600, Size: int64(len(data)), ModTime: b.now}
	if err := b.tw.WriteHeader(hdr); err != nil {
		b.errf("%s: header: %v", name, err)
		return
	}
	if _, err := b.tw.Write(data); err != nil {
		b.errf("%s: write: %v", name, err)
	}
}

func (b *bundleBuilder) addText(name, s string) { b.addBytes(name, b.red.Bytes([]byte(s))) }

func (b *bundleBuilder) addJSON(name string, v any) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		b.errf("%s: marshal: %v", name, err)
		return
	}
	b.addBytes(name, b.red.Bytes(data))
}

// addRawFile copies a file verbatim (then redacted). A missing file is not an
// error — the artifact simply doesn't exist on this install.
func (b *bundleBuilder) addRawFile(name, path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			b.errf("%s: read %s: %v", name, path, err)
		}
		return
	}
	b.addBytes(name, b.red.Bytes(data))
}

func (s *DiagnosticsService) addInstances(b *bundleBuilder) {
	s.copyDir(b, s.paths.InstancesDir, "instances/", func(name string) bool {
		return strings.HasSuffix(name, ".json") && !strings.Contains(name, ".tmp")
	})
}

func (s *DiagnosticsService) addLedgers(b *bundleBuilder) {
	s.copyDir(b, s.paths.LedgerDir, "ledgers/", func(name string) bool {
		return strings.HasSuffix(name, ".ledger.json")
	})
}

// copyDir copies every matching top-level file from dir into the bundle under
// prefix. A missing dir is silently skipped; a readdir error is recorded.
func (s *DiagnosticsService) copyDir(b *bundleBuilder, dir, prefix string, match func(name string) bool) {
	if dir == "" {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !os.IsNotExist(err) {
			b.errf("%sreaddir %s: %v", prefix, dir, err)
		}
		return
	}
	for _, e := range entries {
		if e.IsDir() || !match(e.Name()) {
			continue
		}
		b.addRawFile(prefix+e.Name(), filepath.Join(dir, e.Name()))
	}
}

// addLogs writes a single, size-bounded events.log: the newest bytes across
// events.log and its rotations (events.log.1…5), emitted oldest-first.
func (s *DiagnosticsService) addLogs(b *bundleBuilder) {
	if s.paths.LogsDir == "" {
		return
	}
	names := []string{"events.log"}
	for i := 1; i <= 5; i++ {
		names = append(names, fmt.Sprintf("events.log.%d", i))
	}
	budget := maxBundleLogBytes
	var chunks [][]byte // newest-first
	for _, n := range names {
		if budget <= 0 {
			break
		}
		data, err := os.ReadFile(filepath.Join(s.paths.LogsDir, n))
		if err != nil {
			continue // a missing rotation is normal
		}
		if len(data) > budget {
			data = tailFromLineBoundary(data, budget)
			budget = 0
		} else {
			budget -= len(data)
		}
		chunks = append(chunks, data)
	}
	if len(chunks) == 0 {
		return
	}
	var buf bytes.Buffer
	for i := len(chunks) - 1; i >= 0; i-- { // oldest-first
		buf.Write(chunks[i])
	}
	b.addBytes("events.log", b.red.Bytes(buf.Bytes()))
}

// processInfo is the per-PID liveness/argv view shared by liveness.json (bound
// sessions) and processes.json (the full per-adapter landscape).
type processInfo struct {
	PID                   int      `json:"pid"`
	Alive                 bool     `json:"alive"`
	Argv                  []string `json:"argv,omitempty"`
	CWD                   string   `json:"cwd,omitempty"`
	IsInfraArgv           bool     `json:"is_infra_argv"`
	MatchesAdapterPattern bool     `json:"matches_adapter_pattern"`
}

func (s *DiagnosticsService) procInfo(pid int, exclude func([]string) bool, red *Redactor) processInfo {
	argv, _ := s.obs.ArgvOf(pid)
	cwd, _ := s.obs.CWDOf(pid)
	alive := s.isAlive(pid)
	isInfra := exclude != nil && exclude(argv)
	return processInfo{
		PID:                   pid,
		Alive:                 alive,
		Argv:                  red.Argv(argv),
		CWD:                   red.String(cwd),
		IsInfraArgv:           isInfra,
		MatchesAdapterPattern: alive && !isInfra,
	}
}

type livenessEntry struct {
	SessionID             string   `json:"session_id"`
	Adapter               string   `json:"adapter"`
	ClaimedPID            int      `json:"claimed_pid"`
	Alive                 bool     `json:"alive"`
	CurrentArgv           []string `json:"current_argv,omitempty"`
	CurrentCWD            string   `json:"current_cwd,omitempty"`
	IsInfraArgv           bool     `json:"is_infra_argv"`
	MatchesAdapterPattern bool     `json:"matches_adapter_pattern"`
}

// liveness reports, per PID-bound session, whether the claimed PID is still the
// session it was bound to — the direct #727 diagnosis: a live process whose
// argv the adapter rejects (is_infra_argv:true, matches_adapter_pattern:false)
// is a ghost binding.
func (s *DiagnosticsService) liveness(sessions []*session.SessionState, red *Redactor) []livenessEntry {
	excluder := s.excluderByAdapter()
	out := make([]livenessEntry, 0, len(sessions))
	for _, ss := range sessions {
		if ss.PID <= 0 {
			continue
		}
		info := s.procInfo(ss.PID, excluder(ss.Adapter), red)
		out = append(out, livenessEntry{
			SessionID:             ss.SessionID,
			Adapter:               ss.Adapter,
			ClaimedPID:            ss.PID,
			Alive:                 info.Alive,
			CurrentArgv:           info.Argv,
			CurrentCWD:            info.CWD,
			IsInfraArgv:           info.IsInfraArgv,
			MatchesAdapterPattern: info.MatchesAdapterPattern,
		})
	}
	return out
}

type adapterProcesses struct {
	Adapter   string        `json:"adapter"`
	Processes []processInfo `json:"processes"`
}

// processes enumerates every live process matching each adapter's matcher — the
// superset of liveness's bound PIDs. It catches unbound infra processes that
// became ghost pre-sessions (#644/#645), which the session-scoped view misses.
func (s *DiagnosticsService) processes(red *Redactor) []adapterProcesses {
	out := make([]adapterProcesses, 0, len(s.agents))
	for _, a := range s.agents {
		var pids []int
		switch m := a.Process.Match.(type) {
		case agent.ExactName:
			pids, _ = s.obs.FindByName(m.Name)
		case agent.CommandPattern:
			if m.Regex != nil {
				pids, _ = s.obs.FindByCmdline(m.Regex.String())
			}
		}
		if len(pids) == 0 {
			continue
		}
		sort.Ints(pids)
		procs := make([]processInfo, 0, len(pids))
		for _, pid := range pids {
			procs = append(procs, s.procInfo(pid, a.Process.ExcludeArgv, red))
		}
		out = append(out, adapterProcesses{Adapter: a.Identity.Name, Processes: procs})
	}
	return out
}

// excluderByAdapter resolves an adapter name to its infra-argv predicate (or
// nil if it declares none). An empty adapter name resolves to Claude Code.
func (s *DiagnosticsService) excluderByAdapter() func(adapter string) func([]string) bool {
	m := make(map[string]func([]string) bool, len(s.agents))
	for _, a := range s.agents {
		if a.Process.ExcludeArgv != nil {
			m[a.Identity.Name] = a.Process.ExcludeArgv
		}
	}
	return func(adapter string) func([]string) bool {
		if adapter == "" {
			adapter = defaultAdapterName
		}
		return m[adapter]
	}
}

func (s *DiagnosticsService) versionText() string {
	return fmt.Sprintf("irrlichd version %s\ngenerated %s\n",
		s.version, s.now().UTC().Format(time.RFC3339))
}

func systemText() string {
	host, _ := os.Hostname()
	return fmt.Sprintf("os: %s\narch: %s\ngo: %s\nnum_cpu: %d\nhostname: %s\n",
		runtime.GOOS, runtime.GOARCH, runtime.Version(), runtime.NumCPU(), host)
}

func (s *DiagnosticsService) configView() any {
	return struct {
		MaxSessionAge   string `json:"max_session_age"`
		ReadySessionTTL string `json:"ready_session_ttl"`
		PermissionMode  string `json:"permission_mode"`
	}{
		MaxSessionAge:   s.cfg.MaxSessionAge.String(),
		ReadySessionTTL: s.cfg.ReadySessionTTL.String(),
		PermissionMode:  s.cfg.PermissionMode,
	}
}

func stateView(sessions []*session.SessionState, now time.Time) any {
	view := struct {
		SessionCount int    `json:"session_count"`
		WorkingCount int    `json:"working_count"`
		WaitingCount int    `json:"waiting_count"`
		ReadyCount   int    `json:"ready_count"`
		GeneratedAt  string `json:"generated_at"`
	}{
		SessionCount: len(sessions),
		GeneratedAt:  now.UTC().Format(time.RFC3339),
	}
	for _, ss := range sessions {
		switch ss.State {
		case session.StateWorking:
			view.WorkingCount++
		case session.StateWaiting:
			view.WaitingCount++
		case session.StateReady:
			view.ReadyCount++
		}
	}
	return view
}

// tailFromLineBoundary returns the last n bytes of data, advanced to the first
// newline so the slice never begins mid-line. data shorter than n is returned
// whole.
func tailFromLineBoundary(data []byte, n int) []byte {
	if len(data) <= n {
		return data
	}
	tail := data[len(data)-n:]
	if i := bytes.IndexByte(tail, '\n'); i >= 0 && i+1 < len(tail) {
		tail = tail[i+1:]
	}
	return tail
}

// fileSafe makes a version string safe for a download filename.
func fileSafe(s string) string {
	if s == "" {
		return "unknown"
	}
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			return r
		default:
			return '-'
		}
	}, s)
}
