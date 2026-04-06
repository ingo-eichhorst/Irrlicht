// Package transcript provides shared utilities for parsing transcript JSONL data.
package transcript

import (
	"encoding/json"
	"regexp"
	"strings"
)

// cwdTagRe matches <cwd>/path</cwd> in Codex environment_context blocks.
var cwdTagRe = regexp.MustCompile(`<cwd>([^<]+)</cwd>`)

// ExtractCWDFromLine extracts the working directory from a parsed transcript
// JSON line. It checks three sources in order:
//   - Claude Code: top-level "cwd" field
//   - Codex: <cwd> XML tag inside content[] text blocks
//   - Codex: "workdir" inside function_call arguments
//
// Returns "" if no CWD is found.
func ExtractCWDFromLine(raw map[string]interface{}) string {
	// Claude Code: top-level "cwd" field.
	if cwd, ok := raw["cwd"].(string); ok && cwd != "" {
		return cwd
	}
	// Wrapped Codex metadata: payload.cwd.
	if payload, ok := raw["payload"].(map[string]interface{}); ok {
		if cwd, ok := payload["cwd"].(string); ok && cwd != "" {
			return cwd
		}
	}
	// Codex: <cwd> XML tag inside environment_context content blocks.
	if cwd := extractCWDFromContentBlocks(raw); cwd != "" {
		return cwd
	}
	if payload, ok := raw["payload"].(map[string]interface{}); ok {
		if cwd := extractCWDFromContentBlocks(payload); cwd != "" {
			return cwd
		}
	}
	// Codex: workdir inside function_call arguments.
	if cwd := extractCWDFromArguments(raw); cwd != "" {
		return cwd
	}
	if payload, ok := raw["payload"].(map[string]interface{}); ok {
		if cwd := extractCWDFromArguments(payload); cwd != "" {
			return cwd
		}
	}
	return ""
}

func extractCWDFromArguments(raw map[string]interface{}) string {
	if args, ok := raw["arguments"].(string); ok {
		var parsed map[string]interface{}
		if json.Unmarshal([]byte(args), &parsed) == nil {
			if wd, ok := parsed["workdir"].(string); ok && wd != "" {
				return wd
			}
		}
	}
	return ""
}

// extractCWDFromContentBlocks scans content[] blocks for a <cwd> XML tag
// (Codex environment_context format).
func extractCWDFromContentBlocks(raw map[string]interface{}) string {
	content, ok := raw["content"].([]interface{})
	if !ok {
		return ""
	}
	for _, item := range content {
		block, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		text, ok := block["text"].(string)
		if !ok {
			continue
		}
		if m := cwdTagRe.FindStringSubmatch(text); len(m) > 1 {
			return strings.TrimSpace(m[1])
		}
	}
	return ""
}
