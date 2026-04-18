package main

import (
	"context"
	"embed"
	"encoding/json"
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
	"sync"
	"syscall"
	"time"

	"irrlicht/core/adapters/inbound/agents/claudecode"
	"irrlicht/core/pkg/capacity"
	"irrlicht/core/adapters/inbound/agents/codex"
	"irrlicht/core/adapters/inbound/agents/pi"
	"irrlicht/core/adapters/inbound/agents/processlifecycle"
	gastownadapter "irrlicht/core/adapters/inbound/orchestrators/gastown"
	"irrlicht/core/adapters/outbound/filesystem"
	"irrlicht/core/adapters/outbound/git"
	"irrlicht/core/adapters/outbound/gtbin"
	"irrlicht/core/adapters/outbound/httputil"
	"irrlicht/core/adapters/outbound/logging"
	"irrlicht/core/adapters/outbound/mdns"
	"irrlicht/core/adapters/outbound/metrics"
	"irrlicht/core/adapters/outbound/recorder"
	wshub "irrlicht/core/adapters/outbound/websocket"
	"irrlicht/core/application/services"
	"irrlicht/core/domain/config"
	"irrlicht/core/domain/session"
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

	// Background model capacity refresh from LiteLLM.
	go func() {
		if n, err := capacity.RefreshRemoteDataIfStale(); err != nil {
			logger.LogError("capacity", "", fmt.Sprintf("remote refresh failed: %v", err))
		} else if n > 0 {
			logger.LogInfo("capacity", "", fmt.Sprintf("cached %d remote models from LiteLLM", n))
		}

		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			if n, err := capacity.RefreshRemoteDataIfStale(); err != nil {
				logger.LogError("capacity", "", fmt.Sprintf("remote refresh failed: %v", err))
			} else if n > 0 {
				logger.LogInfo("capacity", "", fmt.Sprintf("cached %d remote models from LiteLLM", n))
			}
		}
	}()

	// Resolve the gt binary path (GT_BIN env → common paths → which gt).
	gtResolver := gtbin.New()
	if p := gtResolver.Path(); p != "" {
		logger.LogInfo("startup", "", fmt.Sprintf("gt binary: %s", p))
	} else {
		logger.LogError("startup", "", "gt binary not found (set GT_BIN or add gt to PATH)")
	}

	// Filesystem repository (persistent backing store).
	fsRepo, err := filesystem.New()
	if err != nil {
		logger.LogError("startup", "", fmt.Sprintf("failed to init filesystem repo: %v", err))
		os.Exit(1)
	}

	// Startup: prune stale session files from disk.
	pruned, err := fsRepo.PruneStale(cfg.MaxSessionAge)
	if err != nil {
		logger.LogError("startup", "", fmt.Sprintf("failed to prune stale sessions: %v", err))
	} else if pruned > 0 {
		logger.LogInfo("startup", "", fmt.Sprintf("pruned %d stale session files", pruned))
	}

	// Prune proc-<pid> sessions whose process is no longer alive.
	// These can survive a daemon restart because the in-memory tracked map
	// is lost, leaving orphaned proc session files on disk.
	if allSessions, err := fsRepo.ListAll(); err == nil {
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

	// Wrap the filesystem repo with a caching layer to avoid redundant
	// directory scans from the many concurrent ListAll() callers.
	cachedRepo := filesystem.NewCachedSessionRepository(fsRepo, 3*time.Second)

	// Cost tracker: append-only per-project JSONL files for trailing-window
	// cost queries. Prune rows older than 400 days on startup so "per year"
	// windows stay fast and files don't grow unbounded.
	costTracker, err := filesystem.NewCostTracker()
	if err != nil {
		logger.LogError("startup", "", fmt.Sprintf("failed to init cost tracker: %v", err))
	} else {
		if err := costTracker.Prune(400); err != nil {
			logger.LogError("startup", "", fmt.Sprintf("cost tracker prune failed: %v", err))
		}
		if allSessions, err := fsRepo.ListAll(); err == nil {
			for _, s := range allSessions {
				if err := costTracker.RecordBaseline(s); err != nil {
					logger.LogError("startup", s.SessionID,
						fmt.Sprintf("cost tracker baseline failed: %v", err))
				}
			}
		}
	}

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

// costAttachCache caches the last ProjectCostsInWindows result so successive
// /api/v1/sessions hits within costAttachTTL reuse one scan. Shared across
// requests; the zero value is an empty cache.
type costAttachCache struct {
	mu          sync.RWMutex
	generatedAt time.Time
	byTimeframe map[string]map[string]float64
}

func (c *costAttachCache) get(now time.Time) (map[string]map[string]float64, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.byTimeframe == nil || now.Sub(c.generatedAt) > costAttachTTL {
		return nil, false
	}
	return c.byTimeframe, true
}

func (c *costAttachCache) put(now time.Time, v map[string]map[string]float64) {
	c.mu.Lock()
	c.generatedAt = now
	c.byTimeframe = v
	c.mu.Unlock()
}

func handleGetSessions(repo outbound.SessionRepository, orchMonitor *services.OrchestratorMonitor, tracker *filesystem.CostTracker) http.HandlerFunc {
	cache := &costAttachCache{}
	return func(w http.ResponseWriter, r *http.Request) {
		sessions, err := repo.ListAll()
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		resp := session.BuildDashboard(sessions, orchMonitor.State("gastown"))
		if tracker != nil {
			attachGroupCosts(resp, tracker, cache)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}
}

// attachGroupCosts populates each top-level group's Costs map with the
// trailing-window cost for day/week/month/year. Orchestrator groups are
// skipped — their agents span projects, so group-level aggregation is
// ambiguous. Uses a single per-file scan (ProjectCostsInWindows) + a small
// per-handler TTL cache to keep I/O bounded under concurrent polling.
func attachGroupCosts(groups []*session.AgentGroup, tracker *filesystem.CostTracker, cache *costAttachCache) {
	now := time.Now()
	byTf, ok := cache.get(now)
	if !ok {
		m, err := tracker.ProjectCostsInWindows(costTimeframeSeconds)
		if err != nil {
			return
		}
		cache.put(now, m)
		byTf = m
	}
	for _, g := range groups {
		if g == nil || g.Type == "gastown" {
			continue
		}
		costs := make(map[string]float64, len(costTimeframeSeconds))
		for tf := range costTimeframeSeconds {
			if v, ok := byTf[tf][g.Name]; ok {
				costs[tf] = v
			}
		}
		if len(costs) > 0 {
			g.Costs = costs
		}
	}
}

func handleGetState(repo outbound.SessionRepository) http.HandlerFunc {
	type sessionEntry struct {
		ID                 string  `json:"id"`
		ProjectName        string  `json:"projectName,omitempty"`
		State              string  `json:"state"`
		Model              string  `json:"model,omitempty"`
		ContextUtilization float64 `json:"contextUtilization"`
		TotalTokens        int64   `json:"totalTokens"`
	}

	type stateResponse struct {
		Sessions     []sessionEntry `json:"sessions"`
		SessionCount int            `json:"sessionCount"`
		WorkingCount int            `json:"workingCount"`
		WaitingCount int            `json:"waitingCount"`
		ReadyCount   int            `json:"readyCount"`
		LastUpdated  string         `json:"lastUpdated"`
	}

	return func(w http.ResponseWriter, r *http.Request) {
		sessions, err := repo.ListAll()
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		entries := make([]sessionEntry, 0, len(sessions))
		var workingCount, waitingCount, readyCount int
		for _, s := range sessions {
			var ctxUtil float64
			var totalTokens int64
			if s.Metrics != nil {
				ctxUtil = s.Metrics.ContextUtilization
				totalTokens = s.Metrics.TotalTokens
			}
			model := s.Model
			if s.Metrics != nil && s.Metrics.ModelName != "" && s.Metrics.ModelName != "unknown" {
				model = s.Metrics.ModelName
			}
			entries = append(entries, sessionEntry{
				ID:                 s.SessionID,
				ProjectName:        s.ProjectName,
				State:              s.State,
				Model:              model,
				ContextUtilization: ctxUtil,
				TotalTokens:        totalTokens,
			})
			switch s.State {
			case session.StateWorking:
				workingCount++
			case session.StateWaiting:
				waitingCount++
			case session.StateReady:
				readyCount++
			}
		}

		resp := stateResponse{
			Sessions:     entries,
			SessionCount: len(sessions),
			WorkingCount: workingCount,
			WaitingCount: waitingCount,
			ReadyCount:   readyCount,
			LastUpdated:  time.Now().UTC().Format(time.RFC3339),
		}

		w.Header().Set("Content-Type", "application/json")
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		enc.Encode(resp)
	}
}

// localhostOnly wraps an HTTP handler to reject requests not originating from
// localhost or Unix sockets. Used to protect sensitive endpoints like pprof.
func localhostOnly(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !httputil.IsLoopbackRequest(r) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		h(w, r)
	}
}
