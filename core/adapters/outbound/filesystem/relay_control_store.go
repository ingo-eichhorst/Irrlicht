// relay_control_store.go persists the default-OFF toggle that decides whether
// this daemon acts on inbound *remote* control frames arriving over the relay
// (issue #724). It is the outer of the two remote gates: even with it on, every
// relayed input still passes the backchannel master toggle + per-agent consent
// + controllability in InputService. A missing file means disabled.
package filesystem

import "path/filepath"

// RelayControlStore persists the relay-control toggle to
// <dir>/relay_control.json. Storage mechanics live in boolToggle.
type RelayControlStore struct{ *boolToggle }

// NewRelayControlStore returns a store rooted at dir, loading the persisted
// value (default false on a missing/unreadable file).
func NewRelayControlStore(dir string) *RelayControlStore {
	return &RelayControlStore{newBoolToggle(filepath.Join(dir, "relay_control.json"))}
}
