package services

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
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
	repo           outbound.SessionRepository
	obs            outbound.ProcessObserver
	isAlive        func(int) bool
	agents         []agent.Agent
	defaultAdapter string // adapter a session with an empty Adapter belongs to
	cfg            config.Config
	version        string
	paths          DiagnosticsPaths
	hookHealth     func() HookHealthSnapshot
	probeHealth    func() ProbeHealthSnapshot
}

// UnknownHookEvent is one (adapter, event name) pair a hook receiver was sent
// and did not recognize, with how many times it arrived (issue #1364). The name
// is half the key on purpose: an upstream rename arrives on every tool call
// forever, a stray local POST arrives once, and a count that cannot separate
// those says nothing.
type UnknownHookEvent struct {
	Adapter string `json:"adapter"`
	Event   string `json:"event"`
	Count   uint64 `json:"count"`
}

// HookHealthSnapshot is the receiver-side hook counters at one instant.
//
// It is a plain value handed in by the composition root rather than read from
// the adapter package, because application/services must not import
// adapters/inbound — the hexagonal rule core/architecture_test.go enforces. The
// daemon converts hookjson's snapshot into this; --diagnose supplies nothing.
type HookHealthSnapshot struct {
	// UnknownEvents is every retained (adapter, name) pair with its count.
	UnknownEvents []UnknownHookEvent
	// UnknownNamesDropped is how many sightings arrived, PER ADAPTER, after the
	// receivers' bounded name table saturated. A non-empty map means
	// UnknownEvents is incomplete for those adapters — reported, and attributed,
	// so a reader never has to infer either fact.
	UnknownNamesDropped map[string]uint64
	// UnknownEventsTotal is every unrecognized event seen, named or dropped, so
	// the total is read rather than reconstructed.
	UnknownEventsTotal uint64

	// Channels is the hook-liveness watchdog's per-adapter view (issue #1368):
	// which channels are being watched, how many requests each has delivered,
	// and which are currently treated as dead. It rides in the SAME snapshot as
	// the unrecognized-event counters, and hooks.json renders both in one
	// section, because they answer two halves of one question — "are hooks
	// arriving, and do we understand them" — and because a second nil-meaningful
	// seam would be a second thing to get wrong in the CLI case.
	Channels []HookChannelHealth

	// ReceiptsTotal is every consent-passed hook request this process was
	// handed, across all adapters.
	ReceiptsTotal uint64

	// EntryReverification is the periodic entry-presence loop's view (#1372):
	// per hook install, whether our entries are still in the agent's config,
	// how often they have had to be put back, and whether a repair storm is
	// being backed off from. It rides in this same snapshot, and renders into
	// the same hooks.json section, for the reason Channels does — a reader
	// looking at "hooks aren't working" needs all three diagnoses side by side,
	// and a fourth nil-meaningful seam would be a fourth thing to get wrong in
	// the --diagnose case.
	EntryReverification HookEntryReverifySnapshot
}

// ProbeCount is one bounded child process the daemon runs, with how often it
// ANSWERED, how often it did not, and how often a memo answered for it without
// starting a child at all (issue #1534).
//
// Six issues in this family — #1485, #1492, #1513, #1524, #1533, #1537 — each
// fixed one probe that collapsed "I could not look" into "I looked and there
// was nothing", and each closed with the same footer: nothing measures how
// often it actually happens. Three of them fail OPEN, so a probe failing
// constantly and one that never fails produce the same observable daemon.
// Unanswered is the number that was missing.
type ProbeCount struct {
	// Probe is the per-call-site kind token, e.g. "lsof.herdr_clients". Per
	// call site rather than per tool: `lsof` answers three different questions
	// here and `ps` two, and a count that cannot say WHICH stopped being
	// answered is the single-scalar failure #1364 already paid for.
	Probe string `json:"probe"`
	// Answered is how many children ran to a normal exit.
	Answered uint64 `json:"answered"`
	// Unanswered is how many were killed (the 2s ceiling, an OOM kill), never
	// started, or failed to fork.
	Unanswered uint64 `json:"unanswered"`
	// MemoHits is how many calls a memo served without starting a child
	// (#1544). Only two probes have a memo, so a zero here is a finding of
	// nothing rather than an inability to look.
	MemoHits uint64 `json:"memo_hits"`
}

// ProbeHealthSnapshot is the probe counters at one instant.
//
// Like HookHealthSnapshot it is a plain value handed in by the composition
// root, because application/services must not import adapters/inbound — the
// hexagonal rule core/architecture_test.go enforces. The daemon converts
// processlifecycle's snapshot into this; --diagnose supplies nothing.
type ProbeHealthSnapshot struct {
	// Probes is every declared probe kind, including one that never ran: a
	// zero row is itself evidence (a missing tool, a daemon that never
	// resolved a host) and omitting it would leave a reader unable to tell
	// "never ran" from "no such probe in this build".
	Probes []ProbeCount
	// OutcomeRule is the counting rule, taken from the package that does the
	// counting rather than restated here — a definition written twice is one
	// that can describe behaviour it no longer has.
	OutcomeRule string
	// UndeclaredKinds is how many observations named a probe kind nobody
	// declared. Non-zero means Probes is INCOMPLETE.
	UndeclaredKinds uint64
	// HerdrCandidatesProbed and HerdrCandidatesAbandonedOnBudget are #1558's
	// figures: how many attached-client candidates the herdr host loop asked
	// about, and how many times it stopped early because the aggregate budget
	// was already spent. Not probe rows — no child process is involved — so
	// they are reported beside them rather than as an outcome.
	HerdrCandidatesProbed            uint64
	HerdrCandidatesAbandonedOnBudget uint64
	// HostGate is #1525's figures: what the #784 host-admission gate decided,
	// per outcome. Not probe rows either, and carried here rather than in a
	// snapshot of their own because they are the DOWNSTREAM view of the same
	// events — a walk aborts because a probe did not answer, so the cause and
	// the effect belong in one artifact and one nil-meaningful seam. hooksView
	// already argues that a fourth such seam is a fourth thing to get wrong in
	// the --diagnose case.
	HostGate []HostGateOutcomeCount
	// HostGateOutcomeRule is what the outcome tokens mean, taken from the
	// package that decides them for the reason OutcomeRule above is.
	HostGateOutcomeRule string
	// UndeclaredHostGateOutcomes is how many evaluations named an outcome
	// nobody declared. Non-zero means HostGate is INCOMPLETE.
	UndeclaredHostGateOutcomes uint64
}

// HostGateOutcomeCount is one outcome of the #784 host-admission gate and how
// often it happened (issue #1525).
//
// The gate has four outcomes on macOS and only one of them left any trace: a
// completed walk that found no allow-listed host logs a line (that is #784
// working), while a completed walk that DID find one, and a walk that reached no
// verdict at all, were both silent. The second silence is the expensive one —
// since #1513 a walk that reached no verdict ADMITS, so the gate quietly stops
// gating and a session admitted on no evidence has nothing anywhere to explain
// it. Since #1574 that case is TWO rows, because the two causes call for
// different responses: a probe that did not answer is a machine to look at, a
// process that no longer exists is a race to expect.
type HostGateOutcomeCount struct {
	// Outcome is the token, e.g. "admitted.walk_aborted". Its prefix is the
	// verdict, so the table reads without a legend.
	Outcome string `json:"outcome"`
	// Count is how many evaluations ended that way.
	Count uint64 `json:"count"`
}

// DiagnosticsServiceDeps bundles NewDiagnosticsService's dependencies.
type DiagnosticsServiceDeps struct {
	Repo    outbound.SessionRepository
	Obs     outbound.ProcessObserver
	IsAlive func(int) bool // per-PID liveness probe (processlifecycle.IsAlive in production)
	Agents  []agent.Agent  // supplies both process matchers (full landscape) and per-adapter infra-argv predicates (ghost-session diagnosis)
	// DefaultAdapter is the adapter an empty-Adapter session belongs to
	// (claudecode.AdapterName), injected so the application layer needn't
	// import the inbound adapter.
	DefaultAdapter string
	Cfg            config.Config
	Version        string
	Paths          DiagnosticsPaths
	// HookHealth snapshots the live hook-receiver counters. NIL IS MEANINGFUL:
	// it says this process never served a hook (the --diagnose CLI), and
	// hooks.json then omits the counts and says where the real ones are, rather
	// than publishing zeros that look like an all-clear.
	HookHealth func() HookHealthSnapshot
	// ProbeHealth snapshots the in-process probe counters (#1534). NIL IS
	// MEANINGFUL and means something SHARPER than HookHealth's nil: the
	// --diagnose CLI does run probes — collecting processes.json shells out to
	// pgrep and lsof through the observer — so its counters are not
	// structurally zero, they are a handful of numbers describing that CLI's
	// own bundle collection. Publishing those under the same field names as
	// the daemon's would be worse than publishing zeros, because they look
	// plausible. probesView omits them and says so.
	ProbeHealth func() ProbeHealthSnapshot
}

// NewDiagnosticsService wires the service.
func NewDiagnosticsService(deps DiagnosticsServiceDeps) *DiagnosticsService {
	return &DiagnosticsService{
		repo:           deps.Repo,
		obs:            deps.Obs,
		isAlive:        deps.IsAlive,
		agents:         deps.Agents,
		defaultAdapter: deps.DefaultAdapter,
		cfg:            deps.Cfg,
		version:        deps.Version,
		paths:          deps.Paths,
		hookHealth:     deps.HookHealth,
		probeHealth:    deps.ProbeHealth,
	}
}

// WriteBundle writes the gzip+tar bundle to w. Per-artifact failures are
// recorded into a collection-errors.txt entry rather than aborting the bundle —
// a partial snapshot still helps. Only a failure of the tar/gzip writer itself
// returns an error.
func (s *DiagnosticsService) WriteBundle(w io.Writer) error {
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	b := &bundleBuilder{tw: tw, red: NewRedactor(s.paths.Home), now: time.Now()}

	b.addText("version.txt", s.versionText(b.now))
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
	b.addJSON("hooks.json", s.hooksView())
	b.addJSON("probes.json", s.probesView())
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

// addLogs writes a single, size-bounded events.log into the bundle.
func (s *DiagnosticsService) addLogs(b *bundleBuilder) {
	if data := gatherTrimmedLog(s.paths.LogsDir, maxBundleLogBytes); data != nil {
		b.addBytes("events.log", b.red.Bytes(data))
	}
}

// gatherTrimmedLog returns at most budget bytes of event log: the newest bytes
// across events.log and its rotations (events.log.1…5), concatenated
// oldest-first. The newest file wins the budget first; when a file overflows
// the remaining budget, only its newest tail (from a line boundary) is kept.
// Returns nil when logsDir is empty or no log files exist.
func gatherTrimmedLog(logsDir string, budget int) []byte {
	if logsDir == "" {
		return nil
	}
	names := []string{"events.log"}
	for i := 1; i <= 5; i++ {
		names = append(names, fmt.Sprintf("events.log.%d", i))
	}
	var chunks [][]byte // newest-first
	for _, n := range names {
		if budget <= 0 {
			break
		}
		data, err := os.ReadFile(filepath.Join(logsDir, n))
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
		return nil
	}
	var buf bytes.Buffer
	for i := len(chunks) - 1; i >= 0; i-- { // oldest-first
		buf.Write(chunks[i])
	}
	return buf.Bytes()
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
			adapter = s.defaultAdapter
		}
		return m[adapter]
	}
}

func (s *DiagnosticsService) versionText(now time.Time) string {
	return fmt.Sprintf("irrlichd version %s\ngenerated %s\n",
		s.version, now.UTC().Format(time.RFC3339))
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

// hooksView renders hooks.json: what the daemon's hook receivers were sent and
// did not recognize (issue #1364).
//
// The honesty problem this section has to solve is structural, not cosmetic.
// The counters live in the daemon process, and `irrlichd --diagnose` builds its
// bundle in a SEPARATE, freshly launched process that resolves the same stores
// off disk and never serves a hook (see runDiagnose's doc comment). Reporting
// its zeros as counts would tell a bug reporter "no unrecognized events" using
// numbers from a process that could not have seen one — the exact silence #1364
// is about, re-created inside the fix for it. So when no snapshot source is
// wired the counts are OMITTED and the section says where the real ones are.
//
// The log line is what covers that gap in practice: the first sighting of each
// distinct name is written at error level, and events.log IS in this bundle, so
// even the CLI-collected form carries the names — just not the volumes.
func (s *DiagnosticsService) hooksView() any {
	if s.hookHealth == nil {
		// A DIFFERENT shape, not the daemon shape with zeros in it. Emitting
		// `"unknown_event_names_dropped": 0` here would publish a count from a
		// process that could not have observed one — the very thing the note
		// beside it says is not being done.
		return struct {
			CollectedFrom     string `json:"collected_from"`
			Note              string `json:"note"`
			LivenessNote      string `json:"liveness_note"`
			EntryReverifyNote string `json:"entry_reverification_note"`
		}{
			EntryReverifyNote: "The hook-entry re-verification loop (#1372) is omitted here for the same reason: " +
				"it runs in the daemon, so this process has performed no passes and its counters are " +
				"structurally zero — publishing them would report 'entries verified present' from a process " +
				"that never looked. Its repairs ARE logged, so events.log in this bundle still carries any " +
				"`hook entries were ... and have been re-installed` line, and a repair that failed is on the " +
				"permission as effect_error in permissions.json. For the live view, fetch GET /debug/bundle " +
				"from the running daemon.",
			CollectedFrom: "cli",
			Note: "Collected by `irrlichd --diagnose`, which runs in a process that never served a hook, " +
				"so the receivers' unrecognized-event counters are not readable here and are omitted rather than " +
				"reported as zero. The FIRST sighting of each unrecognized event name is logged at error level, so " +
				"events.log in this bundle still names them. For live counts, fetch GET /debug/bundle from the " +
				"running daemon.",
			LivenessNote: "The per-adapter hook-liveness watchdog (#1368) is omitted here for the same reason and " +
				"one more: this process served no hooks AND observed no turns, so both sides of the ratio it " +
				"reports are structurally absent — a channel would read as silent with zero turns behind it, which " +
				"is an accusation, not a measurement. Its verdicts are logged, so events.log in this bundle still " +
				"carries any `hook channel for <adapter> looks dead` line. For the live view, fetch " +
				"GET /debug/bundle from the running daemon.",
		}
	}

	snap := s.hookHealth()
	// Copied before sorting: the slice belongs to the injected snapshot source,
	// not to this service, and reordering a caller's slice in place is the
	// aliasing bug this repo has already paid for once (#965/#967/#975).
	events := append([]UnknownHookEvent(nil), snap.UnknownEvents...)
	sort.Slice(events, func(i, j int) bool {
		if events[i].Adapter != events[j].Adapter {
			return events[i].Adapter < events[j].Adapter
		}
		return events[i].Event < events[j].Event
	})
	// Not copied, unlike events above, and not sorted: Snapshot allocates a
	// fresh slice per call and owes a stable order to its own callers, so there
	// is nothing here to alias and re-sorting would be a second copy of one
	// comparator that no test could see go stale.
	channels := snap.Channels
	reverify := snap.EntryReverification
	return struct {
		CollectedFrom            string                     `json:"collected_from"`
		HookRequestsReceived     uint64                     `json:"hook_requests_received"`
		Channels                 []HookChannelHealth        `json:"channels,omitempty"`
		SilentChannelNote        string                     `json:"silent_channel_note,omitempty"`
		EntryReverification      []HookEntryHealth          `json:"entry_reverification,omitempty"`
		EntryReverifyOutcomes    map[ReverifyOutcome]uint64 `json:"entry_reverification_outcomes,omitempty"`
		EntryReverifyNote        string                     `json:"entry_reverification_note,omitempty"`
		UnknownEventsTotal       uint64                     `json:"unknown_events_total"`
		UnknownEvents            []UnknownHookEvent         `json:"unknown_events,omitempty"`
		UnknownEventNamesDropped map[string]uint64          `json:"unknown_event_names_dropped_by_adapter,omitempty"`
	}{
		CollectedFrom:            "daemon",
		HookRequestsReceived:     snap.ReceiptsTotal,
		Channels:                 channels,
		SilentChannelNote:        silentChannelNote(channels),
		EntryReverification:      reverify.Targets,
		EntryReverifyOutcomes:    reverify.Outcomes,
		EntryReverifyNote:        entryReverificationNote(reverify.Targets),
		UnknownEventsTotal:       snap.UnknownEventsTotal,
		UnknownEvents:            events,
		UnknownEventNamesDropped: snap.UnknownNamesDropped,
	}
}

// probesView renders probes.json: how often each bounded child process the
// daemon runs actually ANSWERED (issue #1534).
//
// It is a sibling of hooks.json rather than a section inside it. The two answer
// different questions of different subjects — what arrived at our HTTP
// endpoints, versus what came back from the children we start — and the one
// thing they share is the honesty problem below, which is a reason to draw the
// seam the same way, not to merge the artifacts.
//
// That honesty problem is SHARPER here than for hooks, and getting it wrong
// would be worse. `irrlichd --diagnose` builds its bundle in a separate,
// freshly launched process, and unlike the hook case that process is not
// structurally quiet: collecting processes.json walks every adapter's matcher
// through the observer, which on macOS is `pgrep` and `lsof`. So its counters
// are non-zero — a handful of probes, run by the bundle collector itself, in
// the last second. Published under the daemon's field names they would read as
// a measurement of the daemon's probe health, and they would look plausible
// enough that nobody would check. Zeros at least announce themselves. So when
// no snapshot source is wired the counts are OMITTED and the section says both
// that they are missing and why the numbers this process could have printed
// would not have been the ones asked for.
func (s *DiagnosticsService) probesView() any {
	if s.probeHealth == nil {
		// A DIFFERENT shape, not the daemon shape with this CLI's own numbers
		// (or zeros) in it — the same rule hooksView follows, for a reason that
		// is one step stronger. See the doc comment above.
		return struct {
			CollectedFrom string `json:"collected_from"`
			Note          string `json:"note"`
			HostGateNote  string `json:"host_gate_note"`
		}{
			CollectedFrom: "cli",
			Note: "Collected by `irrlichd --diagnose`, which is not the process that runs the daemon's " +
				"probes, so the per-probe answered/unanswered counters are omitted rather than reported. " +
				"Note they are NOT zero in this process: building processes.json shells out to pgrep and " +
				"lsof through the same observer, so this process has a few counts of its own, describing " +
				"nothing but its own bundle collection — which is why they are omitted rather than printed. " +
				"For the daemon's real counts, fetch GET /debug/bundle from the running daemon.",
			// A DIFFERENT reason from the probe rows above it, in the same
			// section, and saying which is the point. The probe counters are
			// non-zero-but-irrelevant here; the host-gate counters are
			// structurally zero, because runDiagnose never builds a
			// SessionDetector and therefore never calls SetHostGate, so no code
			// path in this process can evaluate the gate even once. Copying the
			// sentence above would teach a reader that this process runs the
			// gate, and copying hooks.json's would be right by accident.
			HostGateNote: "The #784 host-gate outcome counters (#1525) are omitted here too, for a DIFFERENT reason " +
				"than the probe counters above: `irrlichd --diagnose` never builds a session detector, so it never " +
				"installs the gate and cannot evaluate it — these counters are structurally zero in this process, " +
				"not small-and-irrelevant. The gate's aborted-walk line IS logged, so events.log in this bundle still " +
				"carries any `#784 host gate ADMITTED this session` line. For the counts, fetch GET /debug/bundle " +
				"from the running daemon.",
		}
	}

	snap := s.probeHealth()
	// Copied before sorting, for the reason hooksView copies its events: the
	// slice belongs to the injected snapshot source, and reordering a caller's
	// slice in place is the aliasing bug this repo has already paid for once
	// (#965/#967/#975).
	probes := append([]ProbeCount(nil), snap.Probes...)
	sort.Slice(probes, func(i, j int) bool { return probes[i].Probe < probes[j].Probe })
	var totalUnanswered uint64
	for _, p := range probes {
		totalUnanswered += p.Unanswered
	}
	// Copied for the reason probes is, and sorted by the same argument: two
	// captures of one daemon must diff cleanly.
	hostGate := append([]HostGateOutcomeCount(nil), snap.HostGate...)
	sort.Slice(hostGate, func(i, j int) bool { return hostGate[i].Outcome < hostGate[j].Outcome })
	return struct {
		CollectedFrom       string                 `json:"collected_from"`
		OutcomeRule         string                 `json:"outcome_rule"`
		Probes              []ProbeCount           `json:"probes"`
		TotalUnanswered     uint64                 `json:"total_unanswered"`
		UndeclaredKinds     uint64                 `json:"undeclared_probe_kinds,omitempty"`
		HerdrProbed         uint64                 `json:"herdr_client_candidates_probed"`
		HerdrAbandoned      uint64                 `json:"herdr_client_candidates_abandoned_on_budget"`
		HostGate            []HostGateOutcomeCount `json:"host_gate"`
		HostGateRule        string                 `json:"host_gate_outcome_rule"`
		HostGateAbortNote   string                 `json:"host_gate_aborted_walk_note,omitempty"`
		HostGateReconcile   string                 `json:"host_gate_reconciliation_note,omitempty"`
		HostGateUndeclared  uint64                 `json:"undeclared_host_gate_outcomes,omitempty"`
		HostGateUndeclNote  string                 `json:"undeclared_host_gate_outcomes_note,omitempty"`
		HostGatePlatformNte string                 `json:"host_gate_platform_note,omitempty"`
		UnansweredNote      string                 `json:"unanswered_note,omitempty"`
		UndeclaredNote      string                 `json:"undeclared_probe_kinds_note,omitempty"`
		HerdrAbandonNote    string                 `json:"herdr_abandonment_note,omitempty"`
		MemoNote            string                 `json:"memo_note"`
		PlatformNote        string                 `json:"platform_note,omitempty"`
	}{
		CollectedFrom:       "daemon",
		OutcomeRule:         snap.OutcomeRule,
		Probes:              probes,
		TotalUnanswered:     totalUnanswered,
		UndeclaredKinds:     snap.UndeclaredKinds,
		HerdrProbed:         snap.HerdrCandidatesProbed,
		HerdrAbandoned:      snap.HerdrCandidatesAbandonedOnBudget,
		HostGate:            hostGate,
		HostGateRule:        snap.HostGateOutcomeRule,
		HostGateAbortNote:   hostGateAbortedWalkNote(hostGate),
		HostGateReconcile:   hostGateReconciliationNote(hostGate, probes),
		HostGateUndeclared:  snap.UndeclaredHostGateOutcomes,
		HostGateUndeclNote:  undeclaredHostGateOutcomesNote(snap.UndeclaredHostGateOutcomes),
		HostGatePlatformNte: hostGatePlatformNote(),
		UnansweredNote:      unansweredProbeNote(probes),
		UndeclaredNote:      undeclaredProbeKindsNote(snap.UndeclaredKinds),
		HerdrAbandonNote:    herdrAbandonmentNote(snap.HerdrCandidatesAbandonedOnBudget),
		MemoNote: "memo_hits are calls a memo answered without starting a child (#1544), so they are NOT " +
			"included in answered/unanswered — answered+unanswered is how often a child actually ran, and " +
			"answered+unanswered+memo_hits is how often the probe was asked. Only ps.proc_info and " +
			"plutil.bundle_id have a memo; a zero elsewhere means there is no memo, not that it never hit.",
		PlatformNote: probePlatformNote(),
	}
}

// hostGateOutcomeCount pulls one outcome's count out of a snapshot. A token the
// snapshot does not carry reads as zero, which is right: the rows come from the
// package that declares them, so an absent row is a build without that outcome.
func hostGateOutcomeCount(rows []HostGateOutcomeCount, outcome string) uint64 {
	for _, row := range rows {
		if row.Outcome == outcome {
			return row.Count
		}
	}
	return 0
}

// Host-gate outcome tokens, as this layer reads them out of the snapshot.
//
// These are strings rather than an import because application/services must not
// import the inbound adapter that declares them (core/architecture_test.go).
// The copy is deliberate and narrow: it is used only to pick the two rows the
// notes below are about, never to decide anything the daemon acts on, and the
// gate's own verdict is derived from the outcome in the package that owns it —
// so a rename here can lose a note, and can never change an admission.
const (
	hostGateOutcomeWalkAborted  = "admitted.walk_aborted"
	hostGateOutcomeProcessGone  = "admitted.process_gone"
	hostGateOutcomeNotEvaluated = "admitted.not_evaluated"
)

// hostGateAbortedWalkNote fires only when the #784 gate actually admitted a
// session on a walk it could not complete — the figure #1513 and #1524 both
// closed as unmeasured, and the reason this section exists (#1525).
func hostGateAbortedWalkNote(rows []HostGateOutcomeCount) string {
	aborted := hostGateOutcomeCount(rows, hostGateOutcomeWalkAborted)
	if aborted == 0 {
		return ""
	}
	var evaluated uint64
	for _, row := range rows {
		if row.Outcome != hostGateOutcomeNotEvaluated {
			evaluated += row.Count
		}
	}
	return fmt.Sprintf("The #784 host-admission gate admitted %d of %d evaluated session(s) on an ancestry walk it "+
		"could NOT complete. Each of those was admitted on no evidence: the walk did not report a non-interactive "+
		"host, it reported nothing at all, and admitting is the deliberate direction (#1513) because rejecting on an "+
		"unanswered probe declines a legitimate session for the lifetime of the daemon. If a session appeared that "+
		"nobody started interactively, these are the candidates, and events.log names them (component host-gate). "+
		"This is a different row from rejected.no_known_host, which is a walk that RAN and found no allow-listed "+
		"terminal or IDE — that one is #784 working.", aborted, evaluated)
}

// hostGateReconciliationNote cross-checks #1525's aborted walks against
// #1534's probe non-answers, which is the pair of numbers neither issue could
// compare on its own.
//
// A walk aborts because readProcInfo or bundleIDForAppPath returned an error,
// and both are bounded shellouts counted by ps.proc_info and plutil.bundle_id.
// Until #1574 the two figures were not comparable at all: readProcInfo
// classified an ANSWERED "no such process" (`ps` exit 1 for a reaped pid) as a
// probe failure, so a walk could abort with the probe counter recording perfect
// health, and this note existed mostly to stop a reader concluding the counters
// were broken. That reason is gone — a gone process now reaches its own row —
// so the note reports a comparison that is meant to hold, and says which way to
// read it when it does not.
func hostGateReconciliationNote(rows []HostGateOutcomeCount, probes []ProbeCount) string {
	aborted := hostGateOutcomeCount(rows, hostGateOutcomeWalkAborted)
	gone := hostGateOutcomeCount(rows, hostGateOutcomeProcessGone)
	if aborted == 0 && gone == 0 {
		return ""
	}
	var nonAnswers uint64
	for _, p := range probes {
		if p.Probe == "ps.proc_info" || p.Probe == "plutil.bundle_id" {
			nonAnswers += p.Unanswered
		}
	}
	note := fmt.Sprintf("Cross-check: %d aborted walk(s) against %d unanswered ps.proc_info/plutil.bundle_id "+
		"probe(s), the only two children a walk can abort on. ", aborted, nonAnswers)
	if gone > 0 {
		note += fmt.Sprintf("The %d admitted.process_gone walk(s) are NOT part of that comparison and need no "+
			"non-answer behind them (#1574): there `ps` ANSWERED that a process in the chain no longer exists, which "+
			"is the ordinary race between PID discovery and a short-lived agent process rather than a probe that "+
			"failed. ", gone)
	}
	if aborted > 0 && nonAnswers == 0 {
		return note + "Zero non-answers beside a non-zero ABORT count is worth a second look: since #1574 an " +
			"answered \"no such process\" is counted as admitted.process_gone above, so an aborted walk means a `ps` " +
			"or `plutil` was killed, never started or failed to fork — or, rarely, that a `ps` answered with a line " +
			"this daemon could not parse, the one remaining way an answered probe still ends a walk."
	}
	return note + "The two are not required to be equal in either direction: one walk reads several ancestors, so " +
		"one abort can follow several non-answers, and a probe can be run by something other than this gate. " +
		"Non-answers far exceeding aborts means the failing probe is mostly being run elsewhere."
}

// undeclaredHostGateOutcomesNote fires only when an evaluation named an outcome
// nothing declared — which means the host_gate rows are incomplete, and a
// reader must not have to work that out by noticing they do not add up.
func undeclaredHostGateOutcomesNote(n uint64) string {
	if n == 0 {
		return ""
	}
	return fmt.Sprintf("%d host-gate evaluation(s) named an outcome that is not in the declared set, so the "+
		"host_gate rows above are INCOMPLETE and no row accounts for them. This is a wiring defect in the "+
		"daemon, not a machine condition.", n)
}

// hostGatePlatformNote says out loud that ancestry walking is macOS-only, so a
// non-darwin bundle's admitted.not_evaluated row reads as "this platform
// declines to look" rather than as a gate that admitted everything.
func hostGatePlatformNote() string {
	if runtime.GOOS == "darwin" {
		return ""
	}
	return "The #784 gate walks process ancestry, which is implemented on macOS only — the exclusion signal it " +
		"backs (a menu-bar app keeping an agent CLI alive for quota polling) does not exist on " + runtime.GOOS +
		". So every row here but admitted.not_evaluated is structurally zero, and that row is the gate reporting " +
		"that it declined to look rather than a verdict it reached."
}

// unansweredProbeNote explains what a non-zero unanswered count means, and —
// like silentChannelNote beside it — what it does not. Emitted only when
// something actually went unanswered, so a healthy bundle keeps the terse rows
// and no essay.
func unansweredProbeNote(probes []ProbeCount) string {
	var failing []string
	for _, p := range probes {
		if p.Unanswered > 0 {
			failing = append(failing, fmt.Sprintf("%s (%d of %d runs)", p.Probe, p.Unanswered, p.Answered+p.Unanswered))
		}
	}
	if len(failing) == 0 {
		return ""
	}
	return "Probe(s) that did not answer: " + strings.Join(failing, ", ") +
		". A non-answer is a child killed by its 2-second ceiling, killed by something else, or never " +
		"started — never a tool that ran and reported nothing, which is an ANSWER and is counted as one. " +
		"This matters most where the caller fails OPEN: IsKnownInteractiveHost (#1513/#1524) admits a " +
		"session whose ancestry walk it could not complete, so a climbing count on ps.proc_info or " +
		"plutil.bundle_id means the #784 admission gate is quietly not gating. Where the caller degrades " +
		"instead (#1485/#1492/#1537), the cost is a host-window enrichment that stops resolving rather " +
		"than a wrong one. Measured on one machine plutil stayed ~25x under its ceiling even at 12x CPU " +
		"oversubscription, so if these counts are non-zero the trigger is something other than CPU " +
		"pressure and this bundle is the first evidence of it."
}

// undeclaredProbeKindsNote fires only when the counters saw a probe kind
// nothing declared — which means the rows above are incomplete, and a reader
// must not have to work that out by noticing they do not add up.
func undeclaredProbeKindsNote(n uint64) string {
	if n == 0 {
		return ""
	}
	return fmt.Sprintf("%d probe observation(s) named a kind that is not in the declared set, so the "+
		"probes list above is INCOMPLETE and no row accounts for them. This is a wiring defect in the "+
		"daemon, not a machine condition.", n)
}

// herdrAbandonmentNote fires only when the herdr client loop actually stopped
// early on its aggregate budget (#1558). It is the one figure in this file
// whose absence was the whole reason the issue could not be decided.
func herdrAbandonmentNote(n uint64) string {
	if n == 0 {
		return ""
	}
	return fmt.Sprintf("The herdr client loop abandoned its remaining candidates %d time(s) because the "+
		"aggregate budget (#1529) was already spent — normally because the lsof scan ahead of it ran "+
		"slowly and still SUCCEEDED, which is the one window that bound does not cover. The answer is "+
		"then \"I could not look\", which clears no stored host and is re-probed on the next sweep, so "+
		"this is a deferred host-window recovery rather than a wrong answer (#1558).", n)
}

// probePlatformNote says out loud that these probes are macOS-only, so an
// all-zero table on another platform reads as "this build starts no children"
// rather than as a broken counter.
func probePlatformNote() string {
	if runtime.GOOS == "darwin" {
		return ""
	}
	return "Every probe listed here is a macOS bounded shellout. On " + runtime.GOOS +
		" the daemon reads the same process facts from /proc without starting a child, so these counters " +
		"are structurally zero — that is this platform having no such probe, not a probe that never ran."
}

// entryReverificationNote spells out what the entry_reverification rows mean
// (issue #1372) — and, like silentChannelNote beside it, what they do not.
//
// Emitted only when there is something to say: a repair has happened, or a
// target is currently damaged, or a config could not be read. A bundle from a
// healthy machine keeps the terse rows and no essay.
//
// The three-way disambiguation is repeated here rather than cross-referenced
// because the two notes are read independently — a reader lands on whichever
// one fired — and the whole failure this family of issues is about is that the
// same symptom has three causes with three different first moves.
func entryReverificationNote(rows []HookEntryHealth) string {
	var repaired, damaged, unreadable []string
	for _, r := range rows {
		// Keyed off the OUTCOME, never off the residue. Missing/Stale record
		// what the last verdict FOUND and survive a successful repair until the
		// next intact pass clears them — which is at least one back-off window
		// later — so classifying on their length alone reports every repaired
		// target as "the repair has not yet succeeded" for up to an hour after
		// it did.
		switch r.LastOutcome {
		case string(ReverifyUnreadable):
			unreadable = append(unreadable, r.Adapter)
		case string(ReverifyRepairFailed):
			damaged = append(damaged, r.Adapter)
		}
		if r.Repairs > 0 {
			repaired = append(repaired, fmt.Sprintf("%s (%d)", r.Adapter, r.Repairs))
		}
	}
	if len(repaired) == 0 && len(damaged) == 0 && len(unreadable) == 0 {
		return ""
	}

	note := "irrlicht re-verifies that its hook entries are still present in each agent's own config, " +
		"on a timer, and re-installs them when they are not (#1372). "
	if len(repaired) > 0 {
		note += "Re-installed this session: " + strings.Join(repaired, ", ") + ". " +
			"A non-zero count means something OUTSIDE irrlicht removed or rewrote those entries after " +
			"the user granted the permission — an agent whose own settings UI syncs by omission deletes " +
			"every key it did not know about on any config change, and gemini-cli is confirmed to do " +
			"exactly that. A count that keeps climbing is that, happening repeatedly; the loop backs off " +
			"exponentially rather than fighting it, so consecutive_repairs and backoff_seconds are the " +
			"fields to read. "
	}
	if len(damaged) > 0 {
		note += "Repair FAILED for: " + strings.Join(damaged, ", ") +
			" — entries are missing or stale and the re-install errored, so they are still not " +
			"installed. The reason is on the permission as effect_error in permissions.json, and " +
			"re-granting the permission in the wizard retries it. "
	}
	if len(unreadable) > 0 {
		note += "Unreadable: " + strings.Join(unreadable, ", ") +
			" — the config could not be parsed, so nothing was written; the installer refuses to " +
			"overwrite a file it cannot read, and this needs a human. "
	}
	note += "This section is about whether the entries EXIST. It is a different question from " +
		"whether anything is arriving through them, which is the channels/silent_channel_note above " +
		"(#1368), and from an install that FAILED and never wrote entries at all, which shows up as " +
		"effect_error on the permission (#1362). A repair that fails is recorded there too, so a " +
		"repair_failed count here has its reason on the permission. " +
		"watched:false means the permission is not currently granted, so nothing is read or written " +
		"for that row at all — not that the entries are gone."
	return note
}

// silentChannelNote spells out what a `"silent": true` row means, and — just as
// importantly — what it does not (issue #1368).
//
// It is emitted only when a channel is actually silent, so the healthy bundle
// stays terse. The three-way disambiguation is the point: the same
// user-visible symptom ("hooks aren't working") has three causes with three
// different first moves, and a bug reporter pasting this file should not have
// to know which issue number owns which.
func silentChannelNote(channels []HookChannelHealth) string {
	var silent []string
	for _, c := range channels {
		if c.Silent {
			silent = append(silent, c.Adapter)
		}
	}
	if len(silent) == 0 {
		return ""
	}
	return "Hook channel(s) " + strings.Join(silent, ", ") + " delivered nothing across the watchdog's turn " +
		"threshold while consent was granted and the install reported success, so they were demoted to the " +
		"transcript tier and any hook-tier holds they had placed were released (#1368). This says entries were " +
		"WRITTEN and nothing is arriving through them. It does NOT mean the entries are still present — if " +
		"something removed them since, this is how that looks too, and the entry_reverification rows below are " +
		"what tell those apart (#1372): a row with repairs:0 and no missing/stale means the entries WERE there " +
		"at the last pass, so this really is a dead channel and not a clobbered config. And it is a different " +
		"fault from an install that FAILED, which never wrote entries at all and shows up as effect_error on " +
		"the permission rather than here (#1362). Check the daemon's bind port next."
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
