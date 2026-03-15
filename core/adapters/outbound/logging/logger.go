package logging

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	appSupportDir = "Library/Application Support/Irrlicht"
	maxLogSize    = 10 * 1024 * 1024 // 10MB
	maxLogFiles   = 5
)

// logEntry is the JSON structure written to the log file.
type logEntry struct {
	Timestamp        string `json:"timestamp"`
	Level            string `json:"level"`
	EventType        string `json:"event_type,omitempty"`
	SessionID        string `json:"session_id,omitempty"`
	ProcessingTimeMs int64  `json:"processing_time_ms,omitempty"`
	PayloadSizeBytes int    `json:"payload_size_bytes,omitempty"`
	Result           string `json:"result"`
	Message          string `json:"message,omitempty"`
	Error            string `json:"error,omitempty"`
}

// StructuredLogger implements ports/outbound.Logger using a rotating JSON log file.
type StructuredLogger struct {
	logFile     *os.File
	logPath     string
	currentSize int64
	mu          sync.Mutex
}

// New creates a StructuredLogger writing to the default Irrlicht log directory.
func New() (*StructuredLogger, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("failed to get home directory: %w", err)
	}
	logsDir := filepath.Join(homeDir, appSupportDir, "logs")
	if err := os.MkdirAll(logsDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create logs directory: %w", err)
	}
	return newWithPath(filepath.Join(logsDir, "events.log"))
}

// NewWithPath creates a StructuredLogger writing to a specific path (useful for tests).
func NewWithPath(path string) (*StructuredLogger, error) {
	return newWithPath(path)
}

func newWithPath(logPath string) (*StructuredLogger, error) {
	file, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, fmt.Errorf("failed to open log file: %w", err)
	}
	stat, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, fmt.Errorf("failed to stat log file: %w", err)
	}
	sl := &StructuredLogger{
		logFile:     file,
		logPath:     logPath,
		currentSize: stat.Size(),
	}
	if sl.currentSize > maxLogSize {
		if err := sl.rotate(); err != nil {
			file.Close()
			return nil, fmt.Errorf("failed to rotate log: %w", err)
		}
	}
	return sl, nil
}

// Close closes the underlying log file.
func (sl *StructuredLogger) Close() error {
	sl.mu.Lock()
	defer sl.mu.Unlock()
	if sl.logFile != nil {
		return sl.logFile.Close()
	}
	return nil
}

// LogInfo logs an info-level entry.
func (sl *StructuredLogger) LogInfo(eventType, sessionID, message string) {
	sl.writeEntry(logEntry{
		Timestamp: time.Now().Format(time.RFC3339Nano),
		Level:     "info",
		EventType: eventType,
		SessionID: sessionID,
		Result:    "success",
		Message:   message,
	})
}

// LogError logs an error-level entry.
func (sl *StructuredLogger) LogError(eventType, sessionID, errorMsg string) {
	sl.writeEntry(logEntry{
		Timestamp: time.Now().Format(time.RFC3339Nano),
		Level:     "error",
		EventType: eventType,
		SessionID: sessionID,
		Result:    "error",
		Error:     errorMsg,
	})
}

// LogProcessingTime logs processing performance metrics.
func (sl *StructuredLogger) LogProcessingTime(eventType, sessionID string, processingTimeMs int64, payloadSize int, result string) {
	sl.writeEntry(logEntry{
		Timestamp:        time.Now().Format(time.RFC3339Nano),
		Level:            "info",
		EventType:        eventType,
		SessionID:        sessionID,
		ProcessingTimeMs: processingTimeMs,
		PayloadSizeBytes: payloadSize,
		Result:           result,
		Message:          "Event processing completed",
	})
}

func (sl *StructuredLogger) writeEntry(entry logEntry) {
	sl.mu.Lock()
	defer sl.mu.Unlock()

	jsonData, err := json.Marshal(entry)
	if err != nil {
		log.Printf("Failed to marshal log entry: %v", err)
		return
	}
	jsonData = append(jsonData, '\n')

	if sl.currentSize+int64(len(jsonData)) > maxLogSize {
		if err := sl.rotate(); err != nil {
			log.Printf("Failed to rotate log: %v", err)
			return
		}
	}
	n, err := sl.logFile.Write(jsonData)
	if err != nil {
		log.Printf("Failed to write log entry: %v", err)
		return
	}
	sl.currentSize += int64(n)
}

func (sl *StructuredLogger) rotate() error {
	if sl.logFile != nil {
		sl.logFile.Close()
	}
	for i := maxLogFiles - 1; i >= 1; i-- {
		oldPath := fmt.Sprintf("%s.%d", sl.logPath, i)
		newPath := fmt.Sprintf("%s.%d", sl.logPath, i+1)
		if _, err := os.Stat(oldPath); err == nil {
			if i == maxLogFiles-1 {
				os.Remove(newPath)
			}
			os.Rename(oldPath, newPath)
		}
	}
	if _, err := os.Stat(sl.logPath); err == nil {
		os.Rename(sl.logPath, sl.logPath+".1")
	}
	file, err := os.OpenFile(sl.logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("failed to create new log file: %w", err)
	}
	sl.logFile = file
	sl.currentSize = 0
	return nil
}
