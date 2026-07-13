package main

import (
	"context"
	"fmt"
	"mime"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	activationhandler "irrlicht/core/adapters/inbound/activation"
	"irrlicht/core/adapters/inbound/agents"
	"irrlicht/core/adapters/inbound/agents/claudecode"
	"irrlicht/core/adapters/inbound/agents/processlifecycle"
	backchannelhandler "irrlicht/core/adapters/inbound/backchannel"
	gastownadapter "irrlicht/core/adapters/inbound/orchestrators/gastown"
	permissionshandler "irrlicht/core/adapters/inbound/permissions"
	sessionshandler "irrlicht/core/adapters/inbound/sessions"
	"irrlicht/core/adapters/outbound/control"
	"irrlicht/core/adapters/outbound/filesystem"
	"irrlicht/core/adapters/outbound/git"
	"irrlicht/core/adapters/outbound/mdns"
	"irrlicht/core/adapters/outbound/metrics"
	"irrlicht/core/adapters/outbound/recorder"
	"irrlicht/core/adapters/outbound/relay"
	wshub "irrlicht/core/adapters/outbound/websocket"
	"irrlicht/core/application/services"
	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/config"
	"irrlicht/core/domain/session"
	"irrlicht/core/ports/inbound"
	"irrlicht/core/ports/outbound"
)

// initSessionStorage opens the filesystem session repository, prunes stale
// session files and dead proc-<pid> entries left by prior daemon lifetimes,
// and returns both the raw repo (for baseline scans) and a caching wrapper
// (returned as the outbound.SessionRepository interface since the concrete
// cached type is unexported).
func initSessionStorage(logger outbound.Logger, cfg config.Config) (*filesystem.SessionRepository, outbound.SessionRepository) {
	// Resolve the store (IRRLICHT_HOME-aware) then layer the daemon's startup
	// pruning/sweeps on top — see resolveSessionRepo, shared with --diagnose.
	fsRepo, err := resolveSessionRepo()
	if err != nil {
		logger.LogError("startup", "", fmt.Sprintf("failed to init filesystem repo: %v", err))
		os.Exit(1)
	}

	pruned, err := fsRepo.PruneStale(cfg.MaxSessionAge)
	if err != nil {
		logger.LogError("startup", "", fmt.Sprintf("failed to prune stale sessions: %v", err))
	} else if pruned > 0 {
		logger.LogInfo("startup", "", fmt.Sprintf("pruned %d stale session files", pruned))
	}
	pruneDeadProcSessions(fsRepo, logger)
	pruneOrphanLedgers(fsRepo, logger)

	cachedRepo := filesystem.NewCachedSessionRepository(fsRepo, 3*time.Second)
	return fsRepo, cachedRepo
}

// pruneOrphanLedgers removes per-session ledger files in
// ~/.local/share/irrlicht/sessions/ that no longer correspond to any session
// known to the repo. Handles transcripts deleted while the daemon was off —
// the live-daemon case is covered by SessionDetector.onRemoved calling
// MetricsCollector.PruneEntry.
func pruneOrphanLedgers(fsRepo *filesystem.SessionRepository, logger outbound.Logger) {
	dir, err := metrics.LedgerDir()
	if err != nil {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		logger.LogError("startup", "", fmt.Sprintf("ledger dir read failed: %v", err))
		return
	}
	allSessions, err := fsRepo.ListAll()
	if err != nil {
		return
	}
	removed := removeOrphanLedgerFiles(dir, entries, expectedLedgerNames(allSessions))
	if removed > 0 {
		logger.LogInfo("startup", "", fmt.Sprintf("pruned %d orphan ledger files", removed))
	}
}

// expectedLedgerNames maps each session with a transcript to its ledger
// filename, the set of ledger files that are still owned by a known session.
func expectedLedgerNames(allSessions []*session.SessionState) map[string]struct{} {
	expected := make(map[string]struct{}, len(allSessions))
	for _, s := range allSessions {
		if s.TranscriptPath == "" {
			continue
		}
		expected[metrics.LedgerFilename(s.TranscriptPath)] = struct{}{}
	}
	return expected
}

// removeOrphanLedgerFiles deletes every ledger-suffixed file in dir/entries
// that isn't in expected, returning the count removed.
func removeOrphanLedgerFiles(dir string, entries []os.DirEntry, expected map[string]struct{}) int {
	removed := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, metrics.LedgerSuffix) {
			continue
		}
		if _, ok := expected[name]; ok {
			continue
		}
		if err := os.Remove(filepath.Join(dir, name)); err == nil {
			removed++
		}
	}
	return removed
}

// pruneDeadProcSessions removes proc-<pid> session files whose backing
// process is no longer alive. These survive a daemon restart because the
// in-memory tracked map is lost, leaving orphaned proc files on disk.
func pruneDeadProcSessions(fsRepo *filesystem.SessionRepository, logger outbound.Logger) {
	allSessions, err := fsRepo.ListAll()
	if err != nil {
		return
	}
	for _, s := range allSessions {
		var pid int
		if _, err := fmt.Sscanf(s.SessionID, "proc-%d", &pid); err != nil || pid <= 0 {
			continue
		}
		if err := syscall.Kill(pid, 0); err != nil {
			_ = fsRepo.Delete(s.SessionID)
			logger.LogInfo("startup", s.SessionID, "pruned dead proc session")
		}
	}
}

// initCostTracker opens the append-only per-project cost JSONL store,
// backfills CO2 estimates onto pre-#829 rows, prunes rows older than 400 days
// (so per-year queries stay fast), and records a baseline row for every
// existing session so rates are computable without waiting for new activity.
func initCostTracker(logger outbound.Logger, fsRepo *filesystem.SessionRepository) outbound.CostTracker {
	var costTracker *filesystem.CostTracker
	if dir := stateStoreDir("cost"); dir != "" {
		costTracker = filesystem.NewCostTrackerWithDir(dir)
	} else {
		var err error
		costTracker, err = filesystem.NewCostTracker()
		if err != nil {
			logger.LogError("startup", "", fmt.Sprintf("failed to init cost tracker: %v", err))
			return nil
		}
	}
	// One-time migration (issue #829): estimate CO2 for history recorded
	// before the field existed, so upgrading doesn't leave the CO2 chart
	// flat at zero for activity that's already on disk. Idempotent — see
	// BackfillCO2's marker file.
	if err := costTracker.BackfillCO2(); err != nil {
		logger.LogError("startup", "", fmt.Sprintf("cost tracker CO2 backfill failed: %v", err))
	}
	// Attribute wrapper agents (pi, opencode) to the subscription they
	// inherit, so per-provider spend matches the dashboard's quota chip
	// instead of going unattributed.
	costTracker.SetProviderResolver(func(s *session.SessionState) string {
		return services.ProviderForSession(s, "")
	})
	if err := costTracker.Prune(400); err != nil {
		logger.LogError("startup", "", fmt.Sprintf("cost tracker prune failed: %v", err))
	}
	allSessions, err := fsRepo.ListAll()
	if err != nil {
		return costTracker
	}
	for _, s := range allSessions {
		if err := costTracker.RecordBaseline(s); err != nil {
			logger.LogError("startup", s.SessionID, fmt.Sprintf("cost tracker baseline failed: %v", err))
		}
	}
	return costTracker
}

// historyEventBroadcaster maps HistoryEvent (the in-memory tagged union the
// tracker emits) onto PushMessage envelopes the WebSocket hub already knows
// how to serialize.
func historyEventBroadcaster(push outbound.PushBroadcaster) func(services.HistoryEvent) {
	return func(ev services.HistoryEvent) {
		switch ev.Kind {
		case services.HistoryEventSnapshot:
			push.Broadcast(outbound.PushMessage{
				Type:        outbound.PushTypeHistorySnapshot,
				SessionID:   ev.SessionID,
				History:     ev.History,
				Generations: ev.Generations,
			})
		case services.HistoryEventTick:
			push.Broadcast(outbound.PushMessage{
				Type:              outbound.PushTypeHistoryTick,
				GranularitySec:    ev.GranularitySec,
				Buckets:           ev.Buckets,
				BucketGenerations: ev.BucketGenerations,
			})
		case services.HistoryEventUpgrade:
			p := ev.Priority
			push.Broadcast(outbound.PushMessage{
				Type:      outbound.PushTypeHistoryUpgrade,
				SessionID: ev.SessionID,
				Priority:  &p,
			})
		}
	}
}

// historySnapshotProvider builds a connect-time hydration list for the
// WebSocket hub. Each PushMessage carries the snapshot's per-granularity
// tick generations alongside the bit-packed history so a client can dedupe
// any tick that fires between subscribe and the first message dispatch.
func historySnapshotProvider(ht *services.HistoryTracker) wshub.ConnectSnapshots {
	return func() []outbound.PushMessage {
		all := ht.EncodeAll()
		out := make([]outbound.PushMessage, 0, len(all))
		for sid, enc := range all {
			out = append(out, outbound.PushMessage{
				Type:        outbound.PushTypeHistorySnapshot,
				SessionID:   sid,
				History:     enc.History,
				Generations: enc.Generations,
			})
		}
		return out
	}
}

// startHistoryTracker brings up the per-session rolling ring buffers used by
// the history endpoint and returns the tracker plus a cancel func. The
// caller must defer the cancel — HistoryTracker.Run calls save() in its
// ctx.Done branch, so skipping the cancel loses the final flush on clean
// shutdown and history regresses to the last periodic tick.
func startHistoryTracker(logger outbound.Logger) (*services.HistoryTracker, context.CancelFunc) {
	home, _ := os.UserHomeDir()
	ht := services.NewHistoryTrackerWithDir(dataDir(home))
	ht.Load()
	ctx, cancel := context.WithCancel(context.Background())
	go ht.Run(ctx)
	logger.LogInfo("startup", "", "history tracker started")
	return ht, cancel
}

// relaySetup bundles the outbound relay-publishing wiring (issue #722): the
// daemon pushes session events to a standalone irrlichtrelay over the same
// broadcaster the local WebSocket uses, so remote clients see this daemon's
// sessions without needing inbound reachability (works behind NAT). The
// PublishController owns the forwarder's lifecycle so the macOS app can
// start/stop/reconfigure publishing live over the loopback PUT
// /api/v1/relay/publish — no daemon relaunch. inputService is published once
// the consent stack builds InputService (setupBackchannel); it's read
// concurrently by the HTTP handlers and the relay forwarder, so it's an
// atomic.Pointer rather than a plain field — those readers start before the
// assignment.
type relaySetup struct {
	cancel            context.CancelFunc
	publishController *relay.PublishController
	controlStore      *filesystem.RelayControlStore
	inputService      atomic.Pointer[services.InputService]
}

// setupRelay wires the PublishController (always constructed, so the
// activation endpoint can turn publishing on later) and seeds it once from
// IRRLICHT_RELAY_URL / IRRLICHT_RELAY_TOKEN so headless/standalone daemons
// keep working as before.
//
// Remote control (#724): the relay-control toggle (default OFF, env opt-in
// IRRLICHT_RELAY_CONTROL=on) gates whether the forwarder acts on inbound
// control frames. relayControl binds to inputService once it's published;
// a control frame that races startup is rejected, not panicked. Remote
// stays doubly gated — this toggle plus the backchannel toggle + per-agent
// consent re-checked inside InputService.
func setupRelay(logger outbound.Logger, push outbound.PushBroadcaster, cachedRepo outbound.SessionRepository, allAgents []agent.Agent) *relaySetup {
	r := &relaySetup{}
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel

	home, _ := os.UserHomeDir()
	identity, err := relay.LoadOrCreateIdentity(dataDir(home))
	if err != nil {
		logger.LogError("startup", "", fmt.Sprintf("relay identity persistence failed (using ephemeral id): %v", err))
	}
	snapshot := func() ([]*session.SessionState, []relay.AgentInfo) {
		sessions, err := cachedRepo.ListAll()
		if err != nil {
			sessions = nil
		}
		infos := make([]relay.AgentInfo, 0, len(allAgents))
		for _, a := range allAgents {
			infos = append(infos, relay.AgentInfo{
				Name:         a.Identity.Name,
				DisplayName:  a.Identity.DisplayName,
				IconSVGLight: a.Identity.IconSVGLight,
				IconSVGDark:  a.Identity.IconSVGDark,
			})
		}
		return sessions, infos
	}
	r.controlStore = filesystem.NewRelayControlStore(dataDir(home))
	controlEnabled := func() bool {
		return r.controlStore.Enabled() || os.Getenv("IRRLICHT_RELAY_CONTROL") == "on"
	}
	// Bearer token for an auth-enabled relay: IRRLICHT_RELAY_TOKEN or
	// <dataDir>/relay-token.json (mode 0600). Empty against a no-auth relay.
	relayControl := lazyControl{resolve: func() relay.ControlHandler {
		if s := r.inputService.Load(); s != nil {
			return s
		}
		return nil
	}}
	r.publishController = relay.NewPublishController(ctx, identity, push, snapshot, relayControl, controlEnabled, logger)
	if relayURL := os.Getenv("IRRLICHT_RELAY_URL"); relayURL != "" {
		r.publishController.Apply(true, relayURL, relay.LoadDaemonToken(dataDir(home)))
	}
	return r
}

// setupProcessWatcher starts the kqueue EVFILT_PROC NOTE_EXIT monitor unless
// demo mode disables it. detector is a pointer to the not-yet-constructed
// SessionDetector: ProcessWatcher only invokes its exit callback after
// SessionDetector.Run() subscribes to Watcher events, so by the time the
// callback fires, *detector has been assigned by the caller. Returns a nil
// watcher and nil cleanup when disabled or on init failure — the caller
// treats a nil cleanup as "nothing to defer".
func setupProcessWatcher(demoMode bool, detector **services.SessionDetector, logger outbound.Logger) (outbound.ProcessWatcher, func()) {
	if demoMode {
		return nil, nil
	}
	pw, err := processlifecycle.NewMonitor(func(pid int, sessionID string) {
		(*detector).HandleProcessExit(pid, sessionID, "process watcher: pid exited (NOTE_EXIT)")
	})
	if err != nil {
		logger.LogError("startup", "", fmt.Sprintf("ProcessWatcher init failed (non-fatal): %v", err))
		return nil, nil
	}
	// godre:S8188 flags procCancel as not deferred here — intentional:
	// procCancel is bundled into the returned cleanup closure for the CALLER
	// to defer at daemon-shutdown scope (see the doc comment above), not
	// within this setup function. A bare `defer procCancel()` here would
	// cancel the watcher the instant setupProcessWatcher returns.
	procCtx, procCancel := context.WithCancel(context.Background())
	go func() {
		if err := pw.Run(procCtx); err != nil && err != context.Canceled {
			logger.LogError("process-watcher", "", fmt.Sprintf("event loop error: %v", err))
		}
	}()
	return pw, func() {
		pw.Close()
		procCancel()
	}
}

// registerCoreRoutesDeps bundles registerCoreRoutes' dependencies.
type registerCoreRoutesDeps struct {
	FSRepo            *filesystem.SessionRepository
	CachedRepo        outbound.SessionRepository
	HistoryTracker    *services.HistoryTracker
	Push              outbound.PushBroadcaster
	AllAgents         []agent.Agent
	Version           string
	PublishController *relay.PublishController
	Cfg               config.Config
}

// registerCoreRoutes wires the always-on API surface: session state, the
// WebSocket stream, agent/version metadata, relay publish status, pprof, and
// the diagnostics bundle. The sessions endpoint itself is registered
// separately by registerSessionRoutes, once orchMonitor is available.
func registerCoreRoutes(mux *http.ServeMux, deps registerCoreRoutesDeps) {
	mux.HandleFunc("GET /state", handleGetState(deps.CachedRepo))

	hub := wshub.NewHub(deps.Push, historySnapshotProvider(deps.HistoryTracker))
	mux.HandleFunc("GET /api/v1/sessions/stream", hub.ServeWS)
	mux.HandleFunc("GET /api/v1/agents", handleGetAgents(deps.AllAgents))
	mux.HandleFunc("GET /api/v1/version", handleGetVersion(deps.Version))
	mux.HandleFunc("GET /api/v1/relay/publish", handleGetPublishStatus(deps.PublishController))
	// PUT reconfigures publishing on the running daemon (issue #722). Loopback
	// only: it mutates forwarder config and carries the relay token in its body.
	mux.HandleFunc("PUT /api/v1/relay/publish", localhostOnly(handlePutPublishStatus(deps.PublishController)))

	// pprof debug endpoints for runtime profiling (localhost only).
	mux.HandleFunc("GET /debug/pprof/", localhostOnly(pprof.Index))
	mux.HandleFunc("GET /debug/pprof/cmdline", localhostOnly(pprof.Cmdline))
	mux.HandleFunc("GET /debug/pprof/profile", localhostOnly(pprof.Profile))
	mux.HandleFunc("GET /debug/pprof/symbol", localhostOnly(pprof.Symbol))
	mux.HandleFunc("GET /debug/pprof/trace", localhostOnly(pprof.Trace))

	// Diagnostics bundle (issue #736): a redacted .tar.gz of session state,
	// per-PID liveness, trimmed logs, and config for bug reports. Localhost
	// only — it carries session paths and (pre-redaction) process argv. Reads
	// fsRepo directly for a fresh, uncached snapshot.
	diagSvc := buildDiagnostics(deps.FSRepo, deps.AllAgents, deps.Cfg)
	mux.HandleFunc("GET /debug/bundle", localhostOnly(handleDiagnosticsBundle(diagSvc)))
}

// registerUIRoutes serves the dashboard's static assets (index.html,
// irrlicht.css, irrlicht.js) from disk, or a plain-text explainer if the UI
// directory can't be found. Ensures web assets are served with the correct
// Content-Type regardless of the host OS mime.types database (absent on
// stripped Linux images — Go's content-sniffing fallback returns text/plain
// for CSS, which has no magic bytes).
func registerUIRoutes(mux *http.ServeMux, logger outbound.Logger) {
	_ = mime.AddExtensionType(".js", "application/javascript")
	_ = mime.AddExtensionType(".css", "text/css")

	if uiDir := resolveUIDir(); uiDir != "" {
		logger.LogInfo("startup", "", fmt.Sprintf("serving UI from %s", uiDir))
		mux.Handle("/", http.FileServer(http.Dir(uiDir)))
		return
	}

	logger.LogError("startup", "", fmt.Sprintf("UI directory not found — set %s to the directory containing index.html", envUIDir))
	body := fmt.Sprintf("Dashboard UI not found.\nSet %s to the directory containing index.html, or reinstall via the DMG / curl installer.\n", envUIDir)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprint(w, body)
	})
}

// newHTTPServer builds the shared http.Server for both the Unix socket and
// TCP listeners. WriteTimeout is intentionally 0: WebSocket streams and
// long-polling responses need unbounded writes, and gorilla/websocket sets
// its own per-message deadlines.
func newHTTPServer(mux *http.ServeMux) *http.Server {
	return &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
}

// setupUnixSocket creates the daemon's Unix socket, removing any stale
// socket left by a prior daemon lifetime first.
func setupUnixSocket(logger outbound.Logger) (string, net.Listener) {
	sockPath := socketPath()
	if err := os.MkdirAll(filepath.Dir(sockPath), 0700); err != nil {
		logger.LogError("startup", "", fmt.Sprintf("failed to create socket dir: %v", err))
		os.Exit(1)
	}
	os.Remove(sockPath) // remove stale socket
	unixL, err := net.Listen("unix", sockPath)
	if err != nil {
		logger.LogError("startup", "", fmt.Sprintf("failed to listen on unix socket: %v", err))
		os.Exit(1)
	}
	return sockPath, unixL
}

// setupTCPListener binds the daemon's TCP listener — default loopback;
// override with IRRLICHT_BIND_ADDR. IRRLICHT_BIND_ADDR=127.0.0.1:0 binds an
// ephemeral port (used by the startup smoke test so N daemons can run in
// parallel). Returns the listener and its actual bound address (the
// OS-assigned port when the request was :0).
func setupTCPListener(logger outbound.Logger) (net.Listener, string) {
	bindAddr := resolveBindAddr(os.Getenv("IRRLICHT_BIND_ADDR"))
	tcpL, err := net.Listen("tcp", bindAddr)
	if err != nil {
		logger.LogError("startup", "", fmt.Sprintf("failed to listen on TCP %s: %v", bindAddr, err))
		os.Exit(1)
	}
	return tcpL, tcpL.Addr().String()
}

// setupMDNS advertises _irrlicht._tcp on the local network, opt-in via
// IRRLICHT_MDNS=1 to avoid broadcasting the daemon on networks the user did
// not intend to share on. Advertises the port actually bound (resolvedAddr),
// not the compile-time default — otherwise a daemon on a custom/ephemeral
// port would point discovery clients at 7837 (a dead or production port).
func setupMDNS(resolvedAddr string, logger outbound.Logger) *mdns.Advertiser {
	if os.Getenv("IRRLICHT_MDNS") != "1" {
		logger.LogInfo("startup", "", "mDNS: disabled (set IRRLICHT_MDNS=1 to advertise)")
		return nil
	}
	advPort := tcpPort
	if _, p, err := net.SplitHostPort(resolvedAddr); err == nil {
		if n, err := strconv.Atoi(p); err == nil {
			advPort = n
		}
	}
	adv, err := mdns.New(advPort)
	if err != nil {
		logger.LogError("startup", "", fmt.Sprintf("mDNS advertisement failed (non-fatal): %v", err))
		return nil
	}
	logger.LogInfo("startup", "", "mDNS: advertising _irrlicht._tcp on the local network")
	return adv
}

// setupOrchestratorMonitor starts the multi-agent orchestration monitor. It
// runs regardless of any single orchestrator's consent state (idle until a
// watcher registers) so /api/v1/sessions can always consult it.
func setupOrchestratorMonitor(push outbound.PushBroadcaster, logger outbound.Logger) (*services.OrchestratorMonitor, context.Context, context.CancelFunc) {
	orchMonitor := services.NewOrchestratorMonitor(nil, push, logger)
	orchCtx, orchCancel := context.WithCancel(context.Background())
	go func() {
		if err := orchMonitor.Run(orchCtx); err != nil && err != context.Canceled {
			logger.LogError("orchestrator-monitor", "", fmt.Sprintf("monitor error: %v", err))
		}
	}()
	return orchMonitor, orchCtx, orchCancel
}

// gastownEffects builds the start/stop effect closures for the
// "gastown/state" permission (#570): even constructing the adapter reads
// ~/gt state files, so nothing is built until the permission is granted —
// the PermissionService runs these as that permission's Apply/Remove
// effects. The service serializes effect execution, so no extra locking is
// needed around the watch-cancel state captured here.
func gastownEffects(orchCtx context.Context, orchMonitor *services.OrchestratorMonitor, gtPath string, cachedRepo outbound.SessionRepository, logger outbound.Logger) (start, stop func() error) {
	var gtWatchCancel context.CancelFunc
	start = func() error {
		if gtWatchCancel != nil {
			return nil
		}
		gtAdapter := gastownadapter.NewAdapter(gtPath, cachedRepo, logger)
		if !gtAdapter.Detected() {
			logger.LogInfo("permissions", "", "gastown granted but no Gas Town root detected — nothing to watch")
			return nil
		}
		logger.LogInfo("permissions", "", fmt.Sprintf("Gas Town detected at %s", gtAdapter.Root()))
		watchCtx, cancel := context.WithCancel(orchCtx)
		gtWatchCancel = cancel
		orchMonitor.AddWatcher(watchCtx, gtAdapter)
		go func() {
			if err := gtAdapter.Watch(watchCtx); err != nil && err != context.Canceled {
				logger.LogError("orchestrator-watcher", "", fmt.Sprintf("watcher error: %v", err))
			}
		}()
		return nil
	}
	stop = func() error {
		if gtWatchCancel != nil {
			gtWatchCancel()
			gtWatchCancel = nil
		}
		return nil
	}
	return start, stop
}

// registerSessionRoutesDeps bundles registerSessionRoutes' dependencies.
type registerSessionRoutesDeps struct {
	CachedRepo   outbound.SessionRepository
	OrchMonitor  *services.OrchestratorMonitor
	CostTracker  outbound.CostTracker
	InputService *atomic.Pointer[services.InputService]
	SockPath     string
	Push         outbound.PushBroadcaster
	Logger       outbound.Logger
	GitResolver  *git.Adapter
}

// registerSessionRoutes wires the sessions/history/focus endpoints. Kept
// separate from registerCoreRoutes because it needs orchMonitor and the
// not-yet-published inputService: a session only reports controllable once
// setupBackchannel has run.
func registerSessionRoutes(mux *http.ServeMux, deps registerSessionRoutesDeps) {
	mux.HandleFunc("GET /api/v1/sessions", handleGetSessions(deps.CachedRepo, deps.OrchMonitor, deps.CostTracker,
		func(sessionID string) bool {
			if s := deps.InputService.Load(); s != nil {
				return s.Controllable(sessionID)
			}
			return false
		}))

	// History tab analytics (issue #369): trailing/calendar/custom-range cost
	// series + linear forecast, computed from the cost snapshot files. #373 adds
	// chart=yield (per-project productive-vs-reverted spend over sessions). Phase
	// 3 (#751) adds chart=agents, a concurrent-agents series reconstructed from
	// the lifecycle recordings (read-only; empty unless --record has been used).
	// #951 adds chart=dora, computed on request from deps.GitResolver — no
	// persistence, no background sweep.
	concurrencyTracker := filesystem.NewConcurrencyTrackerWithDir(resolveRecordingsDir(deps.SockPath))
	mux.HandleFunc("GET /api/v1/history", handleGetHistory(deps.CostTracker, deps.CachedRepo, concurrencyTracker, deps.GitResolver))

	focusService := services.NewFocusService(deps.CachedRepo, deps.Push, deps.Logger)
	mux.HandleFunc("POST /api/v1/sessions/{id}/focus", sessionshandler.NewFocusHandler(focusService, deps.Logger))
}

// buildDetectorDeps bundles buildDetector's dependencies.
type buildDetectorDeps struct {
	DemoMode         bool
	PWPort           outbound.ProcessWatcher
	CachedRepo       outbound.SessionRepository
	Logger           outbound.Logger
	GitResolver      *git.Adapter
	MetricsCollector outbound.MetricsCollector
	Push             outbound.PushBroadcaster
	Version          string
	Cfg              config.Config
	AllAgents        []agent.Agent
	CostTracker      outbound.CostTracker
	HistoryTracker   *services.HistoryTracker
}

// buildDetector constructs the SessionDetector (orchestrating Watchers +
// ProcessWatcher; Watchers register dynamically via AddWatcher as
// permissions are granted) plus the per-adapter watcher factories consulted
// by the PermissionService. Watcher construction is consent-gated (#570):
// instead of starting every agent's watchers at boot, each agent gets a
// factory that the PermissionService invokes only when the user grants that
// agent's observe permission — and cancels on revoke. Skipped entirely
// under demo mode, which serves only what's already on disk in instances/.
func buildDetector(deps buildDetectorDeps) (*services.SessionDetector, map[string]services.WatcherFactory) {
	// Suppress ghost proc pre-sessions for live processes whose real session
	// is already persisted. The PID discriminator in HasRealSessionForPID
	// prevents historical sessions on disk (GH #113, within MaxSessionAge)
	// from blocking new processes in the same project.
	realSessionCheck := func(projectDir string, pid int) bool {
		sessions, err := deps.CachedRepo.ListAll()
		if err != nil {
			return false
		}
		return processlifecycle.HasRealSessionForPID(sessions, projectDir, pid)
	}

	var watcherFactories map[string]services.WatcherFactory
	if !deps.DemoMode {
		watcherFactories = make(map[string]services.WatcherFactory, len(deps.AllAgents))
		for _, a := range deps.AllAgents {
			watcherFactories[a.Identity.Name] = func() []inbound.Watcher {
				ws, _ := buildAgentWatchers(a, deps.Cfg.MaxSessionAge, realSessionCheck)
				return ws
			}
		}
	}

	pidDiscovers := agents.PIDDiscoverers(deps.AllAgents)
	processNames := agents.ProcessNames(deps.AllAgents)

	detector := services.NewSessionDetector(nil, services.SessionDetectorDeps{
		PW:           deps.PWPort,
		Repo:         deps.CachedRepo,
		Log:          deps.Logger,
		Git:          deps.GitResolver,
		Metrics:      deps.MetricsCollector,
		Broadcaster:  deps.Push,
		Version:      deps.Version,
		ReadyTTL:     deps.Cfg.ReadySessionTTL,
		PIDDiscovers: pidDiscovers,
		ProcessNames: processNames,
		LiveCWDs:     processlifecycle.LiveCWDs,
	})
	if deps.CostTracker != nil {
		detector.SetCostTracker(deps.CostTracker)
	}
	detector.SetHistoryTracker(deps.HistoryTracker)

	// Cache-creation regression detector (#374): per-project baseline from the
	// session repo; findings logged to events.log for the ir:agent-releases
	// consumer. Self-disables when cacheBloatThreshold <= 0 (the kill switch).
	detector.SetCacheBloatDetector(services.NewCacheBloatDetector(
		deps.CachedRepo,
		services.NewLoggerCacheBloatSink(deps.Logger),
		services.CacheBloatConfig{
			BaselineDays:       deps.Cfg.CacheBloatBaselineDays,
			Threshold:          deps.Cfg.CacheBloatThreshold,
			VersionDeltaTokens: deps.Cfg.CacheBloatVersionDeltaTokens,
			MinTurns:           deps.Cfg.CacheBloatMinTurns,
		},
	))

	return detector, watcherFactories
}

// setupPermissionService wires the PermissionService (issue #570): the
// single source of truth for consent state. It exercises grants (hook
// install, watcher start), undoes revokes, arbitrates wizard answers
// between the macOS and web UIs, and feeds the always-on detection poller
// that makes the wizard appear when a new agent shows up. Under demo mode
// the factories and detection are nil — the daemon never monitors anything
// live.
// setupPermissionServiceDeps bundles setupPermissionService's dependencies.
type setupPermissionServiceDeps struct {
	Detector         *services.SessionDetector
	Push             outbound.PushBroadcaster
	Logger           outbound.Logger
	Cfg              config.Config
	AllAgents        []agent.Agent
	WatcherFactories map[string]services.WatcherFactory
	DemoMode         bool
	Home             string
	StartGastown     func() error
	StopGastown      func() error
}

func setupPermissionService(mux *http.ServeMux, deps setupPermissionServiceDeps) *services.PermissionService {
	detector := deps.Detector
	logger := deps.Logger
	allAgents := deps.AllAgents
	permStore := filesystem.NewPermissionStore(dataDir(deps.Home))
	// Fold the pre-wizard task-eta consent record (activation.json, issue
	// #558) into the store before the service loads it (issue #577).
	services.MigrateLegacyTaskEtaConsent(dataDir(deps.Home), permStore,
		claudecode.AdapterName, claudecode.PermissionKeyInstructions, logger)

	var hasLive services.HasLiveProcessFunc
	if !deps.DemoMode {
		hasLive = processlifecycle.HasLiveProcess
	}
	// The consent catalog is the agent adapters plus three daemon-wide
	// entries with no Source/Process axes: the Gas Town orchestrator
	// (reads ~/gt), launcher-identity capture (reads whitelisted env
	// vars from agent processes for click-to-focus), and the kitty
	// remote-control config patch (writes kitty.conf for tab-precise
	// click-to-focus, #425).
	permissionAgents := append(append([]agent.Agent{}, allAgents...),
		gastownadapter.PermissionDeclaration(deps.StartGastown, deps.StopGastown),
		processlifecycle.LauncherPermissionDeclaration(),
		processlifecycle.KittyPermissionDeclaration())
	permService := services.NewPermissionService(services.PermissionServiceDeps{
		Agents:    permissionAgents,
		Store:     permStore,
		Push:      deps.Push,
		Log:       logger,
		Mode:      deps.Cfg.PermissionMode,
		Registrar: detector,
		Factories: deps.WatcherFactories,
		HasLive:   hasLive,
	})
	// Gas Town is detected by root-directory presence (stat-only), not by
	// a live process matcher.
	permService.SetDetectionProbe(gastownadapter.Name, gastownadapter.RootDetected)
	// Launcher capture is relevant whenever ANY agent runs, so its wizard
	// row appears alongside the first detected agent's. Short-circuits on
	// the first live match, and the detection poller only runs while
	// something is pending, so the extra scans are bounded.
	permService.SetDetectionProbe(processlifecycle.LauncherName, func() bool {
		for _, a := range allAgents {
			if processlifecycle.HasLiveProcess(a.Process.Match) {
				return true
			}
		}
		return false
	})
	// kitty is a host integration, not an agent: detected by a live kitty
	// process or an existing kitty config directory.
	permService.SetDetectionProbe(processlifecycle.KittyName, processlifecycle.KittyDetected)

	// Capture terminal/IDE identity at first PID assignment so the menu-bar
	// app can jump back to the launching terminal on row/notification click.
	// Consent-gated per capture (#570): checked on every call so a revoke
	// takes effect immediately; without the grant, click-to-focus falls
	// back to app-level activation.
	detector.SetLauncherEnvReader(func(pid int) *session.Launcher {
		if !permService.Granted(processlifecycle.LauncherName, processlifecycle.PermissionKeyLauncherEnv) {
			return nil
		}
		return processlifecycle.ReadLauncherEnv(pid)
	})
	// Flag detached Claude Code background agents (Agent View bg agents that keep
	// running in the daemon pool) so the UI can badge them instead of showing a
	// phantom row (#744). Reads ~/.claude/sessions/<pid>.json, gated by the same
	// transcripts/observe consent as other claude process reads (#570).
	detector.SetBackgroundReader(func(pid int) *session.BackgroundAgent {
		if !permService.Granted(claudecode.AdapterName, claudecode.PermissionKeyTranscripts) {
			return nil
		}
		name, ok := claudecode.ReadBackgroundMeta(pid)
		if !ok {
			return nil
		}
		return &session.BackgroundAgent{Name: name}
	})
	// Let the liveness sweep reap a session bound to a still-alive PID that is
	// the adapter's background infra rather than the session — Claude Code's
	// --bg-spare pool helper outlives every session, so a mis-bound session
	// would otherwise never be reaped (#727). Demo mode never tracks live
	// processes, so leave the reaper unwired there.
	if !deps.DemoMode {
		detector.SetInfraReaper(agents.ArgvExcluders(allAgents), processlifecycle.ReadArgv)
		// Reject a candidate PID launched by something other than a known
		// terminal or IDE before a session is ever created — e.g. CodexBar
		// keeping an Antigravity `agy` process running in the background for
		// quota polling, with no distinguishing argv or cwd (#784). Demo mode
		// never tracks live processes, so leave the gate unwired there.
		detector.SetHostGate(agents.RequireKnownHost(allAgents), processlifecycle.IsKnownInteractiveHost)
		// Consent gate for the detector's own transcript reads (startup seed +
		// stale-working refresh of PERSISTED sessions) — the watcher pipeline
		// is gated by construction, but these two paths read repo-listed
		// transcripts directly and must honor per-adapter consent too (#570).
		// Demo mode keeps the gate open: it serves synthetic seeded sessions
		// and never reads live agent files in the first place.
		detector.SetConsentGate(permService.ObserveGranted)
	}

	mux.HandleFunc("GET /api/v1/permissions",
		permissionshandler.NewGetHandler(permService, logger))
	mux.HandleFunc("POST /api/v1/permissions/answer",
		permissionshandler.NewAnswerHandler(permService, logger))
	// Task-eta activation (issue #558): legacy alias over the
	// claude-code/instructions permission (issue #577), kept so the macOS
	// Settings toggle's wire shape stays stable. localhostOnly: the granted
	// effect rewrites a sensitive user file (~/.claude/CLAUDE.md) and must
	// not be reachable from the LAN.
	mux.HandleFunc("/api/v1/activation/task-eta",
		localhostOnly(activationhandler.NewHandler(permService,
			claudecode.AdapterName, claudecode.PermissionKeyInstructions, logger)))

	return permService
}

// setupBackchannelDeps bundles setupBackchannel's dependencies.
type setupBackchannelDeps struct {
	CachedRepo        outbound.SessionRepository
	Push              outbound.PushBroadcaster
	PermService       *services.PermissionService
	Detector          *services.SessionDetector
	Logger            outbound.Logger
	Home              string
	InputService      *atomic.Pointer[services.InputService]
	RelayControlStore *filesystem.RelayControlStore
	AllAgents         []agent.Agent
}

// setupBackchannel wires control discovered agents by scripting their
// terminal backend (issue #724). A default-OFF master toggle gates the whole
// capability; the per-adapter "control" consent and a usable backend target
// gate each write. InputService enforces the order (toggle → consent →
// controllable) for both the manual HTTP path and the event→action rule
// engine. Routes are localhostOnly + a Sec-Fetch-Site guard on the mutating
// verbs, since injected input drives a live agent.
func setupBackchannel(mux *http.ServeMux, deps setupBackchannelDeps) (*services.BackchannelEngine, *services.TerminalObserver) {
	logger := deps.Logger
	backchannelStore := filesystem.NewBackchannelStore(dataDir(deps.Home))
	controlController := control.NewController(deps.CachedRepo, deps.Push, logger)
	in := services.NewInputService(deps.CachedRepo, controlController, deps.PermService, backchannelStore.Enabled, logger)
	deps.InputService.Store(in) // publish to the HTTP/forwarder readers
	mux.HandleFunc("/api/v1/activation/backchannel",
		localhostOnly(activationhandler.NewBackchannelHandler(backchannelStore, logger)))
	// Remote-control toggle (#724): default OFF; gates whether the relay
	// forwarder acts on inbound control frames (the outer remote gate).
	mux.HandleFunc("/api/v1/activation/relay-control",
		localhostOnly(activationhandler.NewToggleHandler(deps.RelayControlStore, "relay_control_enabled", logger)))
	mux.HandleFunc("POST /api/v1/sessions/{id}/input",
		localhostOnly(sessionshandler.NewInputHandler(in, logger)))
	mux.HandleFunc("POST /api/v1/sessions/{id}/interrupt",
		localhostOnly(sessionshandler.NewInterruptHandler(in, logger)))

	// Backchannel rules (issue #724): event→action automations (e.g.
	// context_pressure → /compact). The engine consumes the push stream and
	// fires through inputService, so the same toggle+consent+controllable
	// gates apply. Started under detectorCtx by startBackgroundLoops.
	backchannelRules := filesystem.NewBackchannelRulesStore(dataDir(deps.Home))
	backchannelEngine := services.NewBackchannelEngine(backchannelRules, in, agents.ControlPresets(deps.AllAgents), deps.Push, backchannelStore.Enabled, logger)
	mux.HandleFunc("/api/v1/backchannel/rules",
		localhostOnly(backchannelhandler.NewRulesHandler(backchannelRules, logger)))

	// Backchannel read-back (issue #732, Phase 3): the read counterpart to the
	// write path. The observer captures the rendered terminal of controllable
	// sessions and folds transcript-invisible signals (today: the trust dialog)
	// into the lifecycle via the detector's single writer. Same gate chain as
	// inputService — master-toggle + per-adapter "control" consent — so a
	// disabled backchannel reads nothing. Started under detectorCtx by
	// startBackgroundLoops.
	terminalReader := control.NewReader(deps.CachedRepo, logger)
	terminalObserver := services.NewTerminalObserver(deps.CachedRepo, terminalReader, deps.PermService, backchannelStore.Enabled, deps.Detector, logger)

	// Re-key the presession's backchannel bookkeeping onto the reconciled
	// real session whenever any reconciliation path retires a presession
	// (issue #997): SessionDetector carries forward any Waiting state a live
	// terminal-observer signal already persisted onto the presession's own
	// row, and TerminalObserver re-keys its own edge-detection cache so the
	// next poll compares against the right session id. Bind the one
	// long-lived reference this closure needs (detector) instead of closing
	// over the whole setupBackchannelDeps struct.
	detector := deps.Detector
	detector.SetSessionSupersededHandler(func(oldID, newID string) {
		detector.ReconcilePreSessionBackchannel(oldID, newID)
		terminalObserver.RekeySession(oldID, newID)
	})

	return backchannelEngine, terminalObserver
}

// registerHookRoutes wires Claude Code's hook receivers: PermissionRequest/
// PostToolUse events (routed to the detector, which satisfies
// claudecode.HookTarget via HandlePermissionHook) and the per-tick
// statusline JSON carrying rate_limits for Pro/Max subscribers (issue #309).
// Both are consent-gated: hooks installed by a pre-consent daemon keep
// firing until the wizard is answered, so payloads are dropped while
// pending.
func registerHookRoutes(mux *http.ServeMux, detector *services.SessionDetector, metricsCollector outbound.MetricsCollector, permService *services.PermissionService, logger outbound.Logger) {
	mux.HandleFunc("POST /api/v1/hooks/claudecode",
		claudecode.NewHookHandler(detector, metricsCollector, permService, logger))
	mux.HandleFunc("POST /api/v1/hooks/claudecode/statusline",
		claudecode.NewStatuslineHandler(metricsCollector, permService, logger))
}

// publishAddrFile writes the addr file and thereby signals "the daemon is
// up" — called only once every route is registered. Publishing earlier
// raced a fast client (including this package's own startup smoke test)
// against still-in-progress mux registration: a request landing in that
// window fell through to the catch-all "/" handler — surfaced as a spurious
// 503 on GET /api/v1/permissions under loaded CI (#795).
//
// Writes atomically (temp + rename) so a reader polling the file can never
// observe a half-written, truncated host:port.
func publishAddrFile(addrPath, resolvedAddr string, logger outbound.Logger) {
	tmpAddr := addrPath + ".tmp"
	if err := os.WriteFile(tmpAddr, []byte(resolvedAddr+"\n"), 0600); err != nil {
		logger.LogError("startup", "", fmt.Sprintf("failed to write addr file %s: %v", tmpAddr, err))
		return
	}
	if err := os.Rename(tmpAddr, addrPath); err != nil {
		logger.LogError("startup", "", fmt.Sprintf("failed to publish addr file %s: %v", addrPath, err))
	}
}

// setupRecording enables lifecycle recording (opt-in via --record or
// IRRLICHT_RECORD=1). Always returns a cleanup func — a no-op on init
// failure — so the caller can unconditionally defer it.
func setupRecording(detector *services.SessionDetector, sockPath string, logger outbound.Logger) func() {
	recordingsDir := resolveRecordingsDir(sockPath)
	rec, err := recorder.NewJSONLRecorder(recordingsDir)
	if err != nil {
		logger.LogError("startup", "", fmt.Sprintf("failed to init lifecycle recorder: %v", err))
		return func() {
			// no-op cleanup — recorder never initialized
		}
	}
	detector.SetRecorder(rec)
	logger.LogInfo("startup", "", fmt.Sprintf("lifecycle recording enabled: %s", rec.Path()))
	return func() { rec.Close() }
}

// sweepZombies runs the synchronous startup zombie sweep: before the
// detector's event loop and before the API has anything new to broadcast,
// so persisted records inherited from a prior daemon run whose process is
// gone are gone from the API too. Skipped under demo mode (which seeds
// sessions without backing processes on purpose).
func sweepZombies(demoMode bool, detector *services.SessionDetector, logger outbound.Logger) {
	if demoMode {
		return
	}
	if n := detector.CleanupZombies(); n > 0 {
		logger.LogInfo("startup", "", fmt.Sprintf("cleaned up %d zombie session(s) inherited from a prior daemon run", n))
	}
}

// startBackgroundLoops launches the detector's event loop and its
// long-running companions (backchannel rule engine, yield sweeper,
// terminal observer), then — outside demo mode — exercises granted
// permissions (re-apply hook/statusline installs, start watchers) and
// launches the detection poller. Effects run synchronously so a grant-all
// daemon (demo/record/test) is fully monitoring before startup proceeds.
// Returns the cancel func for the shared context; the caller defers it.
// startBackgroundLoopsDeps bundles startBackgroundLoops' dependencies.
type startBackgroundLoopsDeps struct {
	Detector          *services.SessionDetector
	BackchannelEngine *services.BackchannelEngine
	CachedRepo        outbound.SessionRepository
	GitResolver       *git.Adapter
	TerminalObserver  *services.TerminalObserver
	PermService       *services.PermissionService
	Cfg               config.Config
	DemoMode          bool
	Logger            outbound.Logger
}

func startBackgroundLoops(deps startBackgroundLoopsDeps) context.CancelFunc {
	logger := deps.Logger
	detectorCtx, detectorCancel := context.WithCancel(context.Background())
	go func() {
		if err := deps.Detector.Run(detectorCtx); err != nil && err != context.Canceled {
			logger.LogError("session-detector", "", fmt.Sprintf("detector error: %v", err))
		}
	}()

	go deps.BackchannelEngine.Run(detectorCtx)

	// Yield sweep (issue #373): periodically correlates `git revert` commits
	// back to the sessions that authored the reverted work, flipping their
	// YieldState to reverted. Read-mostly and fault-tolerant, so it runs in
	// every mode.
	go services.NewYieldSweeper(deps.CachedRepo, deps.GitResolver, logger, deps.Cfg.YieldSweepInterval).Run(detectorCtx)

	go func() {
		if err := deps.TerminalObserver.Run(detectorCtx); err != nil && err != context.Canceled {
			logger.LogError("terminal-observer", "", fmt.Sprintf("observer error: %v", err))
		}
	}()

	if !deps.DemoMode {
		deps.PermService.Start(detectorCtx)
		// The installed hooks POST via curl; without it they silently no-op
		// and tool-use permission prompts never surface as `waiting`. Warn
		// loudly so this isn't an invisible failure (the transcript
		// heuristic still covers held file-edit prompts). See #488.
		if deps.PermService.Granted(claudecode.AdapterName, claudecode.PermissionKeyHooks) &&
			!claudecode.HookDeliveryAvailable() {
			logger.LogError("startup", "", "curl not found on PATH — Claude Code permission-prompt detection is degraded: hooks POST via curl, so without it permission prompts may not surface as 'waiting'. Install curl to restore full detection (#488).")
		}
	}

	return detectorCancel
}

// shutdown gracefully stops mDNS advertising and the HTTP server, bounded
// by a shared deadline.
func shutdown(srv *http.Server, mdnsAdv *mdns.Advertiser, logger outbound.Logger) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if mdnsAdv != nil {
		mdnsAdv.Shutdown(ctx)
	}

	if err := srv.Shutdown(ctx); err != nil {
		logger.LogError("shutdown", "", fmt.Sprintf("graceful shutdown error: %v", err))
	}
}
