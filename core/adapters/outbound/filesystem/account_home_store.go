// account_home_store.go persists the daemon's per-home account tracking
// (#2095): for each agent identity file, which account it named and since
// when. It is what lets a switch made while the daemon was down show up as a
// change after restart instead of being adopted as the baseline.
package filesystem

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"irrlicht/core/domain/session"
)

// accountHomesFileVersion is bumped if the on-disk shape ever changes.
const accountHomesFileVersion = 1

type accountHomesFile struct {
	Version int                            `json:"version"`
	Homes   map[string]session.HomeAccount `json:"homes"`
}

// AccountHomeStore persists the home -> account map to
// <dir>/account-homes.json with atomic writes. Safe for concurrent use.
type AccountHomeStore struct {
	mu   sync.Mutex
	path string
}

// NewAccountHomeStore returns a store rooted at dir (the daemon data dir).
func NewAccountHomeStore(dir string) *AccountHomeStore {
	return &AccountHomeStore{path: filepath.Join(dir, "account-homes.json")}
}

// Load reads the persisted map. A missing file is an empty map, not an error.
func (s *AccountHomeStore) Load() (map[string]session.HomeAccount, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return map[string]session.HomeAccount{}, nil
	}
	if err != nil {
		return nil, err
	}
	var f accountHomesFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, err
	}
	if f.Homes == nil {
		f.Homes = map[string]session.HomeAccount{}
	}
	return f.Homes, nil
}

// Save writes the map atomically (0600: the keys are filesystem paths and the
// values are account ids).
func (s *AccountHomeStore) Save(homes map[string]session.HomeAccount) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(accountHomesFile{Version: accountHomesFileVersion, Homes: homes}, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(s.path, append(data, '\n'), 0o600)
}
