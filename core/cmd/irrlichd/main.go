package main

import (
	"context"
	"fmt"
	"log"
	"mime"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	activationhandler "irrlicht/core/adapters/inbound/activation"
	"irrlicht/core/adapters/inbound/agents"
	"irrlicht/core/adapters/inbound/agents/agentwiring"
	"irrlicht/core/adapters/inbound/agents/claudecode"
	"irrlicht/core/adapters/inbound/agents/processlifecycle"
	backchannelhandler "irrlicht/core/adapters/inbound/backchannel"
	gastownadapter "irrlicht/core/adapters/inbound/orchestrators/gastown"
	permissionshandler "irrlicht/core/adapters/inbound/permissions"
	sessionshandler "irrlicht/core/adapters/inbound/sessions"
	"irrlicht/core/adapters/outbound/control"
	"irrlicht/core/adapters/outbound/filesystem"
	"irrlicht/core/adapters/outbound/git"
	"irrlicht/core/adapters/outbound/gtbin"
	"irrlicht/core/adapters/outbound/logging"
	"irrlicht/core/adapters/outbound/mdns"
	"irrlicht/core/adapters/outbound/metrics"
	"irrlicht/core/adapters/outbound/recorder"
	"irrlicht/core/adapters/outbound/relay"
	wshub "irrlicht/core/adapters/outbound/websocket"
	"irrlicht/core/application/services"
	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/config"
	"irrlicht/core/domain/permission"
	"irrlicht/core/domain/session"
	"irrlicht/core/pkg/capacity"
	"irrlicht/core/ports/inbound"
	"irrlicht/core/ports/outbound"
)

// Version is injected at build time via -ldflags "-X main.Version=x.y.z".
var Version = "dev"

// lazyControl adapts the daemon's InputService to relay.ControlHandler with a
// late binding: the publish controller is constructed during relay setup, which
// precedes the consent stack that builds InputService. resolve returns nil
// until then; a control frame that races startup is rejected, not panicked.
type lazyControl struct{ resolve func() relay.ControlHandler }

func (l lazyControl) SendInput(id string, d []byte) error {
	if h := l.resolve(); h != nil {
		return h.SendInput(id, d)
	}
	return fmt.Errorf("relay control: input service not ready")
}

func (l lazyControl) Interrupt(id string) error {
	if h := l.resolve(); h != nil {
		return h.Interrupt(id)
	}
	return fmt.Errorf("relay control: input service not ready")
}

const (
	defaultBindAddr = "127.0.0.1:7837"
	tcpPort         = 7837
	envUIDir        = "IRRLICHT_UI_DIR"
)

// resolveBindAddr returns the TCP bind address for the daemon. Default is
// loopback-only; set IRRLICHT_BIND_ADDR to override (e.g. "0.0.0.0:7837" to
// expose the daemon on the LAN).
func resolveBindAddr(envValue string) string {
	if envValue == "" {
		return defaultBindAddr
	}
	if _, _, err := net.SplitHostPort(envValue); err != nil {
		return defaultBindAddr
	}
	return envValue
}

func hasFlag(name string) bool {
	for _, arg := range os.Args[1:] {
		if arg == name {
			return true
		}
	}
	return false
}

func main() {
	if hasFlag("--version") || hasFlag("-v") {
		fmt.Printf("irrlichd version %s\n", Version)
		fmt.Printf("Built with %s %s/%s\n", runtime.Version(), runtime.GOOS, runtime.GOARCH)
		os.Exit(0)
	}

	if hasFlag("--uninstall-hooks") {
		modified, err := claudecode.UninstallHooks()
		if err != nil {
			log.Fatalf("failed to uninstall hooks: %v", err)
		}
		if modified {
			fmt.Println("Removed irrlicht hooks from ~/.claude/settings.json")
		} else {
			fmt.Println("No irrlicht hooks found in ~/.claude/settings.json")
		}
		// Record the decision in the consent store (#570) — a persisted
		// "granted" would otherwise re-install the hooks on the next
		// daemon start, silently reverting this explicit opt-out.
		uhHome, _ := os.UserHomeDir()
		uhStore := filesystem.NewPermissionStore(dataDir(uhHome))
		if set, err := uhStore.Load(); err == nil {
			if set.Get(claudecode.AdapterName, claudecode.PermissionKeyHooks) == permission.StateGranted {
				set.Put(claudecode.AdapterName, claudecode.PermissionKeyHooks, permission.StateDenied)
				if err := uhStore.Save(set); err != nil {
					fmt.Printf("warning: failed to record hooks permission as denied: %v\n", err)
				} else {
					fmt.Println("Recorded the hooks permission as denied (re-grant via the permission wizard)")
				}
			}
		}
		os.Exit(0)
	}

	if hasFlag("--uninstall-task-eta") {
		modified, err := claudecode.UninstallTaskEtaBlock()
		if err != nil {
			log.Fatalf("failed to uninstall task-eta block: %v", err)
		}
		if modified {
			fmt.Println("Removed irrlicht task-eta block from ~/.claude/CLAUDE.md")
		} else {
			fmt.Println("No irrlicht task-eta block found in ~/.claude/CLAUDE.md")
		}
		os.Exit(0)
	}

	if hasFlag("--diagnose") {
		runDiagnose()
		os.Exit(0)
	}

	recordEnabled := hasFlag("--record") || os.Getenv("IRRLICHT_RECORD") == "1"

	logger, err := logging.New()
	if err != nil {
		log.Fatalf("failed to initialise logger: %v", err)
	}
	defer logger.Close()

	// Hook + statusline installation is consent-gated (issue #570): the
	// PermissionService applies them when (and only when) the user grants
	// the corresponding permission — see permService.Start below. Nothing
	// under the user's home is modified before that.

	// Configuration.
	cfg := config.Default()
	if v := os.Getenv("IRRLICHT_MAX_SESSION_AGE"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.MaxSessionAge = d
		} else {
			logger.LogError("startup", "", fmt.Sprintf("invalid IRRLICHT_MAX_SESSION_AGE %q, using default %s", v, cfg.MaxSessionAge))
		}
	}
	logger.LogInfo("startup", "", fmt.Sprintf("max session age: %s", cfg.MaxSessionAge))

	go runCapacityRefreshLoop(context.Background(), logger, 30*time.Second, 256*time.Minute, 24*time.Hour)

	// Resolve the gt binary path (GT_BIN env → common paths → which gt).
	gtResolver := gtbin.New()
	if p := gtResolver.Path(); p != "" {
		logger.LogInfo("startup", "", fmt.Sprintf("gt binary: %s", p))
	} else {
		logger.LogError("startup", "", "gt binary not found (set GT_BIN or add gt to PATH)")
	}

	fsRepo, cachedRepo := initSessionStorage(logger, cfg)
	costTracker := initCostTracker(logger, fsRepo)
	historyTracker, historyCancel := startHistoryTracker(logger)
	defer historyCancel()

	// Push broadcaster for WebSocket fan-out.
	push := services.NewPushService()

	// Stream history events (snapshots, ticks, upgrades) over the same
	// WebSocket envelope as session-state messages.
	historyTracker.SetEmitFunc(historyEventBroadcaster(push))

	// Unified registration for every inbound agent adapter. Wiring below
	// (fswatchers, process scanners, metrics parser map, PID discovery map)
	// derives from this single slice — the only place new agents need to be
	// listed. The slice itself lives in core/adapters/inbound/agents/all.go
	// so the agent-onboarding viewer can build the same metrics Registry
	// during replay without duplicating the construction.
	allAgents := agents.All()

	// Outbound relay publishing: subscribe to the same push broadcaster the
	// local WebSocket uses and push every session event out to a standalone
	// irrlichtrelay, so remote clients see this daemon's sessions through the
	// relay. Pushing out means the daemon needs no inbound reachability (works
	// behind NAT). The PublishController owns the forwarder's lifecycle so the
	// macOS app can start/stop/reconfigure publishing live over the loopback
	// PUT /api/v1/relay/publish — no daemon relaunch (issue #722). The
	// controller is always constructed (so the endpoint can turn publishing on
	// later); it is seeded once below from IRRLICHT_RELAY_URL /
	// IRRLICHT_RELAY_TOKEN so headless/standalone daemons keep working as before.
	relayBaseCtx, relayBaseCancel := context.WithCancel(context.Background())
	defer relayBaseCancel()
	relayHome, _ := os.UserHomeDir()
	relayIdentity, err := relay.LoadOrCreateIdentity(dataDir(relayHome))
	if err != nil {
		logger.LogError("startup", "", fmt.Sprintf("relay identity persistence failed (using ephemeral id): %v", err))
	}
	relaySnapshot := func() ([]*session.SessionState, []relay.AgentInfo) {
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
	// Remote control (#724): the relay-control toggle (default OFF, env opt-in
	// IRRLICHT_RELAY_CONTROL=on) gates whether the forwarder acts on inbound
	// control frames. inputService is built later (consent stack); lazyControl
	// binds to it once ready. Remote stays doubly gated — this toggle plus the
	// backchannel toggle + per-agent consent re-checked inside InputService.
	relayControlStore := filesystem.NewRelayControlStore(dataDir(relayHome))
	relayControlEnabled := func() bool {
		return relayControlStore.Enabled() || os.Getenv("IRRLICHT_RELAY_CONTROL") == "on"
	}
	// inputService is published once the consent stack is built (below). It's
	// read concurrently by the HTTP handlers and the relay forwarder, so it's an
	// atomic.Pointer rather than a plain var: those readers start (Serve / the
	// env-seeded forwarder) before the assignment.
	var inputService atomic.Pointer[services.InputService]
	relayControl := lazyControl{resolve: func() relay.ControlHandler {
		if s := inputService.Load(); s != nil {
			return s
		}
		return nil
	}}
	publishController := relay.NewPublishController(relayBaseCtx, relayIdentity, push, relaySnapshot, relayControl, relayControlEnabled, logger)
	// Seed from the env opt-in (backward compatible with the pre-#722 path).
	// Bearer token for an auth-enabled relay: IRRLICHT_RELAY_TOKEN or
	// <dataDir>/relay-token.json (mode 0600). Empty against a no-auth relay.
	if relayURL := os.Getenv("IRRLICHT_RELAY_URL"); relayURL != "" {
		publishController.Apply(true, relayURL, relay.LoadDaemonToken(dataDir(relayHome)))
	}

	// Shared adapters for SessionDetector.
	gitResolver := git.New()
	// The metrics collector (parser map + aider/opencode overrides +
	// Claude Code fallback) is wired by agentwiring.BuildMetricsCollector,
	// the single source of truth shared with the agent-onboarding viewer.
	metricsCollector := agentwiring.BuildMetricsCollector(allAgents)

	// --- File-based SessionDetector (primary detection path) ---
	// Forward-reference: detector is assigned before any callbacks can fire,
	// because ProcessWatcher only invokes callbacks after
	// SessionDetector.Run() subscribes to Watcher events.
	var detector *services.SessionDetector

	// IRRLICHT_DEMO_MODE=1 disables ProcessWatcher and per-adapter Watchers
	// so the daemon serves only what's already on disk in instances/. Used by
	// tools/seed-demo-sessions to take controlled screenshots without live
	// agent processes leaking into the dropdown.
	demoMode := os.Getenv("IRRLICHT_DEMO_MODE") == "1"
	if demoMode {
		logger.LogInfo("startup", "", "IRRLICHT_DEMO_MODE=1 — process + agent watchers disabled")
	}

	// ProcessWatcher: kqueue EVFILT_PROC NOTE_EXIT monitoring.
	// Exit callback routes to SessionDetector for lifecycle management.
	var pwPort outbound.ProcessWatcher
	if !demoMode {
		pw, err := processlifecycle.NewMonitor(func(pid int, sessionID string) {
			detector.HandleProcessExit(pid, sessionID)
		})
		if err != nil {
			logger.LogError("startup", "", fmt.Sprintf("ProcessWatcher init failed (non-fatal): %v", err))
		} else {
			pwPort = pw
			procCtx, procCancel := context.WithCancel(context.Background())
			defer procCancel()
			go func() {
				if err := pw.Run(procCtx); err != nil && err != context.Canceled {
					logger.LogError("process-watcher", "", fmt.Sprintf("event loop error: %v", err))
				}
			}()
			defer pw.Close()
		}
	}

	// HTTP mux.
	mux := http.NewServeMux()
	// Sessions endpoint registered after orchMonitor is available (see below).
	mux.HandleFunc("GET /state", handleGetState(cachedRepo))

	hub := wshub.NewHub(push, historySnapshotProvider(historyTracker))
	mux.HandleFunc("GET /api/v1/sessions/stream", hub.ServeWS)
	mux.HandleFunc("GET /api/v1/agents", handleGetAgents(allAgents))
	mux.HandleFunc("GET /api/v1/version", handleGetVersion(Version))
	mux.HandleFunc("GET /api/v1/relay/publish", handleGetPublishStatus(publishController))
	// PUT reconfigures publishing on the running daemon (issue #722). Loopback
	// only: it mutates forwarder config and carries the relay token in its body.
	mux.HandleFunc("PUT /api/v1/relay/publish", localhostOnly(handlePutPublishStatus(publishController)))

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
	diagSvc := buildDiagnostics(fsRepo, allAgents, cfg)
	mux.HandleFunc("GET /debug/bundle", localhostOnly(diagSvc.HandleBundle))

	// Ensure web assets are served with correct Content-Type regardless of the
	// host OS mime.types database (absent on stripped Linux images). Go's
	// content-sniffing fallback returns text/plain for CSS (no magic bytes).
	_ = mime.AddExtensionType(".js", "application/javascript")
	_ = mime.AddExtensionType(".css", "text/css")

	// Static web UI: served from disk so the dashboard ships as three files
	// (index.html, irrlicht.css, irrlicht.js) under platforms/web/. API routes
	// registered above take precedence over the catch-all "/".
	if uiDir := resolveUIDir(); uiDir != "" {
		logger.LogInfo("startup", "", fmt.Sprintf("serving UI from %s", uiDir))
		mux.Handle("/", http.FileServer(http.Dir(uiDir)))
	} else {
		logger.LogError("startup", "", fmt.Sprintf("UI directory not found — set %s to the directory containing index.html", envUIDir))
		body := fmt.Sprintf("Dashboard UI not found.\nSet %s to the directory containing index.html, or reinstall via the DMG / curl installer.\n", envUIDir)
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprint(w, body)
		})
	}

	// WriteTimeout is intentionally 0: WebSocket streams and long-polling
	// responses need unbounded writes, and gorilla/websocket sets its own
	// per-message deadlines.
	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	// Unix socket.
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

	// TCP listener — default loopback; override with IRRLICHT_BIND_ADDR.
	// IRRLICHT_BIND_ADDR=127.0.0.1:0 binds an ephemeral port (used by the
	// startup smoke test so N daemons can run in parallel).
	bindAddr := resolveBindAddr(os.Getenv("IRRLICHT_BIND_ADDR"))
	tcpL, err := net.Listen("tcp", bindAddr)
	if err != nil {
		logger.LogError("startup", "", fmt.Sprintf("failed to listen on TCP %s: %v", bindAddr, err))
		os.Exit(1)
	}
	// resolvedAddr is the actual address (with the OS-assigned port when the
	// request was :0). Publish it to <dataDir>/irrlichd.addr so tooling and
	// the smoke test can find a daemon bound to an ephemeral port; the file
	// also doubles as a "daemon is up" signal. Removed on shutdown.
	resolvedAddr := tcpL.Addr().String()
	addrPath := filepath.Join(filepath.Dir(sockPath), "irrlichd.addr")
	// Write atomically (temp + rename) so a reader polling the file can never
	// observe a half-written, truncated host:port.
	tmpAddr := addrPath + ".tmp"
	if err := os.WriteFile(tmpAddr, []byte(resolvedAddr+"\n"), 0600); err != nil {
		logger.LogError("startup", "", fmt.Sprintf("failed to write addr file %s: %v", tmpAddr, err))
	} else if err := os.Rename(tmpAddr, addrPath); err != nil {
		logger.LogError("startup", "", fmt.Sprintf("failed to publish addr file %s: %v", addrPath, err))
	}

	go func() { _ = srv.Serve(unixL) }()
	go func() { _ = srv.Serve(tcpL) }()

	// mDNS/Bonjour advertisement — opt-in via IRRLICHT_MDNS=1 to avoid
	// broadcasting the daemon on networks the user did not intend to share on.
	var mdnsAdv *mdns.Advertiser
	if os.Getenv("IRRLICHT_MDNS") == "1" {
		// Advertise the port we actually bound (resolvedAddr), not the compile-time
		// default — otherwise a daemon on a custom/ephemeral port would point
		// discovery clients at 7837 (a dead or production port).
		advPort := tcpPort
		if _, p, err := net.SplitHostPort(resolvedAddr); err == nil {
			if n, err := strconv.Atoi(p); err == nil {
				advPort = n
			}
		}
		mdnsAdv, err = mdns.New(advPort)
		if err != nil {
			logger.LogError("startup", "", fmt.Sprintf("mDNS advertisement failed (non-fatal): %v", err))
		} else {
			logger.LogInfo("startup", "", "mDNS: advertising _irrlicht._tcp on the local network")
		}
	} else {
		logger.LogInfo("startup", "", "mDNS: disabled (set IRRLICHT_MDNS=1 to advertise)")
	}

	// Orchestrator adapters: detect and watch multi-agent orchestration
	// systems. Gas Town is consent-gated (#570): even constructing the
	// adapter reads ~/gt state files, so nothing is built until the
	// "gastown/state" permission is granted — the PermissionService runs
	// the start/stop closures below as that permission's effects. The
	// monitor runs regardless (idle until a watcher registers) so
	// /api/v1/sessions can always consult it.
	orchMonitor := services.NewOrchestratorMonitor(nil, push, logger)
	orchCtx, orchCancel := context.WithCancel(context.Background())
	defer orchCancel()
	go func() {
		if err := orchMonitor.Run(orchCtx); err != nil && err != context.Canceled {
			logger.LogError("orchestrator-monitor", "", fmt.Sprintf("monitor error: %v", err))
		}
	}()

	// Effect closures for the gastown/state permission. The service
	// serializes effect execution, so no extra locking around gtWatchCancel.
	var gtWatchCancel context.CancelFunc
	startGastown := func() error {
		if gtWatchCancel != nil {
			return nil
		}
		gtAdapter := gastownadapter.NewAdapter(gtResolver.Path(), cachedRepo, logger)
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
	stopGastown := func() error {
		if gtWatchCancel != nil {
			gtWatchCancel()
			gtWatchCancel = nil
		}
		return nil
	}

	// Register API endpoints (after orchMonitor is available). inputService is
	// declared up in the relay block (lazyControl binds to it); it's assigned
	// in the backchannel block below, so this closure resolves it at request
	// time — a session reports controllable only once that wiring runs.
	mux.HandleFunc("GET /api/v1/sessions", handleGetSessions(cachedRepo, orchMonitor, costTracker,
		func(sessionID string) bool {
			if s := inputService.Load(); s != nil {
				return s.Controllable(sessionID)
			}
			return false
		}))

	focusService := services.NewFocusService(cachedRepo, push, logger)
	mux.HandleFunc("POST /api/v1/sessions/{id}/focus", sessionshandler.NewFocusHandler(focusService, logger))

	// Suppress ghost proc pre-sessions for live processes whose real session
	// is already persisted. The PID discriminator in HasRealSessionForPID
	// prevents historical sessions on disk (GH #113, within MaxSessionAge)
	// from blocking new processes in the same project.
	realSessionCheck := func(projectDir string, pid int) bool {
		sessions, err := cachedRepo.ListAll()
		if err != nil {
			return false
		}
		return processlifecycle.HasRealSessionForPID(sessions, projectDir, pid)
	}

	// Per-adapter inbound wiring is consent-gated (issue #570): instead of
	// starting every agent's watchers at boot, each agent gets a factory
	// (dispatching on its Source variant via buildAgentWatchers, see
	// wiring.go) that the PermissionService invokes when the user grants
	// that agent's observe permission — and cancels on revoke. Skipped
	// entirely under IRRLICHT_DEMO_MODE=1 — daemon serves only what's
	// already on disk in instances/.
	var watcherFactories map[string]services.WatcherFactory
	if !demoMode {
		watcherFactories = make(map[string]services.WatcherFactory, len(allAgents))
		for _, a := range allAgents {
			watcherFactories[a.Identity.Name] = func() []inbound.Watcher {
				ws, _ := buildAgentWatchers(a, cfg.MaxSessionAge, realSessionCheck)
				return ws
			}
		}
	}

	pidDiscovers := agents.PIDDiscoverers(allAgents)
	processNames := agents.ProcessNames(allAgents)

	// SessionDetector: orchestrates Watchers + ProcessWatcher. Watchers
	// register dynamically via AddWatcher as permissions are granted.
	detector = services.NewSessionDetector(
		nil, pwPort,
		cachedRepo, logger, gitResolver, metricsCollector, push,
		Version, cfg.ReadySessionTTL,
		pidDiscovers, processNames, processlifecycle.LiveCWDs,
	)
	if costTracker != nil {
		detector.SetCostTracker(costTracker)
	}
	detector.SetHistoryTracker(historyTracker)

	// PermissionService: single source of truth for consent state (issue
	// #570). Exercises grants (hook install, watcher start), undoes
	// revokes, arbitrates wizard answers between the macOS and web UIs,
	// and feeds the always-on detection poller that makes the wizard
	// appear when a new agent shows up. Under demo mode the factories and
	// detection are nil — the daemon never monitors anything live.
	home, _ := os.UserHomeDir()
	permStore := filesystem.NewPermissionStore(dataDir(home))
	// Fold the pre-wizard task-eta consent record (activation.json, issue
	// #558) into the store before the service loads it (issue #577).
	services.MigrateLegacyTaskEtaConsent(dataDir(home), permStore,
		claudecode.AdapterName, claudecode.PermissionKeyInstructions, logger)
	var hasLive services.HasLiveProcessFunc
	if !demoMode {
		hasLive = processlifecycle.HasLiveProcess
	}
	// The consent catalog is the agent adapters plus three daemon-wide
	// entries with no Source/Process axes: the Gas Town orchestrator
	// (reads ~/gt), launcher-identity capture (reads whitelisted env
	// vars from agent processes for click-to-focus), and the kitty
	// remote-control config patch (writes kitty.conf for tab-precise
	// click-to-focus, #425).
	permissionAgents := append(append([]agent.Agent{}, allAgents...),
		gastownadapter.PermissionDeclaration(startGastown, stopGastown),
		processlifecycle.LauncherPermissionDeclaration(),
		processlifecycle.KittyPermissionDeclaration())
	permService := services.NewPermissionService(
		permissionAgents, permStore, push, logger,
		cfg.PermissionMode, detector, watcherFactories, hasLive,
	)
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
	// Let the liveness sweep reap a session bound to a still-alive PID that is
	// the adapter's background infra rather than the session — Claude Code's
	// --bg-spare pool helper outlives every session, so a mis-bound session
	// would otherwise never be reaped (#727). Demo mode never tracks live
	// processes, so leave the reaper unwired there.
	if !demoMode {
		detector.SetInfraReaper(agents.ArgvExcluders(allAgents), processlifecycle.ReadArgv)
	}
	// Consent gate for the detector's own transcript reads (startup seed +
	// stale-working refresh of PERSISTED sessions) — the watcher pipeline
	// is gated by construction, but these two paths read repo-listed
	// transcripts directly and must honor per-adapter consent too (#570).
	// Demo mode keeps the gate open: it serves synthetic seeded sessions
	// and never reads live agent files in the first place.
	if !demoMode {
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

	// Backchannel (issue #724): control discovered agents by scripting their
	// terminal backend. A default-OFF master toggle gates the whole capability;
	// the per-adapter "control" consent and a usable backend target gate each
	// write. InputService enforces the order (toggle → consent → controllable)
	// for both the manual HTTP path and (later) the event→action rule engine.
	// localhostOnly + a Sec-Fetch-Site guard on the mutating verbs, since
	// injected input drives a live agent.
	backchannelStore := filesystem.NewBackchannelStore(dataDir(home))
	controlController := control.NewController(cachedRepo, push, logger)
	in := services.NewInputService(cachedRepo, controlController, permService, backchannelStore.Enabled, logger)
	inputService.Store(in) // publish to the HTTP/forwarder readers (see decl above)
	mux.HandleFunc("/api/v1/activation/backchannel",
		localhostOnly(activationhandler.NewBackchannelHandler(backchannelStore, logger)))
	// Remote-control toggle (#724): default OFF; gates whether the relay
	// forwarder acts on inbound control frames (the outer remote gate).
	mux.HandleFunc("/api/v1/activation/relay-control",
		localhostOnly(activationhandler.NewToggleHandler(relayControlStore, "relay_control_enabled", logger)))
	mux.HandleFunc("POST /api/v1/sessions/{id}/input",
		localhostOnly(sessionshandler.NewInputHandler(in, logger)))
	mux.HandleFunc("POST /api/v1/sessions/{id}/interrupt",
		localhostOnly(sessionshandler.NewInterruptHandler(in, logger)))

	// Backchannel rules (issue #724): event→action automations (e.g.
	// context_pressure → /compact). The engine consumes the push stream and
	// fires through inputService, so the same toggle+consent+controllable
	// gates apply. Started under detectorCtx below.
	backchannelRules := filesystem.NewBackchannelRulesStore(dataDir(home))
	backchannelEngine := services.NewBackchannelEngine(backchannelRules, in, push, backchannelStore.Enabled, logger)
	mux.HandleFunc("/api/v1/backchannel/rules",
		localhostOnly(backchannelhandler.NewRulesHandler(backchannelRules, logger)))

	// Backchannel read-back (issue #732, Phase 3): the read counterpart to the
	// write path. The observer captures the rendered terminal of controllable
	// sessions and folds transcript-invisible signals (today: the trust dialog)
	// into the lifecycle via the detector's single writer. Same gate chain as
	// inputService — master-toggle + per-adapter "control" consent — so a
	// disabled backchannel reads nothing. Started under detectorCtx below.
	terminalReader := control.NewReader(cachedRepo, logger)
	terminalObserver := services.NewTerminalObserver(cachedRepo, terminalReader, permService, backchannelStore.Enabled, detector, logger)

	// Hook receiver: Claude Code PermissionRequest/PostToolUse events.
	// The detector satisfies claudecode.HookTarget via HandlePermissionHook.
	// Consent-gated: hooks installed by a pre-consent daemon keep firing
	// until the wizard is answered, so payloads are dropped while pending.
	mux.HandleFunc("POST /api/v1/hooks/claudecode",
		claudecode.NewHookHandler(detector, metricsCollector, permService, logger))

	// Statusline receiver: Claude Code's per-tick statusline JSON, carrying
	// rate_limits for Pro/Max subscribers (issue #309). Routes to the
	// metrics adapter so the snapshot lands in the right session's tailer.
	mux.HandleFunc("POST /api/v1/hooks/claudecode/statusline",
		claudecode.NewStatuslineHandler(metricsCollector, permService, logger))

	// Lifecycle recording: opt-in via --record flag or IRRLICHT_RECORD=1.
	// Recordings default to <dataDir>/recordings, so IRRLICHT_HOME already
	// isolates them. IRRLICHT_RECORDINGS_DIR is the narrower override that
	// wins even when IRRLICHT_HOME is set, so test harnesses (e.g. the
	// onboarding factory's record path) can pin recordings somewhere specific.
	if recordEnabled {
		recordingsDir := os.Getenv("IRRLICHT_RECORDINGS_DIR")
		if recordingsDir == "" {
			recordingsDir = filepath.Join(filepath.Dir(sockPath), "recordings")
		}
		rec, err := recorder.NewJSONLRecorder(recordingsDir)
		if err != nil {
			logger.LogError("startup", "", fmt.Sprintf("failed to init lifecycle recorder: %v", err))
		} else {
			detector.SetRecorder(rec)
			defer rec.Close()
			logger.LogInfo("startup", "", fmt.Sprintf("lifecycle recording enabled: %s", rec.Path()))
		}
	}
	// Synchronous startup zombie sweep. Runs before the detector's event
	// loop and before the API has anything new to broadcast, so persisted
	// records inherited from a prior daemon run whose process is gone are
	// gone from the API too. Skipped under demo mode (which seeds sessions
	// without backing processes on purpose).
	if !demoMode {
		if n := detector.CleanupZombies(); n > 0 {
			logger.LogInfo("startup", "", fmt.Sprintf("cleaned up %d zombie session(s) inherited from a prior daemon run", n))
		}
	}

	{
		detectorCtx, detectorCancel := context.WithCancel(context.Background())
		defer detectorCancel()
		go func() {
			if err := detector.Run(detectorCtx); err != nil && err != context.Canceled {
				logger.LogError("session-detector", "", fmt.Sprintf("detector error: %v", err))
			}
		}()

		// Backchannel rule engine: consumes the push stream and fires
		// event→action rules through inputService (issue #724).
		go backchannelEngine.Run(detectorCtx)

		// Backchannel read-back observer (issue #732): polls controllable
		// sessions' rendered terminals and folds transcript-invisible UI
		// signals into the lifecycle. Gated by the same master-toggle+consent.
		go func() {
			if err := terminalObserver.Run(detectorCtx); err != nil && err != context.Canceled {
				logger.LogError("terminal-observer", "", fmt.Sprintf("observer error: %v", err))
			}
		}()

		// Exercise granted permissions (re-apply hook/statusline installs,
		// start watchers) and launch the detection poller. Effects run
		// synchronously so a grant-all daemon (demo/record/test) is fully
		// monitoring before startup proceeds. Skipped under demo mode —
		// nothing live is watched, so consent effects must not run either.
		if !demoMode {
			permService.Start(detectorCtx)
			// The installed hooks POST via curl; without it they silently
			// no-op and tool-use permission prompts never surface as
			// `waiting`. Warn loudly so this isn't an invisible failure
			// (the transcript heuristic still covers held file-edit
			// prompts). See #488.
			if permService.Granted(claudecode.AdapterName, claudecode.PermissionKeyHooks) &&
				!claudecode.HookDeliveryAvailable() {
				logger.LogError("startup", "", "curl not found on PATH — Claude Code permission-prompt detection is degraded: hooks POST via curl, so without it permission prompts may not surface as 'waiting'. Install curl to restore full detection (#488).")
			}
		}
	}

	logger.LogInfo("startup", "", fmt.Sprintf("irrlichd %s listening on unix:%s and tcp:%s", Version, sockPath, resolvedAddr))

	// Wait for SIGTERM or SIGINT.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	<-sig

	logger.LogInfo("shutdown", "", "signal received, shutting down")

	// Remove the addr file so it can't outlive the daemon and mislead tooling
	// into connecting to a dead port.
	os.Remove(addrPath)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if mdnsAdv != nil {
		mdnsAdv.Shutdown(ctx)
	}

	if err := srv.Shutdown(ctx); err != nil {
		logger.LogError("shutdown", "", fmt.Sprintf("graceful shutdown error: %v", err))
	}
}

// runCapacityRefreshLoop keeps the LiteLLM model-capacity cache current,
// retrying failed fetches with exponential backoff so a daemon started
// offline recovers as soon as connectivity returns (rather than waiting
// the full successInterval for the next attempt).
func runCapacityRefreshLoop(ctx context.Context, logger outbound.Logger, initialBackoff, maxBackoff, successInterval time.Duration) {
	backoff := initialBackoff
	for {
		if !capacity.IsCacheStale() {
			select {
			case <-ctx.Done():
				return
			case <-time.After(successInterval):
			}
			continue
		}

		config, err := capacity.FetchAndCacheLiteLLMData()
		if err != nil {
			logger.LogError("capacity", "", fmt.Sprintf("remote refresh failed (retry in %s): %v", backoff, err))
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
			continue
		}

		logger.LogInfo("capacity", "", fmt.Sprintf("cached %d remote models from LiteLLM", len(config.Models)))
		backoff = initialBackoff
		select {
		case <-ctx.Done():
			return
		case <-time.After(successInterval):
		}
	}
}

// dataDir returns the irrlichd state directory (~/.local/share/irrlicht).
// home should come from os.UserHomeDir(); pass "" only when the lookup
// failed.
//
// IRRLICHT_HOME relocates this tree — socket, addr file, history rollups,
// the on-disk web fallback, and recordings all live beneath it. The session
// store, per-session ledgers, and cost store live under different roots
// (Application Support); stateStoreDir routes those through IRRLICHT_HOME too,
// so a dev/test daemon is fully isolated from the production install (and from
// other worktrees) without touching ~/.local/share/irrlicht/ or
// ~/Library/Application Support/Irrlicht/. Recordings still honor the narrower
// IRRLICHT_RECORDINGS_DIR override when set.
func dataDir(home string) string {
	if v := os.Getenv("IRRLICHT_HOME"); v != "" {
		return v
	}
	if home == "" {
		return "/tmp/irrlicht"
	}
	return filepath.Join(home, ".local", "share", "irrlicht")
}

// stateStoreDir returns the directory for a named state store (e.g. "instances",
// "cost", "sessions") when IRRLICHT_HOME is set, so every store nests beneath it
// for full isolation. Returns "" when IRRLICHT_HOME is unset, signaling the
// caller to keep its production-default location unchanged. Needed because the
// session repo and cost store root under ~/Library/Application Support/Irrlicht
// and ledgers under ~/.local/share/irrlicht/sessions — none of which flow
// through dataDir.
func stateStoreDir(sub string) string {
	if v := os.Getenv("IRRLICHT_HOME"); v != "" {
		return filepath.Join(v, sub)
	}
	return ""
}

// buildDiagnostics constructs the diagnostics bundle service (issue #736) from
// the daemon's resolved stores. Shared by the GET /debug/bundle handler and the
// --diagnose CLI so both snapshot the exact same locations. fsRepo supplies both
// the (uncached) session list and the instances dir; ledger and log dirs honor
// IRRLICHT_HOME via the same accessors the daemon writes through.
func buildDiagnostics(fsRepo *filesystem.SessionRepository, allAgents []agent.Agent, cfg config.Config) *services.DiagnosticsService {
	home, _ := os.UserHomeDir()
	ledgerDir, _ := metrics.LedgerDir()
	logsDir, _ := logging.LogDir()
	return services.NewDiagnosticsService(
		fsRepo,
		processlifecycle.Observer(),
		processlifecycle.IsAlive,
		allAgents,
		cfg,
		Version,
		services.DiagnosticsPaths{
			Home:            home,
			InstancesDir:    fsRepo.InstancesDir(),
			LedgerDir:       ledgerDir,
			LogsDir:         logsDir,
			PermissionsFile: filepath.Join(dataDir(home), "permissions.json"),
		},
	)
}

// runDiagnose writes a diagnostics bundle to the current directory and exits.
// For headless / curl-only installs that can't hit GET /debug/bundle. It
// resolves the same stores the daemon would (honoring IRRLICHT_HOME) without
// starting the daemon.
func runDiagnose() {
	if dir := stateStoreDir("sessions"); dir != "" {
		metrics.SetLedgerDir(dir)
	}
	var fsRepo *filesystem.SessionRepository
	if dir := stateStoreDir("instances"); dir != "" {
		fsRepo = filesystem.NewWithDir(dir)
	} else {
		var err error
		fsRepo, err = filesystem.New()
		if err != nil {
			log.Fatalf("diagnose: init session repo: %v", err)
		}
	}
	cfg := config.Default()
	if v := os.Getenv("IRRLICHT_MAX_SESSION_AGE"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.MaxSessionAge = d
		}
	}
	const out = "irrlicht-diag.tar.gz"
	f, err := os.Create(out)
	if err != nil {
		log.Fatalf("diagnose: create %s: %v", out, err)
	}
	defer f.Close()
	if err := buildDiagnostics(fsRepo, agents.All(), cfg).WriteBundle(f); err != nil {
		log.Fatalf("diagnose: write bundle: %v", err)
	}
	abs, _ := filepath.Abs(out)
	fmt.Printf("Wrote diagnostics bundle to %s\n", abs)
}

// socketPath returns the Unix socket path for irrlichd. It routes through
// dataDir so the IRRLICHT_HOME override is honored even when os.UserHomeDir()
// fails (dataDir maps an empty home to /tmp/irrlicht when no override is set).
func socketPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(dataDir(home), "irrlichd.sock")
}

// resolveUIDir locates the directory containing the dashboard's index.html.
// See resolveUIDirFor for the search order.
func resolveUIDir() string {
	exe, _ := os.Executable()
	home, _ := os.UserHomeDir()
	return resolveUIDirFor(os.Getenv(envUIDir), exe, home)
}

// resolveUIDirFor is the pure variant of resolveUIDir for testing. Search
// order: env → <exe>/../Resources/web (production .app bundle) → <home>/
// .local/share/irrlicht/web (daemon-only curl install) → walk up from <exe>
// to the enclosing repo root (a directory containing .git) and check for
// platforms/web/index.html (dev checkout). Returns "" on miss.
//
// The dev walk-up is bounded by .git so it can't escape a git worktree
// into a parent repo's platforms/web/ — that bug would silently serve the
// wrong dashboard during dev.
func resolveUIDirFor(env, exe, home string) string {
	hasIndex := func(dir string) bool {
		if dir == "" {
			return false
		}
		_, err := os.Stat(filepath.Join(dir, "index.html"))
		return err == nil
	}

	if hasIndex(env) {
		return env
	}
	if exe != "" {
		if cand := filepath.Join(filepath.Dir(exe), "..", "Resources", "web"); hasIndex(cand) {
			return cand
		}
	}
	if home != "" {
		if cand := filepath.Join(dataDir(home), "web"); hasIndex(cand) {
			return cand
		}
	}
	if exe != "" {
		dir := filepath.Dir(exe)
		for range 8 {
			if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
				if cand := filepath.Join(dir, "platforms", "web"); hasIndex(cand) {
					return cand
				}
				return "" // repo root found, no UI inside — don't escape
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}
	return ""
}

// costTimeframeSeconds maps the four supported time-frame keys to their
// trailing-window duration in seconds. These are rolling windows (not
// calendar-aligned) and are embedded under each project group's "costs"
// field in the /api/v1/sessions response.
var costTimeframeSeconds = map[string]int64{
	"day":   24 * 3600,
	"week":  7 * 24 * 3600,
	"month": 30 * 24 * 3600,
	"year":  365 * 24 * 3600,
}

// costAttachTTL bounds how stale the cached per-project cost maps may be
// before the handler recomputes them. Well below either client's 30 s
// poll cadence, short enough to keep the dashboard feeling live.
const costAttachTTL = 5 * time.Second

// initSessionStorage opens the filesystem session repository, prunes stale
// session files and dead proc-<pid> entries left by prior daemon lifetimes,
// and returns both the raw repo (for baseline scans) and a caching wrapper
// (returned as the outbound.SessionRepository interface since the concrete
// cached type is unexported).
func initSessionStorage(logger outbound.Logger, cfg config.Config) (*filesystem.SessionRepository, outbound.SessionRepository) {
	// Honor IRRLICHT_HOME for full isolation: a dev/test daemon must not prune
	// or mutate the production session store. Ledgers route through the same
	// override (set before the orphan-ledger sweep below reads LedgerDir).
	if dir := stateStoreDir("sessions"); dir != "" {
		metrics.SetLedgerDir(dir)
	}
	var fsRepo *filesystem.SessionRepository
	if dir := stateStoreDir("instances"); dir != "" {
		fsRepo = filesystem.NewWithDir(dir)
	} else {
		var err error
		fsRepo, err = filesystem.New()
		if err != nil {
			logger.LogError("startup", "", fmt.Sprintf("failed to init filesystem repo: %v", err))
			os.Exit(1)
		}
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
	expected := make(map[string]struct{}, len(allSessions))
	for _, s := range allSessions {
		if s.TranscriptPath == "" {
			continue
		}
		expected[metrics.LedgerFilename(s.TranscriptPath)] = struct{}{}
	}
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
	if removed > 0 {
		logger.LogInfo("startup", "", fmt.Sprintf("pruned %d orphan ledger files", removed))
	}
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
// prunes rows older than 400 days (so per-year queries stay fast), and
// records a baseline row for every existing session so rates are
// computable without waiting for new activity.
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
