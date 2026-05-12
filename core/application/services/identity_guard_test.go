package services_test

import (
	"fmt"
	"strings"
	"testing"

	"irrlicht/core/application/services"
	"irrlicht/core/domain/agent"
	"irrlicht/core/ports/inbound"
)

// TestNewSessionDetector_PanicsOnZeroIdentity verifies the safety check
// that fails fast when a watcher with an unset Identity is wired into
// the detector. Without this guard, every session bootstrapped from
// that watcher's events would have an empty Adapter field — silent
// observability failure.
func TestNewSessionDetector_PanicsOnZeroIdentity(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic when watcher has zero Identity, got none")
		}
		// fmt.Sprint handles both string and error payloads, so this test
		// survives a future refactor that wraps the panic in an error.
		msg := fmt.Sprint(r)
		if !strings.Contains(msg, "Identity") || !strings.Contains(msg, "WithIdentity") {
			t.Errorf("panic message should name Identity and WithIdentity; got %q", msg)
		}
	}()

	tw := newMockAgentWatcher()
	tw.identity = agent.Identity{} // clear the default

	services.NewSessionDetector(
		[]inbound.Watcher{tw},
		newMockProcessWatcher(), newMockRepo(),
		&mockLogger{}, &mockGit{}, &mockMetrics{}, nil,
		"test", 0, nil, nil, nil,
	)
}
