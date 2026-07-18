// hookinstaller.go manages Codex CLI hook entries in ~/.codex/hooks.json for
// the irrlicht daemon. It installs PermissionRequest, PostToolUse and Stop
// hooks that POST the hook payload to the daemon's HTTP endpoint via a curl
// `type: command` entry, and can remove them cleanly (issue #1171).
//
// hooks.json (a dedicated file) is used rather than ~/.codex/config.toml so a
// malformed write can never corrupt the user's main Codex config, and so the
// JSON merge/idempotency logic can mirror claudecode's settings.json installer.
package codex

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// hookSentinel is the substring in a hook entry's curl command that identifies
// irrlicht-managed entries. Used by both install (idempotency) and uninstall.
const hookSentinel = "localhost:7837/api/v1/hooks/codex"

// hookEndpointURL is the daemon endpoint the installed hook posts to. Codex
// execs the curl command below with the hook payload on stdin (Codex has no
// native http-delivery hook like Claude Code's, so a curl command is used).
const hookEndpointURL = "http://localhost:7837/api/v1/hooks/codex"

// hookDeliveryCommand is the shell command Codex runs for each installed hook.
// It streams the payload (stdin, @-) to the daemon and never fails the turn
// (|| true): a down/unreachable daemon fails the connection fast, well under
// the --max-time ceiling, and the transcript path still covers turn-end.
const hookDeliveryCommand = "curl -fsS --max-time 1 -X POST --data-binary @- " + hookEndpointURL + " || true"

// hookTimeoutSeconds bounds how long Codex waits on the hook command. The
// daemon's handler is near-instant (a map write plus a channel send); this is
// a safety ceiling against a wedged daemon.
const hookTimeoutSeconds = 5

// hookMatcher matches every tool for the PermissionRequest/PostToolUse events.
// PermissionRequest only fires for tools that actually need approval, so a wide
// matcher is a no-op there; PostToolUse must match all tools so the
// permission-pending overlay is always cleared once an approved tool runs.
const hookMatcher = ".*"

// minHookMajor/Minor/Patch is the lowest Codex version whose hook event set
// includes the events we install (PermissionRequest/PostToolUse/Stop). Hooks
// were introduced experimental in rust-v0.114.0 (~March 2026, issue #1171).
const (
	minHookMajor = 0
	minHookMinor = 114
	minHookPatch = 0
)

// installedHookEvents are the Codex hook events we install handlers for. Codex
// has no PostToolUseFailure event (denial is handled on the Stop path), and
// PreCompact/PostCompact are deferred (only needed if Codex grows a
// transcript-goes-silent-during-compaction problem like Claude Code's #657).
var installedHookEvents = []string{
	HookPermissionRequest,
	HookPostToolUse,
	HookStop,
}

// matcherForEvent returns the matcher we install for the given event. Stop
// takes no matcher (it fires at every turn end); addOurHook omits the matcher
// key entirely for an empty matcher.
func matcherForEvent(event string) string {
	if event == HookStop {
		return ""
	}
	return hookMatcher
}

// ourHookEntry builds the inner hook object we install: a curl command that
// POSTs the payload to the daemon.
func ourHookEntry() map[string]interface{} {
	return map[string]interface{}{
		"type":    "command",
		"command": hookDeliveryCommand,
		"timeout": hookTimeoutSeconds,
	}
}

// EnsureHooksInstalled adds irrlicht hook entries to ~/.codex/hooks.json if
// they are not already present. Creates the file if it doesn't exist. Returns
// true if the file was modified, false if hooks were already installed.
func EnsureHooksInstalled() (bool, error) {
	path, err := codexHooksPath()
	if err != nil {
		return false, err
	}

	settings, err := readCodexHooks(path)
	if err != nil {
		return false, err
	}

	hooksMap := ensureHooksMap(settings)

	modified := false
	for _, event := range installedHookEvents {
		expected := matcherForEvent(event)
		if upgradeStaleHookCommand(hooksMap, event) {
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

	return true, writeCodexHooks(path, settings)
}

// UninstallHooks removes irrlicht hook entries from ~/.codex/hooks.json.
// Returns true if the file was modified, false if no hooks were found.
func UninstallHooks() (bool, error) {
	path, err := codexHooksPath()
	if err != nil {
		return false, err
	}

	settings, err := readCodexHooks(path)
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

	return true, writeCodexHooks(path, settings)
}

// applyCodexHooks is the Apply closure for the "hooks" permission: it
// version-gates the install on the running Codex's cli_version (issue #1171).
// A Codex older than the hooks feature is skipped rather than cluttered with a
// config it ignores; an unknown/unparseable version fails open and installs,
// since a dedicated hooks.json is harmless to an old Codex regardless.
func applyCodexHooks() error {
	if v := newestObservedCLIVersion(); v != "" && !codexSupportsHooks(v) {
		return nil
	}
	_, err := EnsureHooksInstalled()
	return err
}

// codexSupportsHooks reports whether a Codex cli_version string is new enough
// to fire the hook events we install. Accepts bare semver ("0.114.0") and the
// release-tag form ("rust-v0.114.0" / "v0.114.0"); an empty or unparseable
// version fails open (returns true) — see applyCodexHooks.
func codexSupportsHooks(version string) bool {
	major, minor, patch, ok := parseCodexVersion(version)
	if !ok {
		return true
	}
	if major != minHookMajor {
		return major > minHookMajor
	}
	if minor != minHookMinor {
		return minor > minHookMinor
	}
	return patch >= minHookPatch
}

// parseCodexVersion extracts major.minor.patch from a Codex version string,
// stripping a leading "rust-v" or "v" tag prefix. Reports ok=false when the
// first three dot groups don't parse as integers.
func parseCodexVersion(version string) (major, minor, patch int, ok bool) {
	v := strings.TrimSpace(version)
	v = strings.TrimPrefix(v, "rust-")
	v = strings.TrimPrefix(v, "v")
	if v == "" {
		return 0, 0, 0, false
	}
	fields := strings.SplitN(v, ".", 4)
	nums := make([]int, 3)
	for i := 0; i < 3; i++ {
		if i >= len(fields) {
			return 0, 0, 0, false
		}
		// Trim any pre-release/build suffix off the patch field ("0-rc1").
		field := fields[i]
		if j := strings.IndexAny(field, "-+"); j >= 0 {
			field = field[:j]
		}
		n, err := strconv.Atoi(field)
		if err != nil {
			return 0, 0, 0, false
		}
		nums[i] = n
	}
	return nums[0], nums[1], nums[2], true
}

// newestObservedCLIVersion returns the cli_version recorded in the most
// recently modified Codex session file, or "" if none is found. It is the
// install-time proxy for "the running Codex version" — the adapter captures
// cli_version from each session's session_meta header (parser.go), so the
// newest session reflects the Codex the user is running now.
//
// It resolves the ABSOLUTE sessions dir via codexHome rather than sessionsDir:
// sessionsDir returns a $HOME-relative path (".codex/sessions") when CODEX_HOME
// is unset (its home expansion happens downstream in fswatcher), which would
// make this walk run against the daemon's CWD and always find nothing.
func newestObservedCLIVersion() string {
	home, err := codexHome()
	if err != nil {
		return ""
	}
	dir := filepath.Join(home, "sessions")
	var newestPath string
	var newestMod int64
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".jsonl") {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if mod := info.ModTime().UnixNano(); mod > newestMod {
			newestMod = mod
			newestPath = path
		}
		return nil
	})
	if newestPath == "" {
		return ""
	}
	payload := sessionMetaPayload(newestPath)
	if payload == nil {
		return ""
	}
	v, _ := payload["cli_version"].(string)
	return v
}

// --- helpers ---

// codexHome resolves the absolute ~/.codex directory, honoring an absolute
// CODEX_HOME override (mirroring sessionsDir's resolution), else $HOME/.codex.
func codexHome() (string, error) {
	if h := os.Getenv(codexHomeEnvVar); filepath.IsAbs(h) {
		return h, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".codex"), nil
}

func codexHooksPath() (string, error) {
	home, err := codexHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "hooks.json"), nil
}

func readCodexHooks(path string) (map[string]interface{}, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]interface{}{}, nil
	}
	if err != nil {
		return nil, err
	}
	// An empty file is valid (nothing to merge onto yet).
	if len(strings.TrimSpace(string(data))) == 0 {
		return map[string]interface{}{}, nil
	}
	var settings map[string]interface{}
	if err := json.Unmarshal(data, &settings); err != nil {
		return nil, err
	}
	return settings, nil
}

func writeCodexHooks(path string, settings map[string]interface{}) error {
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return atomicWriteFile(path, data)
}

// atomicWriteFile writes data to path via a temp file + rename so a reader (or
// Codex) never observes a half-written hooks.json. Creates the parent dir.
func atomicWriteFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".hooks-*.json.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
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

// hasOurHook reports whether any matcher group carrying our sentinel already
// exists in the event's hook array (matcher-agnostic).
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

// upgradeStaleHookCommand rewrites any sentinel-bearing inner hook whose command
// differs from the current canonical one (e.g. after a curl-flag change) to
// ourHookEntry, in place. Returns true if any entry was rewritten.
func upgradeStaleHookCommand(hooksMap map[string]interface{}, event string) bool {
	arr, ok := hooksMap[event].([]interface{})
	if !ok {
		return false
	}
	upgraded := false
	for _, g := range arr {
		if upgradeGroupHookCommand(g) {
			upgraded = true
		}
	}
	return upgraded
}

func upgradeGroupHookCommand(g interface{}) bool {
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

// upgradeStaleHookMatchers reconciles the matcher of any sentinel-bearing group
// with the expected value (used if an event's matcher changes). For Stop
// (expected empty) it strips any matcher key entirely.
func upgradeStaleHookMatchers(hooksMap map[string]interface{}, event, expected string) bool {
	arr, ok := hooksMap[event].([]interface{})
	if !ok {
		return false
	}
	upgraded := false
	for _, g := range arr {
		group, ok := g.(map[string]interface{})
		if !ok || !containsHookSentinel(g) {
			continue
		}
		if reconcileGroupMatcher(group, expected) {
			upgraded = true
		}
	}
	return upgraded
}

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

// addOurHook appends a matcher group with our hook entry to the event's array.
// The matcher key is omitted entirely for an empty matcher (Stop).
func addOurHook(hooksMap map[string]interface{}, event, matcher string) {
	entry := map[string]interface{}{
		"hooks": []interface{}{ourHookEntry()},
	}
	if matcher != "" {
		entry["matcher"] = matcher
	}

	if existing, ok := hooksMap[event].([]interface{}); ok {
		hooksMap[event] = append(existing, entry)
		return
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

// containsHookSentinel reports whether a matcher group contains an inner hook
// entry carrying our sentinel.
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
// identified by our sentinel appearing in its curl command.
func hookEntryIsSentinel(hook map[string]interface{}) bool {
	cmd, ok := hook["command"].(string)
	return ok && strings.Contains(cmd, hookSentinel)
}

// hookEntryIsCanonical reports whether an inner hook object already matches the
// current canonical form: type "command" with our exact delivery command. The
// timeout value is deliberately excluded from the identity check so tuning
// hookTimeoutSeconds never forces a churny rewrite of every existing install.
func hookEntryIsCanonical(hook map[string]interface{}) bool {
	t, _ := hook["type"].(string)
	cmd, _ := hook["command"].(string)
	return t == "command" && cmd == hookDeliveryCommand
}
