package processlifecycle

import (
	"testing"

	"irrlicht/core/domain/agent"
	"irrlicht/core/ports/inbound"
)

// Compile-time assertion that Scanner satisfies both inbound port interfaces.
var (
	_ inbound.AgentWatcher = (*Scanner)(nil)
	_ inbound.Watcher      = (*Scanner)(nil)
)

func TestScannerWithIdentityRoundTrip(t *testing.T) {
	s := NewScanner("aider", "aider", 0)
	if got := s.Identity(); got != (agent.Identity{}) {
		t.Fatalf("Identity before WithIdentity: got %+v, want zero", got)
	}
	id := agent.Identity{
		Name:         "aider",
		DisplayName:  "Aider",
		IconSVGLight: "<svg>aider-light</svg>",
		IconSVGDark:  "<svg>aider-dark</svg>",
	}
	s.WithIdentity(id)
	if got := s.Identity(); got != id {
		t.Errorf("Identity after WithIdentity: got %+v, want %+v", got, id)
	}
}
