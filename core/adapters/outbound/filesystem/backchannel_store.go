// backchannel_store.go persists the single default-OFF master toggle that
// gates the whole backchannel capability — input injection and event→action
// rules (issue #724). A missing file means disabled, which is the fresh and
// upgrade state: control stays off until the user opts in.
package filesystem

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

const backchannelFileVersion = 1

type backchannelFile struct {
	Version int  `json:"version"`
	Enabled bool `json:"enabled"`
}

// BackchannelStore persists the backchannel master-toggle to
// <dir>/backchannel.json with atomic temp+rename writes. Safe for concurrent
// use. The in-memory value is the source of truth on the hot Enabled() path so
// the InputService gate does not hit disk per request.
type BackchannelStore struct {
	mu      sync.RWMutex
	path    string
	enabled bool
}

// NewBackchannelStore returns a store rooted at dir (the daemon data dir),
// loading the persisted value (default false on a missing/unreadable file).
func NewBackchannelStore(dir string) *BackchannelStore {
	s := &BackchannelStore{path: filepath.Join(dir, "backchannel.json")}
	if data, err := os.ReadFile(s.path); err == nil {
		var f backchannelFile
		if json.Unmarshal(data, &f) == nil {
			s.enabled = f.Enabled
		}
	}
	return s
}

// Enabled reports whether the backchannel master-toggle is on.
func (s *BackchannelStore) Enabled() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.enabled
}

// SetEnabled persists the toggle and updates the in-memory value.
func (s *BackchannelStore) SetEnabled(enabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(backchannelFile{Version: backchannelFileVersion, Enabled: enabled}, "", "  ")
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
