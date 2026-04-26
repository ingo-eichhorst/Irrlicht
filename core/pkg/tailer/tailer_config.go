package tailer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// surviveTurnDone returns true for tools whose tool_result arrives after the
// turn_done event. These must not be swept from openToolCalls:
//
//   - Agent: sub-agents can still be running when the parent's turn ends.
//   - SendMessage: continues a previously spawned sub-agent (Claude Code 2.1.77
//     replaced Agent({resume}) with SendMessage({to}) for resumption). Same
//     rationale as Agent — the sub-agent runs in the background.
//   - AskUserQuestion, ExitPlanMode: user-blocking tools whose result only
//     arrives after the user responds. Also listed in session.isUserBlockingTool;
//     the overlap is intentional — the two predicates serve different purposes.
func surviveTurnDone(name string) bool {
	switch name {
	case "Agent", "SendMessage", "AskUserQuestion", "ExitPlanMode":
		return true
	}
	return false
}

// isUserBlockingToolName returns true for tools that always block the agent
// until the user responds: AskUserQuestion and ExitPlanMode. Used by
// TailAndProcess to flag same-pass open+close of these tools so the daemon
// can synthesise the collapsed working→waiting transition (issue #150).
// Kept local to the tailer to avoid a domain-package import; the canonical
// list also lives at session.isUserBlockingTool.
func isUserBlockingToolName(name string) bool {
	switch name {
	case "AskUserQuestion", "ExitPlanMode":
		return true
	}
	return false
}

// --- Model config fallback ---

// getDefaultModelFromConfig reads the default model from the appropriate config
// based on adapter name.
func getDefaultModelFromConfig(adapter string) string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	switch adapter {
	case "pi":
		return getPiModel(homeDir)
	case "codex":
		return getCodexModel(homeDir)
	default:
		return getClaudeModel(homeDir)
	}
}

func getClaudeModel(homeDir string) string {
	data, err := os.ReadFile(filepath.Join(homeDir, ".claude", "settings.json"))
	if err != nil {
		return ""
	}
	var settings map[string]interface{}
	if err := json.Unmarshal(data, &settings); err != nil {
		return ""
	}
	if model, ok := settings["model"].(string); ok {
		return NormalizeModelName(model)
	}
	return ""
}

func getCodexModel(homeDir string) string {
	data, err := os.ReadFile(filepath.Join(homeDir, ".codex", "config.toml"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "model") && strings.Contains(line, "=") {
			parts := strings.SplitN(line, "=", 2)
			if strings.TrimSpace(parts[0]) == "model" {
				model := strings.Trim(strings.TrimSpace(parts[1]), "\"'")
				if model != "" {
					return model
				}
			}
		}
	}
	return ""
}

func getPiModel(homeDir string) string {
	data, err := os.ReadFile(filepath.Join(homeDir, ".pi", "agent", "settings.json"))
	if err != nil {
		return ""
	}
	var settings map[string]interface{}
	if err := json.Unmarshal(data, &settings); err != nil {
		return ""
	}
	if model, ok := settings["defaultModel"].(string); ok {
		return NormalizeModelName(model)
	}
	return ""
}
