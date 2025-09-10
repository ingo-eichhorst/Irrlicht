package filesystem

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"irrlicht/hook/domain/session"
)

// SessionValidator implements session validation for filesystem storage
type SessionValidator struct {
	sessionIDRegex *regexp.Regexp
	maxPathLength  int
}

// NewSessionValidator creates a new session validator
func NewSessionValidator() *SessionValidator {
	// Session ID should be alphanumeric with hyphens and underscores
	sessionIDRegex := regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)
	
	return &SessionValidator{
		sessionIDRegex: sessionIDRegex,
		maxPathLength:  255, // Maximum path length for most filesystems
	}
}

// ValidateSession validates session data before persistence
func (sv *SessionValidator) ValidateSession(sess *session.Session) error {
	if sess == nil {
		return fmt.Errorf("session cannot be nil")
	}

	// Validate session ID
	if err := sv.ValidateSessionID(sess.ID); err != nil {
		return err
	}

	// Validate state
	if !sess.State.IsValid() {
		return fmt.Errorf("invalid session state: %s", sess.State)
	}

	// Validate compaction state
	if sess.CompactionState != "" && !sess.CompactionState.IsValid() {
		return fmt.Errorf("invalid compaction state: %s", sess.CompactionState)
	}

	// Validate timestamps
	if sess.FirstSeen <= 0 {
		return fmt.Errorf("invalid first seen timestamp: %d", sess.FirstSeen)
	}

	if sess.UpdatedAt <= 0 {
		return fmt.Errorf("invalid updated at timestamp: %d", sess.UpdatedAt)
	}

	if sess.UpdatedAt < sess.FirstSeen {
		return fmt.Errorf("updated at timestamp cannot be before first seen")
	}

	// Validate event count
	if sess.EventCount < 0 {
		return fmt.Errorf("event count cannot be negative: %d", sess.EventCount)
	}

	// Validate paths if present
	if sess.TranscriptPath != "" {
		if err := sv.validatePath(sess.TranscriptPath, "transcript_path"); err != nil {
			return err
		}
	}

	if sess.CWD != "" {
		if err := sv.validatePath(sess.CWD, "cwd"); err != nil {
			return err
		}
	}

	// Validate metrics if present
	if sess.Metrics != nil {
		if err := sv.validateMetrics(sess.Metrics); err != nil {
			return err
		}
	}

	return nil
}

// ValidateSessionID validates a session ID format
func (sv *SessionValidator) ValidateSessionID(sessionID string) error {
	if sessionID == "" {
		return fmt.Errorf("session ID cannot be empty")
	}

	if len(sessionID) > 100 {
		return fmt.Errorf("session ID too long: %d characters (max 100)", len(sessionID))
	}

	if !sv.sessionIDRegex.MatchString(sessionID) {
		return fmt.Errorf("invalid session ID format: %s (must contain only alphanumeric characters, hyphens, and underscores)", sessionID)
	}

	// Prevent path traversal attempts
	if strings.Contains(sessionID, "..") || strings.Contains(sessionID, "/") || strings.Contains(sessionID, "\\") {
		return fmt.Errorf("session ID contains invalid characters")
	}

	return nil
}

// SanitizeSession sanitizes session data
func (sv *SessionValidator) SanitizeSession(sess *session.Session) *session.Session {
	if sess == nil {
		return nil
	}

	// Create a copy to avoid modifying the original
	sanitized := &session.Session{
		ID:                 sv.sanitizeString(sess.ID),
		State:              sess.State,
		CompactionState:    sess.CompactionState,
		Model:              sv.sanitizeString(sess.Model),
		CWD:                sv.sanitizePath(sess.CWD),
		TranscriptPath:     sv.sanitizePath(sess.TranscriptPath),
		GitBranch:          sv.sanitizeString(sess.GitBranch),
		ProjectName:        sv.sanitizeString(sess.ProjectName),
		FirstSeen:          sess.FirstSeen,
		UpdatedAt:          sess.UpdatedAt,
		Confidence:         sv.sanitizeString(sess.Confidence),
		EventCount:         sess.EventCount,
		LastEvent:          sv.sanitizeString(sess.LastEvent),
		LastMatcher:        sv.sanitizeString(sess.LastMatcher),
		Metrics:            sess.Metrics, // Metrics are validated separately
		LastTranscriptSize: sess.LastTranscriptSize,
		WaitingStartTime:   sess.WaitingStartTime,
	}

	return sanitized
}

// validatePath validates file paths
func (sv *SessionValidator) validatePath(path, fieldName string) error {
	if len(path) > sv.maxPathLength {
		return fmt.Errorf("%s path too long: %d characters (max %d)", fieldName, len(path), sv.maxPathLength)
	}

	// Check for null bytes
	if strings.Contains(path, "\x00") {
		return fmt.Errorf("%s contains null bytes", fieldName)
	}

	// Ensure it's an absolute path for security
	if !filepath.IsAbs(path) {
		return fmt.Errorf("%s must be an absolute path: %s", fieldName, path)
	}

	return nil
}

// validateMetrics validates session metrics
func (sv *SessionValidator) validateMetrics(metrics *session.Metrics) error {
	if metrics.ElapsedSeconds < 0 {
		return fmt.Errorf("elapsed seconds cannot be negative: %d", metrics.ElapsedSeconds)
	}

	if metrics.TotalTokens < 0 {
		return fmt.Errorf("total tokens cannot be negative: %d", metrics.TotalTokens)
	}

	if metrics.ContextUtilization < 0 || metrics.ContextUtilization > 100 {
		return fmt.Errorf("context utilization must be between 0 and 100: %f", metrics.ContextUtilization)
	}

	// Validate pressure level
	validPressureLevels := []string{"low", "medium", "high", "critical", "unknown"}
	isValid := false
	for _, level := range validPressureLevels {
		if metrics.PressureLevel == level {
			isValid = true
			break
		}
	}
	if !isValid {
		return fmt.Errorf("invalid pressure level: %s", metrics.PressureLevel)
	}

	return nil
}

// sanitizeString removes potentially dangerous characters from strings
func (sv *SessionValidator) sanitizeString(input string) string {
	// Remove null bytes and control characters
	cleaned := strings.ReplaceAll(input, "\x00", "")
	cleaned = strings.ReplaceAll(cleaned, "\r", "")
	cleaned = strings.ReplaceAll(cleaned, "\n", " ")

	// Trim whitespace
	cleaned = strings.TrimSpace(cleaned)

	// Limit length
	if len(cleaned) > 1000 {
		cleaned = cleaned[:1000]
	}

	return cleaned
}

// sanitizePath sanitizes file paths
func (sv *SessionValidator) sanitizePath(path string) string {
	if path == "" {
		return path
	}

	// Clean the path using filepath.Clean to resolve . and .. elements
	cleaned := filepath.Clean(path)

	// Remove any null bytes
	cleaned = strings.ReplaceAll(cleaned, "\x00", "")

	// Limit length
	if len(cleaned) > sv.maxPathLength {
		cleaned = cleaned[:sv.maxPathLength]
	}

	return cleaned
}

// IsValidSessionFileName checks if a filename is valid for session storage
func (sv *SessionValidator) IsValidSessionFileName(filename string) bool {
	// Should end with .json
	if !strings.HasSuffix(filename, ".json") {
		return false
	}

	// Extract session ID
	sessionID := strings.TrimSuffix(filename, ".json")
	
	// Validate session ID
	return sv.ValidateSessionID(sessionID) == nil
}

// GetSafeFilename returns a safe filename for a session ID
func (sv *SessionValidator) GetSafeFilename(sessionID string) string {
	// Replace any potentially dangerous characters
	safe := sv.sessionIDRegex.ReplaceAllString(sessionID, "_")
	
	// Ensure it's not empty and not too long
	if safe == "" {
		safe = "unknown"
	}
	
	if len(safe) > 100 {
		safe = safe[:100]
	}
	
	return safe + ".json"
}