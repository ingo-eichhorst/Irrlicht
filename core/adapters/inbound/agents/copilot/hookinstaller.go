// hookinstaller.go manages GitHub Copilot CLI hook entries in
// $COPILOT_HOME/hooks/irrlicht.json for the irrlicht daemon (issue #1378,
// epic #1355 Phase D).
//
// Copilot reads every *.json file in its user-level hooks directory and unions
// them — there is no precedence and no shadowing — so irrlicht owns one
// dedicated file rather than merging into a shared config. A malformed write
// can therefore never break a hook the user authored themselves, and uninstall
// is a removal from a file nothing else writes.
//
// Delivery is Copilot's native `type: "http"` hook: no shell spawn, no curl on
// PATH, no beacon. The one cost is an environment variable — see
// RequiresLocalhostOptIn below, which is the finding this phase turned up.
package copilot

import (
	"os"
	"path/filepath"

	"irrlicht/core/adapters/inbound/agents/agentpaths"
	"irrlicht/core/adapters/inbound/agents/hookjson"
	"irrlicht/core/domain/agent"
	"irrlicht/core/pkg/daemonaddr"
)

// HookEndpointPath is the daemon's Copilot hook path. Host and port are
// resolved at install time from the daemon's own bind address, so a daemon on
// an alternate port installs hooks that reach it rather than whatever owns
// :7837 (issue #1178). Exported so the daemon's route comes from the same
// constant the installer writes and matches on.
const HookEndpointPath = "/api/v1/hooks/copilot"

// hookSentinel identifies irrlicht-managed entries. Deliberately the bare
// endpoint path and therefore port-independent: a sentinel carrying a port
// would stop recognizing our own entries the moment the daemon moves,
// orphaning them and appending a duplicate group instead of upgrading in
// place (issue #1178).
const hookSentinel = HookEndpointPath

// RequiresLocalhostOptIn names the environment variable Copilot requires
// before it will deliver an http hook to a loopback address.
//
// This is NOT one of the two https-enforcement rules the Phase C audit
// reasoned from. Those are narrow: the allowedEnvVars rule only bites when
// allowedEnvVars is set, and the authorization-affecting rule names only
// preToolUse / preMcpToolCall / permissionRequest — none of which we install.
// A third, broader rule sits underneath both: an SSRF guard that refuses every
// http hook whose URL resolves to a loopback, private or link-local address,
// regardless of event or configuration. Verified live against CLI 1.0.78 by
// installing this exact hook file and running a session both ways:
//
//	without the variable, every delivery is refused and logged as
//	  HTTP hook URL "http://127.0.0.1:<port>/api/v1/hooks/copilot" resolves to
//	  blocked address 127.0.0.1. URLs must not target loopback, private, or
//	  link-local addresses.
//	with COPILOT_HOOK_ALLOW_LOCALHOST=1, the same file delivers normally.
//
// The daemon cannot set this for the user: Copilot reads it from its own
// process environment, and irrlicht does not launch Copilot. It therefore has
// to be stated in the consent copy so the user knows the channel is inert
// until they export it.
const RequiresLocalhostOptIn = "COPILOT_HOOK_ALLOW_LOCALHOST"

// minCLIVersion is the lowest Copilot version whose hook event set includes
// every event we install, with the semantics we depend on.
//
// 1.0.26 is the notification event's guarantee boundary: below 1.0.18 there is
// no notification event at all, and 1.0.18–1.0.25 fires it even when no prompt
// was actually shown, which would manufacture false waiting states. From
// 1.0.26 the event fires only when a prompt is really displayed.
const minCLIVersion = "1.0.26"

// hookEventSince records the Copilot version each installed event is proven to
// exist at. minCLIVersion must be >= every value here, which
// AssertHookVersionGate enforces.
//
// Both are pinned to the notification guarantee boundary rather than to a
// separately-established date. Stop (Copilot's Claude-compat alias for its
// native agentStop) was verified live only on 1.0.78, the version installed on
// the machine this was developed against; that it also exists at 1.0.26 is
// ASSUMED, not measured. The assumption is safe in the direction that matters:
// if Stop turned out to be newer than 1.0.26, the gate would let an
// in-between CLI receive an entry it ignores, which costs a missing turn-end
// push and nothing else — the transcript already covers turn end.
var hookEventSince = map[string]string{
	HookNotification: "1.0.26",
	HookStop:         "1.0.26",
}

// installedHookEvents are the Copilot hook events we install handlers for.
//
// Deliberately only two, and deliberately NOT these:
//
//   - preToolUse / permissionRequest are excluded on safety grounds. Both fire
//     on every tool call, the matcher grammar filters on tool name only, and
//     since 1.0.57 a preToolUse hook that merely errors DENIES the tool call
//     (exit 2 denies it explicitly since 1.0.70). Hooking either would break
//     the user's tool calls whenever the daemon is down — a fail-closed hazard
//     irrlicht must never introduce into someone else's agent.
//   - userPromptSubmitted is excluded on evidence. Its envelope carries no
//     transcript path at all, so the receiver would have to reconstruct one to
//     satisfy path confinement, and the domain has no turn-start signal to map
//     it to; the transcript's own user.message already opens the turn.
//   - postToolUse is excluded because we install no hold for it to release —
//     see hooks.go on why the permission prompt is dispatch-only.
var installedHookEvents = []string{
	HookNotification,
	HookStop,
}

// matcherForEvent returns the matcher we install for the given event. Neither
// installed event is tool-scoped, so both take no matcher and hookjson omits
// the key entirely.
func matcherForEvent(string) string { return "" }

// ourHookEntry builds the inner hook object we install: Copilot's native http
// delivery, pointed at the daemon's resolved endpoint.
//
// No timeout key is written. Copilot accepts the entry without one (verified
// live on 1.0.78), and both installed events are structurally non-blocking on
// Copilot's side — they are dispatched fire-and-forget and can never stall a
// turn — so a timeout would bound nothing that needs bounding.
func ourHookEntry() map[string]interface{} {
	return map[string]interface{}{
		"type": "http",
		"url":  hookEndpointURL(),
	}
}

// hookEndpointURL is the daemon endpoint the installed hook posts to.
func hookEndpointURL() string {
	return daemonaddr.LocalURL(HookEndpointPath)
}

// hookConfig describes this adapter's install to the shared hookjson
// machinery: which file to merge into, which sentinel marks our entries, which
// events get installed, and what the canonical inner entry looks like.
//
// Copilot's own documented hook file shape is flat — an event maps straight to
// an array of entries — whereas hookjson writes Claude Code's nested
// matcher-group shape. Copilot accepts both: verified live on 1.0.78 by
// installing exactly what hookjson emits (nested group, and no "version" key,
// which hookjson never writes) and observing the Stop hook deliver. That is
// what lets this adapter reuse hookjson unmodified.
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

// EnsureHooksInstalled adds irrlicht hook entries to
// $COPILOT_HOME/hooks/irrlicht.json if they are not already present. Creates
// the file (and the hooks directory) if absent. Returns true if the file was
// modified.
func EnsureHooksInstalled() (bool, error) {
	path, err := copilotHooksPath()
	if err != nil {
		return false, err
	}
	return hookjson.EnsureInstalled(hookConfig(path))
}

// VerifyHooksInstalled reports which of our entries are missing from, or stale
// in, the hooks file — without writing anything (issue #1372). Same hookConfig
// as the installer, so "stale" means exactly what EnsureHooksInstalled would
// rewrite.
func VerifyHooksInstalled() (agent.HookEntryStatus, error) {
	path, err := copilotHooksPath()
	if err != nil {
		return agent.HookEntryStatus{}, err
	}
	return hookjson.Verify(hookConfig(path))
}

// UninstallHooks removes irrlicht hook entries from the hooks file. Returns
// true if the file was modified.
func UninstallHooks() (bool, error) {
	path, err := copilotHooksPath()
	if err != nil {
		return false, err
	}
	return hookjson.Uninstall(hookConfig(path))
}

// newestObservedCLIVersion returns the copilotVersion recorded in the most
// recently modified Copilot transcript, or "" if none is found. It is the
// install-time proxy for "the running Copilot version": the adapter captures
// copilotVersion from each session's session.start header (parser.go), so the
// newest transcript reflects the CLI the user is running now.
//
// It resolves the ABSOLUTE sessions dir rather than the possibly
// $HOME-relative sessionsDir(), which would make this walk run against the
// daemon's CWD and always find nothing.
func newestObservedCLIVersion() string {
	dir, err := copilotSessionsDir()
	if err != nil {
		return ""
	}
	newestPath := agentpaths.NewestFileWithSuffix(dir, transcriptFilename)
	if newestPath == "" {
		return ""
	}
	return sessionStartVersion(newestPath)
}

// --- helpers ---

// copilotHome resolves the absolute Copilot config directory, honoring an
// absolute COPILOT_HOME override (mirroring sessionsDir's resolution), else
// $HOME/.copilot.
func copilotHome() (string, error) {
	if h := os.Getenv(copilotHomeEnvVar); filepath.IsAbs(h) {
		return h, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".copilot"), nil
}

// copilotSessionsDir resolves the absolute session-state tree with symlinks
// resolved, so containment checks against it compare like with like (on macOS
// both /tmp and a home directory routinely reach the real path through a
// symlink).
//
// Derived from the adapter's own declared root rather than re-assembled from
// constants: that second derivation is exactly what issue #1361 removed from
// the hook receiver, and leaving a copy here would let the tree the installer
// walks drift from the one the daemon watches.
func copilotSessionsDir() (string, error) {
	root, err := agentpaths.AbsRoot(sessionsDir())
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(root)
}

// copilotHooksPath is the dedicated file irrlicht owns inside Copilot's
// user-level hooks directory. Copilot unions every *.json in that directory,
// so owning one file keeps our writes off anything the user authored.
//
// The directory is the same on all three OSes — Copilot does not use a
// platform config dir here — so no per-GOOS branch is needed.
func copilotHooksPath() (string, error) {
	home, err := copilotHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "hooks", "irrlicht.json"), nil
}

// atomicWriteFile writes data to path via a temp file + rename so a reader (or
// Copilot) never observes a half-written hook file. Creates the parent dir.
func atomicWriteFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".irrlicht-hooks-*.json.tmp")
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

// hookEntryIsCanonical reports whether an inner hook object already matches the
// current canonical form: an http entry pointing at our currently resolved
// endpoint. An entry left behind by a daemon on a different port reads as
// stale and is rewritten in place rather than duplicated (#1178).
func hookEntryIsCanonical(hook map[string]interface{}) bool {
	t, _ := hook["type"].(string)
	url, _ := hook["url"].(string)
	return t == "http" && url == hookEndpointURL()
}
