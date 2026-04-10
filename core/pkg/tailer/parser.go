// Package tailer provides transcript tailing and metrics computation.
// Format-specific parsing is delegated to TranscriptParser implementations
// that live in each agent adapter package.
package tailer

import (
	"regexp"
	"strings"
	"time"
)

// ParsedEvent is the normalized output from a format-specific transcript parser.
// Each parser maps its native event structure into these fields.
type ParsedEvent struct {
	EventType string    // normalized: "assistant_message", "user_message", "turn_done", etc.
	Timestamp time.Time // event timestamp
	Skip      bool      // true → ignore this line entirely

	// Tool tracking — parser reports deltas, tailer accumulates.
	ToolUseNames    []string // tool names from tool_use/toolCall blocks in this event
	ToolResultCount int      // number of tool_result blocks in this event
	IsError         bool     // true if the tool result had is_error=true
	ClearToolNames  bool     // true → reset lastOpenToolNames (on user messages)

	// IsUserInterrupt is true only for real user ESC cancellations — the
	// exact "[Request interrupted by user]" text marker on a user event,
	// without the "for tool use" suffix. Kept distinct from IsError so the
	// classifier can tell an ESC apart from a normal tool failure (grep
	// with no matches, a failing build, etc.). See issue #102 Bug B.
	IsUserInterrupt bool

	// IsToolDenial is true when the user denied a permission prompt for a
	// tool call ("[Request interrupted by user for tool use]" text marker).
	// This is a *different* signal from IsUserInterrupt: a tool denial does
	// not end the agent's turn — the agent typically continues with a
	// different approach — so it must NOT feed the cancellation rule.
	// Tracked separately for observability and to suppress the spurious
	// working→ready→working flicker that happened when the parser lumped
	// both markers under IsUserInterrupt.
	IsToolDenial bool

	// Metadata extracted by the parser.
	ModelName        string
	ContextWindow    int64
	Tokens           *TokenSnapshot // nil = no token data in this event
	CumulativeTokens *TokenSnapshot // cumulative totals (set by Codex); tailer uses directly for cost
	RequestID        string         // unique API call ID — deduplicates streaming events for cost accumulation
	AssistantText    string         // ≤200 chars, for waiting-state display
	ContentChars     int64          // character count for token estimation
	CWD              string         // working directory if found
	PermissionMode   string         // Claude Code only
}

// TokenSnapshot holds a token breakdown from a single event.
type TokenSnapshot struct {
	Input         int64
	Output        int64
	CacheRead     int64
	CacheCreation int64
	Total         int64
}

// TranscriptParser parses a single JSONL line from a specific transcript format
// and returns a normalized ParsedEvent. Implementations live in each agent
// adapter package (claudecode, codex, pi).
type TranscriptParser interface {
	// ParseLine parses a raw JSON map and returns a normalized event.
	// Returns nil for lines that should be silently ignored (no event emitted).
	ParseLine(raw map[string]interface{}) *ParsedEvent
}

// --- Shared helpers used by multiple parsers ---

// IsUserEventType reports whether a ParsedEvent.EventType represents a user
// turn across any of the supported transcript formats.
func IsUserEventType(eventType string) bool {
	switch eventType {
	case "user", "user_message", "user_input":
		return true
	}
	return false
}

// NormalizeModelName normalizes model names by removing date suffixes, extended
// context markers, and handling aliases. Exported for use by adapter parsers.
func NormalizeModelName(rawModel string) string {
	if rawModel == "" {
		return ""
	}

	// Strip extended context suffix (e.g. "claude-opus-4-6[1m]")
	rawModel = strings.TrimSuffix(rawModel, "[1m]")

	// Handle common aliases first
	aliases := map[string]string{
		"opusplan": "claude-opus-4-1",
		"sonnet":   "claude-sonnet-4-6",
		"haiku":    "claude-haiku-4-5",
	}
	if normalized, exists := aliases[rawModel]; exists {
		return normalized
	}

	// Remove date suffixes (e.g., "claude-opus-4-6-20250715" -> "claude-opus-4-6")
	datePattern := regexp.MustCompile(`-\d{8}$`)
	normalized := datePattern.ReplaceAllString(rawModel, "")

	// Match most-specific patterns first (longer model IDs before shorter)
	switch {
	case strings.Contains(normalized, "claude-opus-4-6"):
		return "claude-opus-4-6"
	case strings.Contains(normalized, "claude-sonnet-4-6"):
		return "claude-sonnet-4-6"
	case strings.Contains(normalized, "claude-sonnet-4-5"):
		return "claude-sonnet-4-5"
	case strings.Contains(normalized, "claude-haiku-4-5"):
		return "claude-haiku-4-5"
	case strings.Contains(normalized, "claude-opus-4-1"):
		return "claude-opus-4-1"
	case strings.Contains(normalized, "claude-sonnet-4"):
		return "claude-4-sonnet"
	case strings.Contains(normalized, "claude-3.5-sonnet"):
		return "claude-3.5-sonnet"
	case strings.Contains(normalized, "claude-3.5-haiku"):
		return "claude-3.5-haiku"
	}
	return normalized
}

// ExtractAssistantText extracts and concatenates text blocks from an assistant
// message, returning at most 200 characters. Checks both Claude Code
// (message.content[].text) and Codex (content[].text / content[].output_text) formats.
func ExtractAssistantText(raw map[string]interface{}) string {
	var parts []string

	collectText := func(arr []interface{}) {
		for _, item := range arr {
			if block, ok := item.(map[string]interface{}); ok {
				bt := block["type"]
				if bt == "text" || bt == "output_text" {
					if text, ok := block["text"].(string); ok && text != "" {
						parts = append(parts, text)
					}
				}
			}
		}
	}

	// Claude Code: message.content[]
	if msg, ok := raw["message"].(map[string]interface{}); ok {
		if arr, ok := msg["content"].([]interface{}); ok {
			collectText(arr)
		}
	}
	// Codex: top-level content[]
	if arr, ok := raw["content"].([]interface{}); ok {
		collectText(arr)
	}

	var text string
	switch len(parts) {
	case 0:
		return ""
	case 1:
		text = strings.TrimSpace(parts[0])
	default:
		text = strings.TrimSpace(strings.Join(parts, " "))
	}

	runes := []rune(text)
	if len(runes) > 200 {
		return string(runes[:200])
	}
	return text
}

// ExtractContentChars returns the total character count of text content in
// a transcript event, checking common content locations across formats.
func ExtractContentChars(raw map[string]interface{}) int64 {
	var chars int64
	addContentChars := func(arr []interface{}) {
		for _, item := range arr {
			if block, ok := item.(map[string]interface{}); ok {
				if text, ok := block["text"].(string); ok {
					chars += int64(len(text))
				}
			}
		}
	}
	// Top-level content array (Codex newer format)
	if arr, ok := raw["content"].([]interface{}); ok {
		addContentChars(arr)
	}
	// Nested message.content array (Claude Code format)
	if msg, ok := raw["message"].(map[string]interface{}); ok {
		if arr, ok := msg["content"].([]interface{}); ok {
			addContentChars(arr)
		}
	}
	// Codex function_call arguments
	if args, ok := raw["arguments"].(string); ok {
		chars += int64(len(args))
	}
	// Codex function_call_output
	if output, ok := raw["output"].(string); ok {
		chars += int64(len(output))
	}
	return chars
}

// ExtractUsage pulls token breakdown fields from a usage map.
// Handles both standard (Claude/Codex) and Pi field naming conventions.
func ExtractUsage(usage map[string]interface{}) *TokenSnapshot {
	snap := &TokenSnapshot{}
	hasBreakdown := false

	if v, ok := usage["input_tokens"].(float64); ok {
		snap.Input = int64(v)
		hasBreakdown = true
	}
	if v, ok := usage["output_tokens"].(float64); ok {
		snap.Output = int64(v)
		hasBreakdown = true
	}
	// Pi uses shorter field names as fallback.
	if !hasBreakdown {
		if v, ok := usage["input"].(float64); ok {
			snap.Input = int64(v)
			hasBreakdown = true
		}
		if v, ok := usage["output"].(float64); ok {
			snap.Output = int64(v)
			hasBreakdown = true
		}
	}
	// Standard cache field names.
	if v, ok := usage["cache_read_input_tokens"].(float64); ok {
		snap.CacheRead = int64(v)
	}
	if v, ok := usage["cache_creation_input_tokens"].(float64); ok {
		snap.CacheCreation = int64(v)
	}
	// Pi cache field names.
	if v, ok := usage["cacheRead"].(float64); ok {
		snap.CacheRead = int64(v)
	}
	if v, ok := usage["cacheWrite"].(float64); ok {
		snap.CacheCreation = int64(v)
	}
	snap.Total = snap.Input + snap.Output + snap.CacheRead + snap.CacheCreation
	// total_tokens override
	if total, ok := usage["total_tokens"].(float64); ok {
		snap.Total = int64(total)
	}
	// Pi totalTokens field.
	if total, ok := usage["totalTokens"].(float64); ok {
		snap.Total = int64(total)
	}

	if !hasBreakdown && snap.Total == 0 {
		return nil
	}
	return snap
}

// ParseTimestamp extracts a timestamp from a raw JSON map, trying RFC3339
// and millisecond-precision formats, then numeric Unix timestamps.
func ParseTimestamp(raw map[string]interface{}) time.Time {
	if ts, ok := raw["timestamp"]; ok {
		if tsStr, ok := ts.(string); ok {
			if parsed, err := time.Parse(time.RFC3339, tsStr); err == nil {
				return parsed
			}
			if parsed, err := time.Parse("2006-01-02T15:04:05.000Z", tsStr); err == nil {
				return parsed
			}
		} else if tsNum, ok := ts.(float64); ok && tsNum > 0 {
			return time.Unix(int64(tsNum), 0)
		}
	}
	return time.Now()
}
