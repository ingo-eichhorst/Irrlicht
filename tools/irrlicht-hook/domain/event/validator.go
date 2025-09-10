package event

import (
	"fmt"
	"path/filepath"
	"strings"
)

// ValidationError represents an event validation error
type ValidationError struct {
	Field   string
	Message string
}

func (e ValidationError) Error() string {
	return fmt.Sprintf("validation error on field '%s': %s", e.Field, e.Message)
}

// FileSystemService defines the interface for file system operations needed by the validator
type FileSystemService interface {
	ValidatePath(path string) error
}

// Validator handles event validation logic
type Validator struct {
	maxPayloadSize    int
	fileSystemService FileSystemService
}

// NewValidator creates a new event validator
func NewValidator(fileSystemService FileSystemService) *Validator {
	return &Validator{
		maxPayloadSize:    512 * 1024, // 512KB default
		fileSystemService: fileSystemService,
	}
}

// Validate validates a hook event for security and correctness
func (v *Validator) Validate(event *HookEvent) error {
	// Check required fields
	if err := v.validateRequiredFields(event); err != nil {
		return err
	}

	// Validate event type
	if err := v.validateEventType(event); err != nil {
		return err
	}

	// Validate paths for security (only if file system service is available)
	if v.fileSystemService != nil {
		if err := v.validatePaths(event); err != nil {
			return err
		}
	}

	// Check payload size
	if err := v.validatePayloadSize(event); err != nil {
		return err
	}

	return nil
}

// validateRequiredFields checks that required fields are present
func (v *Validator) validateRequiredFields(event *HookEvent) error {
	if event.HookEventName == "" {
		return ValidationError{
			Field:   "hook_event_name",
			Message: "missing hook_event_name",
		}
	}

	if event.SessionID == "" {
		return ValidationError{
			Field:   "session_id",
			Message: "missing session_id",
		}
	}

	return nil
}

// validateEventType checks if the event type is valid
func (v *Validator) validateEventType(event *HookEvent) error {
	if !event.IsValidEventType() {
		return ValidationError{
			Field:   "hook_event_name",
			Message: fmt.Sprintf("unknown event type: %s", event.HookEventName),
		}
	}
	return nil
}

// validatePaths validates file paths for security (prevent path traversal, etc.)
func (v *Validator) validatePaths(event *HookEvent) error {
	// Check transcript path
	if transcriptPath := event.GetTranscriptPath(); transcriptPath != "" {
		if err := v.validatePath(transcriptPath, "transcript_path"); err != nil {
			return err
		}
	}

	// Check CWD
	if cwd := event.GetCWD(); cwd != "" {
		if err := v.validatePath(cwd, "cwd"); err != nil {
			return err
		}
	}

	return nil
}

// validatePath validates a single path for security
func (v *Validator) validatePath(path, fieldName string) error {
	if err := v.fileSystemService.ValidatePath(path); err != nil {
		return ValidationError{
			Field:   fieldName,
			Message: err.Error(),
		}
	}
	return nil
}

// validatePayloadSize checks if the event payload is within size limits
func (v *Validator) validatePayloadSize(event *HookEvent) error {
	// Convert to JSON to get actual size
	jsonStr, err := event.ToJSON()
	if err != nil {
		return ValidationError{
			Field:   "payload",
			Message: "failed to serialize event to JSON",
		}
	}

	payloadSize := len(jsonStr)
	if payloadSize > v.maxPayloadSize {
		return ValidationError{
			Field: "payload",
			Message: fmt.Sprintf("payload size %d exceeds maximum %d bytes",
				payloadSize, v.maxPayloadSize),
		}
	}

	return nil
}

// SanitizeEvent sanitizes an event by cleaning potentially dangerous data
func (v *Validator) SanitizeEvent(event *HookEvent) *HookEvent {
	sanitized := event.Clone()

	// Sanitize string fields
	sanitized.SessionID = v.sanitizeString(sanitized.SessionID)
	sanitized.HookEventName = v.sanitizeString(sanitized.HookEventName)
	sanitized.Matcher = v.sanitizeString(sanitized.Matcher)
	sanitized.Reason = v.sanitizeString(sanitized.Reason)
	sanitized.Source = v.sanitizeString(sanitized.Source)

	// Sanitize paths
	sanitized.TranscriptPath = v.sanitizePath(sanitized.TranscriptPath)
	sanitized.CWD = v.sanitizePath(sanitized.CWD)

	// Sanitize data map
	if sanitized.Data != nil {
		sanitized.Data = v.sanitizeDataMap(sanitized.Data)
	}

	return sanitized
}

// sanitizeString removes potentially dangerous characters from strings
func (v *Validator) sanitizeString(input string) string {
	// Remove null bytes and control characters
	cleaned := strings.ReplaceAll(input, "\x00", "")
	cleaned = strings.ReplaceAll(cleaned, "\r", "")
	cleaned = strings.ReplaceAll(cleaned, "\n", " ")

	// Trim whitespace
	cleaned = strings.TrimSpace(cleaned)

	return cleaned
}

// sanitizePath cleans file paths
func (v *Validator) sanitizePath(path string) string {
	if path == "" {
		return path
	}

	// Clean the path using filepath.Clean to resolve . and .. elements
	cleaned := filepath.Clean(path)

	// Remove any null bytes
	cleaned = strings.ReplaceAll(cleaned, "\x00", "")

	return cleaned
}

// sanitizeDataMap recursively sanitizes a data map
func (v *Validator) sanitizeDataMap(data map[string]interface{}) map[string]interface{} {
	sanitized := make(map[string]interface{})

	for key, value := range data {
		sanitizedKey := v.sanitizeString(key)

		switch val := value.(type) {
		case string:
			sanitized[sanitizedKey] = v.sanitizeString(val)
		case map[string]interface{}:
			sanitized[sanitizedKey] = v.sanitizeDataMap(val)
		default:
			// For other types (numbers, booleans, etc.), keep as-is
			sanitized[sanitizedKey] = value
		}
	}

	return sanitized
}

// ValidationResult holds the result of event validation
type ValidationResult struct {
	IsValid        bool
	Errors         []ValidationError
	SanitizedEvent *HookEvent
}

// ValidateAndSanitize performs both validation and sanitization in one step
func (v *Validator) ValidateAndSanitize(event *HookEvent) ValidationResult {
	var errors []ValidationError

	// First sanitize
	sanitized := v.SanitizeEvent(event)

	// Then validate the sanitized event
	if err := v.Validate(sanitized); err != nil {
		if validationErr, ok := err.(ValidationError); ok {
			errors = append(errors, validationErr)
		} else {
			errors = append(errors, ValidationError{
				Field:   "general",
				Message: err.Error(),
			})
		}
	}

	return ValidationResult{
		IsValid:        len(errors) == 0,
		Errors:         errors,
		SanitizedEvent: sanitized,
	}
}

// DefaultValidator creates a validator with default settings
// Note: This should only be used in tests or when a FileSystemService is not available
func DefaultValidator() *Validator {
	return &Validator{
		maxPayloadSize:    512 * 1024, // 512KB max payload
		fileSystemService: nil,
	}
}
