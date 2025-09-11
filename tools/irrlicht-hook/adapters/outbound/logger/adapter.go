package logger

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"irrlicht/hook/ports/outbound"
)

const (
	MaxLogSize  = 10 * 1024 * 1024 // 10MB
	MaxLogFiles = 5
	AppSupportDir = "Library/Application Support/Irrlicht"
)

// StructuredLoggerAdapter implements the Logger port interface
type StructuredLoggerAdapter struct {
	logFile     *os.File
	logPath     string
	currentSize int64
	mu          sync.Mutex
}

// NewStructuredLoggerAdapter creates a new logger adapter with rotation
func NewStructuredLoggerAdapter() (*StructuredLoggerAdapter, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("failed to get home directory: %w", err)
	}

	logsDir := filepath.Join(homeDir, AppSupportDir, "logs")
	if err := os.MkdirAll(logsDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create logs directory: %w", err)
	}

	logPath := filepath.Join(logsDir, "events.log")

	// Open or create log file
	file, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, fmt.Errorf("failed to open log file: %w", err)
	}

	// Get current file size
	stat, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, fmt.Errorf("failed to stat log file: %w", err)
	}

	sl := &StructuredLoggerAdapter{
		logFile:     file,
		logPath:     logPath,
		currentSize: stat.Size(),
	}

	// Check if rotation is needed
	if sl.currentSize > MaxLogSize {
		if err := sl.rotate(); err != nil {
			file.Close()
			return nil, fmt.Errorf("failed to rotate log: %w", err)
		}
	}

	return sl, nil
}

// LogInfo logs an informational message
func (sl *StructuredLoggerAdapter) LogInfo(eventType, sessionID, message string) {
	entry := outbound.LogEntry{
		Timestamp: time.Now().Format(time.RFC3339Nano),
		Level:     "info",
		EventType: eventType,
		SessionID: sessionID,
		Result:    "success",
		Message:   message,
	}
	sl.writeEntry(entry)
}

// LogError logs an error message
func (sl *StructuredLoggerAdapter) LogError(eventType, sessionID, errorMsg string) {
	entry := outbound.LogEntry{
		Timestamp: time.Now().Format(time.RFC3339Nano),
		Level:     "error",
		EventType: eventType,
		SessionID: sessionID,
		Result:    "error",
		Message:   errorMsg,
	}
	sl.writeEntry(entry)
}

// LogProcessingTime logs event processing time and metrics
func (sl *StructuredLoggerAdapter) LogProcessingTime(eventType, sessionID string, processingTimeMs int64, payloadSize int, result string) {
	entry := outbound.LogEntry{
		Timestamp:        time.Now().Format(time.RFC3339Nano),
		Level:            "info",
		EventType:        eventType,
		SessionID:        sessionID,
		ProcessingTimeMs: processingTimeMs,
		PayloadSizeBytes: payloadSize,
		Result:           result,
		Message:          "Event processing completed",
	}
	sl.writeEntry(entry)
}

// LogDebug logs a debug message
func (sl *StructuredLoggerAdapter) LogDebug(eventType, sessionID, message string) {
	entry := outbound.LogEntry{
		Timestamp: time.Now().Format(time.RFC3339Nano),
		Level:     "debug",
		EventType: eventType,
		SessionID: sessionID,
		Result:    "success",
		Message:   message,
	}
	sl.writeEntry(entry)
}

// LogWarning logs a warning message
func (sl *StructuredLoggerAdapter) LogWarning(eventType, sessionID, message string) {
	entry := outbound.LogEntry{
		Timestamp: time.Now().Format(time.RFC3339Nano),
		Level:     "warning",
		EventType: eventType,
		SessionID: sessionID,
		Result:    "success",
		Message:   message,
	}
	sl.writeEntry(entry)
}

// Close closes the logger and flushes any pending writes
func (sl *StructuredLoggerAdapter) Close() error {
	sl.mu.Lock()
	defer sl.mu.Unlock()

	if sl.logFile != nil {
		return sl.logFile.Close()
	}
	return nil
}

// writeEntry writes a log entry to the file with rotation check
func (sl *StructuredLoggerAdapter) writeEntry(entry outbound.LogEntry) {
	sl.mu.Lock()
	defer sl.mu.Unlock()

	// Serialize entry to JSON
	jsonData, err := json.Marshal(entry)
	if err != nil {
		log.Printf("Failed to marshal log entry: %v", err)
		return
	}

	// Add newline
	jsonData = append(jsonData, '\n')

	// Check if rotation is needed before writing
	if sl.currentSize+int64(len(jsonData)) > MaxLogSize {
		if err := sl.rotate(); err != nil {
			log.Printf("Failed to rotate log: %v", err)
			return
		}
	}

	// Write to current log file
	n, err := sl.logFile.Write(jsonData)
	if err != nil {
		log.Printf("Failed to write log entry: %v", err)
		return
	}

	sl.currentSize += int64(n)
}

// rotate rotates the log files
func (sl *StructuredLoggerAdapter) rotate() error {
	// Close current file
	if sl.logFile != nil {
		sl.logFile.Close()
	}

	// Rotate existing files
	for i := MaxLogFiles - 1; i >= 1; i-- {
		oldPath := fmt.Sprintf("%s.%d", sl.logPath, i)
		newPath := fmt.Sprintf("%s.%d", sl.logPath, i+1)

		if _, err := os.Stat(oldPath); err == nil {
			if i == MaxLogFiles-1 {
				// Remove the oldest file
				if err := os.Remove(newPath); err != nil && !os.IsNotExist(err) {
					// Log error but continue rotation
					log.Printf("Failed to remove old log file %s: %v", newPath, err)
				}
			}
			if err := os.Rename(oldPath, newPath); err != nil {
				log.Printf("Failed to rotate log file %s to %s: %v", oldPath, newPath, err)
			}
		}
	}

	// Move current log to .1
	if _, err := os.Stat(sl.logPath); err == nil {
		if err := os.Rename(sl.logPath, sl.logPath+".1"); err != nil {
			log.Printf("Failed to rename current log file: %v", err)
		}
	}

	// Create new log file
	file, err := os.OpenFile(sl.logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("failed to create new log file: %w", err)
	}

	sl.logFile = file
	sl.currentSize = 0

	return nil
}