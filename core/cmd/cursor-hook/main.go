package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"runtime"

	cursorAdapter "irrlicht/core/adapters/inbound/cursor"
	"irrlicht/core/adapters/outbound/filesystem"
	"irrlicht/core/adapters/outbound/git"
	"irrlicht/core/adapters/outbound/logging"
	"irrlicht/core/adapters/outbound/metrics"
	"irrlicht/core/adapters/outbound/security"
	"irrlicht/core/application/services"
	cursorev "irrlicht/core/domain/cursor"
	"irrlicht/core/domain/event"
)

// Version is injected at build time via -ldflags "-X main.Version=x.y.z".
var Version = "dev"

func main() {
	// --version flag
	if len(os.Args) > 1 && (os.Args[1] == "--version" || os.Args[1] == "-v") {
		fmt.Printf("cursor-hook version %s\n", Version)
		fmt.Printf("Built with %s %s/%s\n", runtime.Version(), runtime.GOOS, runtime.GOARCH)
		os.Exit(0)
	}

	// Kill switch: environment variable.
	if os.Getenv("IRRLICHT_DISABLED") == "1" {
		os.Exit(0)
	}

	// Kill switch: Cursor hooks.json settings.
	if isDisabledInCursorSettings() {
		os.Exit(0)
	}

	// Initialise logger first so subsequent errors can be recorded.
	logger, err := logging.New()
	if err != nil {
		log.Printf("Failed to initialise logger: %v", err)
		os.Exit(1)
	}
	defer logger.Close()

	// Build outbound adapters (shared with irrlicht-hook).
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

	// Read raw JSON from stdin.
	input, err := io.ReadAll(os.Stdin)
	if err != nil {
		logger.LogError("", "", fmt.Sprintf("failed to read stdin: %v", err))
		os.Exit(1)
	}
	if len(input) > event.MaxPayloadSize {
		logger.LogError("", "", fmt.Sprintf("payload size %d exceeds maximum %d", len(input), event.MaxPayloadSize))
		os.Exit(1)
	}

	// Parse as Cursor event.
	var cursorEvt cursorev.CursorEvent
	if err := json.Unmarshal(input, &cursorEvt); err != nil {
		logger.LogError("", "", fmt.Sprintf("failed to parse Cursor event JSON: %v", err))
		os.Exit(1)
	}

	// Validate required fields.
	if cursorEvt.ConversationID == "" {
		logger.LogError("", "", "missing conversation_id in Cursor event")
		os.Exit(1)
	}
	if cursorEvt.HookEventName == "" {
		logger.LogError("", "", "missing hook_event_name in Cursor event")
		os.Exit(1)
	}

	// Normalize Cursor event to irrlicht HookEvent.
	normalized := cursorAdapter.NormalizeEvent(&cursorEvt)

	logger.LogInfo(normalized.HookEventName, normalized.SessionID,
		fmt.Sprintf("Cursor event %q normalized (model=%s cwd=%s)",
			cursorEvt.HookEventName, normalized.Model, normalized.CWD))

	// Process through the shared event service.
	if err := svc.HandleEvent(normalized); err != nil {
		logger.LogError(normalized.HookEventName, normalized.SessionID, err.Error())
		os.Exit(1)
	}

	logger.LogInfo(normalized.HookEventName, normalized.SessionID, "Cursor event processed successfully")
}

// isDisabledInCursorSettings checks hooks.irrlicht.disabled in ~/.cursor/hooks.json.
func isDisabledInCursorSettings() bool {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	data, err := os.ReadFile(homeDir + "/.cursor/hooks.json")
	if err != nil {
		return false
	}
	var hooks map[string]interface{}
	if err := json.Unmarshal(data, &hooks); err != nil {
		return false
	}
	irrlicht, ok := hooks["irrlicht"].(map[string]interface{})
	if !ok {
		return false
	}
	disabled, ok := irrlicht["disabled"].(bool)
	return ok && disabled
}
