package fswatcher

import (
	"testing"

	"irrlicht/core/domain/agent"
	"irrlicht/core/ports/inbound"
)

// Compile-time assertion that Watcher satisfies the inbound port.
var _ inbound.Watcher = (*Watcher)(nil)

func TestWithIdentityRoundTrip(t *testing.T) {
	w := New("", "claude-code", 0)
	if got := w.Identity(); got != (agent.Identity{}) {
		t.Fatalf("Identity before WithIdentity: got %+v, want zero", got)
	}
	id := agent.Identity{
		Name:         "claude-code",
		DisplayName:  "Claude Code",
		IconSVGLight: "<svg>light</svg>",
		IconSVGDark:  "<svg>dark</svg>",
	}
	w.WithIdentity(id)
	if got := w.Identity(); got != id {
		t.Errorf("Identity after WithIdentity: got %+v, want %+v", got, id)
	}
}
