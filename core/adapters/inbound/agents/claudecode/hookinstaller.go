// hookinstaller.go manages Claude Code hook entries in ~/.claude/settings.json
// for the irrlicht daemon. It installs PermissionRequest, PreToolUse,
// PostToolUse, PostToolUseFailure, PreCompact, Stop and Notification hooks that
// POST to the daemon's HTTP endpoint via native `type: http` delivery, and can
// remove them cleanly. (Issues #108, #307, #657, #1161, #1173.)
package claudecode

import (
	"os"
	"path/filepath"

	"irrlicht/core/adapters/inbound/agents/hookjson"
	"irrlicht/core/pkg/daemonaddr"
)

// hookEndpointPath is the daemon's Claude Code hook path. Host and port are
// resolved at install time from the daemon's own bind address, so a daemon on
// an alternate port installs hooks that reach it rather than whatever owns
// :7837 (issue #1178).
const hookEndpointPath = "/api/v1/hooks/claudecode"

// hookSentinel is the substring in the hook entry (curl command or http url)
// that identifies irrlicht-managed entries. Used by both install (idempotency
// check) and uninstall, and by the delivery-form migration. It is deliberately
// port-independent: a sentinel carrying a port would stop recognizing our own
// entries the moment the daemon moves, orphaning them and appending a
// duplicate group instead of upgrading in place (#1178). As the bare endpoint
// path it is a substring of the legacy curl command, of a pre-#1178 :7837
// url, and of the current url alike.
//
// It is also a prefix of statuslineSentinel, which is harmless: hook entries
// and statusLine.command live under disjoint keys of settings.json and are
// never matched against each other.
const hookSentinel = hookEndpointPath

// hookEndpointURL is the daemon endpoint the installed hook posts to. Claude
// Code delivers the hook payload as a JSON POST body directly to this URL via
// its native `type: http` hook — no shell, no curl (issue #1161). Removing the
// curl dependency means hook delivery no longer silently no-ops when curl is
// missing from PATH, which was the failure mode the OpenToolStalled transcript
// fallback (#488) exists to cover; that fallback is retained (it still covers a
// down/unreachable daemon), but its primary trigger is now gone.
func hookEndpointURL() string {
	return daemonaddr.LocalURL(hookEndpointPath)
}

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

// hookMatcherNotification is the matcher for the Notification event. Like
// PreCompact (whose matcher is the compaction trigger), Claude Code's
// Notification matcher matches the notification_type rather than a tool name.
// We install "idle_prompt" so the hook fires only when the agent goes idle at
// the prompt (issue #1173) — permission_prompt is already covered by the
// blocking PermissionRequest hook, and the other types don't affect state.
const hookMatcherNotification = "idle_prompt"

// installedHookEvents are the Claude Code hook events we install handlers for.
var installedHookEvents = []string{
	HookPermissionRequest,
	HookPreToolUse,
	HookPostToolUse,
	HookPostToolUseFailure,
	HookPreCompact,
	HookStop,
	HookNotification,
}

// matcherForEvent returns the matcher we install for the given event. For most
// events this is a tool-name regex; for PreCompact it is the compaction trigger
// ("manual"); for Notification it is the notification_type ("idle_prompt"); for
// Stop it is empty — Claude Code's Stop hook takes no matcher (it fires at every
// turn end) and rejects settings.json that gives it one, so hookjson omits the
// matcher key entirely for an empty matcher.
func matcherForEvent(event string) string {
	switch event {
	case HookPreToolUse:
		return hookMatcherPreToolUse
	case HookPreCompact:
		return hookMatcherPreCompact
	case HookNotification:
		return hookMatcherNotification
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
		"url":     hookEndpointURL(),
		"timeout": hookTimeoutSeconds,
	}
}

// hookConfig describes this adapter's install to the shared hookjson
// machinery (issue #1179): which file to merge into, which sentinel marks our
// entries, which events get installed with which matcher, and what the
// canonical inner entry looks like. Everything the two JSON-hook adapters have
// in common — the merge, idempotency, in-place upgrade and removal logic —
// lives there; everything Claude-Code-specific stays here.
func hookConfig(path string) hookjson.Config {
	return hookjson.Config{
		Path:        path,
		Sentinel:    hookSentinel,
		Events:      installedHookEvents,
		MatcherFor:  matcherForEvent,
		Entry:       ourHookEntry,
		IsCanonical: hookEntryIsCanonical,
		WriteFile:   atomicWriteFile,
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
	return hookjson.EnsureInstalled(hookConfig(path))
}

// UninstallHooks removes irrlicht hook entries from ~/.claude/settings.json.
// Returns true if the file was modified, false if no hooks were found.
func UninstallHooks() (bool, error) {
	path, err := claudeSettingsPath()
	if err != nil {
		return false, err
	}
	return hookjson.Uninstall(hookConfig(path))
}

// --- helpers ---

func claudeSettingsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude", "settings.json"), nil
}

// readClaudeSettings / writeClaudeSettings are thin adapters onto the shared
// codec so the statusline installer, which merges into the same settings.json,
// keeps reading and writing it exactly the way the hook installer does.
func readClaudeSettings(path string) (map[string]interface{}, error) {
	return hookjson.ReadSettings(path)
}

func writeClaudeSettings(path string, settings map[string]interface{}) error {
	return hookjson.WriteSettings(path, settings, atomicWriteFile)
}

// hookEntryIsCanonical reports whether an inner hook object is already in the
// current native-http form: type "http", our endpoint url, and no leftover
// legacy `command` key. The timeout value is deliberately not part of the
// identity check, so tuning hookTimeoutSeconds never forces a churny rewrite of
// every existing install.
//
// The url comparison is against the *currently resolved* endpoint, so an entry
// left behind by a daemon on a different port reads as stale and is rewritten
// in place by upgradeStaleHookDelivery (#1178).
func hookEntryIsCanonical(hook map[string]interface{}) bool {
	if _, hasCmd := hook["command"]; hasCmd {
		return false
	}
	t, _ := hook["type"].(string)
	u, _ := hook["url"].(string)
	return t == "http" && u == hookEndpointURL()
}
