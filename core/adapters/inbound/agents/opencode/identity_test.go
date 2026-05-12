package opencode

import (
	"testing"
	"time"

	"irrlicht/core/domain/agent"
	"irrlicht/core/ports/inbound"
)

// Compile-time assertion that Watcher satisfies the inbound port.
var _ inbound.Watcher = (*Watcher)(nil)

func TestOpenCodeWithIdentityRoundTrip(t *testing.T) {
	w := New(time.Hour)
	if got := w.Identity(); got != (agent.Identity{}) {
		t.Fatalf("Identity before WithIdentity: got %+v, want zero", got)
	}
	id := Agent().Identity
	w.WithIdentity(id)
	if got := w.Identity(); got != id {
		t.Errorf("Identity after WithIdentity: got %+v, want %+v", got, id)
	}
}
