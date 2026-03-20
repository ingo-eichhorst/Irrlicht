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

	"irrlicht/core/adapters/inbound/claudecode"
	"irrlicht/core/adapters/inbound/codex"
	gastownadapter "irrlicht/core/adapters/inbound/gastown"
	"irrlicht/core/adapters/outbound/filesystem"
	graceperiodadapter "irrlicht/core/adapters/outbound/graceperiod"
	"irrlicht/core/adapters/outbound/git"
	"irrlicht/core/adapters/outbound/gtbin"
	"irrlicht/core/adapters/outbound/logging"
	"irrlicht/core/adapters/outbound/mdns"
	"irrlicht/core/adapters/outbound/memory"
	"irrlicht/core/adapters/outbound/metrics"
	processadapter "irrlicht/core/adapters/outbound/process"
	wshub "irrlicht/core/adapters/outbound/websocket"
	"irrlicht/core/application/services"
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

	// Memory store wraps filesystem for fast in-process access.
	memRepo := memory.New(fsRepo)

	// Crash recovery: seed memory from existing session files.
	if err := memRepo.SeedFromDisk(); err != nil {
		logger.LogError("startup", "", fmt.Sprintf("failed to seed from disk: %v", err))
		// Non-fatal: continue with empty in-memory state.
	}

	// Push broadcaster for WebSocket fan-out.
	push := services.NewPushService()

	// Shared adapters for SessionDetector.
	gitResolver := git.New()
	metricsCollector := metrics.New()

	// --- File-based SessionDetector (primary detection path) ---
	// Forward-reference: detector is assigned before any callbacks can fire,
	// because ProcessWatcher and GracePeriodTimer only invoke callbacks after
	// SessionDetector.Run() subscribes to TranscriptWatcher events.
	var detector *services.SessionDetector

	// ProcessWatcher: kqueue EVFILT_PROC NOTE_EXIT monitoring.
	// Exit callback routes to SessionDetector for lifecycle management.
	var pwPort outbound.ProcessWatcher
	pw, err := processadapter.New(func(pid int, sessionID string) {
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

	// GracePeriodTimer: per-session 2s idle timer → waiting when no open tool calls.
	gpTimer := graceperiodadapter.New(2*time.Second, func(sessionID string) {
		detector.HandleGracePeriodExpiry(sessionID)
	})

	// HTTP mux.
	mux := http.NewServeMux()
	registerReadRoutes(mux, memRepo)
	// Gas Town endpoint registered after poller is available (see below).

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

	// Gas Town collector: detect GT_ROOT and watch daemon/state.json.
	gtCollector := gastownadapter.New()
	var gtPoller *gastownadapter.Poller
	if gtCollector.Detected() {
		logger.LogInfo("startup", "", fmt.Sprintf("Gas Town detected at %s", gtCollector.Root()))
		watchCtx, watchCancel := context.WithCancel(context.Background())
		defer watchCancel()
		go func() {
			if err := gtCollector.Watch(watchCtx); err != nil && err != context.Canceled {
				logger.LogError("gastown", "", fmt.Sprintf("watcher error: %v", err))
			}
		}()

		// Poller: periodically fetch rig + polecat + convoy state via gt CLI,
		// enrich with session data, and broadcast gastown_state via WebSocket.
		if gtBin := gtResolver.Path(); gtBin != "" {
			gtPoller = gastownadapter.NewPoller(gtCollector, gtBin, 5*time.Second, memRepo, push)
			pollerCtx, pollerCancel := context.WithCancel(context.Background())
			defer pollerCancel()
			go func() {
				if err := gtPoller.Run(pollerCtx); err != nil && err != context.Canceled {
					logger.LogError("gastown-poller", "", fmt.Sprintf("poller error: %v", err))
				}
			}()
			logger.LogInfo("startup", "", "Gas Town poller started (5s interval, enriched mode)")
		}
	} else {
		logger.LogInfo("startup", "", "Gas Town not detected — skipping daemon watcher")
	}

	// Register Gas Town API endpoint.
	mux.HandleFunc("GET /api/v1/gastown", handleGetGasTown(gtCollector, gtPoller))

	// Inbound adapters: watch agent transcript directories for session files.
	claudeCodeWatcher := claudecode.New()
	codexWatcher := codex.New()
	watchers := []inbound.AgentWatcher{claudeCodeWatcher, codexWatcher}

	// SessionDetector: orchestrates AgentWatchers + ProcessWatcher + GracePeriodTimer.
	detector = services.NewSessionDetector(
		watchers, pwPort, gpTimer,
		memRepo, logger, gitResolver, metricsCollector, push,
	)
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

// registerReadRoutes registers the read-only HTTP endpoints on mux.
func registerReadRoutes(mux *http.ServeMux, repo outbound.SessionRepository) {
	mux.HandleFunc("GET /api/v1/sessions", handleGetSessions(repo))
	mux.HandleFunc("GET /state", handleGetState(repo))
}

func handleGetSessions(repo outbound.SessionRepository) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sessions, err := repo.ListAll()
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if len(sessions) == 0 {
			w.Write([]byte("[]"))
			return
		}
		json.NewEncoder(w).Encode(sessions)
	}
}

func handleGetGasTown(collector *gastownadapter.Collector, poller *gastownadapter.Poller) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		if poller == nil {
			json.NewEncoder(w).Encode(struct {
				Detected bool `json:"detected"`
			}{Detected: collector.Detected()})
			return
		}

		// Return enriched gastown_state if available.
		if state := poller.State(); state != nil {
			enc := json.NewEncoder(w)
			enc.SetIndent("", "  ")
			enc.Encode(state)
			return
		}

		// Fallback to legacy snapshot.
		if snap := poller.Snapshot(); snap != nil {
			enc := json.NewEncoder(w)
			enc.SetIndent("", "  ")
			enc.Encode(snap)
			return
		}

		json.NewEncoder(w).Encode(struct {
			Detected bool `json:"detected"`
		}{Detected: collector.Detected()})
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
