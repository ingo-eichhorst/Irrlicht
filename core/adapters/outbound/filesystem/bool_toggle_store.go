package filesystem

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

const boolToggleFileVersion = 1

type boolToggleFile struct {
	Version int  `json:"version"`
	Enabled bool `json:"enabled"`
}

// boolToggle persists a single default-OFF boolean to a versioned JSON file
// with atomic temp+rename writes. The in-memory value backs the hot Enabled()
// path so a gate check never hits disk. Safe for concurrent use. It is the
// shared implementation behind the daemon's default-OFF toggles
// (BackchannelStore, RelayControlStore).
type boolToggle struct {
	mu      sync.RWMutex
	path    string
	enabled bool
}

// newBoolToggle returns a toggle backed by path, loading the persisted value
// (default false on a missing/unreadable file).
func newBoolToggle(path string) *boolToggle {
	t := &boolToggle{path: path}
	if data, err := os.ReadFile(path); err == nil {
		var f boolToggleFile
		if json.Unmarshal(data, &f) == nil {
			t.enabled = f.Enabled
		}
	}
	return t
}

// Enabled reports the current value.
func (t *boolToggle) Enabled() bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.enabled
}

// SetEnabled persists the value and updates the in-memory copy.
func (t *boolToggle) SetEnabled(enabled bool) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(t.path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(boolToggleFile{Version: boolToggleFileVersion, Enabled: enabled}, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := writeFileAtomic(t.path, data, 0o600); err != nil {
		return err
	}
	t.enabled = enabled
	return nil
}
