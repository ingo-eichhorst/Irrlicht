// hookinstaller.go manages Claude Code hook entries in ~/.claude/settings.json
// for the irrlicht daemon. It installs PermissionRequest, PreToolUse,
// PostToolUse, and PostToolUseFailure hooks that POST to the daemon's HTTP
// endpoint, and can remove them cleanly. (Issues #108, #307.)
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
//
// `|| true` keeps exit status 0 when the daemon is down so Claude Code
// doesn't surface "connection refused" as a PostToolUse hook error.
const installedHookCommand = "curl -fsS --max-time 1 -X POST --data-binary @- http://localhost:7837/api/v1/hooks/claudecode || true"

// hookMatcherDefault filters which tools trigger the permission-style hooks.
// PermissionRequest only fires for tools that need permission, but
// PostToolUse/PostToolUseFailure fire for all completions — the matcher
// reduces noise for the latter.
const hookMatcherDefault = "Bash|Write|Edit|MultiEdit|NotebookEdit|WebFetch|mcp__.*"

// hookMatcherUserInput is the matcher for user-blocking tools that suspend the
// agent waiting for user input. PreToolUse fires synchronously when the model
// emits the tool_use (before the transcript is flushed), so the daemon flips
// to waiting immediately; the paired PostToolUse group clears the flag once
// the user answers. (Issue #307.)
const hookMatcherUserInput = "AskUserQuestion|ExitPlanMode"

// hookSpec is a single (event, matcher) entry we manage in settings.json.
// We may install multiple groups under the same event (e.g. PostToolUse has
// both the default matcher and the user-input matcher).
type hookSpec struct {
	event   string
	matcher string
}

// installedHooks is the set of hook groups irrlicht manages. Each entry maps
// to one matcher group under the named event in ~/.claude/settings.json.
var installedHooks = []hookSpec{
	{HookPermissionRequest, hookMatcherDefault},
	{HookPostToolUse, hookMatcherDefault},
	{HookPostToolUseFailure, hookMatcherDefault},
	{HookPreToolUse, hookMatcherUserInput},
	{HookPostToolUse, hookMatcherUserInput},
}

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
	// Upgrade stale sentinel commands once per event (matcher-agnostic).
	for _, event := range uniqueHookEvents() {
		if upgradeStaleHookCommands(hooksMap, event) {
			modified = true
		}
	}
	// Install any missing (event, matcher) group.
	for _, spec := range installedHooks {
		if !hasOurHook(hooksMap, spec.event, spec.matcher) {
			addOurHook(hooksMap, spec.event, spec.matcher)
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
	for _, event := range uniqueHookEvents() {
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

// hasOurHook checks if a matcher group with the given matcher AND our sentinel
// command already exists in the event's hook array. Both (event, matcher)
// dimensions are required so we can install multiple groups per event
// (e.g. PostToolUse has both the default matcher and the user-input matcher).
func hasOurHook(hooksMap map[string]interface{}, event, matcher string) bool {
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
		if m, _ := group["matcher"].(string); m != matcher {
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

// uniqueHookEvents returns the distinct event names across installedHooks,
// preserving order of first appearance. Used for per-event operations
// (stale-command upgrade, uninstall) that don't depend on the matcher.
func uniqueHookEvents() []string {
	seen := make(map[string]bool, len(installedHooks))
	events := make([]string, 0, len(installedHooks))
	for _, spec := range installedHooks {
		if seen[spec.event] {
			continue
		}
		seen[spec.event] = true
		events = append(events, spec.event)
	}
	return events
}

// upgradeStaleHookCommands rewrites any hook command that contains hookSentinel
// but isn't the canonical installedHookCommand. Returns true if any entry was
// rewritten. This migrates users whose settings.json still has an older form
// of our command (e.g., missing the trailing `|| true`).
func upgradeStaleHookCommands(hooksMap map[string]interface{}, event string) bool {
	arr, ok := hooksMap[event]
	if !ok {
		return false
	}
	groups, ok := arr.([]interface{})
	if !ok {
		return false
	}
	upgraded := false
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
			cmd, ok := hook["command"].(string)
			if !ok {
				continue
			}
			if strings.Contains(cmd, hookSentinel) && cmd != installedHookCommand {
				hook["command"] = installedHookCommand
				upgraded = true
			}
		}
	}
	return upgraded
}

// addOurHook appends a matcher group with our hook command to the event's array.
func addOurHook(hooksMap map[string]interface{}, event, matcher string) {
	entry := map[string]interface{}{
		"matcher": matcher,
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
