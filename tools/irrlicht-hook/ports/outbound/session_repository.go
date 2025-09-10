package outbound

import (
	"irrlicht/hook/domain/session"
)

// SessionRepository defines the outbound port for session persistence
type SessionRepository interface {
	// SaveSession persists a session to storage
	SaveSession(session *session.Session) error
	
	// LoadSession retrieves a session by ID
	LoadSession(sessionID string) (*session.Session, error)
	
	// GetSession retrieves a session by ID (alias for LoadSession)
	GetSession(sessionID string) (*session.Session, error)
	
	// DeleteSession removes a session from storage
	DeleteSession(sessionID string) error
	
	// ListSessions returns all active sessions
	ListSessions() ([]*session.Session, error)
	
	// SessionExists checks if a session exists without loading it
	SessionExists(sessionID string) bool
	
	// GetSessionPath returns the storage path for a session (implementation-specific)
	GetSessionPath(sessionID string) string
}

// SessionStorage defines lower-level storage operations
type SessionStorage interface {
	// Write writes session data to storage
	Write(sessionID string, data []byte) error
	
	// Read reads session data from storage
	Read(sessionID string) ([]byte, error)
	
	// Delete removes session data from storage
	Delete(sessionID string) error
	
	// List returns all stored session IDs
	List() ([]string, error)
	
	// Exists checks if session data exists
	Exists(sessionID string) bool
}

// SessionWatcher defines the interface for watching session changes
type SessionWatcher interface {
	// WatchSessions watches for session changes and sends updates
	WatchSessions(updates chan<- SessionUpdate) error
	
	// StopWatching stops watching for session changes
	StopWatching() error
}

// SessionUpdate represents a session change notification
type SessionUpdate struct {
	SessionID string
	Type      UpdateType
	Session   *session.Session
}

// UpdateType represents the type of session update
type UpdateType string

const (
	SessionCreated UpdateType = "created"
	SessionUpdated UpdateType = "updated"
	SessionDeleted UpdateType = "deleted"
)

// SessionBackup defines the interface for session backup operations
type SessionBackup interface {
	// BackupSession creates a backup of a session
	BackupSession(sessionID string) error
	
	// RestoreSession restores a session from backup
	RestoreSession(sessionID string) (*session.Session, error)
	
	// ListBackups returns available backups for a session
	ListBackups(sessionID string) ([]BackupInfo, error)
	
	// CleanupOldBackups removes old backup files
	CleanupOldBackups(maxAge int64) error
}

// BackupInfo contains information about a backup
type BackupInfo struct {
	SessionID   string
	BackupTime  int64
	BackupPath  string
	FileSize    int64
}

// SessionValidator defines validation for session data
type SessionValidator interface {
	// ValidateSession validates session data before persistence
	ValidateSession(session *session.Session) error
	
	// ValidateSessionID validates a session ID format
	ValidateSessionID(sessionID string) error
	
	// SanitizeSession sanitizes session data
	SanitizeSession(session *session.Session) *session.Session
}