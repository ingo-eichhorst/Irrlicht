// hookinstaller.go installs irrlicht's pi extension — the ONE file this
// adapter writes into a user's pi installation (issue #1721, epic #1355).
//
// # Why this adapter ships code where every other one ships a config entry
//
// pi has no config-file hook mechanism. Measured against the installed
// package (@earendil-works/pi-coding-agent 0.83.0), not against rendered
// docs: `hook` does not appear anywhere in docs/settings.md, settings.json
// has no hooks key, and the only lifecycle subscription surface pi exposes
// is `pi.on(...)` inside an extension module. So the choice for pi is not
// "config entry vs. code" — it is "code, or leave pi at epic #1355's tier
// 5 with no authoritative turn-end signal at all".
//
// That makes this the first adapter where irrlicht installs executable code
// into a user's agent, which epic #1355 cut opencode (#1719) over. The
// distinguishing facts, all read out of the installed package's own source
// rather than its documentation:
//
//   - **No package manager is involved.** pi auto-discovers every *.ts and
//     *.js file directly under `<agent dir>/extensions/`
//     (dist/core/extensions/loader.js: `discoverExtensionsInDir(path.join(
//     resolvedAgentDir, "extensions"))`, gated by `isExtensionFile`, which
//     matches BOTH extensions). `pi install` — the npm:/git:/https:/./local
//     route the issue was filed around — writes a `packages` or `extensions`
//     entry to settings.json and is a SECOND, separate mechanism. This
//     installer does not use it, does not touch settings.json, and does not
//     resolve, fetch, build or version any package. It writes one file.
//
//   - **No toolchain.** The artifact is plain JavaScript with one import of
//     `node:child_process`. pi loads extensions through jiti, so TypeScript
//     would need no compile step either — but shipping .js means there is no
//     transform between the bytes irrlicht writes and the code that runs.
//
//   - **Uninstall is `os.Remove`.** Nothing else was touched, so there is
//     nothing else to undo, and no `pi remove` to be idempotent about.
//
//   - **Blast radius is bounded by pi itself.** loader.js wraps the whole
//     load in try/catch and reports a failing extension as a load error
//     while the agent continues; runner.js wraps every handler dispatch the
//     same way — for every event EXCEPT tool_call, which is why extension.js
//     never subscribes to that one and extension_test.go pins it.
//
// The honest residue, stated in the consent copy rather than only here: it
// is still code, running in-process in the user's agent with the user's own
// permissions, and #570's contract is what makes that the user's decision.
//
// # Delivery
//
// Address-free (contracttesting.DeliveryAddressFree): the file names the
// `irrlichd hook-post pi` beacon (core/pkg/hookbeacon), which reads the
// daemon's published address at fire time. Nothing address-shaped is written
// into the user's pi installation, so the #1178 stale-port class is
// inexpressible here rather than fixed once more. It also makes the shipped
// artifact smaller, which is the whole argument above: the JavaScript does
// not speak HTTP, does not parse an addr file, and does not know what a port
// is — it spawns one command and writes JSON to its stdin.
//
// #1721 records the beacon as "built, contract-covered, and adopted by
// nobody". That was true when #1373 landed and is not true now — geminicli
// (#1717), kirocli (#1716) and vibe (#1718) all declare
// DeliveryAddressFree, as docs/testing-contracts.md's delivery bullet
// already says. pi is the fourth adopter, not the first, and the reason to
// pick the route here is its own rather than "nobody has": what it removes
// is a chunk of the SHIPPED ARTIFACT. A URL-carrying install would need the
// JavaScript to speak HTTP and to be rewritten in the user's agent every
// time the daemon's port changed; naming the beacon means the file spawns
// one command and knows nothing about addresses.
package pi

import (
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"irrlicht/core/adapters/inbound/agents/agentpaths"
	"irrlicht/core/domain/agent"
	"irrlicht/core/pkg/atomicfile"
	"irrlicht/core/pkg/hookbeacon"
)

// HookEventAgentSettled is pi's own wire name for the one event this adapter
// subscribes to. pi emits it when the agent will not continue running on its
// own — no retry, no auto-compaction, no queued follow-up left
// (docs/extensions.md: "Use agent_settled for status integrations that need
// to know Pi will not continue running automatically"), which is the same
// authoritative turn-end claim Claude Code's Stop makes.
//
// It is NOT mapped onto session.HookStop or any other existing row in
// session.hookSignalEffects, and does not need to be: the receiver calls
// SessionDetector.HandleStopHook directly, exactly as vibe's single event
// does, so no name-keyed lookup is involved on this path.
const HookEventAgentSettled = "agent_settled"

// installedHookEvents is every pi lifecycle event this adapter subscribes
// to. Exactly one, and the exclusions are the finding rather than an
// omission:
//
//   - tool_call is excluded on a source fact, not a preference: pi's
//     runner dispatches it WITHOUT the try/catch every other event gets, and
//     a handler returning {block: true} blocks the user's tool call. A
//     monitoring extension must not be able to break a coding session.
//
//   - tool_execution_start / tool_execution_end were considered as the
//     assert/release pair gemini-cli's BeforeTool/AfterTool and kiro-cli's
//     preToolUse/postToolUse form, and rejected for the reason vibe's
//     hooks.go already records for its own after_tool: a broad RELEASE is
//     only useful paired with a narrow ASSERT on the same signal from the
//     same adapter. pi has no approval prompt to assert on — it has no
//     sandbox at all (pi's docs/security.md: tools run with the process's
//     permissions), and repo-wide session.SignalPermissionPrompt is held
//     only by hook-derived calls. Installing them would spawn a beacon per
//     tool call to release a signal nothing ever holds.
//
//   - session_start / session_shutdown / turn_start / turn_end / input are
//     excluded on the same evidence copilot's, gemini-cli's and kiro-cli's
//     analogous exclusions cite: the transcript's own lines already open the
//     turn and already record the session, so a hook duplicating them buys
//     nothing irrlicht's three-state model needs, at the cost of a process
//     spawn each.
//
//   - project_trust is pi's ONE genuine blocked-on-user prompt and is
//     excluded because it CANNOT be correlated to a session — see
//     hooks.go's header for the measurement.
var installedHookEvents = []string{HookEventAgentSettled}

// hookEventSince records the pi version each installed event first shipped
// in, read from the package's own CHANGELOG.md: agent_settled landed in
// 0.80.4 (2026-07-09, "Added extension and RPC `agent_settled` events plus
// session-level idle waiting for fully settled agent runs"). minPiVersion
// must be >= every value here, which AssertHookVersionGate enforces.
var hookEventSince = map[string]string{
	HookEventAgentSettled: "0.80.4",
}

// minPiVersion is the lowest pi version this adapter's install requires.
//
// Pinned to the version everything here was measured against — 0.83.0, live,
// in an isolated PI_CODING_AGENT_DIR — rather than to the 0.80.4 the
// CHANGELOG dates agent_settled to. The floor is not a claim about one
// event: it covers `.js` auto-discovery (`isExtensionFile`), the extensions
// directory layout, the per-handler try/catch, and the shape of
// ctx.sessionManager.getSessionFile() on agent_settled — none of which was
// bisected backward. core/pkg/cliversion fails OPEN on an unknown or
// unparseable version, so overshooting costs a missing install on an old CLI
// rather than a wrong one on a new CLI, which is the safe direction (the
// same reasoning kiro-cli's and gemini-cli's floors document).
const minPiVersion = "0.83.0"

// HookEndpointPath is the daemon's pi hook route. Exported so
// registerHookRoutes (core/cmd/irrlichd/startup.go) and beaconroute_test.go's
// pinned map both derive from the same segment this installer renders into
// the beacon command it writes.
//
// A var, not a const, because hookbeacon.EndpointPath composes the route from
// its own unexported prefix — deriving it here keeps this constant from
// drifting away from the beacon's own routing convention. A mismatch is a
// permanent silent 404: the beacon exits 0 by design, so nothing anywhere
// reports it.
var HookEndpointPath = hookbeacon.EndpointPath(AdapterName)

// extensionsDirName is the pi-agent-dir-relative directory pi auto-discovers
// extensions from (loader.js's discoverAndLoadExtensions).
const extensionsDirName = "extensions"

// extensionFilename is the file this adapter owns inside that directory. The
// .js suffix is load-bearing twice over: pi's isExtensionFile accepts only
// .ts and .js, and .js is the spelling that involves no transform between
// the bytes written here and the code pi runs.
const extensionFilename = "irrlicht.js"

// displayExtensionPath is the consent copy's home-relative spelling of the
// file ExtensionPath resolves absolutely.
const displayExtensionPath = "~/.pi/agent/extensions/irrlicht.js"

//go:embed extension.js
var extensionTemplate string

// beaconCommandPlaceholder is the token renderExtension substitutes. It
// appears exactly once in extension.js, inside a JavaScript string literal,
// and extension_test.go fails if that stops being true — a template whose
// placeholder silently stopped matching would render a file that runs the
// literal placeholder as a shell command.
const beaconCommandPlaceholder = `"__IRRLICHT_BEACON_COMMAND__"`

// beaconLineRE reads the rendered command back out of an installed file. It
// anchors on the whole assignment line rather than searching for the command
// text, so a command that happens to appear in a comment cannot be mistaken
// for the live one.
var beaconLineRE = regexp.MustCompile(`(?m)^const IRRLICHT_BEACON = (".*");$`)

// renderExtension produces the exact bytes to install for a given,
// already-resolved beacon command.
//
// strconv.Quote, not fmt.Sprintf("%q") by habit and not string concatenation:
// the command embeds an absolute filesystem path the user chose, and a path
// containing a quote or a backslash must not be able to terminate the
// JavaScript string literal it lands in. Go's quoted-string escapes are a
// subset of JavaScript's for every byte Quote can emit.
func renderExtension(command string) ([]byte, error) {
	if command == "" {
		return nil, errors.New("pi: refusing to render an extension with an empty beacon command")
	}
	if !strings.Contains(extensionTemplate, beaconCommandPlaceholder) {
		// The embedded template stopped carrying the token this function
		// substitutes. Failing loudly here is the difference between "no
		// install" and "an install that shells out to the placeholder".
		return nil, fmt.Errorf("pi: embedded extension template no longer contains %s", beaconCommandPlaceholder)
	}
	return []byte(strings.Replace(extensionTemplate, beaconCommandPlaceholder, strconv.Quote(command), 1)), nil
}

// beaconCommandOf extracts the beacon command an installed file carries.
// Reports false for a file that is not ours, or whose assignment line has
// been edited into something that is not a quoted string.
func beaconCommandOf(src []byte) (string, bool) {
	m := beaconLineRE.FindSubmatch(src)
	if m == nil {
		return "", false
	}
	command, err := strconv.Unquote(string(m[1]))
	if err != nil {
		return "", false
	}
	return command, true
}

// isOurs reports whether a file on disk is an irrlicht-installed extension —
// by the beacon sentinel, which deliberately excludes the binary path, so a
// copy naming an irrlichd that has since moved is still recognized as ours.
// Recognizing it is the precondition for rewriting it in place instead of
// appending a second one beside it.
func isOurs(src []byte) bool {
	return strings.Contains(string(src), hookbeacon.Sentinel(AdapterName))
}

// --- path resolution ---

// agentDirEnvVar is pi's own override for its agent config directory. pi
// resolves it in dist/config.js's getAgentDir(), which every other agent-dir
// path — sessions/, settings.json, extensions/ — is composed from.
const agentDirEnvVar = "PI_CODING_AGENT_DIR"

// defaultAgentDirName is the $HOME-relative pi agent directory.
const defaultAgentDirName = ".pi/agent"

// piAgentDir resolves the absolute pi agent directory the way pi's own
// getAgentDir does: $PI_CODING_AGENT_DIR when set and absolute, else
// $HOME/.pi/agent.
func piAgentDir() (string, error) {
	return agentpaths.AbsRoot(agentpaths.FromEnv("pi", agentDirEnvVar, defaultAgentDirName))
}

// ExtensionPath is the single file this permission's Writes.Path resolves —
// the extension pi auto-discovers and runs.
func ExtensionPath() (string, error) {
	dir, err := piAgentDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, extensionsDirName, extensionFilename), nil
}

// --- install / verify / uninstall ---

// EnsureHooksInstalled writes irrlicht's pi extension for the irrlichd that
// is running now, creating pi's extensions directory if it does not exist.
// Returns true if anything changed.
//
// Idempotent, and self-healing across a moved binary: an already-installed
// copy whose beacon command names a different irrlichd is rewritten IN PLACE
// (same path, one file), never duplicated.
func EnsureHooksInstalled() (bool, error) {
	command, err := hookbeacon.InstalledCommand(AdapterName)
	if err != nil {
		return false, err
	}
	return ensureInstalledWithCommand(command)
}

// ensureInstalledWithCommand is EnsureHooksInstalled with the beacon command
// resolved by the caller — the seam the #1178 contract's ForeignInstall uses
// to seed the install a DIFFERENTLY-situated daemon would have written.
// hookbeacon.InstalledCommand is called exactly once per entry point, which
// is the shape hookbeacon's own doc comment asks adopters for.
func ensureInstalledWithCommand(command string) (bool, error) {
	path, err := ExtensionPath()
	if err != nil {
		return false, err
	}
	want, err := renderExtension(command)
	if err != nil {
		return false, err
	}

	existing, readErr := os.ReadFile(path)
	switch {
	case readErr == nil && isOurs(existing):
		if string(existing) == string(want) {
			return false, nil
		}
		// Ours, but stale — a moved irrlichd, or a template change.
	case readErr == nil:
		// A file we did not write occupies the path we install to.
		// Overwriting it would destroy a user's own extension for the sake
		// of monitoring, which is never the right trade; refusing surfaces
		// as "granted but NOT applied" in the wizard (issue #1362) with this
		// message, which is the honest outcome.
		return false, fmt.Errorf("pi: %s exists and was not written by irrlicht; refusing to overwrite it", path)
	case !errors.Is(readErr, os.ErrNotExist):
		return false, readErr
	}

	if err := atomicfile.WriteFile(path, want); err != nil {
		return false, err
	}
	return true, nil
}

// VerifyHooksInstalled reports whether the installed extension is still
// present and current, without writing anything (issue #1372). This is what
// services.HookEntryVerifier re-checks a granted install with on a timer —
// which matters more here than for a config-entry adapter, because pi's own
// `pi install` / `pi remove` and any manual tidy of the extensions directory
// can remove this file with the permission still reading granted.
func VerifyHooksInstalled() (agent.HookEntryStatus, error) {
	path, err := ExtensionPath()
	if err != nil {
		return agent.HookEntryStatus{}, err
	}
	command, err := hookbeacon.InstalledCommand(AdapterName)
	if err != nil {
		return agent.HookEntryStatus{}, err
	}
	want, err := renderExtension(command)
	if err != nil {
		return agent.HookEntryStatus{}, err
	}

	existing, readErr := os.ReadFile(path)
	if errors.Is(readErr, os.ErrNotExist) {
		return agent.HookEntryStatus{Missing: append([]string(nil), installedHookEvents...)}, nil
	}
	if readErr != nil {
		return agent.HookEntryStatus{}, readErr
	}
	if !isOurs(existing) {
		// Something else owns the path now. Reported as Missing rather than
		// Stale: "stale" means EnsureHooksInstalled would rewrite it, and it
		// deliberately will not.
		return agent.HookEntryStatus{Missing: append([]string(nil), installedHookEvents...)}, nil
	}
	if string(existing) != string(want) {
		return agent.HookEntryStatus{Stale: append([]string(nil), installedHookEvents...)}, nil
	}
	return agent.HookEntryStatus{}, nil
}

// UninstallHooks removes irrlicht's pi extension. Returns true if anything
// changed.
//
// Removes the FILE, because the file is the entire install — nothing was
// added to settings.json, no package was installed, and pi's own
// auto-discovery is what loaded it. A file at our path that is not ours is
// left alone, the same "leave a foreign entry alone" rule every hook
// uninstall in this codebase follows.
//
// pi's extensions directory is deliberately NOT removed when it empties out,
// even if this install is what created it: an empty `extensions/` is an
// ordinary state for a pi installation, and removing a directory a user's
// editor or shell may have open is a worse surprise than leaving one behind.
func UninstallHooks() (bool, error) {
	path, err := ExtensionPath()
	if err != nil {
		return false, err
	}
	existing, readErr := os.ReadFile(path)
	if errors.Is(readErr, os.ErrNotExist) {
		return false, nil
	}
	if readErr != nil {
		return false, readErr
	}
	if !isOurs(existing) {
		return false, nil
	}
	if err := os.Remove(path); err != nil {
		return false, err
	}
	return true, nil
}
