// irrlicht-hook-copilot receives GitHub Copilot CLI hook events and maintains Irrlicht
// session state files, following the same hexagonal architecture as irrlicht-hook.
//
// Usage:
//
//	irrlicht-hook-copilot --event <copilot-event-name>
//
// The Copilot hook config passes the event name via --event. For example:
//
//	~/.copilot/hooks/irrlicht.json entry:
//	  "bash": "irrlicht-hook-copilot --event sessionStart"
//
// The full hook payload is read from stdin as a JSON object.
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"time"

	copilotAdapter "irrlicht/core/adapters/inbound/copilot"
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
		fmt.Printf("irrlicht-hook-copilot version %s\n", Version)
		fmt.Printf("Built with %s %s/%s\n", runtime.Version(), runtime.GOOS, runtime.GOARCH)
		os.Exit(0)
	}

	// --event <name> is required
	eventName := ""
	for i, arg := range os.Args[1:] {
		if arg == "--event" && i+1 < len(os.Args[1:]) {
			eventName = os.Args[i+2]
			break
		}
	}
	if eventName == "" {
		fmt.Fprintln(os.Stderr, "Error: --event <name> is required")
		fmt.Fprintln(os.Stderr, "Usage: irrlicht-hook-copilot --event <copilot-event-name>")
		fmt.Fprintln(os.Stderr, "  Known events: sessionStart, userPromptSubmitted, preToolUse, postToolUse,")
		fmt.Fprintln(os.Stderr, "                agentStop, subagentStop, errorOccurred, sessionEnd")
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
		logger.LogInfo("", "", "Kill switch activated via IRRLICHT_DISABLED=1, exiting")
		os.Exit(0)
	}

	// Kill switch: Copilot hooks config file.
	if isCopilotDisabled() {
		logger.LogInfo("", "", "Kill switch activated via Copilot hooks config, exiting")
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
	adapter := copilotAdapter.New(svc, eventName)
	processStart := time.Now()
	payloadSize, err := adapter.ReadAndHandle()
	processingTime := time.Since(processStart).Milliseconds()
	totalTime := time.Since(startTime).Milliseconds()

	if err != nil {
		logger.LogError("copilot:"+eventName, "", err.Error())
		logger.LogProcessingTime("copilot:"+eventName, "", processingTime, payloadSize, "error")
		os.Exit(1)
	}

	logger.LogProcessingTime("copilot:"+eventName, "", processingTime, payloadSize, "success")
	logger.LogInfo("copilot:"+eventName, "", fmt.Sprintf("Successfully processed event in %dms", totalTime))
}

// isCopilotDisabled reads ~/.copilot/hooks/irrlicht.json and checks for a disabled flag.
func isCopilotDisabled() bool {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	configPath := filepath.Join(homeDir, ".copilot", "hooks", "irrlicht.json")
	data, err := os.ReadFile(configPath)
	if err != nil {
		return false
	}
	var config map[string]interface{}
	if err := json.Unmarshal(data, &config); err != nil {
		return false
	}
	disabled, ok := config["disabled"].(bool)
	return ok && disabled
}
