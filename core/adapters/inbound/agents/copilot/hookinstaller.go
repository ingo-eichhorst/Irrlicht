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
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"irrlicht/core/adapters/inbound/agents/agentpaths"
	"irrlicht/core/adapters/inbound/agents/hookjson"
	"irrlicht/core/domain/agent"
	"irrlicht/core/pkg/atomicfile"
	"irrlicht/core/pkg/daemonaddr"
)

// copilotHomeDirName is the $HOME-relative Copilot config directory, the
// parent of both session-state/ (adapter.go's defaultRootDir) and hooks/.
const copilotHomeDirName = ".copilot"

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
//	  HTTP hook URL "http://localhost:<port>/api/v1/hooks/copilot" resolves to
//	  blocked address 127.0.0.1. URLs must not target loopback, private, or
//	  link-local addresses.
//	with COPILOT_HOOK_ALLOW_LOCALHOST=1, the same file delivers normally.
//
// The quoted refusal uses the "localhost" spelling this installer actually
// writes (daemonaddr.LocalURL), not a 127.0.0.1 literal — the guard resolves
// the host before judging it, so both spellings are refused identically. That
// distinction is deliberate evidence, because the consent copy makes an
// absolute claim about the channel being inert without the variable, and a
// literal-IP-only guard would have made that claim wrong.
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
// was actually shown. From 1.0.26 the event fires only when a prompt is really
// displayed.
//
// Note what a spurious notification would and would not cost HERE. It could not
// manufacture a false waiting state — this adapter's notification branch takes
// no hold at all (see hooks.go) — but it would inject a synthetic activity
// event, and forceReadyToWorkingIfActive can bounce a ready session to WORKING
// on one. That is the concrete harm the floor prevents; stating it accurately
// matters because a floor defended by a reason someone can disprove is a floor
// that gets lowered.
const minCLIVersion = notificationGuaranteeSince

// notificationGuaranteeSince is the Copilot version from which the
// notification event is guaranteed to fire ONLY when a prompt was really
// shown. Named once and referenced by both the floor above and every
// hookEventSince entry below, which is honest rather than merely DRY: the two
// installed events are pinned to this one boundary, and a future event with
// its own arrival version gets its own constant rather than reusing this.
const notificationGuaranteeSince = "1.0.26"

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
	HookNotification: notificationGuaranteeSince,
	HookStop:         notificationGuaranteeSince,
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
		WriteFile:   atomicfile.WriteFile,
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
//
// Unlike claudecode's settings.json and codex's hooks.json, this file is one
// irrlicht CREATED and nothing else writes — so removing our entries and
// leaving an empty document behind is the same failure #1371 fixed one level
// up: revoking consent has to give the user their directory back, not leave a
// stray file they never made. hookjson.Uninstall drops the "hooks" key; if
// that empties the document entirely, the file itself goes too.
//
// Gated on the document being empty, so a hook the user hand-added to our file
// keeps it alive (Copilot unions every *.json in the directory, so that is a
// legitimate thing for them to have done) — TestUninstallLeavesForeignEntriesAlone
// covers that side.
func UninstallHooks() (bool, error) {
	path, err := copilotHooksPath()
	if err != nil {
		return false, err
	}
	modified, err := hookjson.Uninstall(hookConfig(path))
	if err != nil || !modified {
		return modified, err
	}
	removeIfEmpty(path)
	return true, nil
}

// removeIfEmpty deletes path when it holds no keys at all. Best-effort: a
// failure to remove leaves an inert empty file, which is strictly better than
// failing an uninstall that already succeeded.
func removeIfEmpty(path string) {
	settings, err := hookjson.ReadSettings(path)
	if err != nil || len(settings) > 0 {
		return
	}
	_ = os.Remove(path)
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

// copilotHome resolves the absolute Copilot config directory: $COPILOT_HOME
// when that override is set and absolute, else $HOME/.copilot.
//
// Composed from agentpaths rather than re-deciding the rule with a bare
// filepath.IsAbs. agentpaths owns "how an agent home override is read" for the
// whole repo precisely so it is not reimplemented per adapter; the hand-rolled
// version silently dropped a RELATIVE COPILOT_HOME, so the watcher warned the
// user their value was ignored while the installer said nothing and wrote to a
// different directory than the one being watched.
func copilotHome() (string, error) {
	return agentpaths.AbsRoot(agentpaths.FromEnv("copilot", copilotHomeEnvVar, copilotHomeDirName))
}

// copilotSessionsDir resolves the absolute session-state tree with symlinks
// resolved, so the WalkDir below is handed a real directory and a
// "no sessions yet" tree fails cleanly to "" rather than to an error.
//
// It is NOT the receiver's confinement root: hookjson.PathConfiner resolves its
// own roots (see confine.go), and the receiver deliberately confines against
// the DECLARED, un-resolved root so its path key matches the fswatcher's. Said
// explicitly because assuming otherwise is what makes the $HOME-relative
// hazard in resolveTranscriptPath easy to miss.
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

// hookEntryIsCanonical reports whether an inner hook object already matches the
// current canonical form: an http entry pointing at our currently resolved
// endpoint. An entry left behind by a daemon on a different port reads as
// stale and is rewritten in place rather than duplicated (#1178).
func hookEntryIsCanonical(hook map[string]interface{}) bool {
	t, _ := hook["type"].(string)
	url, _ := hook["url"].(string)
	return t == "http" && url == hookEndpointURL()
}

// sessionStartVersion reads copilotVersion from a transcript's session.start
// header. Returns "" if the header is missing or unreadable.
//
// Bounded to the first few lines: session.start is the first event Copilot
// writes, and an unbounded scan of a large transcript at install time would be
// paid for nothing.
func sessionStartVersion(path string) string {
	// Sink-local traversal guard. The only caller hands over a path produced by
	// WalkDir over a self-resolved root, so this is not reachable in practice —
	// but the plain ".." check is the form CodeQL's go/path-injection query
	// recognizes as a sanitizer (the root derives from COPILOT_HOME, a taint
	// source), and both sibling adapters carry it for exactly that reason: see
	// codex/session_meta.go and claudecode/hookinstaller.go. A rejected path
	// falls through to the same "unknown version" result an unreadable file
	// already produces, which fails OPEN to the version gate's Probe.
	if strings.Contains(path, "..") {
		return ""
	}
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), maxVersionScanLineBytes)
	for i := 0; i < maxVersionScanLines && scanner.Scan(); i++ {
		var line struct {
			Type string         `json:"type"`
			Data map[string]any `json:"data"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			continue
		}
		if line.Type != evSessionStart {
			continue
		}
		return str(line.Data, "copilotVersion")
	}
	return ""
}

const (
	// maxVersionScanLines bounds how far into a transcript the version probe
	// reads before giving up.
	maxVersionScanLines = 20
	// maxVersionScanLineBytes bounds a single line, so an oversized transcript
	// line cannot make the probe allocate without limit (cell 2-16).
	maxVersionScanLineBytes = 1 << 20
)
