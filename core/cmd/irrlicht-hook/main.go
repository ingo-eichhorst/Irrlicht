package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"runtime"
	"sync"
	"time"

	"irrlicht/core/adapters/inbound/stdin"
	"irrlicht/core/adapters/outbound/filesystem"
	"irrlicht/core/adapters/outbound/git"
	"irrlicht/core/adapters/outbound/logging"
	"irrlicht/core/adapters/outbound/metrics"
	"irrlicht/core/adapters/outbound/security"
	"irrlicht/core/application/services"
)

// Version is injected at build time via -ldflags "-X main.Version=x.y.z".
var Version = "dev"

// appMetrics tracks in-process performance counters.
var appMetrics = struct {
	mu              sync.Mutex
	eventsProcessed int64
	totalLatencyMs  int64
	lastEventTime   time.Time
}{}

func main() {
	startTime := time.Now()

	// --version flag
	if len(os.Args) > 1 && (os.Args[1] == "--version" || os.Args[1] == "-v") {
		fmt.Printf("irrlicht-hook version %s\n", Version)
		fmt.Printf("Built with %s %s/%s\n", runtime.Version(), runtime.GOOS, runtime.GOARCH)
		os.Exit(0)
	}

	// Initialise logger first so subsequent errors can be recorded.
	logger, err := logging.New()
	if err != nil {
		log.Printf("Failed to initialise logger: %v", err)
		os.Exit(1)
	}
	defer logger.Close()

	// Build outbound adapters.
	repo, err := filesystem.New()
	if err != nil {
		logger.LogError("startup", "", fmt.Sprintf("Failed to init session repository: %v", err))
		os.Exit(1)
	}
	pathValidator, err := security.New()
	if err != nil {
		logger.LogError("startup", "", fmt.Sprintf("Failed to init path validator: %v", err))
		os.Exit(1)
	}
	svc := services.NewEventService(repo, logger, git.New(), metrics.New(), pathValidator)

	// --speculative-wait <sessionID> mode: runs as a detached background process.
	if len(os.Args) > 2 && os.Args[1] == "--speculative-wait" {
		if isDisabledInSettings() {
			os.Exit(0)
		}
		svc.RunSpeculativeWait(os.Args[2])
		return
	}

	// Kill switch: environment variable.
	if os.Getenv("IRRLICHT_DISABLED") == "1" {
		logger.LogInfo("", "", "Kill switch activated via IRRLICHT_DISABLED=1, exiting")
		os.Exit(0)
	}

	// Kill switch: settings file.
	if isDisabledInSettings() {
		logger.LogInfo("", "", "Kill switch activated via settings, exiting")
		os.Exit(0)
	}

	// Reap orphaned sessions opportunistically on every invocation.
	svc.CleanupOrphanedSessions()

	// Read event from stdin and process.
	stdinAdapter := stdin.New(svc)
	processStart := time.Now()
	payloadSize, err := stdinAdapter.ReadAndHandle()
	processingTime := time.Since(processStart).Milliseconds()
	totalTime := time.Since(startTime).Milliseconds()

	if err != nil {
		logger.LogError("", "", err.Error())
		logger.LogProcessingTime("", "", processingTime, payloadSize, "error")
		os.Exit(1)
	}

	appMetrics.mu.Lock()
	appMetrics.eventsProcessed++
	appMetrics.totalLatencyMs += totalTime
	appMetrics.lastEventTime = time.Now()
	appMetrics.mu.Unlock()

	logger.LogProcessingTime("", "", processingTime, payloadSize, "success")
	logger.LogInfo("", "", fmt.Sprintf("Successfully processed event in %dms", totalTime))
}

// isDisabledInSettings checks hooks.irrlicht.disabled in ~/.claude/settings.json.
func isDisabledInSettings() bool {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	settingsPath := homeDir + "/.claude/settings.json"
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		return false
	}
	// Minimal JSON walk to avoid importing encoding/json at startup hot-path.
	// We use encoding/json anyway since it's already a dep, just inline it.
	var settings map[string]interface{}
	if err := json.Unmarshal(data, &settings); err != nil {
		return false
	}
	hooks, ok := settings["hooks"].(map[string]interface{})
	if !ok {
		return false
	}
	irrlicht, ok := hooks["irrlicht"].(map[string]interface{})
	if !ok {
		return false
	}
	disabled, ok := irrlicht["disabled"].(bool)
	return ok && disabled
}
