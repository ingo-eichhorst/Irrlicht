// relay_control_store.go persists the default-OFF toggle that decides whether
// this daemon acts on inbound *remote* control frames arriving over the relay
// (issue #724). It is the outer of the two remote gates: even with it on, every
// relayed input still passes the backchannel master toggle + per-agent consent
// + controllability in InputService. A missing file means disabled.
package filesystem

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

const relayControlFileVersion = 1

type relayControlFile struct {
	Version int  `json:"version"`
	Enabled bool `json:"enabled"`
}

// RelayControlStore persists the relay-control toggle to
// <dir>/relay_control.json with atomic temp+rename writes. Safe for concurrent
// use; the in-memory value backs the hot Enabled() path.
type RelayControlStore struct {
	mu      sync.RWMutex
	path    string
	enabled bool
}

// NewRelayControlStore returns a store rooted at dir, loading the persisted
// value (default false on a missing/unreadable file).
func NewRelayControlStore(dir string) *RelayControlStore {
	s := &RelayControlStore{path: filepath.Join(dir, "relay_control.json")}
	if data, err := os.ReadFile(s.path); err == nil {
		var f relayControlFile
		if json.Unmarshal(data, &f) == nil {
			s.enabled = f.Enabled
		}
	}
	return s
}

// Enabled reports whether remote (relay) control is accepted by this daemon.
func (s *RelayControlStore) Enabled() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.enabled
}

// SetEnabled persists the toggle and updates the in-memory value.
func (s *RelayControlStore) SetEnabled(enabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(relayControlFile{Version: relayControlFileVersion, Enabled: enabled}, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := writeFileAtomic(s.path, data, 0o600); err != nil {
		return err
	}
	s.enabled = enabled
	return nil
}
