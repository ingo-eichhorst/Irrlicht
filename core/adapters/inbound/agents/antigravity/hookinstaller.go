// hookinstaller.go merges irrlicht's lifecycle hook into antigravity's
// customization root (issue #1723, epic #1355 — the last adapter).
//
// # The one file that loads, and how that was established
//
// Antigravity's shipped doc (`~/.gemini/antigravity-cli/builtin/skills/
// agy-customizations/docs/hooks.md`) says hooks live "in your customization
// root directory (e.g. `.agents/hooks.json`)". Measured against agy 1.1.18,
// the only file it actually loads is **`~/.gemini/config/hooks.json`**. The
// observable is agy's own startup line (`hooks_manager.go:53`), and it was
// driven red-first in an isolated HOME that was a byte-complete clone of the
// real `~/.gemini`:
//
//	(no file)                     loaded 0 named hooks from 0 hooks.json file(s)
//	one named hook                loaded 1 named hooks from 1 hooks.json file(s)
//	two named hooks, ONE file     loaded 2 named hooks from 1 hooks.json file(s)
//	`{ this is not json`          loaded 0 named hooks from 1 ... + a parse error naming our path
//	back to one named hook        loaded 1 named hooks from 1 hooks.json file(s)
//
// The 1→2 row is the load-bearing one: the count tracks the CONTENTS of the
// file, so this is antigravity parsing what irrlicht wrote rather than merely
// stat-ing it. Three plausible alternatives were measured to load NOTHING and
// are recorded so nobody re-treads them: `~/.gemini/config/plugins/<name>/`
// (what `agy plugin install` stages, and it prints `hooks : 1 processed`
// while loading zero), `<workspace>/.agents/hooks.json`, and any location
// reachable via `JETSKI_APP_DATA_DIR`, which does not relocate the
// customization root at all.
//
// # Why this is a merge and not a file irrlicht owns
//
// That file is also where antigravity's own `/hooks` TUI command writes — the
// binary's changelog says so ("Fixed a bug where the /hooks command wrote
// configurations to ~/.gemini/antigravity-cli/hooks.json instead of the
// shared ~/.gemini/config/hooks.json"). So unlike pi (#1721) and opencode
// (#1719), irrlicht does not own this document. Apply/Verify/Remove are a
// merge into a user-owned JSON file, keyed by a top-level hook NAME, and
// upstream states that multiple named hooks for the same event are merged and
// executed sequentially — coexistence is the normal case, not an edge one.
//
// Nothing here diffs bytes. The map is built with ordinary Go map operations
// and handed to hookjson.ReadSettings/WriteSettings UNMODIFIED, exactly as
// kiro-cli's flat installer does: jsonc.go's splice is the only place bytes
// are diffed against the file on disk, and it already carries the property
// test docs/testing-philosophy.md requires of a structural-diff byte emitter
// (TestSplice_PropertyRandomMutations). What this file adds on top of that is
// MERGE SEMANTICS over a document shape the splicer knows nothing about, so it
// carries its own generator-driven property test —
// hookinstaller_property_test.go — over the axis a real user's file varies on:
// other named hooks, other events under our own name, foreign handlers inside
// our own event array, and comments. #1753 is the incident that says a
// property test which does not vary the axis the real input varies on is not
// coverage.
//
// # The document shape, and the trap in it
//
//	{
//	  "lint-checker": { "PostToolUse": [ {"matcher": "...", "hooks": [ ... ]} ] },
//	  "irrlicht":     { "Stop":        [ {"type":"command","command":"...","timeout":5} ] }
//	}
//
// The per-event STRUCTURE differs and getting it wrong means the hook silently
// does not load: `PreToolUse`/`PostToolUse` are GROUPED (a `matcher` +
// `hooks` wrapper), while `PreInvocation`/`PostInvocation`/`Stop` are FLAT —
// the array elements are handler objects directly. This adapter installs only
// `Stop`, so only the flat shape is written; hookEntry's doc records the
// grouped shape so a future event addition does not have to rediscover it.
//
// # Delivery
//
// Address-free (contracttesting.DeliveryAddressFree): the installed command
// names the `irrlichd hook-post antigravity` beacon (core/pkg/hookbeacon),
// which reads the daemon's published address at fire time. Upstream supports
// only `type: "command"` — the doc's own "Current Limitations" says so, and
// there is no `url` field anywhere in the handler schema — so the beacon is
// not a preference here, it is the only expressible delivery. Nothing
// address-shaped reaches the user's file, which makes the #1178 stale-port
// class inexpressible rather than fixed once more.
package antigravity

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"irrlicht/core/adapters/inbound/agents/agentpaths"
	"irrlicht/core/adapters/inbound/agents/hookjson"
	"irrlicht/core/domain/agent"
	"irrlicht/core/pkg/hookbeacon"
)

// HookEventStop is antigravity's wire name for the turn-end event — the one
// event this adapter installs. It fires when the execution loop terminates,
// and its payload carries the conversation id, the transcript path, a
// terminationReason and fullyIdle (see hooks.go for what is and is not read
// off it).
const HookEventStop = "Stop"

// installedHookEvents is every antigravity lifecycle event this adapter
// registers. Exactly one, and the exclusions are the finding rather than an
// omission. Antigravity's binary accepts five config keys — PreToolUse,
// PostToolUse, PreInvocation, PostInvocation, Stop (CallSessionStartHook
// exists as a symbol but SessionStart is not a valid key, so it is not
// registrable at all):
//
//   - PostToolUse fires after EVERY tool step. It is the event the #1723
//     probes proved the channel with, and it buys irrlicht nothing the
//     transcript does not already have — the transcript records every tool
//     call and its result — at the cost of one process spawn per tool call,
//     inside a loop antigravity blocks on synchronously.
//
//   - PreToolUse is excluded on the #1719 hazard at its maximum strength.
//     Its result can carry `decision: allow|deny|ask|force_ask`,
//     `permissionOverrides` and `overwrite` — a monitoring channel that can
//     answer a user's permission prompt or rewrite a tool call's arguments.
//     It was ALSO the one event with a plausible payoff: the #1723 audit
//     argued that `matcher: "ask_question"` would bracket antigravity's one
//     genuine blocked-on-user interval and win the `2-17` catalog cell. That
//     matcher was never observed to fire — the audit read the step type
//     `CORTEX_STEP_TYPE_ASK_QUESTION` out of the binary, which is evidence
//     that a tool name exists, not that a hook matched to it is invoked — and
//     shipping it would mean installing the most dangerous result type in the
//     schema on the strength of an inference. It is a deliberate follow-up
//     behind one live observation, not a gap nobody noticed.
//
//   - PreInvocation/PostInvocation can inject steps into the agent's own loop
//     (`injectSteps`, `terminationBehavior`), and neither reports anything
//     about the turn that Stop does not.
//
// The residue: through hooks, antigravity is a working/ready adapter, exactly
// as pi is (#1721). `waiting` stays where it already was — the transcript-tail
// cue — and that is a measured position rather than an open gap.
var installedHookEvents = []string{HookEventStop}

// hookEventSince records the agy version each installed event is known to
// exist at. There is no upstream changelog entry dating the Stop event, so
// this is pinned to the version it was OBSERVED firing at rather than to the
// version it was introduced at; minAgyVersion must be >= every value here,
// which AssertHookVersionGate enforces.
var hookEventSince = map[string]string{
	HookEventStop: "1.1.18",
}

// minAgyVersion is the lowest agy version this adapter's install requires.
//
// 1.1.18 is the only version anything here was verified against, and the floor
// covers more than one event name: the load location (`~/.gemini/config/
// hooks.json` rather than the doc's `.agents/`, a path the binary's own
// changelog records as having MOVED), the flat-vs-grouped per-event structure,
// the camelCase protojson payload field names, and that Stop is invoked at
// all. None of that was bisected backwards, and 1.1.2 — the version #1723's
// audit read — is known to differ in the code around it. core/pkg/cliversion
// fails OPEN on an unknown or unparseable version, so overshooting costs a
// missing install on an old CLI rather than a wrong install on a new one,
// which is the safe direction (the same reasoning pi's, kiro-cli's and
// gemini-cli's floors document).
const minAgyVersion = "1.1.18"

// HookEndpointPath is the daemon's antigravity hook route. Exported so
// registerHookRoutes (core/cmd/irrlichd/startup.go) and beaconroute_test.go's
// pinned map both derive from the same segment this installer renders into the
// beacon command it writes. A mismatch is a permanent silent 404: the beacon
// exits 0 by design, so nothing anywhere reports it.
//
// A var, not a const, because hookbeacon.EndpointPath composes the route from
// its own unexported prefix — deriving it here keeps this constant from
// drifting away from the beacon's own routing convention.
var HookEndpointPath = hookbeacon.EndpointPath(AdapterName)

// configDirName is the $HOME-relative customization root antigravity loads
// hooks.json from, and hooksFilename is the file inside it. No environment
// override is honored because none exists: JETSKI_APP_DATA_DIR was tested
// against three layouts under a scratch directory and the boot still reported
// `loaded 0 named hooks from 0`.
const (
	configDirName = ".gemini/config"
	hooksFilename = "hooks.json"
)

// displayHooksPath is the consent copy's home-relative spelling of the file
// HooksPath resolves absolutely.
const displayHooksPath = "~/.gemini/config/hooks.json"

// hookName is the top-level key irrlicht owns in that document. Every other
// top-level key is somebody else's named hook — the user's own `/hooks`
// output, or another tool's — and is preserved untouched by install, verify
// and uninstall alike.
const hookName = "irrlicht"

// hookTimeoutSeconds bounds how long antigravity waits for our handler.
//
// Antigravity's `timeout` is in SECONDS (hooks.md: "Execution timeout in
// seconds", default 30) — NOT milliseconds, which is the unit gemini-cli uses
// for the same-named field and which hookbeacon's own doc warns callers about.
// Five seconds is comfortably above the beacon's own 1s internal deadline
// (hookbeacon.DefaultTimeout, which bounds reading stdin, dialing and the
// POST) and far below the 30s default, because these hooks "run synchronously
// and block the agent loop": a wedged process must not be able to stall a
// user's turn for half a minute.
const hookTimeoutSeconds = 5

// inertResultSuffix is appended to the beacon command so the handler's stdout
// is exactly `{}`.
//
// This is the mitigation for the one genuine hazard in this install, and it is
// not belt-and-braces. The shipped doc CONTRADICTS ITSELF about whether Stop's
// result is honored: the Supported Event Types table says "N/A (ignored)",
// while §5 "Stop Contract" documents `decision: "continue"` blocking the stop
// and re-entering the loop with an injected reason. Both cannot be true, and
// the safe reading is §5's — irrlicht's monitoring channel must be incapable
// of influencing the agent under EITHER reading.
//
// `{}` is inert under both: it is a valid JSON object with no `decision` key,
// and §5 says "any other value allows the agent to stop". It is also exactly
// what #1723's probe emitted on the run where Stop was measured firing, so the
// installed handler's stdout is byte-identical to the one that was observed to
// be safe rather than merely argued to be.
//
// It is a SUFFIX rather than a change to hookbeacon.Command because the beacon
// deliberately never holds a stdout handle (that package's "Why Options has no
// Stdout"), and it already redirects its own stdout to /dev/null. So the
// beacon still cannot write a control value; the shell prints one fixed inert
// byte pair after it. printf exits 0, which also means the compound command's
// status is 0 even if a future beacon change stopped guaranteeing it.
const inertResultSuffix = `; printf '{}'`

// HooksPath is the single file this permission's Writes.Path resolves — the
// shared, user-owned document antigravity loads named hooks from.
func HooksPath() (string, error) {
	dir, err := agentpaths.AbsRoot(configDirName)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, hooksFilename), nil
}

// hookCommand renders the shell command to install for a resolved beacon
// command line. See inertResultSuffix for why the `printf` is not optional.
func hookCommand(beacon string) string { return beacon + inertResultSuffix }

// beaconOf reads the beacon command back out of an installed handler command,
// reporting false for anything not in the shape hookCommand renders.
func beaconOf(command string) (string, bool) {
	if !strings.HasSuffix(command, inertResultSuffix) {
		return "", false
	}
	return strings.TrimSuffix(command, inertResultSuffix), true
}

// commandIsCanonical reports whether an installed handler command is the one
// this daemon would write now.
//
// It delegates to hookbeacon.IsCanonical rather than comparing against a
// freshly rendered string, and that is load-bearing: IsCanonical deliberately
// answers TRUE for the two drifts a rewrite cannot repair (our own path being
// unresolvable, and a dead binary that the rewrite would name again), because
// hookjson has no "the bytes did not change, skip the write" branch and a
// drift reported forever is a user's config rewritten on every daemon start,
// forever, without converging.
func commandIsCanonical(command string) bool {
	beacon, ok := beaconOf(command)
	if !ok {
		return false
	}
	return hookbeacon.IsCanonical(beacon, AdapterName)
}

// hookEntry builds the flat handler object installed under our named hook.
//
// FLAT is the shape `Stop` requires — the array elements are handler objects
// directly. `PreToolUse`/`PostToolUse` would instead need a grouped wrapper
// (`{"matcher": "...", "hooks": [ <this object> ]}`); getting that wrong is
// not a validation error, the hook simply never loads, which is why the two
// shapes are named here rather than left to whoever adds a second event.
//
// `type` is written explicitly even though it defaults to "command": it is the
// only supported value, and stating it means an entry read back from disk is
// judged on the same three keys it was written with.
func hookEntry(beacon string) map[string]interface{} {
	return map[string]interface{}{
		"type":    "command",
		"command": hookCommand(beacon),
		"timeout": hookTimeoutSeconds,
	}
}

// isOurs reports whether a handler object is one irrlicht installed, by the
// beacon sentinel. The sentinel names neither the daemon's address (there is
// none) nor the irrlichd binary path, so an entry left by a differently-
// situated daemon is still recognized as ours — which is the precondition for
// rewriting it in place instead of appending a second one beside it, and for
// `irrlichd --uninstall-hooks` finding it from any daemon.
func isOurs(handler map[string]interface{}) bool {
	command, _ := handler["command"].(string)
	return strings.Contains(command, hookbeacon.Sentinel(AdapterName))
}

// entryIsCanonical reports whether one of OUR handler objects is exactly what
// this daemon would write now.
//
// The key-count check is deliberate: an entry carrying an extra key is
// rewritten rather than tolerated, so a rewrite always produces a canonical
// entry and the ensure/verify loop converges. Without it a stray key would be
// reported stale forever by a repair that never removed it.
func entryIsCanonical(handler map[string]interface{}) bool {
	if len(handler) != 3 {
		return false
	}
	if t, _ := handler["type"].(string); t != "command" {
		return false
	}
	if !numberIs(handler["timeout"], hookTimeoutSeconds) {
		return false
	}
	command, _ := handler["command"].(string)
	return commandIsCanonical(command)
}

// numberIs compares a JSON-decoded number against an int. A value written by
// this process is an int; the same value read back through encoding/json is a
// float64, so both spellings have to answer alike or every re-read would
// report drift.
func numberIs(v interface{}, want int) bool {
	switch n := v.(type) {
	case int:
		return n == want
	case float64:
		return n == float64(want)
	default:
		return false
	}
}

// --- install / verify / uninstall ---

// EnsureHooksInstalled merges irrlicht's named hook into the user's
// hooks.json for the irrlichd that is running now, creating the file (and
// antigravity's config directory) if neither exists. Returns true if anything
// changed.
//
// Idempotent, and self-healing across a moved binary: an entry of ours whose
// beacon command names a different irrlichd is rewritten IN PLACE, never
// duplicated.
func EnsureHooksInstalled() (bool, error) {
	beacon, err := hookbeacon.InstalledCommand(AdapterName)
	if err != nil {
		return false, err
	}
	return ensureInstalledWithCommand(beacon)
}

// ensureInstalledWithCommand is EnsureHooksInstalled with the beacon command
// resolved by the caller — the seam the #1178 contract's ForeignInstall uses
// to seed the install a DIFFERENTLY-situated daemon would have written.
// hookbeacon.InstalledCommand is called exactly once per entry point, which is
// the shape hookbeacon's own doc comment asks adopters for.
func ensureInstalledWithCommand(beacon string) (bool, error) {
	path, err := HooksPath()
	if err != nil {
		return false, err
	}
	doc, err := hookjson.ReadSettings(path)
	if err != nil {
		return false, err
	}

	ours, err := ourNamedHook(doc)
	if err != nil {
		return false, err
	}

	modified := false
	for _, event := range installedHookEvents {
		handlers, _ := ours[event].([]interface{})
		next, changed := reconcileHandlers(handlers, beacon)
		if changed {
			ours[event] = next
			modified = true
		}
	}
	if !modified {
		return false, nil
	}

	doc[hookName] = ours
	return true, hookjson.WriteSettings(path, doc, atomicWriteFile)
}

// ourNamedHook returns the named-hook object irrlicht owns, creating an empty
// one when the document has none.
//
// A top-level "irrlicht" key holding something that is NOT an object is a
// refusal, not something to overwrite: this is a hand-editable file irrlicht
// does not own, and replacing a value we cannot model would destroy content
// the user meant to keep. It surfaces as #1362's "granted but NOT applied,
// because <this message>", which is the honest outcome and the same trade pi's
// installer makes for a foreign file at its path.
func ourNamedHook(doc map[string]interface{}) (map[string]interface{}, error) {
	existing, present := doc[hookName]
	if !present {
		return map[string]interface{}{}, nil
	}
	ours, ok := existing.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf(
			"antigravity: the %q key in the hooks file is a %T, not a hook object; refusing to overwrite it",
			hookName, existing)
	}
	return ours, nil
}

// reconcileHandlers brings one event's handler array in line with what this
// daemon would install, and reports whether it changed anything.
//
// Every handler that is not ours is preserved, in order. Upstream merges and
// runs multiple named hooks for an event sequentially, so a foreign handler
// sitting in OUR named hook's array is unusual but harmless, and deleting it
// would be irrlicht editing a user's configuration for no reason. Exactly one
// of ours survives: the first is rewritten in place when stale, and any
// further copies are dropped, because two irrlicht handlers on one event mean
// two beacon spawns per turn.
func reconcileHandlers(handlers []interface{}, beacon string) ([]interface{}, bool) {
	next := make([]interface{}, 0, len(handlers)+1)
	changed := false
	found := false

	for _, h := range handlers {
		handler, ok := h.(map[string]interface{})
		if !ok || !isOurs(handler) {
			next = append(next, h)
			continue
		}
		if found {
			// A duplicate of ours. Drop it.
			changed = true
			continue
		}
		found = true
		if entryIsCanonical(handler) {
			next = append(next, h)
			continue
		}
		next = append(next, hookEntry(beacon))
		changed = true
	}

	if !found {
		next = append(next, hookEntry(beacon))
		changed = true
	}
	return next, changed
}

// inspectHandlers reports what one event's handler array holds: whether any
// handler of ours is present at all, and whether the array is already in the
// shape reconcileHandlers would leave it in.
//
// It is the single definition both EnsureHooksInstalled and
// VerifyHooksInstalled read, for the reason hookjson's entryIsStale/
// matcherIsStale pair documents: two definitions of "this install is not what
// we would write today" drift, and both directions are live bugs — stricter
// than the repair is a loop that writes forever without converging, laxer is a
// clobbered install that reads healthy in the diagnostics bundle.
// TestVerifyAgreesWithEnsureInstalled pins the equivalence over a corpus
// rather than trusting this comment.
func inspectHandlers(handlers []interface{}) (found, current bool) {
	seen := 0
	current = true
	for _, h := range handlers {
		handler, ok := h.(map[string]interface{})
		if !ok || !isOurs(handler) {
			continue
		}
		seen++
		if seen > 1 || !entryIsCanonical(handler) {
			current = false
		}
	}
	return seen > 0, current
}

// VerifyHooksInstalled reports whether our entries are still present and
// current, without writing anything (issue #1372).
//
// This matters more here than for an adapter that owns its file: antigravity's
// own `/hooks` TUI writes to this exact document, so the user's next `/hooks`
// run — or a hand-edit, or another tool's plugin install — can remove our
// named hook with the permission still reading granted and nothing anywhere
// reporting it.
func VerifyHooksInstalled() (agent.HookEntryStatus, error) {
	path, err := HooksPath()
	if err != nil {
		return agent.HookEntryStatus{}, err
	}
	doc, err := hookjson.ReadSettings(path)
	if err != nil {
		return agent.HookEntryStatus{}, err
	}

	// A missing document, a missing "irrlicht" key and an "irrlicht" key
	// holding a non-object all read as "no entries": indexing a nil map is
	// legal and yields the zero value, so every installed event falls out as
	// Missing. That is exactly right for the first two. For the third,
	// EnsureHooksInstalled REFUSES rather than repairing — reporting Missing
	// aims the verifier's repair at a write that will fail loudly with the
	// reason, which is better than reporting health for a channel that is dead.
	ours, _ := doc[hookName].(map[string]interface{})

	var status agent.HookEntryStatus
	for _, event := range installedHookEvents {
		handlers, _ := ours[event].([]interface{})
		found, current := inspectHandlers(handlers)
		switch {
		case !found:
			status.Missing = append(status.Missing, event)
		case !current:
			status.Stale = append(status.Stale, event)
		}
	}
	return status, nil
}

// UninstallHooks removes irrlicht's handlers from the user's hooks.json,
// leaving every other named hook — and every foreign handler inside our own —
// untouched. Returns true if anything changed.
//
// Identification is by the beacon sentinel alone, which names neither an
// address nor a binary path, so this is not scoped to the daemon that
// installed: `irrlichd --uninstall-hooks` run from one daemon removes what
// another wrote (#1178's fourth obligation).
//
// The FILE is deliberately never deleted, even when our removal empties the
// document to `{}`. It is shared with antigravity's own `/hooks` command,
// which may write into it moments later, and an empty object is a state agy
// reads without complaint (`loaded 0 named hooks from 1 hooks.json file(s)`).
// What IS cleaned up is our own named hook: an empty `"irrlicht": {}` left
// behind would keep claiming a key in a document irrlicht no longer has any
// business in, which is #1371's "revoking consent gives the user their file
// back" one level in.
func UninstallHooks() (bool, error) {
	path, err := HooksPath()
	if err != nil {
		return false, err
	}
	doc, err := hookjson.ReadSettings(path)
	if err != nil {
		return false, err
	}
	ours, ok := doc[hookName].(map[string]interface{})
	if !ok {
		return false, nil
	}

	modified := false
	for _, event := range installedHookEvents {
		handlers, ok := ours[event].([]interface{})
		if !ok {
			continue
		}
		kept, removed := dropOurHandlers(handlers)
		if !removed {
			continue
		}
		modified = true
		if len(kept) == 0 {
			delete(ours, event)
		} else {
			ours[event] = kept
		}
	}
	if !modified {
		return false, nil
	}
	if len(ours) == 0 {
		delete(doc, hookName)
	}
	return true, hookjson.WriteSettings(path, doc, atomicWriteFile)
}

// dropOurHandlers removes every handler of ours from one event's array,
// preserving the rest in order.
func dropOurHandlers(handlers []interface{}) ([]interface{}, bool) {
	var kept []interface{}
	removed := false
	for _, h := range handlers {
		handler, ok := h.(map[string]interface{})
		if ok && isOurs(handler) {
			removed = true
			continue
		}
		kept = append(kept, h)
	}
	return kept, removed
}

// atomicWriteFile writes data to path via a temp file + rename so antigravity
// never loads a half-written hooks.json. Creates the config directory. Matches
// every sibling hook-installing adapter's own atomic writer.
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
	defer func() {
		if err := os.Remove(tmpName); err != nil && !errors.Is(err, os.ErrNotExist) {
			// Best effort: the rename below already succeeded or the write
			// already failed, and a leftover temp file is not worth failing an
			// install over.
			_ = err
		}
	}()

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
