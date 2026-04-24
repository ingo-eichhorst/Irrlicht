// hookinstaller.go manages Claude Code hook entries in ~/.claude/settings.json
// for the irrlicht daemon. It installs PermissionRequest, PostToolUse, and
// PostToolUseFailure hooks that POST to the daemon's HTTP endpoint, and can
// remove them cleanly. (Issue #108.)
package claudecode

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// hookSentinel is the substring in the hook command that identifies irrlicht-
// managed entries. Used by both install (idempotency check) and uninstall.
const hookSentinel = "localhost:7837/api/v1/hooks/claudecode"

// installedHookCommand is the curl command installed as the hook handler. It
// pipes the hook payload (stdin) to the daemon's HTTP endpoint. Flags:
//   - -f: fail silently on HTTP errors (non-blocking)
//   - -sS: silent but show errors
//   - --max-time 1: abort after 1 second (fast no-op when daemon is down)
const installedHookCommand = "curl -fsS --max-time 1 -X POST --data-binary @- http://localhost:7837/api/v1/hooks/claudecode"

// hookMatcher filters which tools trigger the hooks. PermissionRequest only
// fires for tools that need permission, but PostToolUse/PostToolUseFailure
// fire for all completions — the matcher reduces noise for the latter.
const hookMatcher = "Bash|Write|Edit|MultiEdit|NotebookEdit|WebFetch|mcp__.*"

// installedHookEvents are the Claude Code hook events we install handlers for.
var installedHookEvents = []string{HookPermissionRequest, HookPostToolUse, HookPostToolUseFailure}

// EnsureHooksInstalled adds irrlicht hook entries to ~/.claude/settings.json
// if they are not already present. Creates the file if it doesn't exist.
// Returns true if the file was modified, false if hooks were already installed.
func EnsureHooksInstalled() (bool, error) {
	path, err := claudeSettingsPath()
	if err != nil {
		return false, err
	}

	settings, err := readClaudeSettings(path)
	if err != nil {
		return false, err
	}

	hooksMap := ensureHooksMap(settings)

	modified := false
	for _, event := range installedHookEvents {
		if !hasOurHook(hooksMap, event) {
			addOurHook(hooksMap, event)
			modified = true
		}
	}

	if !modified {
		return false, nil
	}

	return true, writeClaudeSettings(path, settings)
}

// UninstallHooks removes irrlicht hook entries from ~/.claude/settings.json.
// Returns true if the file was modified, false if no hooks were found.
func UninstallHooks() (bool, error) {
	path, err := claudeSettingsPath()
	if err != nil {
		return false, err
	}

	settings, err := readClaudeSettings(path)
	if err != nil {
		return false, err
	}

	hooksObj, ok := settings["hooks"]
	if !ok {
		return false, nil
	}
	hooksMap, ok := hooksObj.(map[string]interface{})
	if !ok {
		return false, nil
	}

	modified := false
	for _, event := range installedHookEvents {
		if removeOurHook(hooksMap, event) {
			modified = true
		}
	}

	if !modified {
		return false, nil
	}

	return true, writeClaudeSettings(path, settings)
}

// --- helpers ---

func claudeSettingsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude", "settings.json"), nil
}

func readClaudeSettings(path string) (map[string]interface{}, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]interface{}{}, nil
	}
	if err != nil {
		return nil, err
	}
	var settings map[string]interface{}
	if err := json.Unmarshal(data, &settings); err != nil {
		return nil, err
	}
	return settings, nil
}

func writeClaudeSettings(path string, settings map[string]interface{}) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	// Atomic write: temp file + rename.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ensureHooksMap returns (or creates) the top-level "hooks" map in settings.
func ensureHooksMap(settings map[string]interface{}) map[string]interface{} {
	if h, ok := settings["hooks"]; ok {
		if m, ok := h.(map[string]interface{}); ok {
			return m
		}
	}
	m := map[string]interface{}{}
	settings["hooks"] = m
	return m
}

// hasOurHook checks if a matcher group with our sentinel command already exists
// in the given event's hook array.
func hasOurHook(hooksMap map[string]interface{}, event string) bool {
	arr, ok := hooksMap[event]
	if !ok {
		return false
	}
	groups, ok := arr.([]interface{})
	if !ok {
		return false
	}
	for _, g := range groups {
		group, ok := g.(map[string]interface{})
		if !ok {
			continue
		}
		innerArr, ok := group["hooks"]
		if !ok {
			continue
		}
		innerHooks, ok := innerArr.([]interface{})
		if !ok {
			continue
		}
		for _, h := range innerHooks {
			hook, ok := h.(map[string]interface{})
			if !ok {
				continue
			}
			if cmd, ok := hook["command"].(string); ok && strings.Contains(cmd, hookSentinel) {
				return true
			}
		}
	}
	return false
}

// addOurHook appends a matcher group with our hook command to the event's array.
func addOurHook(hooksMap map[string]interface{}, event string) {
	entry := map[string]interface{}{
		"matcher": hookMatcher,
		"hooks": []interface{}{
			map[string]interface{}{
				"type":    "command",
				"command": installedHookCommand,
			},
		},
	}

	existing, ok := hooksMap[event]
	if ok {
		if arr, ok := existing.([]interface{}); ok {
			hooksMap[event] = append(arr, entry)
			return
		}
	}
	hooksMap[event] = []interface{}{entry}
}

// removeOurHook removes matcher groups containing our sentinel from the event's
// array. Returns true if any were removed.
func removeOurHook(hooksMap map[string]interface{}, event string) bool {
	arr, ok := hooksMap[event]
	if !ok {
		return false
	}
	groups, ok := arr.([]interface{})
	if !ok {
		return false
	}

	var kept []interface{}
	removed := false
	for _, g := range groups {
		if containsHookSentinel(g) {
			removed = true
			continue
		}
		kept = append(kept, g)
	}
	if !removed {
		return false
	}

	if len(kept) == 0 {
		delete(hooksMap, event)
	} else {
		hooksMap[event] = kept
	}
	return true
}

// containsHookSentinel checks if a matcher group contains a hook command with our sentinel.
func containsHookSentinel(g interface{}) bool {
	group, ok := g.(map[string]interface{})
	if !ok {
		return false
	}
	innerArr, ok := group["hooks"]
	if !ok {
		return false
	}
	innerHooks, ok := innerArr.([]interface{})
	if !ok {
		return false
	}
	for _, h := range innerHooks {
		hook, ok := h.(map[string]interface{})
		if !ok {
			continue
		}
		if cmd, ok := hook["command"].(string); ok && strings.Contains(cmd, hookSentinel) {
			return true
		}
	}
	return false
}
