// permission_store.go persists the user's consent answers for the
// permission transparency wizard (issue #570) as JSON under the daemon
// data dir. A missing file means every permission is pending — which is
// both the fresh-install state and the upgrade migration: existing
// installs pause all monitoring until the wizard is answered.
package filesystem

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"irrlicht/core/domain/permission"
)

// permissionFileVersion is bumped if the on-disk shape ever changes.
const permissionFileVersion = 1

// permissionFile is the on-disk JSON shape of permissions.json.
type permissionFile struct {
	Version int            `json:"version"`
	Agents  permission.Set `json:"agents"`
}

// PermissionStore persists permission.Set to <dir>/permissions.json with
// atomic temp+rename writes. Safe for concurrent use.
type PermissionStore struct {
	mu   sync.Mutex
	path string
}

// NewPermissionStore returns a store rooted at dir (the daemon data dir,
// IRRLICHT_HOME-aware via the caller).
func NewPermissionStore(dir string) *PermissionStore {
	return &PermissionStore{path: filepath.Join(dir, "permissions.json")}
}

// Load reads the persisted permission set. A missing file returns an
// empty set (all pending), not an error.
func (s *PermissionStore) Load() (permission.Set, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return permission.Set{}, nil
	}
	if err != nil {
		return nil, err
	}
	var f permissionFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, err
	}
	if f.Agents == nil {
		f.Agents = permission.Set{}
	}
	return f.Agents, nil
}

// Save writes the permission set atomically (temp file + rename).
func (s *PermissionStore) Save(set permission.Set) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(permissionFile{Version: permissionFileVersion, Agents: set}, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return writeFileAtomic(s.path, data, 0o600)
}
