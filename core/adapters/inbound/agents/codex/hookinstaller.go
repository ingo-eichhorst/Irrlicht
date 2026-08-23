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
	"os"
	"path/filepath"

	"irrlicht/core/adapters/inbound/agents/agentpaths"
	"irrlicht/core/adapters/inbound/agents/hookjson"
	"irrlicht/core/domain/agent"
	"irrlicht/core/pkg/atomicfile"
	"irrlicht/core/pkg/daemonaddr"
)

// HookEndpointPath is the daemon's Codex hook path. Host and port are resolved
// at install time from the daemon's own bind address, so a daemon on an
// alternate port installs hooks that reach it rather than whatever owns :7837
// (issue #1178). Exported so the daemon's route comes from the same constant
// the installer writes and matches on — since #1178 this path *is* the
// sentinel, so drift would orphan every existing entry rather than 404.
const HookEndpointPath = "/api/v1/hooks/codex"

// hookSentinel is the substring in a hook entry's curl command that identifies
// irrlicht-managed entries. Used by both install (idempotency) and uninstall.
// Deliberately port-independent: a sentinel carrying a port would stop
// recognizing our own entries the moment the daemon moves, orphaning them and
// appending a duplicate group instead of upgrading in place. As the bare
// endpoint path it still matches a pre-#1178 :7837 command.
const hookSentinel = HookEndpointPath

// hookEndpointURL is the daemon endpoint the installed hook posts to. Codex
// execs the curl command below with the hook payload on stdin (Codex has no
// native http-delivery hook like Claude Code's, so a curl command is used).
func hookEndpointURL() string {
	return daemonaddr.LocalURL(HookEndpointPath)
}

// hookDeliveryCommand is the shell command Codex runs for each installed hook.
// It streams the payload (stdin, @-) to the daemon and never fails the turn
// (|| true): a down/unreachable daemon fails the connection fast, well under
// the --max-time ceiling, and the transcript path still covers turn-end.
func hookDeliveryCommand() string {
	return "curl -fsS --max-time 1 -X POST --data-binary @- " + hookEndpointURL() + " || true"
}

// hookTimeoutSeconds bounds how long Codex waits on the hook command. The
// daemon's handler is near-instant (a map write plus a channel send); this is
// a safety ceiling against a wedged daemon.
const hookTimeoutSeconds = 5

// hookMatcher matches every tool for the PermissionRequest/PostToolUse events.
// PermissionRequest only fires for tools that actually need approval, so a wide
// matcher is a no-op there; PostToolUse must match all tools so the
// permission-pending overlay is always cleared once an approved tool runs.
const hookMatcher = ".*"

// minCLIVersion is the lowest Codex version whose hook event set includes
// every event we install. Declared here and enforced generically by
// PermissionService via agent.ManagedUserFile.Version (issue #1365), replacing the
// adapter-private codexSupportsHooks/parseCodexVersion pair this file used to
// carry — the thing a third adapter would otherwise have copied.
const minCLIVersion = "0.114.0"

// hookEventSince records the Codex version each event we install arrived in.
// It is the machine-checkable form of "do not write an entry the installed CLI
// does not know" (#1365 scope item 2): minCLIVersion must be >= every value
// here, which AssertHookVersionGate enforces, so at any version where we
// install at all, every event we write is one the CLI understands. Adding an
// event that landed later than minCLIVersion fails that test rather than
// shipping an entry an in-range CLI would reject.
//
// All three arrived together with the experimental hooks feature in
// rust-v0.114.0 (~March 2026, issue #1171); Codex has no separately-dated hook
// events yet.
var hookEventSince = map[string]string{
	HookPermissionRequest: "0.114.0",
	HookPostToolUse:       "0.114.0",
	HookStop:              "0.114.0",
}

// installedHookEvents are the Codex hook events we install handlers for. Codex
// has no PostToolUseFailure event, so PostToolUse is the sole pending-clear
// signal; PreCompact/PostCompact are deferred (only needed if Codex grows a
// transcript-goes-silent-during-compaction problem like Claude Code's #657).
var installedHookEvents = []string{
	HookPermissionRequest,
	HookPostToolUse,
	HookStop,
}

// matcherForEvent returns the matcher we install for the given event. Stop
// takes no matcher (it fires at every turn end); hookjson omits the matcher
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
		"command": hookDeliveryCommand(),
		"timeout": hookTimeoutSeconds,
	}
}

// hookConfig describes this adapter's install to the shared hookjson
// machinery (issue #1179): which file to merge into, which sentinel marks our
// entries, which events get installed with which matcher, and what the
// canonical inner entry looks like. Everything the two JSON-hook adapters have
// in common — the merge, idempotency, in-place upgrade and removal logic —
// lives there; everything Codex-specific stays here.
func hookConfig(path string) hookjson.Config {
	return hookjson.Config{
		Path:        path,
		Sentinel:    hookSentinel,
		Events:      installedHookEvents,
		MatcherFor:  matcherForEvent,
		Entry:       ourHookEntry,
		IsCanonical: hookEntryIsCanonical,
		WriteFile:   atomicfile.WriteFile,
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
	return hookjson.EnsureInstalled(hookConfig(path))
}

// VerifyHooksInstalled reports which of our entries are missing from, or stale
// in, ~/.codex/hooks.json — without writing anything (issue #1372). Same
// hookConfig as the installer, so "stale" means exactly what
// EnsureHooksInstalled would rewrite.
func VerifyHooksInstalled() (agent.HookEntryStatus, error) {
	path, err := codexHooksPath()
	if err != nil {
		return agent.HookEntryStatus{}, err
	}
	return hookjson.Verify(hookConfig(path))
}

// UninstallHooks removes irrlicht hook entries from ~/.codex/hooks.json.
// Returns true if the file was modified, false if no hooks were found.
func UninstallHooks() (bool, error) {
	path, err := codexHooksPath()
	if err != nil {
		return false, err
	}
	return hookjson.Uninstall(hookConfig(path))
}

// newestObservedCLIVersion returns the cli_version recorded in the most
// recently modified Codex session file, or "" if none is found. It is the
// install-time proxy for "the running Codex version" — the adapter captures
// cli_version from each session's session_meta header (parser.go), so the
// newest session reflects the Codex the user is running now.
//
// It resolves the ABSOLUTE sessions dir via codexSessionsDir rather than
// sessionsDir: sessionsDir returns a $HOME-relative path (".codex/sessions")
// when CODEX_HOME is unset (its home expansion happens downstream in
// fswatcher), which would make this walk run against the daemon's CWD and
// always find nothing.
func newestObservedCLIVersion() string {
	dir, err := codexSessionsDir()
	if err != nil {
		return ""
	}
	newestPath := agentpaths.NewestFileWithSuffix(dir, ".jsonl")
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

// codexSessionsDir resolves the absolute sessions tree — $CODEX_HOME/sessions
// when that override is set, else ~/.codex/sessions — with symlinks resolved so
// that containment checks against it compare like with like (on macOS both
// /tmp and a home directory routinely reach the real path through a symlink).
// It reports an error when the tree does not exist, which is the honest answer
// for both callers: nothing to walk, and nothing a transcript could sit inside.
func codexSessionsDir() (string, error) {
	// Derived from the adapter's own declared root, not re-assembled from
	// ".codex" + "sessions": that second derivation is exactly what issue #1361
	// removed from the hook receiver, and leaving a copy here would let the
	// tree the installer walks drift from the one the daemon watches.
	root, err := agentpaths.AbsRoot(sessionsDir())
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(root)
}

func codexHooksPath() (string, error) {
	home, err := codexHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "hooks.json"), nil
}

// hookEntryIsCanonical reports whether an inner hook object already matches the
// current canonical form: type "command" with our exact delivery command. The
// timeout value is deliberately excluded from the identity check so tuning
// hookTimeoutSeconds never forces a churny rewrite of every existing install.
//
// The command comparison is against the *currently resolved* endpoint, so an
// entry left behind by a daemon on a different port reads as stale and is
// rewritten in place by upgradeStaleHookCommand (#1178).
func hookEntryIsCanonical(hook map[string]interface{}) bool {
	t, _ := hook["type"].(string)
	cmd, _ := hook["command"].(string)
	return t == "command" && cmd == hookDeliveryCommand()
}
