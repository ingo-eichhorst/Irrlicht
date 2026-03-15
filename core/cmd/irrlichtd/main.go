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

	inboundhttp "irrlicht/core/adapters/inbound/http"
	"irrlicht/core/adapters/outbound/filesystem"
	"irrlicht/core/adapters/outbound/git"
	"irrlicht/core/adapters/outbound/logging"
	"irrlicht/core/adapters/outbound/mdns"
	"irrlicht/core/adapters/outbound/memory"
	"irrlicht/core/adapters/outbound/metrics"
	"irrlicht/core/adapters/outbound/security"
	wshub "irrlicht/core/adapters/outbound/websocket"
	"irrlicht/core/application/services"
)

// Version is injected at build time via -ldflags "-X main.Version=x.y.z".
var Version = "dev"

const (
	tcpAddr = ":7837"
	tcpPort = 7837
)

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "--version" || os.Args[1] == "-v") {
		fmt.Printf("irrlichtd version %s\n", Version)
		fmt.Printf("Built with %s %s/%s\n", runtime.Version(), runtime.GOOS, runtime.GOARCH)
		os.Exit(0)
	}

	logger, err := logging.New()
	if err != nil {
		log.Fatalf("failed to initialise logger: %v", err)
	}
	defer logger.Close()

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

	// Path validator.
	pathValidator, err := security.New()
	if err != nil {
		logger.LogError("startup", "", fmt.Sprintf("failed to init path validator: %v", err))
		os.Exit(1)
	}

	// Event service wired to memory repo + broadcaster.
	svc := services.NewEventService(memRepo, logger, git.New(), metrics.New(), pathValidator)
	svc.SetBroadcaster(push)

	// HTTP mux.
	mux := http.NewServeMux()
	handler := inboundhttp.NewHandler(svc, memRepo)
	handler.RegisterRoutes(mux)

	hub := wshub.NewHub(push)
	mux.HandleFunc("GET /api/v1/sessions/stream", hub.ServeWS)

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

	logger.LogInfo("startup", "", fmt.Sprintf("irrlichtd %s listening on unix:%s and tcp:%s", Version, sockPath, tcpAddr))

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

// socketPath returns the Unix socket path for irrlichtd.
func socketPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "/tmp/irrlichtd.sock"
	}
	return filepath.Join(home, ".local", "share", "irrlicht", "irrlichtd.sock")
}
