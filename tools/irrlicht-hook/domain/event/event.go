package event

import (
	"encoding/json"
	"time"
)

// HookEvent represents a Claude Code hook event
type HookEvent struct {
	HookEventName   string                 `json:"hook_event_name"`
	SessionID       string                 `json:"session_id"`
	Timestamp       string                 `json:"timestamp"`
	Matcher         string                 `json:"matcher,omitempty"`
	Reason          string                 `json:"reason,omitempty"`
	Data            map[string]interface{} `json:"data"`
	// Direct fields that Claude Code sends at top level
	TranscriptPath  string                 `json:"transcript_path,omitempty"`
	CWD             string                 `json:"cwd,omitempty"`
	Model           string                 `json:"model,omitempty"`
	PermissionMode  string                 `json:"permission_mode,omitempty"`
	Prompt          string                 `json:"prompt,omitempty"`
	Source          string                 `json:"source,omitempty"`
}

// EventType represents the different types of hook events
type EventType string

const (
	SessionStart     EventType = "SessionStart"
	SessionEnd       EventType = "SessionEnd"
	UserPromptSubmit EventType = "UserPromptSubmit"
	Notification     EventType = "Notification"
	PreToolUse       EventType = "PreToolUse"
	PostToolUse      EventType = "PostToolUse"
	PreCompact       EventType = "PreCompact"
	PostCompact      EventType = "PostCompact"
	Stop             EventType = "Stop"
	SubagentStop     EventType = "SubagentStop"
)

// NewHookEvent creates a new HookEvent with the provided details
func NewHookEvent(eventName, sessionID string) *HookEvent {
	return &HookEvent{
		HookEventName: eventName,
		SessionID:     sessionID,
		Timestamp:     time.Now().Format(time.RFC3339),
		Data:          make(map[string]interface{}),
	}
}

// GetEventType returns the EventType enum for the event name
func (e *HookEvent) GetEventType() EventType {
	return EventType(e.HookEventName)
}

// IsValidEventType checks if the event type is valid
func (e *HookEvent) IsValidEventType() bool {
	switch e.GetEventType() {
	case SessionStart, SessionEnd, UserPromptSubmit, Notification,
		 PreToolUse, PostToolUse, PreCompact, PostCompact, Stop, SubagentStop:
		return true
	default:
		return false
	}
}

// RequiresSessionFile determines if this event type should create/modify session files
func (e *HookEvent) RequiresSessionFile() bool {
	return e.GetEventType() != SessionEnd
}

// IsSessionTerminating returns true if this event terminates a session
func (e *HookEvent) IsSessionTerminating() bool {
	return e.GetEventType() == SessionEnd
}

// HasTranscriptPath checks if the event has a transcript path (either direct or in data)
func (e *HookEvent) HasTranscriptPath() bool {
	if e.TranscriptPath != "" {
		return true
	}
	if e.Data != nil {
		if path, exists := e.Data["transcript_path"].(string); exists && path != "" {
			return true
		}
	}
	return false
}

// GetTranscriptPath extracts the transcript path from the event
func (e *HookEvent) GetTranscriptPath() string {
	if e.TranscriptPath != "" {
		return e.TranscriptPath
	}
	if e.Data != nil {
		if path, exists := e.Data["transcript_path"].(string); exists {
			return path
		}
	}
	return ""
}

// GetCWD extracts the current working directory from the event
func (e *HookEvent) GetCWD() string {
	if e.CWD != "" {
		return e.CWD
	}
	if e.Data != nil {
		if cwd, exists := e.Data["cwd"].(string); exists {
			return cwd
		}
	}
	return ""
}

// GetModel extracts the model information from the event
func (e *HookEvent) GetModel() string {
	if e.Model != "" {
		return e.Model
	}
	if e.Data != nil {
		if model, exists := e.Data["model"].(string); exists {
			return model
		}
	}
	return ""
}

// GetProjectName extracts the project name from the event data
func (e *HookEvent) GetProjectName() string {
	if e.Data != nil {
		if projectName, exists := e.Data["project_name"].(string); exists {
			return projectName
		}
	}
	return ""
}

// ToJSON converts the event to JSON string
func (e *HookEvent) ToJSON() (string, error) {
	bytes, err := json.Marshal(e)
	if err != nil {
		return "", err
	}
	return string(bytes), nil
}

// FromJSON creates a HookEvent from JSON string
func FromJSON(jsonStr string) (*HookEvent, error) {
	var event HookEvent
	err := json.Unmarshal([]byte(jsonStr), &event)
	if err != nil {
		return nil, err
	}
	return &event, nil
}

// FromRawMap creates a HookEvent from a raw map (from JSON unmarshaling)
func FromRawMap(rawEvent map[string]interface{}) *HookEvent {
	event := &HookEvent{
		Data: make(map[string]interface{}),
	}

	// Extract string fields
	if hookEventName, ok := rawEvent["hook_event_name"].(string); ok {
		event.HookEventName = hookEventName
	}
	if sessionID, ok := rawEvent["session_id"].(string); ok {
		event.SessionID = sessionID
	}
	if timestamp, ok := rawEvent["timestamp"].(string); ok {
		event.Timestamp = timestamp
	}
	if matcher, ok := rawEvent["matcher"].(string); ok {
		event.Matcher = matcher
	}
	if reason, ok := rawEvent["reason"].(string); ok {
		event.Reason = reason
	}
	if transcriptPath, ok := rawEvent["transcript_path"].(string); ok {
		event.TranscriptPath = transcriptPath
	}
	if cwd, ok := rawEvent["cwd"].(string); ok {
		event.CWD = cwd
	}
	if model, ok := rawEvent["model"].(string); ok {
		event.Model = model
	}
	if permissionMode, ok := rawEvent["permission_mode"].(string); ok {
		event.PermissionMode = permissionMode
	}
	if prompt, ok := rawEvent["prompt"].(string); ok {
		event.Prompt = prompt
	}
	if source, ok := rawEvent["source"].(string); ok {
		event.Source = source
	}

	// Extract data field
	if data, ok := rawEvent["data"].(map[string]interface{}); ok {
		event.Data = data
	}

	return event
}

// Clone creates a deep copy of the event
func (e *HookEvent) Clone() *HookEvent {
	// Create new data map
	newData := make(map[string]interface{})
	for k, v := range e.Data {
		newData[k] = v
	}
	
	return &HookEvent{
		HookEventName:  e.HookEventName,
		SessionID:      e.SessionID,
		Timestamp:      e.Timestamp,
		Matcher:        e.Matcher,
		Reason:         e.Reason,
		Data:           newData,
		TranscriptPath: e.TranscriptPath,
		CWD:            e.CWD,
		Model:          e.Model,
		PermissionMode: e.PermissionMode,
		Prompt:         e.Prompt,
		Source:         e.Source,
	}
}

// EventProcessor defines the interface for processing events
type EventProcessor interface {
	ProcessEvent(event *HookEvent) error
}

// EventLogger defines the interface for logging events
type EventLogger interface {
	LogEvent(event *HookEvent, result string, processingTime time.Duration)
}

// EventMetrics holds metrics for event processing
type EventMetrics struct {
	EventsProcessed int64
	TotalLatencyMs  int64
	LastEventTime   time.Time
}

// NewEventMetrics creates a new EventMetrics instance
func NewEventMetrics() *EventMetrics {
	return &EventMetrics{
		LastEventTime: time.Now(),
	}
}

// RecordEvent records metrics for a processed event
func (m *EventMetrics) RecordEvent(processingTime time.Duration) {
	m.EventsProcessed++
	m.TotalLatencyMs += processingTime.Milliseconds()
	m.LastEventTime = time.Now()
}

// GetAverageLatencyMs returns the average processing latency in milliseconds
func (m *EventMetrics) GetAverageLatencyMs() float64 {
	if m.EventsProcessed == 0 {
		return 0
	}
	return float64(m.TotalLatencyMs) / float64(m.EventsProcessed)
}