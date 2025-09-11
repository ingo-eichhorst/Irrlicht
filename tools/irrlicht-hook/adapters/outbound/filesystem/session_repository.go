package filesystem

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"irrlicht/hook/domain/session"
	"irrlicht/hook/ports/outbound"
)

const (
	DefaultAppSupportDir = "Library/Application Support/Irrlicht"
	DefaultInstancesDir  = "instances"
	FileExtension        = ".json"
	BackupExtension      = ".backup"
	TempExtension        = ".tmp"
)

// FilesystemSessionRepository implements session persistence using the filesystem
type FilesystemSessionRepository struct {
	instancesDir string
	mu           sync.RWMutex
	validator    outbound.SessionValidator
}

// NewFilesystemSessionRepository creates a new filesystem session repository
func NewFilesystemSessionRepository() *FilesystemSessionRepository {
	instancesDir := getDefaultInstancesDir()
	return &FilesystemSessionRepository{
		instancesDir: instancesDir,
		validator:    NewSessionValidator(),
	}
}

// NewFilesystemSessionRepositoryWithPath creates a repository with a custom path
func NewFilesystemSessionRepositoryWithPath(instancesDir string) *FilesystemSessionRepository {
	return &FilesystemSessionRepository{
		instancesDir: instancesDir,
		validator:    NewSessionValidator(),
	}
}

// SaveSession persists a session to storage
func (fsr *FilesystemSessionRepository) SaveSession(sess *session.Session) error {
	if sess == nil {
		return fmt.Errorf("session cannot be nil")
	}

	// Validate session
	if err := fsr.validator.ValidateSession(sess); err != nil {
		return fmt.Errorf("session validation failed: %w", err)
	}

	// Sanitize session
	sanitized := fsr.validator.SanitizeSession(sess)

	// Ensure instances directory exists
	if err := fsr.ensureInstancesDir(); err != nil {
		return fmt.Errorf("failed to create instances directory: %w", err)
	}

	// Convert to legacy format for compatibility
	legacySession := sanitized.ToLegacySessionState()
	legacySession.Version = 1 // Ensure version is set

	// Serialize to JSON
	data, err := json.MarshalIndent(legacySession, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal session: %w", err)
	}

	// Write atomically using temporary file
	sessionPath := fsr.GetSessionPath(sess.ID)
	tempPath := sessionPath + TempExtension

	fsr.mu.Lock()
	defer fsr.mu.Unlock()

	// Write to temporary file
	if err := os.WriteFile(tempPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write temporary file: %w", err)
	}

	// Atomic rename
	if err := os.Rename(tempPath, sessionPath); err != nil {
		// Clean up temp file on error
		os.Remove(tempPath)
		return fmt.Errorf("failed to rename temporary file: %w", err)
	}

	return nil
}

// LoadSession retrieves a session by ID
func (fsr *FilesystemSessionRepository) LoadSession(sessionID string) (*session.Session, error) {
	if err := fsr.validator.ValidateSessionID(sessionID); err != nil {
		return nil, fmt.Errorf("invalid session ID: %w", err)
	}

	sessionPath := fsr.GetSessionPath(sessionID)

	fsr.mu.RLock()
	data, err := os.ReadFile(sessionPath)
	fsr.mu.RUnlock()

	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("session not found: %s", sessionID)
		}
		return nil, fmt.Errorf("failed to read session file: %w", err)
	}

	// Parse legacy format
	var legacySession session.LegacySessionState
	if err := json.Unmarshal(data, &legacySession); err != nil {
		return nil, fmt.Errorf("failed to unmarshal session: %w", err)
	}

	// Convert to domain session
	domainSession := session.FromLegacySessionState(&legacySession)

	return domainSession, nil
}

// DeleteSession removes a session from storage
func (fsr *FilesystemSessionRepository) DeleteSession(sessionID string) error {
	if err := fsr.validator.ValidateSessionID(sessionID); err != nil {
		return fmt.Errorf("invalid session ID: %w", err)
	}

	sessionPath := fsr.GetSessionPath(sessionID)

	fsr.mu.Lock()
	defer fsr.mu.Unlock()

	if err := os.Remove(sessionPath); err != nil {
		if os.IsNotExist(err) {
			return nil // Already deleted, no error
		}
		return fmt.Errorf("failed to delete session file: %w", err)
	}

	return nil
}

// ListSessions returns all active sessions
func (fsr *FilesystemSessionRepository) ListSessions() ([]*session.Session, error) {
	fsr.mu.RLock()
	files, err := os.ReadDir(fsr.instancesDir)
	if err != nil {
		fsr.mu.RUnlock()
		if os.IsNotExist(err) {
			return []*session.Session{}, nil
		}
		return nil, fmt.Errorf("failed to read instances directory: %w", err)
	}

	// Collect session IDs while holding the lock
	var sessionIDs []string
	for _, file := range files {
		if file.IsDir() || !strings.HasSuffix(file.Name(), FileExtension) {
			continue
		}
		// Extract session ID from filename
		sessionID := strings.TrimSuffix(file.Name(), FileExtension)
		sessionIDs = append(sessionIDs, sessionID)
	}
	fsr.mu.RUnlock()

	// Load sessions outside the lock to avoid deadlock
	var sessions []*session.Session
	for _, sessionID := range sessionIDs {
		sess, err := fsr.LoadSession(sessionID)
		if err != nil {
			// Log error but continue with other sessions
			continue
		}
		sessions = append(sessions, sess)
	}

	return sessions, nil
}

// SessionExists checks if a session exists without loading it
func (fsr *FilesystemSessionRepository) SessionExists(sessionID string) bool {
	if err := fsr.validator.ValidateSessionID(sessionID); err != nil {
		return false
	}

	sessionPath := fsr.GetSessionPath(sessionID)
	
	fsr.mu.RLock()
	_, err := os.Stat(sessionPath)
	fsr.mu.RUnlock()

	return err == nil
}

// GetSessionPath returns the storage path for a session
func (fsr *FilesystemSessionRepository) GetSessionPath(sessionID string) string {
	return filepath.Join(fsr.instancesDir, sessionID+FileExtension)
}

// ensureInstancesDir creates the instances directory if it doesn't exist
func (fsr *FilesystemSessionRepository) ensureInstancesDir() error {
	if _, err := os.Stat(fsr.instancesDir); os.IsNotExist(err) {
		return os.MkdirAll(fsr.instancesDir, 0755)
	}
	return nil
}

// getDefaultInstancesDir returns the default instances directory path
func getDefaultInstancesDir() string {
	// Check for test override
	if testDir := os.Getenv("IRRLICHT_TEST_DIR"); testDir != "" {
		return filepath.Join(testDir, DefaultInstancesDir)
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		// Fallback to current directory
		return DefaultInstancesDir
	}

	return filepath.Join(homeDir, DefaultAppSupportDir, DefaultInstancesDir)
}

// SessionBackupManager implements session backup functionality
type SessionBackupManager struct {
	repository *FilesystemSessionRepository
	backupDir  string
}

// NewSessionBackupManager creates a new session backup manager
func NewSessionBackupManager(repository *FilesystemSessionRepository) *SessionBackupManager {
	backupDir := filepath.Join(repository.instancesDir, "backups")
	return &SessionBackupManager{
		repository: repository,
		backupDir:  backupDir,
	}
}

// BackupSession creates a backup of a session
func (sbm *SessionBackupManager) BackupSession(sessionID string) error {
	sessionPath := sbm.repository.GetSessionPath(sessionID)
	
	// Check if session exists
	if !sbm.repository.SessionExists(sessionID) {
		return fmt.Errorf("session does not exist: %s", sessionID)
	}

	// Ensure backup directory exists
	if err := os.MkdirAll(sbm.backupDir, 0755); err != nil {
		return fmt.Errorf("failed to create backup directory: %w", err)
	}

	// Create backup filename with timestamp
	timestamp := time.Now().Format("20060102_150405")
	backupFilename := fmt.Sprintf("%s_%s%s", sessionID, timestamp, BackupExtension)
	backupPath := filepath.Join(sbm.backupDir, backupFilename)

	// Copy session file to backup
	data, err := os.ReadFile(sessionPath)
	if err != nil {
		return fmt.Errorf("failed to read session file: %w", err)
	}

	if err := os.WriteFile(backupPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write backup file: %w", err)
	}

	return nil
}

// RestoreSession restores a session from backup
func (sbm *SessionBackupManager) RestoreSession(sessionID string) (*session.Session, error) {
	// Find the latest backup for the session
	backups, err := sbm.ListBackups(sessionID)
	if err != nil {
		return nil, err
	}

	if len(backups) == 0 {
		return nil, fmt.Errorf("no backups found for session: %s", sessionID)
	}

	// Get the most recent backup (backups are sorted by time)
	latestBackup := backups[len(backups)-1]

	// Read backup file
	data, err := os.ReadFile(latestBackup.BackupPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read backup file: %w", err)
	}

	// Parse session
	var legacySession session.LegacySessionState
	if err := json.Unmarshal(data, &legacySession); err != nil {
		return nil, fmt.Errorf("failed to unmarshal backup: %w", err)
	}

	return session.FromLegacySessionState(&legacySession), nil
}

// ListBackups returns available backups for a session
func (sbm *SessionBackupManager) ListBackups(sessionID string) ([]outbound.BackupInfo, error) {
	if _, err := os.Stat(sbm.backupDir); os.IsNotExist(err) {
		return []outbound.BackupInfo{}, nil
	}

	files, err := os.ReadDir(sbm.backupDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read backup directory: %w", err)
	}

	var backups []outbound.BackupInfo
	prefix := sessionID + "_"

	for _, file := range files {
		if file.IsDir() || !strings.HasPrefix(file.Name(), prefix) || !strings.HasSuffix(file.Name(), BackupExtension) {
			continue
		}

		backupPath := filepath.Join(sbm.backupDir, file.Name())
		info, err := file.Info()
		if err != nil {
			continue
		}

		backups = append(backups, outbound.BackupInfo{
			SessionID:   sessionID,
			BackupTime:  info.ModTime().Unix(),
			BackupPath:  backupPath,
			FileSize:    info.Size(),
		})
	}

	return backups, nil
}

// CleanupOldBackups removes old backup files
func (sbm *SessionBackupManager) CleanupOldBackups(maxAge int64) error {
	if _, err := os.Stat(sbm.backupDir); os.IsNotExist(err) {
		return nil // No backup directory, nothing to clean
	}

	files, err := os.ReadDir(sbm.backupDir)
	if err != nil {
		return fmt.Errorf("failed to read backup directory: %w", err)
	}

	cutoffTime := time.Now().Unix() - maxAge

	for _, file := range files {
		if file.IsDir() || !strings.HasSuffix(file.Name(), BackupExtension) {
			continue
		}

		info, err := file.Info()
		if err != nil {
			continue
		}

		if info.ModTime().Unix() < cutoffTime {
			backupPath := filepath.Join(sbm.backupDir, file.Name())
			if err := os.Remove(backupPath); err != nil {
				// Log error but continue with other files
				continue
			}
		}
	}

	return nil
}