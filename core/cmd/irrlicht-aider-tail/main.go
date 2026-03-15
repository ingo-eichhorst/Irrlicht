// irrlicht-aider-tail tails an Aider analytics JSONL log and maintains Irrlicht
// session state files, following the same hexagonal architecture as irrlicht-hook.
//
// Usage:
//
//	irrlicht-aider-tail --session <id> --log <path> [--cwd <dir>]
//
// This binary is launched in the background by the irrlicht-aider wrapper script.
// It exits when:
//   - An "exit" event is seen in the analytics log (normal session end)
//   - SIGTERM or SIGINT is received (wrapper script cleanup)
//   - An unrecoverable read error occurs
package main

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	aiderAdapter "irrlicht/core/adapters/inbound/aider"
	"irrlicht/core/adapters/outbound/filesystem"
	"irrlicht/core/adapters/outbound/git"
	"irrlicht/core/adapters/outbound/logging"
	"irrlicht/core/adapters/outbound/metrics"
	"irrlicht/core/adapters/outbound/security"
	"irrlicht/core/application/services"
)

// Version is injected at build time via -ldflags "-X main.Version=x.y.z".
var Version = "dev"

func main() {
	// --version flag
	if len(os.Args) > 1 && (os.Args[1] == "--version" || os.Args[1] == "-v") {
		fmt.Printf("irrlicht-aider-tail version %s\n", Version)
		fmt.Printf("Built with %s %s/%s\n", runtime.Version(), runtime.GOOS, runtime.GOARCH)
		os.Exit(0)
	}

	// Parse flags: --session <id> --log <path> [--cwd <dir>]
	sessionID := ""
	logPath := ""
	cwd := ""

	args := os.Args[1:]
	for i := 0; i < len(args)-1; i++ {
		switch args[i] {
		case "--session":
			sessionID = args[i+1]
			i++
		case "--log":
			logPath = args[i+1]
			i++
		case "--cwd":
			cwd = args[i+1]
			i++
		}
	}

	if sessionID == "" || logPath == "" {
		fmt.Fprintln(os.Stderr, "Error: --session <id> and --log <path> are required")
		fmt.Fprintln(os.Stderr, "Usage: irrlicht-aider-tail --session <id> --log <path> [--cwd <dir>]")
		os.Exit(1)
	}

	// Initialise logger first so subsequent errors can be recorded.
	logger, err := logging.New()
	if err != nil {
		log.Printf("Failed to initialise logger: %v", err)
		os.Exit(1)
	}
	defer logger.Close()

	// Kill switch: environment variable.
	if os.Getenv("IRRLICHT_DISABLED") == "1" {
		logger.LogInfo("aider", sessionID, "Kill switch activated via IRRLICHT_DISABLED=1, exiting")
		os.Exit(0)
	}

	// Build outbound adapters.
	repo, err := filesystem.New()
	if err != nil {
		logger.LogError("aider:startup", sessionID, fmt.Sprintf("Failed to init session repository: %v", err))
		os.Exit(1)
	}
	pathValidator, err := security.New()
	if err != nil {
		logger.LogError("aider:startup", sessionID, fmt.Sprintf("Failed to init path validator: %v", err))
		os.Exit(1)
	}
	svc := services.NewEventService(repo, logger, git.New(), metrics.New(), pathValidator)

	// Reap orphaned sessions opportunistically.
	svc.CleanupOrphanedSessions()

	// Create aider adapter.
	adapter := aiderAdapter.New(svc, sessionID, cwd)

	// Set up signal handling for graceful exit.
	stopCh := make(chan struct{})
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-sigCh
		close(stopCh)
	}()

	logger.LogInfo("aider:tail", sessionID, fmt.Sprintf("Starting analytics log tailer: %s", logPath))
	startTime := time.Now()

	if err := adapter.TailFile(logPath, stopCh); err != nil {
		logger.LogError("aider:tail", sessionID, fmt.Sprintf("Tailer error: %v", err))
		os.Exit(1)
	}

	logger.LogInfo("aider:tail", sessionID,
		fmt.Sprintf("Analytics tailer finished after %v", time.Since(startTime).Round(time.Millisecond)))
}
