package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"irrlicht/core/adapters/outbound/filesystem"
	"irrlicht/core/adapters/outbound/git"
	"irrlicht/core/adapters/outbound/logging"
	"irrlicht/core/adapters/outbound/metrics"
	"irrlicht/core/adapters/outbound/security"
	"irrlicht/core/application/services"
	"irrlicht/core/domain/event"
)

// Version is injected at build time via -ldflags "-X main.Version=x.y.z".
var Version = "dev"

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "--version" || os.Args[1] == "-v") {
		fmt.Printf("irrlicht-shim version %s\n", Version)
		fmt.Printf("Built with %s %s/%s\n", runtime.Version(), runtime.GOOS, runtime.GOARCH)
		os.Exit(0)
	}

	// --speculative-wait <sessionID> mode: delegate inline (daemon handles its own).
	if len(os.Args) > 2 && os.Args[1] == "--speculative-wait" {
		if isDisabledInSettings() {
			os.Exit(0)
		}
		svc := buildInlineService()
		svc.RunSpeculativeWait(os.Args[2])
		return
	}

	// Kill switch: environment variable.
	if os.Getenv("IRRLICHT_DISABLED") == "1" {
		os.Exit(0)
	}

	// Kill switch: settings file.
	if isDisabledInSettings() {
		os.Exit(0)
	}

	// Read event from stdin.
	input, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "irrlicht-shim: failed to read stdin: %v\n", err)
		os.Exit(1)
	}

	if len(input) > event.MaxPayloadSize {
		fmt.Fprintf(os.Stderr, "irrlicht-shim: payload size %d exceeds maximum %d\n", len(input), event.MaxPayloadSize)
		os.Exit(1)
	}

	var evt event.HookEvent
	if err := json.Unmarshal(input, &evt); err != nil {
		fmt.Fprintf(os.Stderr, "irrlicht-shim: failed to parse JSON: %v\n", err)
		os.Exit(1)
	}

	// Try to relay to the daemon first.
	if relayToDaemon(input) {
		return
	}

	// Fallback: process inline using EventService directly.
	processInline(&evt)
}

// relayToDaemon attempts to POST the raw event bytes to irrlichtd via Unix socket.
// Returns true if the daemon accepted the event; false if the socket is unavailable
// or the request times out.
func relayToDaemon(body []byte) bool {
	sockPath := daemonSocketPath()

	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", sockPath)
		},
	}
	client := &http.Client{
		Transport: transport,
		Timeout:   500 * time.Millisecond,
	}

	req, err := http.NewRequest("POST", "http://irrlichtd/api/v1/events", bytes.NewReader(body))
	if err != nil {
		return false
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}

// daemonSocketPath returns the Unix socket path for irrlichtd.
func daemonSocketPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "/tmp/irrlichtd.sock"
	}
	return filepath.Join(home, ".local", "share", "irrlicht", "irrlichtd.sock")
}

// processInline falls back to processing the event directly, embedding the same
// logic as irrlicht-hook to ensure zero regression when the daemon is unavailable.
func processInline(evt *event.HookEvent) {
	svc := buildInlineService()
	svc.CleanupOrphanedSessions()
	if err := svc.HandleEvent(evt); err != nil {
		log.Printf("irrlicht-shim: inline processing error: %v", err)
		os.Exit(1)
	}
}

// buildInlineService wires up a standalone EventService (same as irrlicht-hook).
func buildInlineService() *services.EventService {
	logger, err := logging.New()
	if err != nil {
		log.Fatalf("irrlicht-shim: failed to init logger: %v", err)
	}

	repo, err := filesystem.New()
	if err != nil {
		logger.LogError("startup", "", fmt.Sprintf("failed to init session repository: %v", err))
		os.Exit(1)
	}

	pathValidator, err := security.New()
	if err != nil {
		logger.LogError("startup", "", fmt.Sprintf("failed to init path validator: %v", err))
		os.Exit(1)
	}

	return services.NewEventService(repo, logger, git.New(), metrics.New(), pathValidator)
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
