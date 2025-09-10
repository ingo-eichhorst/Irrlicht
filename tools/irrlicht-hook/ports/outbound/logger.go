package outbound

import (
	"time"
)

// Logger defines the outbound port for structured logging
type Logger interface {
	// LogInfo logs an informational message
	LogInfo(eventType, sessionID, message string)
	
	// LogError logs an error message
	LogError(eventType, sessionID, errorMsg string)
	
	// LogProcessingTime logs event processing time and metrics
	LogProcessingTime(eventType, sessionID string, processingTimeMs int64, payloadSize int, result string)
	
	// LogDebug logs a debug message (only in debug mode)
	LogDebug(eventType, sessionID, message string)
	
	// LogWarning logs a warning message
	LogWarning(eventType, sessionID, message string)
	
	// Close closes the logger and flushes any pending writes
	Close() error
}

// StructuredLogger defines an interface for structured logging with fields
type StructuredLogger interface {
	Logger
	
	// LogWithFields logs a message with additional structured fields
	LogWithFields(level LogLevel, fields map[string]interface{}, message string)
	
	// SetLevel sets the minimum log level
	SetLevel(level LogLevel)
	
	// GetLevel returns the current log level
	GetLevel() LogLevel
}

// LogLevel represents the severity level of a log message
type LogLevel int

const (
	LogLevelDebug LogLevel = iota
	LogLevelInfo
	LogLevelWarning
	LogLevelError
)

func (l LogLevel) String() string {
	switch l {
	case LogLevelDebug:
		return "debug"
	case LogLevelInfo:
		return "info"
	case LogLevelWarning:
		return "warning"
	case LogLevelError:
		return "error"
	default:
		return "unknown"
	}
}

// LogEntry represents a structured log entry
type LogEntry struct {
	Timestamp       string                 `json:"timestamp"`
	Level           string                 `json:"level"`
	EventType       string                 `json:"event_type,omitempty"`
	SessionID       string                 `json:"session_id,omitempty"`
	ProcessingTimeMs int64                 `json:"processing_time_ms,omitempty"`
	PayloadSizeBytes int                   `json:"payload_size_bytes,omitempty"`
	Result          string                 `json:"result"`
	Message         string                 `json:"message,omitempty"`
	Fields          map[string]interface{} `json:"fields,omitempty"`
}

// NewLogEntry creates a new log entry with timestamp
func NewLogEntry(level LogLevel, message string) *LogEntry {
	return &LogEntry{
		Timestamp: time.Now().Format(time.RFC3339),
		Level:     level.String(),
		Message:   message,
		Result:    "success",
	}
}

// LogWriter defines the interface for writing log entries
type LogWriter interface {
	// WriteEntry writes a log entry
	WriteEntry(entry *LogEntry) error
	
	// Flush flushes any pending writes
	Flush() error
	
	// Close closes the writer
	Close() error
}

// LogRotator defines the interface for log rotation
type LogRotator interface {
	// ShouldRotate checks if logs should be rotated
	ShouldRotate() bool
	
	// Rotate performs log rotation
	Rotate() error
	
	// GetCurrentLogFile returns the current log file path
	GetCurrentLogFile() string
	
	// CleanupOldLogs removes old log files
	CleanupOldLogs() error
}

// LogFormatter defines the interface for formatting log entries
type LogFormatter interface {
	// Format formats a log entry for output
	Format(entry *LogEntry) ([]byte, error)
}

// LogFilter defines the interface for filtering log entries
type LogFilter interface {
	// ShouldLog determines if a log entry should be written
	ShouldLog(entry *LogEntry) bool
}

// LogConfig holds configuration for logging
type LogConfig struct {
	Level          LogLevel
	OutputPath     string
	MaxFileSize    int64
	MaxBackups     int
	EnableRotation bool
	EnableColor    bool
	Format         string // "json" or "text"
}

// DefaultLogConfig returns a default logging configuration
func DefaultLogConfig() *LogConfig {
	return &LogConfig{
		Level:          LogLevelInfo,
		OutputPath:     "",
		MaxFileSize:    10 * 1024 * 1024, // 10MB
		MaxBackups:     5,
		EnableRotation: true,
		EnableColor:    false,
		Format:         "json",
	}
}