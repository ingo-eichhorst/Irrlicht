// irrlicht-hook-cursor receives Cursor IDE hook events and maintains Irrlicht
// session state files, following the same hexagonal architecture as irrlicht-hook.
//
// Usage:
//
//	cursor-hook
//
// The full hook payload (including hook_event_name) is read from stdin as JSON.
// Unlike irrlicht-hook-copilot, no --event flag is needed because Cursor embeds
// the event name in every payload as "hook_event_name".
//
// Example ~/.cursor/hooks.json entry:
//
//	{
//	  "hooks": {
//	    "sessionStart": [{"command": "/usr/local/bin/cursor-hook", "timeout": 30}],
//	    "sessionEnd":   [{"command": "/usr/local/bin/cursor-hook", "timeout": 30}]
//	  }
//	}
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"time"

	cursorAdapter "irrlicht/core/adapters/inbound/cursor"
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
	startTime := time.Now()

	// --version flag
	if len(os.Args) > 1 && (os.Args[1] == "--version" || os.Args[1] == "-v") {
		fmt.Printf("irrlicht-hook-cursor version %s\n", Version)
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

	// Kill switch: environment variable.
	if os.Getenv("IRRLICHT_DISABLED") == "1" {
		logger.LogInfo("", "", "Kill switch activated via IRRLICHT_DISABLED=1, exiting")
		os.Exit(0)
	}

	// Kill switch: Cursor hooks config file.
	if isCursorDisabled() {
		logger.LogInfo("", "", "Kill switch activated via Cursor hooks config, exiting")
		os.Exit(0)
	}

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

	// Reap orphaned sessions opportunistically on every invocation.
	svc.CleanupOrphanedSessions()

	// Read event from stdin and process.
	adapter := cursorAdapter.New(svc)
	processStart := time.Now()
	payloadSize, err := adapter.ReadAndHandle()
	processingTime := time.Since(processStart).Milliseconds()
	totalTime := time.Since(startTime).Milliseconds()

	if err != nil {
		logger.LogError("cursor", "", err.Error())
		logger.LogProcessingTime("cursor", "", processingTime, payloadSize, "error")
		os.Exit(1)
	}

	logger.LogProcessingTime("cursor", "", processingTime, payloadSize, "success")
	logger.LogInfo("cursor", "", fmt.Sprintf("Successfully processed event in %dms", totalTime))
}

// isCursorDisabled reads ~/.cursor/hooks.json and checks for a disabled flag.
func isCursorDisabled() bool {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	configPath := filepath.Join(homeDir, ".cursor", "hooks.json")
	data, err := os.ReadFile(configPath)
	if err != nil {
		return false
	}
	var config map[string]any
	if err := json.Unmarshal(data, &config); err != nil {
		return false
	}
	hooks, ok := config["hooks"].(map[string]any)
	if !ok {
		return false
	}
	irrlicht, ok := hooks["irrlicht"].(map[string]any)
	if !ok {
		return false
	}
	disabled, ok := irrlicht["disabled"].(bool)
	return ok && disabled
}
