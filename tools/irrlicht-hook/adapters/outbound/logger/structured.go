package logger

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"irrlicht/hook/ports/outbound"
)

// StructuredLogger implements the Logger interface with JSON output
type StructuredLogger struct {
	output *os.File
}

// NewStructuredLogger creates a new structured logger that outputs to stderr
func NewStructuredLogger() *StructuredLogger {
	return &StructuredLogger{
		output: os.Stderr,
	}
}

// LogEvent logs an event with structured JSON output
func (l *StructuredLogger) LogEvent(level outbound.LogLevel, message string, fields map[string]interface{}) {
	logEntry := map[string]interface{}{
		"timestamp": time.Now().Format(time.RFC3339),
		"level":     level.String(),
		"message":   message,
	}
	
	// Add additional fields
	for key, value := range fields {
		logEntry[key] = value
	}
	
	// Marshal to JSON and write
	if jsonData, err := json.Marshal(logEntry); err == nil {
		fmt.Fprintln(l.output, string(jsonData))
	} else {
		// Fallback to plain text if JSON marshaling fails
		fmt.Fprintf(l.output, "[%s] %s: %s\n", time.Now().Format(time.RFC3339), level, message)
	}
}

// Info logs an info level message
func (l *StructuredLogger) Info(message string, fields map[string]interface{}) {
	l.LogEvent(outbound.LogLevelInfo, message, fields)
}

// Warn logs a warning level message
func (l *StructuredLogger) Warn(message string, fields map[string]interface{}) {
	l.LogEvent(outbound.LogLevelWarning, message, fields)
}

// Error logs an error level message
func (l *StructuredLogger) Error(message string, fields map[string]interface{}) {
	l.LogEvent(outbound.LogLevelError, message, fields)
}

// Debug logs a debug level message
func (l *StructuredLogger) Debug(message string, fields map[string]interface{}) {
	l.LogEvent(outbound.LogLevelDebug, message, fields)
}

// SetOutput sets the output destination for the logger
func (l *StructuredLogger) SetOutput(output *os.File) {
	l.output = output
}

// Close closes the logger (no-op for stderr output)
func (l *StructuredLogger) Close() error {
	// Don't close stderr
	return nil
}