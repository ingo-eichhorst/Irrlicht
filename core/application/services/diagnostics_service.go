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
		switch {
		case r.LastOutcome == string(ReverifyUnreadable):
			unreadable = append(unreadable, r.Adapter)
		case len(r.Missing) > 0 || len(r.Stale) > 0:
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
		note += "Currently damaged: " + strings.Join(damaged, ", ") +
			" — entries are missing or stale as of the last pass and the repair has not yet succeeded. "
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
