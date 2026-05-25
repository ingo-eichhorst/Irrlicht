// hookinstaller.go manages Claude Code hook entries in ~/.claude/settings.json
// for the irrlicht daemon. It installs PermissionRequest, PreToolUse,
// PostToolUse, and PostToolUseFailure hooks that POST to the daemon's HTTP
// endpoint, and can remove them cleanly. (Issues #108, #307.)
package claudecode

import (
	"encoding/json"
	"os"
	"os/exec"
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

// hookMatcher is the matcher used by PermissionRequest, PostToolUse, and
// PostToolUseFailure. AskUserQuestion / ExitPlanMode are included so the
// PostToolUse clearing edge fires for user-input overlays too (issue #307).
// PermissionRequest only fires for tools that actually need permission, so
// the extra alternatives are no-ops there.
const hookMatcher = "Bash|Write|Edit|MultiEdit|NotebookEdit|WebFetch|mcp__.*|AskUserQuestion|ExitPlanMode"

// hookMatcherPreToolUse is the narrow matcher for the PreToolUse event. We
// only want to flip working→waiting on the user-input tools — matching every
// Bash/Write/… would set permissionPending on every tool call. (Issue #307.)
const hookMatcherPreToolUse = "AskUserQuestion|ExitPlanMode"

// installedHookEvents are the Claude Code hook events we install handlers for.
var installedHookEvents = []string{
	HookPermissionRequest,
	HookPreToolUse,
	HookPostToolUse,
	HookPostToolUseFailure,
}

// matcherForEvent returns the tool matcher we install for the given event.
func matcherForEvent(event string) string {
	if event == HookPreToolUse {
		return hookMatcherPreToolUse
	}
	return hookMatcher
}

// HookDeliveryAvailable reports whether the tool the installed hook command
// relies on to reach the daemon is on PATH. installedHookCommand POSTs via
// `curl`, so a missing curl means every hook silently no-ops (the trailing
// `|| true` swallows "command not found") and permission prompts never
// surface as `waiting`. main.go calls this at startup to turn that otherwise
// invisible failure into a logged warning (#488).
func HookDeliveryAvailable() bool {
	_, err := exec.LookPath("curl")
	return err == nil
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
	for _, event := range installedHookEvents {
		expected := matcherForEvent(event)
		if upgradeStaleHookCommands(hooksMap, event) {
			modified = true
		}
		if upgradeStaleHookMatchers(hooksMap, event, expected) {
			modified = true
		}
		if !hasOurHook(hooksMap, event) {
			addOurHook(hooksMap, event, expected)
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

// hasOurHook checks if any matcher group containing our sentinel command
// already exists in the event's hook array. Matcher-agnostic: a group with
// our sentinel is "ours" regardless of which matcher string it carries.
func hasOurHook(hooksMap map[string]interface{}, event string) bool {
	arr, ok := hooksMap[event].([]interface{})
	if !ok {
		return false
	}
	for _, g := range arr {
		if containsHookSentinel(g) {
			return true
		}
	}
	return false
}

// upgradeStaleHookCommands rewrites any hook command that contains hookSentinel
// but isn't the canonical installedHookCommand. Returns true if any entry was
// rewritten. This migrates users whose settings.json still has an older form
// of our command (e.g., missing the trailing `|| true`).
func upgradeStaleHookCommands(hooksMap map[string]interface{}, event string) bool {
	arr, ok := hooksMap[event].([]interface{})
	if !ok {
		return false
	}
	upgraded := false
	for _, g := range arr {
		group, ok := g.(map[string]interface{})
		if !ok {
			continue
		}
		innerHooks, ok := group["hooks"].([]interface{})
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

// upgradeStaleHookMatchers rewrites the matcher of any group containing our
// sentinel whose matcher differs from the expected value. Used to migrate
// existing installs when we widen (or change) the matcher for an event — for
// example, the #307 expansion that adds AskUserQuestion|ExitPlanMode to the
// PostToolUse matcher.
func upgradeStaleHookMatchers(hooksMap map[string]interface{}, event, expected string) bool {
	arr, ok := hooksMap[event].([]interface{})
	if !ok {
		return false
	}
	upgraded := false
	for _, g := range arr {
		group, ok := g.(map[string]interface{})
		if !ok {
			continue
		}
		if !containsHookSentinel(g) {
			continue
		}
		if m, _ := group["matcher"].(string); m != expected {
			group["matcher"] = expected
			upgraded = true
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
	arr, ok := hooksMap[event].([]interface{})
	if !ok {
		return false
	}

	var kept []interface{}
	removed := false
	for _, g := range arr {
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
	innerHooks, ok := group["hooks"].([]interface{})
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
