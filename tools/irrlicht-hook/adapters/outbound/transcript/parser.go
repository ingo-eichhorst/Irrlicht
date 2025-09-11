package transcript

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Parser handles parsing of transcript lines
type Parser struct {
	modelRegex     *regexp.Regexp
	timestampRegex *regexp.Regexp
}

// NewParser creates a new transcript parser
func NewParser() *Parser {
	return &Parser{
		modelRegex:     regexp.MustCompile(`\*\*Model:\*\*\s*(.+)`),
		timestampRegex: regexp.MustCompile(`\*\*Started:\*\*\s*(.+)`),
	}
}

// ParseTranscriptLine attempts to parse a transcript line into a message event
func (p *Parser) ParseTranscriptLine(line string) (*MessageEvent, error) {
	line = strings.TrimSpace(line)
	if line == "" {
		return nil, nil
	}

	// Try to parse as JSON first (JSONL format)
	if strings.HasPrefix(line, "{") {
		return p.parseJSONLine(line)
	}

	// Try to parse as markdown format
	return p.parseMarkdownLine(line)
}

// parseJSONLine parses a JSON line from the transcript
func (p *Parser) parseJSONLine(line string) (*MessageEvent, error) {
	var data map[string]interface{}
	if err := json.Unmarshal([]byte(line), &data); err != nil {
		return nil, err
	}

	event := &MessageEvent{}
	
	// Extract timestamp
	if timestampStr, ok := data["timestamp"].(string); ok {
		if timestamp, err := time.Parse(time.RFC3339, timestampStr); err == nil {
			event.Timestamp = timestamp
		}
	}
	
	// Extract message type
	if eventType, ok := data["type"].(string); ok {
		event.Type = eventType
	}
	
	// Extract role and model from message
	if message, ok := data["message"].(map[string]interface{}); ok {
		if role, ok := message["role"].(string); ok {
			event.Role = role
		}
	}
	
	// Extract token counts and detailed token info
	tokenInfo := p.extractTokenInfoFromJSON(data)
	event.TokenInfo = tokenInfo
	if tokenInfo != nil {
		event.Tokens = tokenInfo.TotalTokens
	}
	
	// Only return events that have meaningful data or are message events
	if !event.Timestamp.IsZero() || event.Type != "" || event.Tokens > 0 || p.isMessageEvent(event.Type) {
		return event, nil
	}
	
	return nil, nil
}

// isMessageEvent determines if an event type should be counted as a message
func (p *Parser) isMessageEvent(eventType string) bool {
	messageEvents := map[string]bool{
		"user":         true,
		"assistant":    true,
		"tool_use":     true,
		"tool_result":  true,
		"message":      true,
		"system":       true,
	}
	return messageEvents[eventType]
}

// parseMarkdownLine parses a markdown line from the transcript
func (p *Parser) parseMarkdownLine(line string) (*MessageEvent, error) {
	// Check for message headers
	if strings.HasPrefix(line, "### User") {
		return &MessageEvent{
			Timestamp: time.Now(), // Fallback timestamp
			Type:      "message",
			Role:      "user",
		}, nil
	}
	
	if strings.HasPrefix(line, "### Assistant") {
		return &MessageEvent{
			Timestamp: time.Now(), // Fallback timestamp  
			Type:      "message",
			Role:      "assistant",
		}, nil
	}
	
	// Check for model information
	if matches := p.modelRegex.FindStringSubmatch(line); len(matches) > 1 {
		return &MessageEvent{
			Timestamp: time.Now(),
			Type:      "model_info",
		}, nil
	}
	
	// Check for session start time
	if matches := p.timestampRegex.FindStringSubmatch(line); len(matches) > 1 {
		if timestamp, err := time.Parse(time.RFC3339, strings.TrimSpace(matches[1])); err == nil {
			return &MessageEvent{
				Timestamp: timestamp,
				Type:      "session_start",
			}, nil
		}
	}
	
	return nil, nil
}

// extractTokensFromJSON extracts token counts from JSON data
func (p *Parser) extractTokensFromJSON(data map[string]interface{}) int64 {
	var totalTokens int64
	
	// Check usage field at root level (Claude API format)
	if usage, ok := data["usage"].(map[string]interface{}); ok {
		totalTokens += p.extractTokensFromUsage(usage)
	}
	
	// Check message.usage field (Claude Code format for assistant messages)
	if message, ok := data["message"].(map[string]interface{}); ok {
		if usage, ok := message["usage"].(map[string]interface{}); ok {
			totalTokens += p.extractTokensFromUsage(usage)
		}
	}
	
	// Check for token count in response metadata
	if response, ok := data["response"].(map[string]interface{}); ok {
		if usage, ok := response["usage"].(map[string]interface{}); ok {
			totalTokens += p.extractTokensFromUsage(usage)
		}
	}
	
	// Direct token fields
	if tokens, ok := data["total_tokens"].(float64); ok {
		totalTokens += int64(tokens)
	}
	
	// String token fields
	if tokensStr, ok := data["total_tokens"].(string); ok {
		if tokens, err := strconv.ParseInt(tokensStr, 10, 64); err == nil {
			totalTokens += tokens
		}
	}
	
	return totalTokens
}

// extractTokenInfoFromJSON extracts detailed token usage information from JSON data
func (p *Parser) extractTokenInfoFromJSON(data map[string]interface{}) *TokenInfo {
	var tokenInfo *TokenInfo

	// Check usage field at root level (Claude API format)
	if usage, ok := data["usage"].(map[string]interface{}); ok {
		tokenInfo = p.extractTokenInfoFromUsage(usage)
		if tokenInfo != nil {
			return tokenInfo
		}
	}

	// Check message.usage field (Claude Code format for assistant messages)
	if message, ok := data["message"].(map[string]interface{}); ok {
		if usage, ok := message["usage"].(map[string]interface{}); ok {
			tokenInfo = p.extractTokenInfoFromUsage(usage)
			if tokenInfo != nil {
				return tokenInfo
			}
		}
	}

	// Check for token count in response metadata
	if response, ok := data["response"].(map[string]interface{}); ok {
		if usage, ok := response["usage"].(map[string]interface{}); ok {
			tokenInfo = p.extractTokenInfoFromUsage(usage)
			if tokenInfo != nil {
				return tokenInfo
			}
		}
	}

	// Fallback: try to extract from direct token fields
	tokenInfo = &TokenInfo{}
	hasTokenData := false

	if tokens, ok := data["total_tokens"].(float64); ok {
		tokenInfo.TotalTokens = int64(tokens)
		hasTokenData = true
	} else if tokensStr, ok := data["total_tokens"].(string); ok {
		if tokens, err := strconv.ParseInt(tokensStr, 10, 64); err == nil {
			tokenInfo.TotalTokens = tokens
			hasTokenData = true
		}
	}

	if hasTokenData {
		return tokenInfo
	}

	return nil
}

// extractTokensFromUsage extracts tokens from a usage object
func (p *Parser) extractTokensFromUsage(usage map[string]interface{}) int64 {
	// Check for total_tokens directly first (most accurate)
	if total, ok := usage["total_tokens"].(float64); ok {
		return int64(total)
	}
	
	// Fallback: calculate from input and output tokens only
	// Note: cache tokens are a breakdown of input tokens, not additional tokens
	var totalTokens int64
	
	// Input tokens (includes cached and non-cached input)
	if inputTokens, ok := usage["input_tokens"].(float64); ok {
		totalTokens += int64(inputTokens)
	}
	
	// Output tokens
	if outputTokens, ok := usage["output_tokens"].(float64); ok {
		totalTokens += int64(outputTokens)
	}
	
	return totalTokens
}

// extractTokenInfoFromUsage extracts detailed token information from a usage object (ccusage-compatible)
func (p *Parser) extractTokenInfoFromUsage(usage map[string]interface{}) *TokenInfo {
	tokenInfo := &TokenInfo{}
	hasData := false

	// Extract input tokens
	if inputTokens, ok := usage["input_tokens"].(float64); ok {
		tokenInfo.InputTokens = int64(inputTokens)
		hasData = true
	}

	// Extract output tokens
	if outputTokens, ok := usage["output_tokens"].(float64); ok {
		tokenInfo.OutputTokens = int64(outputTokens)
		hasData = true
	}

	// Extract cache creation tokens (ccusage-compatible)
	if cacheCreationTokens, ok := usage["cache_creation_input_tokens"].(float64); ok {
		tokenInfo.CacheCreationInputTokens = int64(cacheCreationTokens)
		hasData = true
	}

	// Extract cache read tokens (ccusage-compatible)  
	if cacheReadTokens, ok := usage["cache_read_input_tokens"].(float64); ok {
		tokenInfo.CacheReadInputTokens = int64(cacheReadTokens)
		hasData = true
	}

	// Check for total_tokens directly first (most accurate)
	if total, ok := usage["total_tokens"].(float64); ok {
		tokenInfo.TotalTokens = int64(total)
		hasData = true
	} else if hasData {
		// Calculate total from all token types (ccusage-style)
		tokenInfo.TotalTokens = tokenInfo.InputTokens + tokenInfo.OutputTokens + 
			tokenInfo.CacheCreationInputTokens + tokenInfo.CacheReadInputTokens
	}

	if hasData {
		return tokenInfo
	}
	return nil
}

// ExtractModelFromJSON extracts model information from JSON data
func (p *Parser) ExtractModelFromJSON(data map[string]interface{}) string {
	// Look for model name in various possible fields
	modelName := ""
	
	// Check for model field directly
	if model, ok := data["model"].(string); ok {
		modelName = model
	} else if request, ok := data["request"].(map[string]interface{}); ok {
		if model, ok := request["model"].(string); ok {
			modelName = model
		}
	} else if metadata, ok := data["metadata"].(map[string]interface{}); ok {
		if model, ok := metadata["model"].(string); ok {
			modelName = model
		}
	}
	
	// Check for message.model field (Claude Code format for assistant messages)
	if modelName == "" {
		if message, ok := data["message"].(map[string]interface{}); ok {
			if model, ok := message["model"].(string); ok {
				modelName = model
			}
		}
	}
	
	// If this is an assistant message, prioritize its model info (most recent)
	if typeField, ok := data["type"].(string); ok && typeField == "assistant" {
		if message, ok := data["message"].(map[string]interface{}); ok {
			if model, ok := message["model"].(string); ok {
				modelName = model
			}
		}
	}
	
	if modelName != "" {
		// Normalize the model name before returning
		return normalizeModelName(modelName)
	}
	
	return ""
}

// normalizeModelName normalizes model names by removing date suffixes and handling aliases
func normalizeModelName(rawModel string) string {
	if rawModel == "" {
		return ""
	}
	
	// Handle common aliases first
	aliases := map[string]string{
		"opusplan": "claude-opus-4-1",
		"sonnet":   "claude-sonnet-4",
		"haiku":    "claude-haiku-4",
	}
	
	if normalized, exists := aliases[rawModel]; exists {
		return normalized
	}
	
	// Remove date suffixes (e.g., "claude-opus-4-1-20250805" -> "claude-opus-4-1")
	datePattern := regexp.MustCompile(`-\d{8}$`)
	normalized := datePattern.ReplaceAllString(rawModel, "")
	
	// Convert full model IDs to shorter forms for capacity matching
	// claude-opus-4-1-20250805 -> claude-4.1-opus
	if strings.Contains(normalized, "claude-opus-4-1") {
		return "claude-4.1-opus"
	}
	if strings.Contains(normalized, "claude-sonnet-4") {
		return "claude-4-sonnet"
	}
	if strings.Contains(normalized, "claude-3.5-sonnet") {
		return "claude-3.5-sonnet"
	}
	if strings.Contains(normalized, "claude-3.5-haiku") {
		return "claude-3.5-haiku"
	}
	
	return normalized
}