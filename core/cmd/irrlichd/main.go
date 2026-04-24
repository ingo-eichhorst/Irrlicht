package main

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"irrlicht/core/adapters/inbound/agents/claudecode"
	"irrlicht/core/adapters/inbound/agents/codex"
	"irrlicht/core/adapters/inbound/agents/pi"
	"irrlicht/core/adapters/inbound/agents/processlifecycle"
	gastownadapter "irrlicht/core/adapters/inbound/orchestrators/gastown"
	sessionshandler "irrlicht/core/adapters/inbound/sessions"
	"irrlicht/core/adapters/outbound/filesystem"
	"irrlicht/core/adapters/outbound/git"
	"irrlicht/core/adapters/outbound/gtbin"
	"irrlicht/core/adapters/outbound/logging"
	"irrlicht/core/adapters/outbound/mdns"
	"irrlicht/core/adapters/outbound/metrics"
	"irrlicht/core/adapters/outbound/recorder"
	wshub "irrlicht/core/adapters/outbound/websocket"
	"irrlicht/core/application/services"
	"irrlicht/core/domain/config"
	"irrlicht/core/pkg/capacity"
	"irrlicht/core/ports/inbound"
	"irrlicht/core/ports/outbound"
)

//go:generate sh -c "mkdir -p ui && cp ../../../platforms/web/index.html ui/index.html"

//go:embed ui
var uiFS embed.FS

// Version is injected at build time via -ldflags "-X main.Version=x.y.z".
var Version = "dev"

const (
	defaultBindAddr = "127.0.0.1:7837"
	tcpPort         = 7837
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
		os.Exit(0)
	}

	recordEnabled := hasFlag("--record") || os.Getenv("IRRLICHT_RECORD") == "1"

	logger, err := logging.New()
	if err != nil {
		log.Fatalf("failed to initialise logger: %v", err)
	}
	defer logger.Close()

	// Auto-install Claude Code hooks for permission-pending detection.
	if modified, err := claudecode.EnsureHooksInstalled(); err != nil {
		logger.LogError("startup", "", fmt.Sprintf("failed to install hooks: %v", err))
	} else if modified {
		logger.LogInfo("startup", "", "installed Claude Code hooks for permission tracking")
	}

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
	historyTracker := startHistoryTracker(logger)

	// Push broadcaster for WebSocket fan-out.
	push := services.NewPushService()

	// Shared adapters for SessionDetector.
	gitResolver := git.New()
	metricsCollector := metrics.New()

	// --- File-based SessionDetector (primary detection path) ---
	// Forward-reference: detector is assigned before any callbacks can fire,
	// because ProcessWatcher only invokes callbacks after
	// SessionDetector.Run() subscribes to AgentWatcher events.
	var detector *services.SessionDetector

	// ProcessWatcher: kqueue EVFILT_PROC NOTE_EXIT monitoring.
	// Exit callback routes to SessionDetector for lifecycle management.
	var pwPort outbound.ProcessWatcher
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

	// HTTP mux.
	mux := http.NewServeMux()
	// Sessions endpoint registered after orchMonitor is available (see below).
	mux.HandleFunc("GET /state", handleGetState(cachedRepo))

	hub := wshub.NewHub(push)
	mux.HandleFunc("GET /api/v1/sessions/stream", hub.ServeWS)

	// pprof debug endpoints for runtime profiling (localhost only).
	mux.HandleFunc("GET /debug/pprof/", localhostOnly(pprof.Index))
	mux.HandleFunc("GET /debug/pprof/cmdline", localhostOnly(pprof.Cmdline))
	mux.HandleFunc("GET /debug/pprof/profile", localhostOnly(pprof.Profile))
	mux.HandleFunc("GET /debug/pprof/symbol", localhostOnly(pprof.Symbol))
	mux.HandleFunc("GET /debug/pprof/trace", localhostOnly(pprof.Trace))

	// Static web UI: serve the embedded ui/ directory at root.
	// API routes registered above take precedence over the catch-all "/".
	uiSub, err := fs.Sub(uiFS, "ui")
	if err != nil {
		logger.LogError("startup", "", fmt.Sprintf("failed to sub ui fs: %v", err))
		os.Exit(1)
	}
	mux.Handle("/", http.FileServer(http.FS(uiSub)))

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
	bindAddr := resolveBindAddr(os.Getenv("IRRLICHT_BIND_ADDR"))
	tcpL, err := net.Listen("tcp", bindAddr)
	if err != nil {
		logger.LogError("startup", "", fmt.Sprintf("failed to listen on TCP %s: %v", bindAddr, err))
		os.Exit(1)
	}

	go func() { _ = srv.Serve(unixL) }()
	go func() { _ = srv.Serve(tcpL) }()

	// mDNS/Bonjour advertisement — opt-in via IRRLICHT_MDNS=1 to avoid
	// broadcasting the daemon on networks the user did not intend to share on.
	var mdnsAdv *mdns.Advertiser
	if os.Getenv("IRRLICHT_MDNS") == "1" {
		mdnsAdv, err = mdns.New(tcpPort)
		if err != nil {
			logger.LogError("startup", "", fmt.Sprintf("mDNS advertisement failed (non-fatal): %v", err))
		} else {
			logger.LogInfo("startup", "", "mDNS: advertising _irrlicht._tcp on the local network")
		}
	} else {
		logger.LogInfo("startup", "", "mDNS: disabled (set IRRLICHT_MDNS=1 to advertise)")
	}

	// Orchestrator adapters: detect and watch multi-agent orchestration systems.
	gtAdapter := gastownadapter.NewAdapter(gtResolver.Path(), 5*time.Second, cachedRepo)
	var orchWatchers []inbound.OrchestratorWatcher
	if gtAdapter.Detected() {
		logger.LogInfo("startup", "", fmt.Sprintf("Gas Town detected at %s", gtAdapter.Root()))
		orchWatchers = append(orchWatchers, gtAdapter)
	} else {
		logger.LogInfo("startup", "", "Gas Town not detected — skipping orchestrator watcher")
	}

	orchMonitor := services.NewOrchestratorMonitor(orchWatchers, push, logger)
	{
		orchCtx, orchCancel := context.WithCancel(context.Background())
		defer orchCancel()
		// Start each orchestrator watcher.
		for _, ow := range orchWatchers {
			go func() {
				if err := ow.Watch(orchCtx); err != nil && err != context.Canceled {
					logger.LogError("orchestrator-watcher", "", fmt.Sprintf("watcher error: %v", err))
				}
			}()
		}
		go func() {
			if err := orchMonitor.Run(orchCtx); err != nil && err != context.Canceled {
				logger.LogError("orchestrator-monitor", "", fmt.Sprintf("monitor error: %v", err))
			}
		}()
	}

	// Register API endpoints (after orchMonitor is available).
	mux.HandleFunc("GET /api/v1/sessions", handleGetSessions(cachedRepo, orchMonitor, costTracker))
	mux.HandleFunc("GET /api/v1/sessions/history", handleGetSessionsHistory(cachedRepo, historyTracker))

	focusService := services.NewFocusService(cachedRepo, push, logger)
	mux.HandleFunc("POST /api/v1/sessions/{id}/focus", sessionshandler.NewFocusHandler(focusService, logger))

	// Inbound adapters: watch agent transcript directories for session files.
	claudeCodeWatcher := claudecode.New(cfg.MaxSessionAge)
	codexWatcher := codex.New(cfg.MaxSessionAge)
	piWatcher := pi.New(cfg.MaxSessionAge)

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

	// Process scanners: detect agent processes before they create a
	// transcript, so the session appears as ready from the moment the app opens.
	procScanner := processlifecycle.NewScanner(
		claudecode.ProcessName,
		claudecode.AdapterName,
		0, // use default interval
	)
	procScanner.WithSessionChecker(realSessionCheck)

	codexProcScanner := processlifecycle.NewScanner(
		codex.ProcessName,
		codex.AdapterName,
		0,
	)
	codexProcScanner.WithSessionChecker(realSessionCheck)

	piProcScanner := processlifecycle.NewScanner(
		pi.ProcessName,
		pi.AdapterName,
		0,
	)
	piProcScanner.WithSessionChecker(realSessionCheck)

	watchers := []inbound.AgentWatcher{
		claudeCodeWatcher, codexWatcher, piWatcher,
		procScanner, codexProcScanner, piProcScanner,
	}

	// Per-adapter PID discovery: Claude Code uses CWD-based matching,
	// Codex/Pi use transcript file writer detection.
	pidDiscovers := map[string]services.PIDDiscoverFunc{
		claudecode.AdapterName: claudecode.DiscoverPID,
		codex.AdapterName:      codex.DiscoverPID,
		pi.AdapterName:         pi.DiscoverPID,
	}

	// SessionDetector: orchestrates AgentWatchers + ProcessWatcher.
	detector = services.NewSessionDetector(
		watchers, pwPort,
		cachedRepo, logger, gitResolver, metricsCollector, push,
		Version, cfg.ReadySessionTTL,
		pidDiscovers,
	)
	if costTracker != nil {
		detector.SetCostTracker(costTracker)
	}
	detector.SetHistoryTracker(historyTracker)
	// Capture terminal/IDE identity at first PID assignment so the menu-bar
	// app can jump back to the launching terminal on row/notification click.
	detector.SetLauncherEnvReader(processlifecycle.ReadLauncherEnv)

	// Hook receiver: Claude Code PermissionRequest/PostToolUse events.
	// The detector satisfies claudecode.HookTarget via HandlePermissionHook.
	mux.HandleFunc("POST /api/v1/hooks/claudecode",
		claudecode.NewHookHandler(detector, logger))

	// Lifecycle recording: opt-in via --record flag or IRRLICHT_RECORD=1.
	if recordEnabled {
		recordingsDir := filepath.Join(filepath.Dir(sockPath), "recordings")
		rec, err := recorder.NewJSONLRecorder(recordingsDir)
		if err != nil {
			logger.LogError("startup", "", fmt.Sprintf("failed to init lifecycle recorder: %v", err))
		} else {
			detector.SetRecorder(rec)
			defer rec.Close()
			logger.LogInfo("startup", "", fmt.Sprintf("lifecycle recording enabled: %s", rec.Path()))
		}
	}
	{
		detectorCtx, detectorCancel := context.WithCancel(context.Background())
		defer detectorCancel()
		logger.LogInfo("startup", "", fmt.Sprintf("watching Claude Code (%s), Codex (%s), Pi (%s)",
			claudeCodeWatcher.Root(), codexWatcher.Root(), piWatcher.Root()))
		for _, w := range watchers {
			go func() {
				if err := w.Watch(detectorCtx); err != nil && err != context.Canceled {
					logger.LogError("agent-watcher", "", fmt.Sprintf("watcher error: %v", err))
				}
			}()
		}
		go func() {
			if err := detector.Run(detectorCtx); err != nil && err != context.Canceled {
				logger.LogError("session-detector", "", fmt.Sprintf("detector error: %v", err))
			}
		}()
	}

	logger.LogInfo("startup", "", fmt.Sprintf("irrlichd %s listening on unix:%s and tcp:%s", Version, sockPath, bindAddr))

	// Wait for SIGTERM or SIGINT.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	<-sig

	logger.LogInfo("shutdown", "", "signal received, shutting down")

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

// socketPath returns the Unix socket path for irrlichd.
func socketPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "/tmp/irrlichd.sock"
	}
	return filepath.Join(home, ".local", "share", "irrlicht", "irrlichd.sock")
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
	fsRepo, err := filesystem.New()
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

	cachedRepo := filesystem.NewCachedSessionRepository(fsRepo, 3*time.Second)
	return fsRepo, cachedRepo
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
	costTracker, err := filesystem.NewCostTracker()
	if err != nil {
		logger.LogError("startup", "", fmt.Sprintf("failed to init cost tracker: %v", err))
		return nil
	}
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

// startHistoryTracker brings up the per-session rolling ring buffers (1s/
// 10s/60s) used by the history endpoint. State persists to
// ~/.local/share/irrlicht/history.json so data survives a daemon restart.
// The background goroutine is owned by main's lifetime — cancellation
// flushes at shutdown.
func startHistoryTracker(logger outbound.Logger) *services.HistoryTracker {
	home, _ := os.UserHomeDir()
	histDir := filepath.Join(home, ".local", "share", "irrlicht")
	ht := services.NewHistoryTrackerWithDir(histDir)
	ht.Load()
	histCtx, histCancel := context.WithCancel(context.Background())
	_ = histCancel // cancelled at process exit
	go ht.Run(histCtx)
	logger.LogInfo("startup", "", "history tracker started")
	return ht
}
