package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"irrlicht/hook/domain/event"
	"irrlicht/hook/domain/session"
	"irrlicht/hook/infrastructure/container"
)

// TestFixture represents a test case with expected behavior
type TestFixture struct {
	Name             string
	FixturePath      string
	ExpectedState    string
	ShouldCreateFile bool
	ShouldFail       bool
	Description      string
}

// GetTestFixtures returns all test fixtures with their expected outcomes
func GetTestFixtures() []TestFixture {
	return []TestFixture{
		{
			Name:             "SessionStart",
			FixturePath:      "../../fixtures/session-start.json",
			ExpectedState:    "ready",
			ShouldCreateFile: true,
			ShouldFail:       false,
			Description:      "Should create session file with ready state (new session)",
		},
		{
			Name:             "SessionEnd",
			FixturePath:      "../../fixtures/session-end.json",
			ExpectedState:    "",
			ShouldCreateFile: false,
			ShouldFail:       false,
			Description:      "Should delete session file",
		},
		{
			Name:             "UserPromptSubmit",
			FixturePath:      "../../fixtures/user-prompt-submit.json",
			ExpectedState:    "working",
			ShouldCreateFile: true,
			ShouldFail:       false,
			Description:      "Should create/update session with working state",
		},
		{
			Name:             "Notification",
			FixturePath:      "../../fixtures/notification.json",
			ExpectedState:    "waiting",
			ShouldCreateFile: true,
			ShouldFail:       false,
			Description:      "Should create session with waiting state",
		},
		{
			Name:             "Stop",
			FixturePath:      "../../fixtures/stop.json",
			ExpectedState:    "ready",
			ShouldCreateFile: true,
			ShouldFail:       false,
			Description:      "Should create session with ready state",
		},
		{
			Name:             "SubagentStop",
			FixturePath:      "../../fixtures/subagent-stop.json",
			ExpectedState:    "ready",
			ShouldCreateFile: true,
			ShouldFail:       false,
			Description:      "Should create session with ready state",
		},
	}
}

// setupTestEnvironment creates a temporary directory for test session files
func setupTestEnvironment(t *testing.T) string {
	tempDir := t.TempDir()
	instancesDir := filepath.Join(tempDir, "instances")
	err := os.MkdirAll(instancesDir, 0755)
	if err != nil {
		t.Fatalf("Failed to create test instances directory: %v", err)
	}

	// Set environment variable to override the app support directory
	os.Setenv("IRRLICHT_TEST_DIR", tempDir)
	return tempDir
}

// cleanupTestEnvironment removes test environment
func cleanupTestEnvironment() {
	os.Unsetenv("IRRLICHT_TEST_DIR")
}

// processEventFromFixture processes a single event from a fixture file using the new architecture
func processEventFromFixture(fixturePath string) (*session.Session, error) {
	// Initialize dependency injection container
	di, err := container.NewContainer()
	if err != nil {
		return nil, fmt.Errorf("failed to initialize container: %v", err)
	}
	defer di.Close()

	// Read fixture file
	fixtureData, err := os.ReadFile(fixturePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read fixture: %v", err)
	}

	// Parse JSON
	var rawEvent map[string]interface{}
	if err := json.Unmarshal(fixtureData, &rawEvent); err != nil {
		return nil, fmt.Errorf("failed to parse fixture JSON: %v", err)
	}

	// Convert to domain event
	hookEvent := event.FromRawMap(rawEvent)
	if hookEvent == nil {
		return nil, fmt.Errorf("failed to convert raw event to domain event")
	}

	// Process the event using the use case
	useCase := di.GetProcessHookEventUseCase()
	err = useCase.Execute(hookEvent)
	if err != nil {
		return nil, fmt.Errorf("event processing failed: %v", err)
	}

	// If the event was processed successfully, try to load the resulting session state
	if hookEvent.SessionID != "" && !session.ShouldDeleteSession(hookEvent.HookEventName) {
		// Get the session repository to load the session
		configService := di.GetConfigService()
		instancesDir := configService.GetInstancesDir()
		
		// Try to read the session file directly
		sessionPath := filepath.Join(instancesDir, hookEvent.SessionID+".json")
		data, err := os.ReadFile(sessionPath)
		if err != nil {
			return nil, nil // Session might not exist (expected for some cases)
		}

		// Parse legacy session state
		var legacySession session.LegacySessionState
		if err := json.Unmarshal(data, &legacySession); err != nil {
			return nil, fmt.Errorf("failed to unmarshal session: %v", err)
		}

		// Convert to domain session
		domainSession := session.FromLegacySessionState(&legacySession)
		return domainSession, nil
	}

	return nil, nil
}

// TestProcessEventIntegration tests the complete event processing pipeline
func TestProcessEventIntegration(t *testing.T) {
	defer cleanupTestEnvironment()

	fixtures := GetTestFixtures()

	for _, fixture := range fixtures {
		t.Run(fixture.Name, func(t *testing.T) {
			// Setup test environment
			testDir := setupTestEnvironment(t)
			_ = testDir

			// Create a simple test event instead of relying on fixtures
			var hookEvent *event.HookEvent
			switch fixture.Name {
			case "SessionStart":
				hookEvent = &event.HookEvent{
					HookEventName:  "SessionStart",
					SessionID:      "test-session-start",
					Timestamp:      time.Now().Format(time.RFC3339),
					Data:           make(map[string]interface{}),
					TranscriptPath: "/Users/ingo/test.md",
					CWD:            "/Users/ingo",
				}
			case "SessionEnd":
				// First create a session, then end it
				startEvent := &event.HookEvent{
					HookEventName:  "SessionStart",
					SessionID:      "test-session-end",
					Timestamp:      time.Now().Format(time.RFC3339),
					Data:           make(map[string]interface{}),
					TranscriptPath: "/Users/ingo/test.md",
					CWD:            "/Users/ingo",
				}
				di, _ := container.NewContainer()
				useCase := di.GetProcessHookEventUseCase()
				useCase.Execute(startEvent)
				di.Close()
				
				hookEvent = &event.HookEvent{
					HookEventName: "SessionEnd",
					SessionID:     "test-session-end",
					Timestamp:     time.Now().Format(time.RFC3339),
					Data:          make(map[string]interface{}),
					Reason:        "test",
				}
			case "UserPromptSubmit":
				hookEvent = &event.HookEvent{
					HookEventName:  "UserPromptSubmit",
					SessionID:      "test-user-prompt",
					Timestamp:      time.Now().Format(time.RFC3339),
					Data:           make(map[string]interface{}),
					TranscriptPath: "/Users/ingo/test.md",
					CWD:            "/Users/ingo",
				}
			case "Notification":
				hookEvent = &event.HookEvent{
					HookEventName:  "Notification",
					SessionID:      "test-notification",
					Timestamp:      time.Now().Format(time.RFC3339),
					Data:           make(map[string]interface{}),
					TranscriptPath: "/Users/ingo/test.md",
					CWD:            "/Users/ingo",
				}
			case "Stop":
				hookEvent = &event.HookEvent{
					HookEventName:  "Stop",
					SessionID:      "test-stop",
					Timestamp:      time.Now().Format(time.RFC3339),
					Data:           make(map[string]interface{}),
					TranscriptPath: "/Users/ingo/test.md",
					CWD:            "/Users/ingo",
				}
			case "SubagentStop":
				hookEvent = &event.HookEvent{
					HookEventName:  "SubagentStop",
					SessionID:      "test-subagent-stop",
					Timestamp:      time.Now().Format(time.RFC3339),
					Data:           make(map[string]interface{}),
					TranscriptPath: "/Users/ingo/test.md",
					CWD:            "/Users/ingo",
				}
			default:
				t.Skipf("Unknown fixture: %s", fixture.Name)
				return
			}

			t.Logf("Testing fixture: %s", fixture.Description)

			// Process the event using the new architecture
			di, err := container.NewContainer()
			if err != nil {
				t.Fatalf("Failed to initialize container: %v", err)
			}
			defer di.Close()

			useCase := di.GetProcessHookEventUseCase()
			err = useCase.Execute(hookEvent)

			// Check if the result matches expectations
			if fixture.ShouldFail {
				if err == nil {
					t.Errorf("Expected processing to fail, but it succeeded")
				}
				return
			}

			if err != nil {
				t.Fatalf("Unexpected error processing fixture: %v", err)
			}

			// Check if session file should exist
			if fixture.ShouldCreateFile {
				configService := di.GetConfigService()
				instancesDir := configService.GetInstancesDir()
				sessionPath := filepath.Join(instancesDir, hookEvent.SessionID+".json")

				data, err := os.ReadFile(sessionPath)
				if err != nil {
					t.Errorf("Expected session file to be created at %s, but got error: %v", sessionPath, err)
					return
				}

				// Parse and check the session state
				var legacySession session.LegacySessionState
				if err := json.Unmarshal(data, &legacySession); err != nil {
					t.Errorf("Failed to parse session file: %v", err)
					return
				}

				// Check the state matches expectations
				if fixture.ExpectedState != "" && legacySession.State != fixture.ExpectedState {
					t.Errorf("Expected state %s, got %s", fixture.ExpectedState, legacySession.State)
				}

				// Verify basic session state fields
				if legacySession.SessionID == "" {
					t.Errorf("Expected session ID to be set")
				}
				if legacySession.FirstSeen == 0 {
					t.Errorf("Expected FirstSeen timestamp to be set")
				}
				if legacySession.UpdatedAt == 0 {
					t.Errorf("Expected UpdatedAt timestamp to be set")
				}
				if legacySession.Version == 0 {
					t.Errorf("Expected Version to be set")
				}

			} else {
				// For SessionEnd events, the session file should be deleted
				configService := di.GetConfigService()
				instancesDir := configService.GetInstancesDir()
				sessionPath := filepath.Join(instancesDir, hookEvent.SessionID+".json")

				if _, err := os.Stat(sessionPath); !os.IsNotExist(err) {
					t.Errorf("Expected session file to be deleted, but it still exists")
				}
			}
		})
	}
}

// TestStateTransitions tests the state transition logic with various sequences
func TestStateTransitions(t *testing.T) {
	defer cleanupTestEnvironment()
	testDir := setupTestEnvironment(t)
	_ = testDir

	sessionID := "test-session-transitions"

	// Test sequence: SessionStart -> Notification -> Stop -> SessionEnd
	testSequence := []struct {
		eventName     string
		expectedState string
		description   string
	}{
		{"SessionStart", "ready", "Initial session start should set ready state (new session)"},
		{"Notification", "waiting", "Notification should transition to waiting state"},
		{"Stop", "ready", "Stop should transition to ready state"},
		{"SessionEnd", "", "SessionEnd should delete the session file"},
	}

	// Initialize DI container once for the whole sequence
	di, err := container.NewContainer()
	if err != nil {
		t.Fatalf("Failed to initialize container: %v", err)
	}
	defer di.Close()

	useCase := di.GetProcessHookEventUseCase()

	for i, step := range testSequence {
		t.Run(fmt.Sprintf("Step%d_%s", i+1, step.eventName), func(t *testing.T) {
			// Create a mock event
			hookEvent := &event.HookEvent{
				HookEventName: step.eventName,
				SessionID:     sessionID,
				Timestamp:     time.Now().Format(time.RFC3339),
				Data:          make(map[string]interface{}),
			}

			// Add required fields based on event type
			if step.eventName == "SessionStart" {
				hookEvent.TranscriptPath = "/Users/ingo/test-transcript.md"
				hookEvent.CWD = "/Users/ingo"
			}
			if step.eventName == "SessionEnd" {
				hookEvent.Reason = "test"
			}

			// Process the event
			err := useCase.Execute(hookEvent)
			if err != nil {
				t.Fatalf("Failed to process event %s: %v", step.eventName, err)
			}

			// Check the resulting state
			configService := di.GetConfigService()
			instancesDir := configService.GetInstancesDir()
			sessionPath := filepath.Join(instancesDir, sessionID+".json")

			if step.expectedState == "" {
				// SessionEnd should delete the file
				if _, err := os.Stat(sessionPath); !os.IsNotExist(err) {
					t.Errorf("Expected session file to be deleted after SessionEnd")
				}
			} else {
				data, err := os.ReadFile(sessionPath)
				if err != nil {
					t.Fatalf("Failed to load session state after %s: %v", step.eventName, err)
				}

				var legacySession session.LegacySessionState
				if err := json.Unmarshal(data, &legacySession); err != nil {
					t.Fatalf("Failed to parse session state: %v", err)
				}

				if legacySession.State != step.expectedState {
					t.Errorf("Expected state %s after %s, got %s", step.expectedState, step.eventName, legacySession.State)
				}
			}
		})
	}
}

// TestEventValidation tests the event validation logic
func TestEventValidation(t *testing.T) {
	testCases := []struct {
		name      string
		event     *event.HookEvent
		shouldErr bool
	}{
		{
			name: "ValidEvent",
			event: &event.HookEvent{
				HookEventName:  "SessionStart",
				SessionID:      "test-session",
				Timestamp:      time.Now().Format(time.RFC3339),
				Data:           make(map[string]interface{}),
				TranscriptPath: "/Users/ingo/test.md",
				CWD:            "/Users/ingo",
			},
			shouldErr: false,
		},
		{
			name: "MissingSessionID",
			event: &event.HookEvent{
				HookEventName: "SessionStart",
				Timestamp:     time.Now().Format(time.RFC3339),
				Data:          make(map[string]interface{}),
			},
			shouldErr: true,
		},
		{
			name: "MissingEventName",
			event: &event.HookEvent{
				SessionID: "test-session",
				Timestamp: time.Now().Format(time.RFC3339),
				Data:      make(map[string]interface{}),
			},
			shouldErr: true,
		},
		{
			name: "InvalidPath",
			event: &event.HookEvent{
				HookEventName:  "SessionStart",
				SessionID:      "test-session",
				Timestamp:      time.Now().Format(time.RFC3339),
				Data:           make(map[string]interface{}),
				TranscriptPath: "/etc/passwd", // Outside home directory
				CWD:            "/Users/ingo",
			},
			shouldErr: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Setup DI container with validator
			di, err := container.NewContainer()
			if err != nil {
				t.Fatalf("Failed to initialize container: %v", err)
			}
			defer di.Close()

			useCase := di.GetProcessHookEventUseCase()
			err = useCase.Execute(tc.event)

			if tc.shouldErr && err == nil {
				t.Errorf("Expected validation error, but got nil")
			}
			if !tc.shouldErr && err != nil {
				t.Errorf("Expected no validation error, but got: %v", err)
			}
		})
	}
}

// TestConcurrentEventProcessing tests that concurrent event processing doesn't cause issues
func TestConcurrentEventProcessing(t *testing.T) {
	defer cleanupTestEnvironment()
	testDir := setupTestEnvironment(t)
	_ = testDir

	// Process multiple events concurrently
	numSessions := 10
	done := make(chan error, numSessions)

	for i := 0; i < numSessions; i++ {
		go func(sessionNum int) {
			// Each goroutine gets its own DI container
			di, err := container.NewContainer()
			if err != nil {
				done <- fmt.Errorf("failed to initialize container: %v", err)
				return
			}
			defer di.Close()

			sessionID := fmt.Sprintf("concurrent-test-%d", sessionNum)
			hookEvent := &event.HookEvent{
				HookEventName:  "SessionStart",
				SessionID:      sessionID,
				Timestamp:      time.Now().Format(time.RFC3339),
				Data:           make(map[string]interface{}),
				TranscriptPath: fmt.Sprintf("/Users/ingo/test-%d.md", sessionNum),
				CWD:            "/Users/ingo",
			}

			useCase := di.GetProcessHookEventUseCase()
			err = useCase.Execute(hookEvent)
			done <- err
		}(i)
	}

	// Wait for all goroutines to complete
	for i := 0; i < numSessions; i++ {
		if err := <-done; err != nil {
			t.Errorf("Concurrent event processing failed: %v", err)
		}
	}

	// Verify all session files were created
	di, err := container.NewContainer()
	if err != nil {
		t.Fatalf("Failed to initialize container: %v", err)
	}
	defer di.Close()

	configService := di.GetConfigService()
	instancesDir := configService.GetInstancesDir()

	for i := 0; i < numSessions; i++ {
		sessionID := fmt.Sprintf("concurrent-test-%d", i)
		sessionPath := filepath.Join(instancesDir, sessionID+".json")
		if _, err := os.Stat(sessionPath); os.IsNotExist(err) {
			t.Errorf("Failed to find session file for %s", sessionID)
		}
	}
}

// BenchmarkEventProcessing benchmarks the event processing performance
func BenchmarkEventProcessing(b *testing.B) {
	// Setup test environment
	tempDir := b.TempDir()
	instancesDir := filepath.Join(tempDir, "instances")
	os.MkdirAll(instancesDir, 0755)
	os.Setenv("IRRLICHT_TEST_DIR", tempDir)
	defer os.Unsetenv("IRRLICHT_TEST_DIR")

	// Initialize DI container once
	di, err := container.NewContainer()
	if err != nil {
		b.Fatalf("Failed to initialize container: %v", err)
	}
	defer di.Close()

	useCase := di.GetProcessHookEventUseCase()

	// Create a test event
	baseEvent := &event.HookEvent{
		HookEventName:  "SessionStart",
		SessionID:      "benchmark-session",
		Timestamp:      time.Now().Format(time.RFC3339),
		Data:           make(map[string]interface{}),
		TranscriptPath: "/Users/ingo/benchmark.md",
		CWD:            "/Users/ingo",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Use a unique session ID for each iteration
		hookEvent := *baseEvent
		hookEvent.SessionID = fmt.Sprintf("benchmark-session-%d", i)
		
		err := useCase.Execute(&hookEvent)
		if err != nil {
			b.Fatalf("Event processing failed: %v", err)
		}
	}
}