// hookinstaller.go manages Gemini CLI hook entries in the user's own
// ~/.gemini/settings.json for the irrlicht daemon (issue #1717, epic #1355
// Phase D).
//
// Unlike claudecode's and codex's own settings files, ~/.gemini/settings.json
// is Gemini CLI's ONE general-purpose settings file — theme, auth, and every
// other user preference live in it alongside "hooks" — so this installer
// follows claudecode's shared-file shape (merge our "hooks" key into the
// document, leave every other key untouched, never delete the file) rather
// than copilot's dedicated-file shape.
//
// Delivery is the beacon (core/pkg/hookbeacon), not a native http hook and not
// a bare curl line, and that is not a preference — it is what the Phase 1
// audit for this issue established from source. Gemini CLI's HookType is
// {Command, Runtime} (source-read from the installed CLI's own bundle:
// validateHookConfig accepts only "command", "plugin" and "runtime" — there is
// no JSON-expressible "http"), so a shell command is the only delivery this
// CLI can be given. And source-read from the same bundle,
// convertPlainTextToHookOutput turns ANY exit code other than 0 or 1 into
// decision "deny" for a hook that returned no parseable JSON, which
// evaluateBeforeToolHook and fireBeforeAgentHookSafe then turn into a blocked
// tool call or a blocked turn — so a bare `curl` against a daemon that is not
// running (exit 7) would not merely fail to report state, it would break the
// user's tool calls. hookbeacon's `irrlichd hook-post gemini-cli` always exits
// 0, by construction (core/pkg/hookbeacon's own package doc has the full
// argument, verified against gemini-cli specifically in this issue's audit
// rather than assumed from Codex's or Copilot's shape).
package geminicli

import (
	"path/filepath"

	"irrlicht/core/adapters/inbound/agents/agentpaths"
	"irrlicht/core/adapters/inbound/agents/hookjson"
	"irrlicht/core/domain/agent"
	"irrlicht/core/pkg/atomicfile"
	"irrlicht/core/pkg/hookbeacon"
)

// geminiHomeDirName is the $HOME-relative Gemini CLI config directory. Gemini
// CLI has no home-override environment variable — source-read:
// Storage.getGlobalGeminiDir() resolves via Node's plain os.homedir(), not an
// env var this adapter could honor — which is also why sessionsDir() in
// adapter.go reads no environment either.
const geminiHomeDirName = ".gemini"

// geminiSettingsFilename is Gemini CLI's one general-purpose settings file —
// theme, auth, security and hooks all live in the same document.
const geminiSettingsFilename = "settings.json"

// HookEndpointPath is the daemon's Gemini CLI hook route. Exported so
// registerHookRoutes (core/cmd/irrlichd/startup.go) and
// beaconroute_test.go's pinned map both derive from the same segment this
// installer renders into the config, per hookbeacon's install.go doc: "A
// mismatch is a permanent silent 404: the beacon exits 0 by design, so
// nothing anywhere reports it."
//
// AdapterName ("gemini-cli") is used directly as the beacon segment — unlike
// claudecode, which has a historical route segment ("claudecode") distinct
// from its AdapterName ("claude-code"), this adapter has never installed
// under any other spelling, and "gemini-cli" is itself a valid beacon segment
// (hookbeacon's adapterSegment grammar allows hyphens).
//
// A var, not a const: hookbeacon.EndpointPath composes the route from its own
// unexported prefix, so deriving it here (rather than duplicating that prefix
// as a second literal) is what keeps this constant from being able to drift
// from the beacon's own routing convention.
var HookEndpointPath = hookbeacon.EndpointPath(AdapterName)

// minCLIVersion is the lowest Gemini CLI version this adapter's install
// requires.
//
// NOT the date the hook system was introduced upstream — PR archaeology
// (google-gemini/gemini-cli #9074-#9112, all merged 2025-09-22) puts the
// BeforeTool/AfterTool/BeforeAgent/AfterAgent/Notification event set at
// roughly that date, with the first stable release after it 0.6.0
// (2025-09-24). This floor is deliberately more conservative than that
// evidence supports, because bisecting an old release for the exact payload
// shape turned out to be unreliable: those 2025-era npm packages ship a
// completely different (unbundled dist/src) layout than the single-file
// bundle the CLI ships today, so a file-presence diff across that packaging
// change proves nothing.
//
// What IS directly verified, with zero drift across an upgrade that actually
// happened on the dev machine mid-audit, is the exact behavior this adapter
// depends on: 0.54.4 (source-read
// at the start of this issue's audit) and 0.56.0 (source-read again after the
// installed CLI auto-updated mid-audit) — validateHookConfig's accepted
// types, the exit-code-to-decision mapping, fireAfterAgentHookSafe's
// dedup/pending-tool gate, resolveConfirmation's unconditional pre-block
// notifyHooks call, and the NotificationType/HookEventName enums are
// byte-identical between them. Note the honest limit of that window rather
// than overstating it: npm publishes those two on 2026-08-07 and 2026-08-19
// (`npm view @google/gemini-cli time`), so it spans TWELVE DAYS and two minor
// releases — evidence that the shape survived a real upgrade, not evidence
// that it is long-settled. 0.54.0 is the edge of that verified window,
// not the edge of "some form of hooks existed" — overshooting a version
// floor is the safe direction (core/pkg/cliversion fails OPEN on an unknown
// or unparseable version), undershooting is not, so the floor sits at the
// boundary of what was actually read rather than at the boundary PR dates
// alone would justify.
const minCLIVersion = "0.54.0"

// hookEventSince records the Gemini CLI version each installed event is
// proven (by the same source read, not a separate per-event date) to exist
// with the payload shape this adapter depends on. minCLIVersion must be >=
// every value here, which AssertHookVersionGate enforces. All three share one
// value for the reason given on minCLIVersion: the evidence is one source
// read covering all three events together, not three independent dates.
var hookEventSince = map[string]string{
	HookNotification: minCLIVersion,
	HookAfterTool:    minCLIVersion,
	HookAfterAgent:   minCLIVersion,
}

// installedHookEvents are the Gemini CLI hook events we install handlers for
// — three of the eleven the CLI's hook system fires.
//
// Deliberately NOT these:
//
//   - BeforeTool is excluded on safety grounds, source-verified rather than
//     assumed: it fires for EVERY tool call with no discriminator separating
//     "about to run silently" from "about to block on the user" (its payload
//     is tool_name/tool_input and nothing else), and a hook that errors on it
//     turns into a denied tool call (evaluateBeforeToolHook). Notification,
//     gated on notification_type=="ToolPermission", gives the same
//     information as a real, narrow signal instead.
//   - SessionStart/SessionEnd/PreCompress/BeforeModel/AfterModel/
//     BeforeToolSelection are excluded on evidence: none of them has a
//     stated payoff for irrlicht's three-state model (working/waiting/ready)
//     beyond what process discovery and transcript tailing already give, and
//     BeforeModel/AfterModel additionally carry full LLM request/response
//     bodies — blast radius with no offsetting benefit. SessionStart and
//     SessionEnd were the two events this issue's live audit could actually
//     fire (everything else needed a working model call, which the
//     account's auth was ineligible for) — that they were observable is not
//     itself a reason to install them, and no other justification was found.
var installedHookEvents = []string{
	HookNotification,
	HookAfterTool,
	HookAfterAgent,
}

// matcherForEvent returns the matcher we install for the given event. None of
// the three installed events is tool-scoped in a way we want narrowed at the
// CLI config level — AfterTool must fire for every tool (it is the broad
// release side) and Notification's ToolPermission discriminator lives in the
// payload's notification_type field, not in anything Gemini's matcher grammar
// filters on. So every event takes no matcher, and hookjson omits the key
// entirely — the same choice copilot makes for the same reason.
//
// Filtering happens adapter-side instead (handleNotificationHook), the same
// defense-in-depth claudecode's handlers apply on top of their own narrower
// matchers: this receiver would still filter correctly even if Gemini CLI's
// matcher grammar turned out to support a notification_type filter after all.
func matcherForEvent(string) string { return "" }

// ourHookEntry builds the inner hook object we install for the currently
// running daemon: a beacon command, never a curl line and never Gemini's
// native http type (which does not exist for this CLI — see the package
// doc).
func ourHookEntry() map[string]interface{} {
	command, _ := hookbeacon.InstalledCommand(AdapterName)
	return beaconEntry(command)
}

// beaconEntry is the beacon inner hook object for a given, already-resolved
// command line. Split from ourHookEntry so hookConfig can close over a command
// resolved ONCE per entry point rather than re-resolving (and re-swallowing
// any error) on every Entry() call — the same shape
// hook_endpoint_addressfree_test.go's reference wiring uses, and the reason
// hookbeacon.InstalledCommand exists as a single fallible step rather than
// BinaryPath+Command composed at every call site (measured at +12 lines when
// copilot evaluated beacon delivery, per hookbeacon's own doc).
func beaconEntry(command string) map[string]interface{} {
	return map[string]interface{}{
		"type":    "command",
		"command": command,
	}
}

// hookEntryIsCanonical reports whether an inner hook object matches the
// canonical beacon command for the currently running daemon — hookbeacon.
// IsCanonical rewrites in place an entry naming a moved or missing binary
// (#1373's DriftBinaryPath/DriftBinaryMissing), the beacon's equivalent of
// claudecode's/codex's own port-staleness rewrite.
func hookEntryIsCanonical(hook map[string]interface{}) bool {
	command, _ := hook["command"].(string)
	return hookbeacon.IsCanonical(command, AdapterName)
}

// hookConfig describes this adapter's install to the shared hookjson
// machinery, for a command already resolved by the caller (see beaconEntry).
func hookConfig(path, command string) hookjson.Config {
	return hookjson.Config{
		Path:        path,
		Sentinel:    hookbeacon.Sentinel(AdapterName),
		Events:      installedHookEvents,
		MatcherFor:  matcherForEvent,
		Entry:       func() map[string]interface{} { return beaconEntry(command) },
		IsCanonical: hookEntryIsCanonical,
		WriteFile:   atomicfile.WriteFile,
	}
}

// EnsureHooksInstalled adds irrlicht hook entries to ~/.gemini/settings.json
// if they are not already present. Every other key in that file — theme,
// security.auth, whatever else the user or Gemini CLI itself has written — is
// left byte-for-byte alone; hookjson merges only the "hooks" key. Returns true
// if the file was modified.
func EnsureHooksInstalled() (bool, error) {
	path, err := geminiSettingsPath()
	if err != nil {
		return false, err
	}
	command, err := hookbeacon.InstalledCommand(AdapterName)
	if err != nil {
		return false, err
	}
	return hookjson.EnsureInstalled(hookConfig(path, command))
}

// VerifyHooksInstalled reports which of our entries are missing from, or
// stale in, ~/.gemini/settings.json — without writing anything (issue #1372).
// This is what makes gemini-cli's sync-by-omission settings writer survivable
// (see agent.go's consent copy and #1355's Phase C audit finding): if Gemini
// CLI's own settings UI later rewrites the file and drops our entries,
// services.HookEntryVerifier reads this on a timer and repairs through
// PermissionService rather than silently reading "granted" over a dead
// install.
func VerifyHooksInstalled() (agent.HookEntryStatus, error) {
	path, err := geminiSettingsPath()
	if err != nil {
		return agent.HookEntryStatus{}, err
	}
	command, err := hookbeacon.InstalledCommand(AdapterName)
	if err != nil {
		return agent.HookEntryStatus{}, err
	}
	return hookjson.Verify(hookConfig(path, command))
}

// UninstallHooks removes irrlicht hook entries from ~/.gemini/settings.json.
// Returns true if the file was modified.
//
// Unlike copilot's dedicated hooks/irrlicht.json, this file is the user's
// general Gemini CLI settings — theme, auth, everything else lives here too —
// so uninstall never removes the file itself, only our "hooks" key's
// entries, mirroring claudecode's own shared-settings uninstall exactly.
func UninstallHooks() (bool, error) {
	path, err := geminiSettingsPath()
	if err != nil {
		return false, err
	}
	command, err := hookbeacon.InstalledCommand(AdapterName)
	if err != nil {
		return false, err
	}
	return hookjson.Uninstall(hookConfig(path, command))
}

// --- helpers ---

// geminiHome resolves the absolute Gemini CLI config directory: $HOME/.gemini.
// No env-override to honor (see geminiHomeDirName).
func geminiHome() (string, error) {
	return agentpaths.AbsRoot(geminiHomeDirName)
}

// geminiSettingsPath is the user's own Gemini CLI settings file — the
// agent.ManagedUserFile.Path this permission declares.
func geminiSettingsPath() (string, error) {
	home, err := geminiHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, geminiSettingsFilename), nil
}
