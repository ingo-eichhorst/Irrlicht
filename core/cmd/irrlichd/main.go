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
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"strings"

	"irrlicht/core/adapters/inbound/agents/claudecode"
	"irrlicht/core/adapters/inbound/agents/codex"
	"irrlicht/core/adapters/inbound/agents/processlifecycle"
	gastownadapter "irrlicht/core/adapters/inbound/orchestrators/gastown"
	"irrlicht/core/adapters/outbound/filesystem"
	"irrlicht/core/adapters/outbound/git"
	"irrlicht/core/adapters/outbound/gtbin"
	"irrlicht/core/adapters/outbound/logging"
	"irrlicht/core/adapters/outbound/mdns"
	"irrlicht/core/adapters/outbound/metrics"
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
	tcpAddr = ":7837"
	tcpPort = 7837
)

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "--version" || os.Args[1] == "-v") {
		fmt.Printf("irrlichd version %s\n", Version)
		fmt.Printf("Built with %s %s/%s\n", runtime.Version(), runtime.GOOS, runtime.GOARCH)
		os.Exit(0)
	}

	logger, err := logging.New()
	if err != nil {
		log.Fatalf("failed to initialise logger: %v", err)
	}
	defer logger.Close()

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
	mux.HandleFunc("GET /state", handleGetState(fsRepo))

	hub := wshub.NewHub(push)
	mux.HandleFunc("GET /api/v1/sessions/stream", hub.ServeWS)

	// Static web UI: serve the embedded ui/ directory at root.
	// API routes registered above take precedence over the catch-all "/".
	uiSub, err := fs.Sub(uiFS, "ui")
	if err != nil {
		logger.LogError("startup", "", fmt.Sprintf("failed to sub ui fs: %v", err))
		os.Exit(1)
	}
	mux.Handle("/", http.FileServer(http.FS(uiSub)))

	srv := &http.Server{Handler: mux}

	// Unix socket.
	sockPath := socketPath()
	if err := os.MkdirAll(filepath.Dir(sockPath), 0755); err != nil {
		logger.LogError("startup", "", fmt.Sprintf("failed to create socket dir: %v", err))
		os.Exit(1)
	}
	os.Remove(sockPath) // remove stale socket
	unixL, err := net.Listen("unix", sockPath)
	if err != nil {
		logger.LogError("startup", "", fmt.Sprintf("failed to listen on unix socket: %v", err))
		os.Exit(1)
	}

	// TCP listener.
	tcpL, err := net.Listen("tcp", tcpAddr)
	if err != nil {
		logger.LogError("startup", "", fmt.Sprintf("failed to listen on TCP %s: %v", tcpAddr, err))
		os.Exit(1)
	}

	go func() { _ = srv.Serve(unixL) }()
	go func() { _ = srv.Serve(tcpL) }()

	// mDNS/Bonjour advertisement — non-fatal if unavailable.
	mdnsAdv, err := mdns.New(tcpPort)
	if err != nil {
		logger.LogError("startup", "", fmt.Sprintf("mDNS advertisement failed (non-fatal): %v", err))
	} else {
		logger.LogInfo("startup", "", "mDNS: advertising _irrlicht._tcp on the local network")
	}

	// Orchestrator adapters: detect and watch multi-agent orchestration systems.
	gtAdapter := gastownadapter.NewAdapter(gtResolver.Path(), 5*time.Second, fsRepo)
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
	mux.HandleFunc("GET /api/v1/sessions", handleGetSessions(fsRepo, orchMonitor))
	mux.HandleFunc("GET /api/v1/orchestrators/{name}", handleGetOrchestrator(orchMonitor))
	mux.HandleFunc("GET /api/v1/gastown", handleGetOrchestrator(orchMonitor)) // backward compat

	// Inbound adapters: watch agent transcript directories for session files.
	claudeCodeWatcher := claudecode.New(cfg.MaxSessionAge)
	codexWatcher := codex.New(cfg.MaxSessionAge)

	// Process scanner: detects Claude Code processes before they create a
	// transcript, so the session appears as ready from the moment the app opens.
	procScanner := processlifecycle.NewScanner(
		"claude",
		claudecode.AdapterName,
		claudeCodeWatcher.Root(),
		0, // use default interval
	)
	// Suppress ghost proc sessions for directories that already have a real
	// (transcript-backed) session, even if the transcript hasn't been written
	// recently. Without this, idle sessions allow short-lived helper processes
	// to create spurious proc-<pid> entries in the UI.
	procScanner.WithSessionChecker(func(projectDir string) bool {
		sessions, err := fsRepo.ListAll()
		if err != nil {
			return false
		}
		for _, s := range sessions {
			if strings.HasPrefix(s.SessionID, "proc-") {
				continue
			}
			if s.TranscriptPath != "" &&
				filepath.Base(filepath.Dir(s.TranscriptPath)) == projectDir {
				return true
			}
		}
		return false
	})

	watchers := []inbound.AgentWatcher{claudeCodeWatcher, codexWatcher, procScanner}

	// SessionDetector: orchestrates AgentWatchers + ProcessWatcher.
	detector = services.NewSessionDetector(
		watchers, pwPort,
		fsRepo, logger, gitResolver, metricsCollector, push,
		Version, cfg.ReadySessionTTL,
	)
	detector.WithCWDDiscovery(processlifecycle.DiscoverPIDByCWD)
	{
		detectorCtx, detectorCancel := context.WithCancel(context.Background())
		defer detectorCancel()
		logger.LogInfo("startup", "", fmt.Sprintf("watching Claude Code (%s), Codex (%s)",
			claudeCodeWatcher.Root(), codexWatcher.Root()))
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

	logger.LogInfo("startup", "", fmt.Sprintf("irrlichd %s listening on unix:%s and tcp:%s", Version, sockPath, tcpAddr))

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

func handleGetSessions(repo outbound.SessionRepository, orchMonitor *services.OrchestratorMonitor) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sessions, err := repo.ListAll()
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		resp := session.BuildDashboard(sessions, orchMonitor.State("gastown"))
		json.NewEncoder(w).Encode(resp)
	}
}

func handleGetOrchestrator(monitor *services.OrchestratorMonitor) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		// Determine which orchestrator: from path param or default to "gastown".
		name := r.PathValue("name")
		if name == "" {
			name = "gastown"
		}

		state := monitor.State(name)
		if state == nil {
			json.NewEncoder(w).Encode(struct {
				Detected bool `json:"detected"`
			}{Detected: false})
			return
		}

		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		enc.Encode(state)
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
