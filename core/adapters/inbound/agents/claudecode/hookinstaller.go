// hookinstaller.go manages Claude Code hook entries in ~/.claude/settings.json
// for the irrlicht daemon. It installs PermissionRequest, PreToolUse,
// PostToolUse, PostToolUseFailure, PreCompact and Stop hooks that POST to the
// daemon's HTTP endpoint via native `type: http` delivery, and can remove them
// cleanly. (Issues #108, #307, #657, #1161.)
package claudecode

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// hookSentinel is the substring in the hook entry (curl command or http url)
// that identifies irrlicht-managed entries. Used by both install (idempotency
// check) and uninstall, and by the curl→http migration. It is a substring of
// both the legacy command and the current URL, so a pre-#1161 install is still
// recognized as ours and upgraded in place.
const hookSentinel = "localhost:7837/api/v1/hooks/claudecode"

// hookEndpointURL is the daemon endpoint the installed hook posts to. Claude
// Code delivers the hook payload as a JSON POST body directly to this URL via
// its native `type: http` hook — no shell, no curl (issue #1161). Removing the
// curl dependency means hook delivery no longer silently no-ops when curl is
// missing from PATH, which was the failure mode the OpenToolStalled transcript
// fallback (#488) exists to cover; that fallback is retained (it still covers a
// down/unreachable daemon), but its primary trigger is now gone.
const hookEndpointURL = "http://localhost:7837/api/v1/hooks/claudecode"

// hookTimeoutSeconds bounds how long Claude Code waits on the daemon before
// giving up on a hook delivery. The daemon's handler is near-instant (an
// in-memory map write plus a channel send) and a down daemon fails the
// connection immediately, so this is only a safety ceiling against a wedged
// daemon — well below Claude Code's 600s default so a hook can never stall a
// turn.
const hookTimeoutSeconds = 5

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

// hookMatcherPreCompact is the matcher for the PreCompact event. Unlike the
// other events (whose matcher is a tool-name regex), Claude Code's PreCompact
// matcher matches the compaction *trigger* — "manual" or "auto". We install
// "manual" so the hook fires only for a user-invoked /compact, gating at the
// source: auto-compaction fires mid-turn while the session is already working,
// so forcing working there would be a spurious blip (#657).
const hookMatcherPreCompact = "manual"

// installedHookEvents are the Claude Code hook events we install handlers for.
var installedHookEvents = []string{
	HookPermissionRequest,
	HookPreToolUse,
	HookPostToolUse,
	HookPostToolUseFailure,
	HookPreCompact,
	HookStop,
}

// matcherForEvent returns the matcher we install for the given event. For most
// events this is a tool-name regex; for PreCompact it is the compaction trigger
// ("manual"); for Stop it is empty — Claude Code's Stop hook takes no matcher
// (it fires at every turn end) and rejects settings.json that gives it one, so
// addOurHook omits the matcher key entirely for an empty matcher.
func matcherForEvent(event string) string {
	switch event {
	case HookPreToolUse:
		return hookMatcherPreToolUse
	case HookPreCompact:
		return hookMatcherPreCompact
	case HookStop:
		return ""
	default:
		return hookMatcher
	}
}

// ourHookEntry builds the inner hook object we install: native `type: http`
// delivery straight to the daemon (issue #1161), no shell wrapper. Claude Code
// POSTs the hook payload as a JSON body to url and treats a 2xx with no body as
// "no decision" — exactly the daemon's behaviour.
func ourHookEntry() map[string]interface{} {
	return map[string]interface{}{
		"type":    "http",
		"url":     hookEndpointURL,
		"timeout": hookTimeoutSeconds,
	}
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
		if upgradeStaleHookDelivery(hooksMap, event) {
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
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return atomicWriteFile(path, data)
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

// upgradeStaleHookDelivery rewrites any sentinel-bearing inner hook that is not
// the canonical native-http entry to the current form. Returns true if any
// entry was rewritten. This migrates an existing install from the legacy curl
// `command` wrapper to native `type: http` delivery (issue #1161) — and, more
// generally, any older shape carrying our sentinel — in place, without
// appending a duplicate group.
func upgradeStaleHookDelivery(hooksMap map[string]interface{}, event string) bool {
	arr, ok := hooksMap[event].([]interface{})
	if !ok {
		return false
	}
	upgraded := false
	for _, g := range arr {
		if upgradeGroupHookDelivery(g) {
			upgraded = true
		}
	}
	return upgraded
}

// upgradeGroupHookDelivery rewrites, in one matcher group, every inner hook that
// carries our sentinel but isn't the canonical native-http entry (e.g. the
// legacy curl command). Returns true if any entry was rewritten.
func upgradeGroupHookDelivery(g interface{}) bool {
	group, ok := g.(map[string]interface{})
	if !ok {
		return false
	}
	innerHooks, ok := group["hooks"].([]interface{})
	if !ok {
		return false
	}
	upgraded := false
	for i, h := range innerHooks {
		hook, ok := h.(map[string]interface{})
		if !ok {
			continue
		}
		if hookEntryIsSentinel(hook) && !hookEntryIsCanonical(hook) {
			innerHooks[i] = ourHookEntry()
			upgraded = true
		}
	}
	return upgraded
}

// upgradeStaleHookMatchers reconciles the matcher of any group containing our
// sentinel with the expected value. Used to migrate existing installs when we
// widen or change an event's matcher (e.g. the #307 expansion that added
// AskUserQuestion|ExitPlanMode to the PostToolUse matcher). For an event whose
// expected matcher is empty (Stop, #1161) it strips any matcher key entirely,
// since Claude Code rejects a Stop hook that carries one.
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
		if reconcileGroupMatcher(group, expected) {
			upgraded = true
		}
	}
	return upgraded
}

// reconcileGroupMatcher brings one sentinel-bearing group's matcher into line
// with expected: sets it when it differs, or deletes the key when expected is
// empty (a Stop hook must carry no matcher). Returns true if it changed the group.
func reconcileGroupMatcher(group map[string]interface{}, expected string) bool {
	m, has := group["matcher"].(string)
	if expected == "" {
		if has {
			delete(group, "matcher")
			return true
		}
		return false
	}
	if m != expected {
		group["matcher"] = expected
		return true
	}
	return false
}

// addOurHook appends a matcher group with our native-http hook entry to the
// event's array. The matcher key is omitted entirely for an empty matcher
// (Stop), which Claude Code requires.
func addOurHook(hooksMap map[string]interface{}, event, matcher string) {
	entry := map[string]interface{}{
		"hooks": []interface{}{ourHookEntry()},
	}
	if matcher != "" {
		entry["matcher"] = matcher
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

// containsHookSentinel checks if a matcher group contains an inner hook entry
// carrying our sentinel — either the legacy curl command or the native-http url.
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
		if hookEntryIsSentinel(hook) {
			return true
		}
	}
	return false
}

// hookEntryIsSentinel reports whether an inner hook object is one of ours,
// identified by our sentinel appearing in either the legacy `command` (curl) or
// the current `url` (native http) field.
func hookEntryIsSentinel(hook map[string]interface{}) bool {
	if cmd, ok := hook["command"].(string); ok && strings.Contains(cmd, hookSentinel) {
		return true
	}
	if url, ok := hook["url"].(string); ok && strings.Contains(url, hookSentinel) {
		return true
	}
	return false
}

// hookEntryIsCanonical reports whether an inner hook object is already in the
// current native-http form: type "http", our endpoint url, and no leftover
// legacy `command` key. The timeout value is deliberately not part of the
// identity check, so tuning hookTimeoutSeconds never forces a churny rewrite of
// every existing install.
func hookEntryIsCanonical(hook map[string]interface{}) bool {
	if _, hasCmd := hook["command"]; hasCmd {
		return false
	}
	t, _ := hook["type"].(string)
	u, _ := hook["url"].(string)
	return t == "http" && u == hookEndpointURL
}
