package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"irrlicht/core/adapters/inbound/agents"
	"irrlicht/core/adapters/inbound/agents/agentwiring"
	"irrlicht/core/adapters/inbound/agents/claudecode"
	"irrlicht/core/adapters/outbound/filesystem"
	"irrlicht/core/adapters/outbound/git"
	"irrlicht/core/adapters/outbound/gtbin"
	"irrlicht/core/adapters/outbound/logging"
	"irrlicht/core/adapters/outbound/relay"
	"irrlicht/core/application/services"
	"irrlicht/core/domain/config"
	"irrlicht/core/domain/permission"
	"irrlicht/core/pkg/capacity"
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
		uninstallHooks()
		os.Exit(0)
	}
	if hasFlag("--uninstall-task-eta") {
		uninstallTaskEtaBlocks()
		os.Exit(0)
	}
	if hasFlag("--diagnose") {
		runDiagnose()
		os.Exit(0)
	}
	runDaemon()
}

// uninstallHooks removes irrlicht's Claude Code hooks from
// ~/.claude/settings.json and, if the hooks permission was previously
// granted, records the explicit opt-out in the consent store (#570) — a
// persisted "granted" would otherwise re-install the hooks on the next
// daemon start, silently reverting this decision.
func uninstallHooks() {
	modified, err := claudecode.UninstallHooks()
	if err != nil {
		log.Fatalf("failed to uninstall hooks: %v", err)
	}
	if modified {
		fmt.Println("Removed irrlicht hooks from ~/.claude/settings.json")
	} else {
		fmt.Println("No irrlicht hooks found in ~/.claude/settings.json")
	}

	home, _ := os.UserHomeDir()
	store := filesystem.NewPermissionStore(dataDir(home))
	set, err := store.Load()
	if err != nil {
		return
	}
	if set.Get(claudecode.AdapterName, claudecode.PermissionKeyHooks) != permission.StateGranted {
		return
	}
	set.Put(claudecode.AdapterName, claudecode.PermissionKeyHooks, permission.StateDenied)
	if err := store.Save(set); err != nil {
		fmt.Printf("warning: failed to record hooks permission as denied: %v\n", err)
		return
	}
	fmt.Println("Recorded the hooks permission as denied (re-grant via the permission wizard)")
}

// uninstallTaskEtaBlocks removes irrlicht's managed task-eta and
// task-summary blocks from ~/.claude/CLAUDE.md.
func uninstallTaskEtaBlocks() {
	etaModified, err := claudecode.UninstallTaskEtaBlock()
	if err != nil {
		log.Fatalf("failed to uninstall task-eta block: %v", err)
	}
	summaryModified, err := claudecode.UninstallTaskSummaryBlock()
	if err != nil {
		log.Fatalf("failed to uninstall task-summary block: %v", err)
	}
	if etaModified || summaryModified {
		fmt.Println("Removed irrlicht task-eta/task-summary blocks from ~/.claude/CLAUDE.md")
	} else {
		fmt.Println("No irrlicht managed blocks found in ~/.claude/CLAUDE.md")
	}
}

// loadConfig builds the daemon's runtime config, applying the
// IRRLICHT_MAX_SESSION_AGE override when set and valid.
func loadConfig(logger outbound.Logger) config.Config {
	cfg := config.Default()
	if v := os.Getenv("IRRLICHT_MAX_SESSION_AGE"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.MaxSessionAge = d
		} else {
			logger.LogError("startup", "", fmt.Sprintf("invalid IRRLICHT_MAX_SESSION_AGE %q, using default %s", v, cfg.MaxSessionAge))
		}
	}
	logger.LogInfo("startup", "", fmt.Sprintf("max session age: %s", cfg.MaxSessionAge))
	return cfg
}

// runDaemon brings up the full daemon: config, core services, HTTP routes,
// the consent-gated detection/permission/backchannel stack, then serves
// until SIGTERM/SIGINT and shuts down gracefully. Each phase is wired by a
// dedicated setup function (see startup.go); this is the sequencing.
func runDaemon() {
	recordEnabled := hasFlag("--record") || os.Getenv("IRRLICHT_RECORD") == "1"

	logger, err := logging.New()
	if err != nil {
		log.Fatalf("failed to initialise logger: %v", err)
	}
	defer logger.Close()

	// Hook + statusline installation is consent-gated (issue #570): the
	// PermissionService applies them when (and only when) the user grants
	// the corresponding permission — see startBackgroundLoops below.
	// Nothing under the user's home is modified before that.
	cfg := loadConfig(logger)

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

	// Outbound relay publishing (#722) + remote control (#724) — see
	// setupRelay's doc comment for the full rationale.
	rel := setupRelay(logger, push, cachedRepo, allAgents)
	defer rel.cancel()

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

	pwPort, pwCleanup := setupProcessWatcher(demoMode, &detector, logger)
	if pwCleanup != nil {
		defer pwCleanup()
	}

	mux := http.NewServeMux()
	registerCoreRoutes(mux, registerCoreRoutesDeps{
		FSRepo:            fsRepo,
		CachedRepo:        cachedRepo,
		HistoryTracker:    historyTracker,
		Push:              push,
		AllAgents:         allAgents,
		Version:           Version,
		PublishController: rel.publishController,
		Cfg:               cfg,
	})

	// Static web UI: served from disk so the dashboard ships as three files
	// (index.html, irrlicht.css, irrlicht.js) under platforms/web/. API routes
	// registered above take precedence over the catch-all "/".
	registerUIRoutes(mux, logger)

	srv := newHTTPServer(mux)

	sockPath, unixL := setupUnixSocket(logger)

	tcpL, resolvedAddr := setupTCPListener(logger)
	// addrPath is where the "daemon is up" signal is published — see
	// publishAddrFile, called once every route below is registered.
	addrPath := filepath.Join(filepath.Dir(sockPath), "irrlichd.addr")

	mdnsAdv := setupMDNS(resolvedAddr, logger)

	// Orchestrator adapters: detect and watch multi-agent orchestration
	// systems. Gas Town is consent-gated (#570) via the start/stop effects
	// below; the monitor itself runs regardless (idle until a watcher
	// registers) so /api/v1/sessions can always consult it.
	orchMonitor, orchCtx, orchCancel := setupOrchestratorMonitor(push, logger)
	defer orchCancel()
	startGastown, stopGastown := gastownEffects(orchCtx, orchMonitor, gtResolver.Path(), cachedRepo, logger)

	// Register API endpoints that need orchMonitor. inputService is
	// resolved at request time via rel.inputService (published once
	// setupBackchannel runs), so a session reports controllable only once
	// that wiring completes.
	registerSessionRoutes(mux, registerSessionRoutesDeps{
		CachedRepo:   cachedRepo,
		OrchMonitor:  orchMonitor,
		CostTracker:  costTracker,
		InputService: &rel.inputService,
		SockPath:     sockPath,
		Push:         push,
		Logger:       logger,
		GitResolver:  gitResolver,
	})

	var watcherFactories map[string]services.WatcherFactory
	detector, watcherFactories = buildDetector(buildDetectorDeps{
		DemoMode:         demoMode,
		PWPort:           pwPort,
		CachedRepo:       cachedRepo,
		Logger:           logger,
		GitResolver:      gitResolver,
		MetricsCollector: metricsCollector,
		Push:             push,
		Version:          Version,
		Cfg:              cfg,
		AllAgents:        allAgents,
		CostTracker:      costTracker,
		HistoryTracker:   historyTracker,
	})

	home, _ := os.UserHomeDir()
	permService := setupPermissionService(mux, setupPermissionServiceDeps{
		Detector:         detector,
		Push:             push,
		Logger:           logger,
		Cfg:              cfg,
		AllAgents:        allAgents,
		WatcherFactories: watcherFactories,
		DemoMode:         demoMode,
		Home:             home,
		StartGastown:     startGastown,
		StopGastown:      stopGastown,
	})

	backchannelEngine, terminalObserver := setupBackchannel(mux, setupBackchannelDeps{
		CachedRepo:        cachedRepo,
		Push:              push,
		PermService:       permService,
		Detector:          detector,
		Logger:            logger,
		Home:              home,
		InputService:      &rel.inputService,
		RelayControlStore: rel.controlStore,
		AllAgents:         allAgents,
	})

	registerHookRoutes(mux, detector, metricsCollector, permService, logger)

	publishAddrFile(addrPath, resolvedAddr, logger)

	go func() { _ = srv.Serve(unixL) }()
	go func() { _ = srv.Serve(tcpL) }()

	// Lifecycle recording: opt-in via --record flag or IRRLICHT_RECORD=1.
	// Recordings default to <dataDir>/recordings, so IRRLICHT_HOME already
	// isolates them. IRRLICHT_RECORDINGS_DIR is the narrower override that
	// wins even when IRRLICHT_HOME is set, so test harnesses (e.g. the
	// onboarding factory's record path) can pin recordings somewhere specific.
	if recordEnabled {
		defer setupRecording(detector, sockPath, logger)()
	}

	sweepZombies(demoMode, detector, logger)

	defer startBackgroundLoops(startBackgroundLoopsDeps{
		Detector:          detector,
		BackchannelEngine: backchannelEngine,
		CachedRepo:        cachedRepo,
		GitResolver:       gitResolver,
		TerminalObserver:  terminalObserver,
		PermService:       permService,
		Cfg:               cfg,
		DemoMode:          demoMode,
		Logger:            logger,
	})()

	logger.LogInfo("startup", "", fmt.Sprintf("irrlichd %s listening on unix:%s and tcp:%s", Version, sockPath, resolvedAddr))

	// Wait for SIGTERM or SIGINT.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	<-sig

	logger.LogInfo("shutdown", "", "signal received, shutting down")

	// Remove the addr file so it can't outlive the daemon and mislead tooling
	// into connecting to a dead port.
	os.Remove(addrPath)

	shutdown(srv, mdnsAdv, logger)
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
