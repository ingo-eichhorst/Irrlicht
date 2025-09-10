package filesystem

import (
	"irrlicht/hook/domain/session"
	"irrlicht/hook/ports/outbound"
)

// NewSessionRepository creates a new session repository adapter that implements the port interface
func NewSessionRepository(configService outbound.ConfigurationService, logger outbound.Logger) outbound.SessionRepository {
	instancesDir := configService.GetInstancesDir()
	return NewFilesystemSessionRepositoryWithPath(instancesDir)
}

// Ensure FilesystemSessionRepository implements SessionRepository interface
var _ outbound.SessionRepository = (*FilesystemSessionRepository)(nil)

// GetSession retrieves a session by ID (adapter method for the port interface)
func (fsr *FilesystemSessionRepository) GetSession(sessionID string) (*session.Session, error) {
	return fsr.LoadSession(sessionID)
}

// SaveSession persists a session (already implemented)
// DeleteSession removes a session (already implemented)