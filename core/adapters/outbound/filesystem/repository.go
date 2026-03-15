package filesystem

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"irrlicht/core/domain/session"
)

const appSupportDir = "Library/Application Support/Irrlicht"

// SessionRepository implements ports/outbound.SessionRepository using the local filesystem.
type SessionRepository struct {
	instancesDir string
}

// New returns a SessionRepository rooted at the user's Application Support directory.
func New() (*SessionRepository, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("cannot determine home directory: %w", err)
	}
	return &SessionRepository{
		instancesDir: filepath.Join(homeDir, appSupportDir, "instances"),
	}, nil
}

// NewWithDir returns a SessionRepository rooted at the given directory (useful for tests).
func NewWithDir(dir string) *SessionRepository {
	return &SessionRepository{instancesDir: dir}
}

// InstancesDir returns the directory where session files are stored.
func (r *SessionRepository) InstancesDir() string {
	return r.instancesDir
}

// Load reads a session state from disk. Returns (nil, err) if the file does not exist.
func (r *SessionRepository) Load(sessionID string) (*session.SessionState, error) {
	data, err := os.ReadFile(r.statePath(sessionID))
	if err != nil {
		return nil, err
	}
	var state session.SessionState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	return &state, nil
}

// Save atomically writes a session state to disk.
func (r *SessionRepository) Save(state *session.SessionState) error {
	if err := os.MkdirAll(r.instancesDir, 0755); err != nil {
		return fmt.Errorf("failed to create instances directory: %w", err)
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal session state: %w", err)
	}
	path := r.statePath(state.SessionID)
	tmpPath := fmt.Sprintf("%s.tmp.%d.%d", path, os.Getpid(), time.Now().UnixNano())
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write temp file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("failed to rename temp file: %w", err)
	}
	return nil
}

// Delete removes a session state file. Returns nil if the file does not exist.
func (r *SessionRepository) Delete(sessionID string) error {
	err := os.Remove(r.statePath(sessionID))
	if err != nil && os.IsNotExist(err) {
		return nil
	}
	return err
}

// ListAll returns all session states found in the instances directory.
// Files that cannot be parsed are silently skipped.
func (r *SessionRepository) ListAll() ([]*session.SessionState, error) {
	entries, err := os.ReadDir(r.instancesDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var states []*session.SessionState
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if filepath.Ext(name) != ".json" || strings.Contains(name, ".tmp.") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(r.instancesDir, name))
		if err != nil {
			continue
		}
		var state session.SessionState
		if err := json.Unmarshal(data, &state); err != nil {
			continue
		}
		states = append(states, &state)
	}
	return states, nil
}

func (r *SessionRepository) statePath(sessionID string) string {
	return filepath.Join(r.instancesDir, sessionID+".json")
}
