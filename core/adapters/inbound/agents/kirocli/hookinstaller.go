// hookinstaller.go manages Kiro CLI hook entries for the irrlicht daemon
// (issue #1716, epic #1355 Phase D).
//
// Two things make this installer unlike claudecode's, codex's, copilot's or
// gemini-cli's, and both come from the #1716 audit rather than from this
// file's own invention.
//
// # The entry shape is flat, not hookjson's nested matcher-group shape
//
// hookjson.EnsureInstalled unconditionally writes Claude Code's
// `"stop": [ { "matcher": "", "hooks": [ { "command": "..." } ] } ]` shape.
// kiro-cli wants `"stop": [ { "command": "..." } ]` — no matcher-group
// wrapper — and given the nested form it does not merely ignore the unknown
// field: it rejects the WHOLE agent config and silently falls back to a
// different one, while `kiro-cli agent validate` on that same file returns
// exit 0 (validate is looser than load). That is a false-positive install:
// the permission would read granted, EnsureHooksInstalled would report
// success, and the channel would be dead.
//
// Per AGENTS.md, a hand-rolled structural-diff writer is exactly the class
// of code that owes property-test coverage (hookjson's own splicer shipped
// with seven green round-trip tests and a defect corrupting ~11% of random
// documents). This file avoids writing one: ensureFlatHooksInstalled,
// uninstallFlatHooks and verifyFlatHooksInstalled below build an ordinary Go
// map — a flat array of {"command": ...} objects instead of nested matcher
// groups — and hand it to hookjson.ReadSettings/WriteSettings UNMODIFIED.
// Those two functions are the only place bytes are diffed against the file on
// disk (jsonc.go's splice, already covered by hookjson's own property test),
// so this file inherits that coverage rather than duplicating the risk.
//
// # No config to add a hooks key to exists for a default user
//
// Every other adapter's install adds a "hooks" key to a config the user's
// agent already reads. kiro-cli only fires hooks for an agent config
// selected via `--agent <name>` or `chat.defaultAgent`, and a default
// install of Kiro CLI has neither: `~/.kiro/agents/` holds no config (only
// the packaged `agent_config.json.example`), and
// `~/.kiro/settings/cli.json` sets no `chat.defaultAgent`. There is nothing
// to add a hooks key to.
//
// kiro-cli ships exactly the two commands built for this
// (`kiro-cli agent --help`): `agent create <name> --from <src>` and
// `agent set-default <name>`. So EnsureHooksInstalled, on first grant,
// materializes a full copy of the user's actual built-in default
// (`kiro-cli agent create irrlicht --from kiro_default` — measured live to
// reproduce the complete system prompt, `"tools": ["*"]`, the standard
// `resources` list and `"model": null`), injects the flat hook entries into
// that copy, and points `chat.defaultAgent` at it. The prior default (which
// may be "none — the built-in was in effect") is recorded before it is
// overwritten and restored on uninstall; see restorePriorDefaultAgent.
//
// This is a real, ongoing cost, stated in the consent copy (agent.go) rather
// than only here: the copy is a snapshot of the built-in default at grant
// time, not a mirror, so it will not pick up what a kiro-cli upgrade changes
// in the built-in agent. A version-triggered refresh (regenerate from
// kiro_default and re-inject on a kiro-cli upgrade, skipped when the file no
// longer matches what was written) is exactly the shape
// services.HookEntryVerifier already re-checks granted installs with, and is
// left as a follow-up rather than built here — the audit's own comments
// record it as "part of the feature, not a follow-up" for a reason: without
// it, the agent silently drifts from what the user would get by upgrading
// normally. Flagged in the PR description this file ships with, not buried.
package kirocli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"irrlicht/core/adapters/inbound/agents/agentpaths"
	"irrlicht/core/adapters/inbound/agents/hookjson"
	"irrlicht/core/domain/agent"
	"irrlicht/core/pkg/hookbeacon"
)

// kiroAgentsDirName is the KIRO_HOME-relative directory holding per-agent
// config files, one JSON document per agent.
const kiroAgentsDirName = "agents"

// kiroSettingsDirName and kiroSettingsFilename locate kiro-cli's own
// general-purpose CLI settings file, the one chat.defaultAgent lives in.
const (
	kiroSettingsDirName  = "settings"
	kiroSettingsFilename = "cli.json"
)

// HookEndpointPath is the daemon's kiro-cli hook route. Exported so
// registerHookRoutes (core/cmd/irrlichd/startup.go) and beaconroute_test.go's
// pinned map both derive from the same segment this installer renders into
// the beacon command it writes, per hookbeacon's install.go doc: "A mismatch
// is a permanent silent 404: the beacon exits 0 by design, so nothing
// anywhere reports it."
//
// A var, not a const, for the same reason gemini-cli's is: hookbeacon.EndpointPath
// composes the route from its own unexported prefix, so deriving it here
// keeps this constant from being able to drift from the beacon's own routing
// convention.
var HookEndpointPath = hookbeacon.EndpointPath(AdapterName)

// irrlichtAgentName is both the agent kiro-cli materializes
// (agents/irrlicht.json) and the value written to chat.defaultAgent.
const irrlichtAgentName = "irrlicht"

// kiroBuiltinDefaultAgent is the name kiro-cli's own `agent list` shows for
// the built-in default agent a fresh install runs with no config on disk —
// verified live to be usable as an `agent create --from` source and as an
// `agent set-default` target (restorePriorDefaultAgent uses the latter).
const kiroBuiltinDefaultAgent = "kiro_default"

// defaultAgentSettingsKey is the settings/cli.json key kiro-cli reads to pick
// which agent `kiro-cli chat` launches with no --agent flag.
const defaultAgentSettingsKey = "chat.defaultAgent"

// priorDefaultStateFilename is irrlicht's own record of what
// chat.defaultAgent held before this adapter first changed it, so uninstall
// can restore it rather than assuming kiro_default (which would silently
// discard a user's own prior choice). Deliberately NOT inside agents/ — that
// directory's *.json files are kiro-cli agent configs subject to its
// deny_unknown_fields schema, and this document is not one.
const priorDefaultStateFilename = ".irrlicht-prior-default.json"

// displayAgentConfigPath is the consent copy's home-relative spelling of the
// file this permission's Writes.Path resolves absolutely (agent.go).
const displayAgentConfigPath = "~/.kiro/agents/irrlicht.json"

// minCLIVersion is the lowest kiro-cli version this adapter's install
// requires.
//
// Pinned to exactly the version the #1716 audit verified against —
// kiro-cli 2.6.0, live, in an isolated KIRO_HOME — rather than assumed to
// extend backward. No earlier kiro-cli was bisected for when `agent create
// --from`, `agent set-default` or the flat hook-entry shape first shipped,
// so nothing supports a lower floor; core/pkg/cliversion fails OPEN on an
// unparseable or unknown version, so overshooting here costs a missing
// install on an old CLI rather than a wrong one on a new CLI, which is the
// safe direction (see hookversion_test.go for the same reasoning gemini-cli's
// floor documents).
const minCLIVersion = "2.6.0"

// hookEventSince records the kiro-cli version each installed event is proven
// (by the same live session the floor above is pinned to) to exist with the
// payload shape this adapter depends on. minCLIVersion must be >= every
// value here, which AssertHookVersionGate enforces.
var hookEventSince = map[string]string{
	HookPostToolUse: minCLIVersion,
	HookStop:        minCLIVersion,
}

// installedHookEvents are the kiro-cli hook events we install handlers for —
// two of the five the CLI's hook system fires (agentSpawn, userPromptSubmit,
// preToolUse, postToolUse, stop; #1716's audit confirmed all five live).
//
// Deliberately NOT these three:
//
//   - preToolUse is excluded on the audit's own finding: it fires for EVERY
//     tool call under trust-all, and — measured at a live approval prompt —
//     neither its payload nor the transcript carries any discriminator
//     separating "waiting for approval" from "trust-all, running now". Only
//     DURATION distinguishes them, and any waiting detection built on that
//     would need a calibrated threshold this issue never measured, not a
//     value typed into a constant. This mirrors gemini-cli's exclusion of the
//     structurally identical BeforeTool for the same reason.
//   - agentSpawn is excluded on evidence: it fires once at session start, and
//     process discovery plus the transcript's own first write already cover
//     that — the same "no stated payoff beyond what discovery and tailing
//     already give" reasoning gemini-cli's package comment applies to
//     SessionStart/SessionEnd.
//   - userPromptSubmit is excluded on the same evidence copilot's and
//     gemini-cli's analogous exclusions cite: the transcript's own turn-
//     opening event (a "Prompt"-kind line, parser.go) already opens the turn,
//     so a hook duplicating that signal buys nothing irrlicht's three-state
//     model needs.
var installedHookEvents = []string{
	HookPostToolUse,
	HookStop,
}

// ourHookEntry builds the flat inner hook object we install for the
// currently running daemon: a beacon command (core/pkg/hookbeacon), never a
// bare curl line. kiro-cli's delivery is exec/command with the payload piped
// on stdin (issue #1716's audit), which is exactly the beacon's own input
// shape — Command() resolves the daemon's binary once, so the same argument
// #1717's gemini-cli adapter used for adopting the beacon over a curl line
// applies here unchanged: no port is ever written into the config, so the
// entire #1178 stale-port class is inexpressible rather than fixed once more.
func ourHookEntry() map[string]interface{} {
	command, _ := hookbeacon.InstalledCommand(AdapterName)
	return flatBeaconEntry(command)
}

// flatBeaconEntry is the flat hook entry for a given, already-resolved beacon
// command line — split out so hookConfig can close over a command resolved
// ONCE per entry point (see gemini-cli's beaconEntry for the same split and
// its own doc on why).
func flatBeaconEntry(command string) map[string]interface{} {
	return map[string]interface{}{"command": command}
}

// hookEntryIsCanonical reports whether a flat inner hook object matches the
// canonical beacon command for the currently running daemon.
func hookEntryIsCanonical(hook map[string]interface{}) bool {
	command, _ := hook["command"].(string)
	return hookbeacon.IsCanonical(command, AdapterName)
}

// flatHookConfig is this file's analogue of hookjson.Config, over the flat
// entry shape kiro-cli requires: Path/Sentinel/Events/Entry/IsCanonical/
// WriteFile mean exactly what they do in hookjson.Config, and the per-event
// MATCHER-GROUP wrapping is gone entirely — there is no surrounding
// {"matcher": ..., "hooks": [...]} group to build, and no per-event matcher
// concept either. Neither installed event is tool-scoped in a way we want
// narrowed at the config level: postToolUse must fire for every tool (it is
// the broad release side, mirroring gemini-cli's AfterTool), and stop is not
// a per-tool event at all. So a flat entry carries no "matcher" key at all —
// kiro-cli's own schema treats an absent matcher as "no filter" (verified
// live: the stop entry #1716's audit installed carried no matcher key and
// fired) — and this struct declares no field for one; an earlier draft did
// (MatcherFor func(event string) string, assigned matcherForEvent, which
// always returned "") and nothing ever called it.
type flatHookConfig struct {
	Path        string
	Sentinel    string
	Events      []string
	Entry       func() map[string]interface{}
	IsCanonical func(hook map[string]interface{}) bool
	WriteFile   func(path string, data []byte) error
}

// kiroHookConfig describes this adapter's flat install for a given agent
// config path, sharing the sentinel/entry/canonical logic with the beacon
// package exactly as gemini-cli's hookConfig does.
func kiroHookConfig(path string) flatHookConfig {
	return flatHookConfig{
		Path:        path,
		Sentinel:    hookbeacon.Sentinel(AdapterName),
		Events:      installedHookEvents,
		Entry:       ourHookEntry,
		IsCanonical: hookEntryIsCanonical,
		WriteFile:   atomicWriteFile,
	}
}

// ensureFlatHooksInstalled merges cfg's flat hook entries into the settings
// file at cfg.Path, creating the file (as an empty document — the CALLER is
// responsible for materializing a real kiro-cli agent first; see
// EnsureHooksInstalled) if it is not already there. Returns true if the file
// was modified.
//
// Mirrors hookjson.EnsureInstalled's two reconciliations — a sentinel-bearing
// entry that is not canonical is rewritten in place, and a fresh entry is
// appended only when no sentinel-bearing entry exists for the event at all —
// over the flat shape instead of the nested one, so an entry left by a
// daemon on a different port (or naming a moved binary, #1373's
// DriftBinaryPath/DriftBinaryMissing) is upgraded rather than duplicated.
func ensureFlatHooksInstalled(cfg flatHookConfig) (bool, error) {
	settings, err := hookjson.ReadSettings(cfg.Path)
	if err != nil {
		return false, err
	}
	hooksMap := ensureFlatHooksMap(settings)

	modified := false
	for _, event := range cfg.Events {
		arr, _ := hooksMap[event].([]interface{})
		newArr, changed := reconcileFlatEvent(arr, cfg)
		if changed {
			hooksMap[event] = newArr
			modified = true
		}
	}
	if !modified {
		return false, nil
	}
	return true, hookjson.WriteSettings(cfg.Path, settings, cfg.WriteFile)
}

// uninstallFlatHooks removes cfg's flat hook entries from the settings file
// at cfg.Path, leaving every foreign entry (and every other top-level key —
// name, prompt, tools, resources, ...) untouched. Returns true if the file
// was modified.
func uninstallFlatHooks(cfg flatHookConfig) (bool, error) {
	settings, err := hookjson.ReadSettings(cfg.Path)
	if err != nil {
		return false, err
	}
	hooksMap, ok := settings["hooks"].(map[string]interface{})
	if !ok {
		return false, nil
	}

	modified := false
	for _, event := range cfg.Events {
		arr, ok := hooksMap[event].([]interface{})
		if !ok {
			continue
		}
		kept, removed := removeSentinelEntries(arr, cfg.Sentinel)
		if !removed {
			continue
		}
		modified = true
		if len(kept) == 0 {
			delete(hooksMap, event)
		} else {
			hooksMap[event] = kept
		}
	}
	if !modified {
		return false, nil
	}
	// Unlike copilot's dedicated file, this document is a full kiro-cli agent
	// config with a required schema (name, prompt, tools, ...) — an empty
	// "hooks": {} left behind is correct and expected, not the #1371 "give
	// the directory back" case, so the "hooks" key is intentionally NOT
	// deleted even when it empties out.
	return true, hookjson.WriteSettings(cfg.Path, settings, cfg.WriteFile)
}

// verifyFlatHooksInstalled reports which of cfg's flat entries are missing
// from, or stale in, the settings file at cfg.Path — without writing
// anything (issue #1372). Same reconciliation logic as
// ensureFlatHooksInstalled, so "stale" means exactly what that function would
// rewrite (TestFlatVerifyAgreesWithFlatEnsureInstalled pins the agreement).
func verifyFlatHooksInstalled(cfg flatHookConfig) (agent.HookEntryStatus, error) {
	settings, err := hookjson.ReadSettings(cfg.Path)
	if err != nil {
		return agent.HookEntryStatus{}, err
	}
	hooksMap, _ := settings["hooks"].(map[string]interface{})

	var status agent.HookEntryStatus
	for _, event := range cfg.Events {
		arr, _ := hooksMap[event].([]interface{})
		found, stale := flatEventStatus(arr, cfg)
		switch {
		case !found:
			status.Missing = append(status.Missing, event)
		case stale:
			status.Stale = append(status.Stale, event)
		}
	}
	return status, nil
}

// --- flat-entry helpers (no byte-level diffing here — see the package
// comment on why WriteSettings alone owns that) ---

func ensureFlatHooksMap(settings map[string]interface{}) map[string]interface{} {
	if h, ok := settings["hooks"].(map[string]interface{}); ok {
		return h
	}
	m := map[string]interface{}{}
	settings["hooks"] = m
	return m
}

// reconcileFlatEvent rewrites any sentinel-bearing entry in arr that is not
// canonical, and appends a fresh one when none exists. Returns the
// (possibly unchanged) array and whether it changed.
func reconcileFlatEvent(arr []interface{}, cfg flatHookConfig) ([]interface{}, bool) {
	changed := false
	found := false
	out := make([]interface{}, 0, len(arr)+1)
	for _, e := range arr {
		entry, ok := e.(map[string]interface{})
		if !ok {
			out = append(out, e)
			continue
		}
		if entryHasSentinel(entry, cfg.Sentinel) {
			found = true
			if !cfg.IsCanonical(entry) {
				entry = cfg.Entry()
				changed = true
			}
		}
		out = append(out, entry)
	}
	if !found {
		out = append(out, cfg.Entry())
		changed = true
	}
	return out, changed
}

// flatEventStatus reports whether arr already holds a sentinel-bearing entry
// (found) and, if so, whether it is stale.
func flatEventStatus(arr []interface{}, cfg flatHookConfig) (found, stale bool) {
	for _, e := range arr {
		entry, ok := e.(map[string]interface{})
		if !ok {
			continue
		}
		if entryHasSentinel(entry, cfg.Sentinel) {
			found = true
			if !cfg.IsCanonical(entry) {
				stale = true
			}
		}
	}
	return found, stale
}

// removeSentinelEntries returns arr with every sentinel-bearing entry
// dropped, and whether anything was removed.
func removeSentinelEntries(arr []interface{}, sentinel string) ([]interface{}, bool) {
	var kept []interface{}
	removed := false
	for _, e := range arr {
		entry, ok := e.(map[string]interface{})
		if ok && entryHasSentinel(entry, sentinel) {
			removed = true
			continue
		}
		kept = append(kept, e)
	}
	return kept, removed
}

// entryHasSentinel reports whether a flat hook entry's command names our
// sentinel. Only "command" — kiro-cli's hook entries have no "url" field, so
// there is no second delivery shape to check the way hookjson's
// entryIsSentinel checks both for claudecode's http/curl migration.
func entryHasSentinel(entry map[string]interface{}, sentinel string) bool {
	v, _ := entry["command"].(string)
	return strings.Contains(v, sentinel)
}

// --- orchestration: EnsureHooksInstalled / VerifyHooksInstalled / UninstallHooks ---

// EnsureHooksInstalled brings the kiro-cli install fully into the granted
// state: the irrlicht agent config exists (materialized from the user's
// built-in default on first grant), carries our flat hook entries, and is
// the effective default agent. Returns true if anything was modified.
//
// The three steps are independent and each is idempotent, so a retry after a
// partial failure (materialized but not yet defaulted, say) only performs
// what is still missing.
func EnsureHooksInstalled() (bool, error) {
	configPath, err := irrlichtAgentConfigPath()
	if err != nil {
		return false, err
	}

	modified := false
	if _, statErr := os.Stat(configPath); errors.Is(statErr, os.ErrNotExist) {
		if err := kiroAgentCreateFromDefault(context.Background(), irrlichtAgentName, kiroBuiltinDefaultAgent); err != nil {
			return false, err
		}
		modified = true
	} else if statErr != nil {
		return false, statErr
	}

	hookMod, err := ensureFlatHooksInstalled(kiroHookConfig(configPath))
	if err != nil {
		return modified, err
	}
	modified = modified || hookMod

	defaultMod, err := ensureDefaultAgent(context.Background())
	if err != nil {
		return modified, err
	}
	modified = modified || defaultMod

	return modified, nil
}

// ensureDefaultAgent points chat.defaultAgent at the irrlicht agent,
// recording whatever it held before (once — see recordPriorDefaultAgentOnce)
// so UninstallHooks can restore it. A no-op, reported as unmodified, when
// irrlicht is already the default.
func ensureDefaultAgent(ctx context.Context) (bool, error) {
	settingsPath, err := kiroSettingsPath()
	if err != nil {
		return false, err
	}
	settings, err := hookjson.ReadSettings(settingsPath)
	if err != nil {
		return false, err
	}
	current, _ := settings[defaultAgentSettingsKey].(string)
	if current == irrlichtAgentName {
		return false, nil
	}
	if err := recordPriorDefaultAgentOnce(current); err != nil {
		return false, err
	}
	if err := kiroAgentSetDefault(ctx, irrlichtAgentName); err != nil {
		return false, err
	}
	return true, nil
}

// VerifyHooksInstalled reports what our entries look like in the irrlicht
// agent config right now, without writing anything (issue #1372). Read-only
// by construction: it neither materializes the agent config nor touches
// chat.defaultAgent, so an absent config reports every event Missing rather
// than being created by the read.
func VerifyHooksInstalled() (agent.HookEntryStatus, error) {
	configPath, err := irrlichtAgentConfigPath()
	if err != nil {
		return agent.HookEntryStatus{}, err
	}
	return verifyFlatHooksInstalled(kiroHookConfig(configPath))
}

// UninstallHooks removes irrlicht's flat hook entries from the irrlicht
// agent config and restores whatever chat.defaultAgent held before this
// adapter first changed it. Returns true if anything was modified.
//
// The agent config file itself is left in place, inert, rather than deleted:
// once defaulted away from, the user may since have started using it
// directly (kiro-cli does not distinguish "the agent irrlicht created" from
// any other), and removing a file out from under a running kiro-cli chat
// session is a worse surprise than an unused file surviving a toggle-off.
func UninstallHooks() (bool, error) {
	modified := false

	if restored, err := restorePriorDefaultAgent(context.Background()); err != nil {
		return modified, err
	} else if restored {
		modified = true
	}

	configPath, err := irrlichtAgentConfigPath()
	if err != nil {
		return modified, err
	}
	if _, statErr := os.Stat(configPath); statErr != nil {
		// Nothing to strip entries from — either never installed or already
		// gone. Not an error: uninstall of an absent install is a no-op.
		return modified, nil
	}
	hookMod, err := uninstallFlatHooks(kiroHookConfig(configPath))
	if err != nil {
		return modified, err
	}
	return modified || hookMod, nil
}

// priorDefaultState is the sidecar irrlicht owns to remember what
// chat.defaultAgent held before this adapter's first grant.
type priorDefaultState struct {
	// PriorDefaultAgent is the value chat.defaultAgent held. Empty means the
	// key was absent — the user was running the built-in default with no
	// explicit choice on record.
	PriorDefaultAgent string `json:"prior_default_agent"`
}

// recordPriorDefaultAgentOnce persists prior into the sidecar state file,
// but only if nothing is recorded yet. Never overwrites an existing record:
// a later EnsureHooksInstalled call (a daemon restart while already granted,
// say) must not clobber the TRUE original value with whatever
// chat.defaultAgent happens to hold on that later call — which, once we are
// installed, is "irrlicht" itself.
func recordPriorDefaultAgentOnce(prior string) error {
	path, err := priorDefaultStatePath()
	if err != nil {
		return err
	}
	if _, statErr := os.Stat(path); statErr == nil {
		return nil
	}
	data, err := json.Marshal(priorDefaultState{PriorDefaultAgent: prior})
	if err != nil {
		return err
	}
	return atomicWriteFile(path, data)
}

// restorePriorDefaultAgent points chat.defaultAgent back at whatever it held
// before this adapter's first grant, and removes the sidecar record. Reports
// whether it changed anything.
//
// Two guards keep this from doing the wrong thing on an unusual sequence:
// nothing to restore (we never changed it — a granted-but-never-applied
// permission, or an install that failed before reaching ensureDefaultAgent)
// is a no-op, and chat.defaultAgent no longer being "irrlicht" (the user, or
// something else, already pointed it elsewhere) leaves that choice alone
// rather than overwriting it — the same "leave a foreign entry alone" rule
// every hook uninstall in this codebase follows, applied to a single-valued
// setting instead of an array of entries.
func restorePriorDefaultAgent(ctx context.Context) (bool, error) {
	statePath, err := priorDefaultStatePath()
	if err != nil {
		return false, err
	}
	data, err := os.ReadFile(statePath)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var state priorDefaultState
	if err := json.Unmarshal(data, &state); err != nil {
		return false, err
	}

	settingsPath, err := kiroSettingsPath()
	if err != nil {
		return false, err
	}
	settings, err := hookjson.ReadSettings(settingsPath)
	if err != nil {
		return false, err
	}
	current, _ := settings[defaultAgentSettingsKey].(string)
	if current != irrlichtAgentName {
		_ = os.Remove(statePath)
		return false, nil
	}

	restoreTo := state.PriorDefaultAgent
	if restoreTo == "" {
		// No prior explicit choice — the built-in default was in effect.
		// kiro-cli has no "unset" verb, so its own built-in name is the
		// honest restoration.
		restoreTo = kiroBuiltinDefaultAgent
	}
	if err := kiroAgentSetDefault(ctx, restoreTo); err != nil {
		return false, err
	}
	_ = os.Remove(statePath)
	return true, nil
}

// --- path resolution ---

// kiroHome resolves the absolute Kiro CLI config directory: $KIRO_HOME when
// that override is set and absolute, else $HOME/.kiro. Composed from
// agentpaths for the same reason copilot's copilotHome is: a hand-rolled
// override read is how a relative env value silently gets ignored by one
// caller and honored by another.
func kiroHome() (string, error) {
	return agentpaths.AbsRoot(agentpaths.FromEnv("kirocli", kiroHomeEnvVar, defaultKiroHomeDirName))
}

// defaultKiroHomeDirName is the $HOME-relative Kiro CLI config directory —
// the parent of sessions/cli (adapter.go's defaultRootDir), agents/ and
// settings/.
const defaultKiroHomeDirName = ".kiro"

// irrlichtAgentConfigPath is the agent config file this permission's
// Writes.Path resolves — the file the hook entries actually live in.
func irrlichtAgentConfigPath() (string, error) {
	home, err := kiroHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, kiroAgentsDirName, irrlichtAgentName+".json"), nil
}

// kiroSettingsPath is kiro-cli's own general CLI settings file — where
// chat.defaultAgent lives.
func kiroSettingsPath() (string, error) {
	home, err := kiroHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, kiroSettingsDirName, kiroSettingsFilename), nil
}

// priorDefaultStatePath is irrlicht's own sidecar record, a sibling of
// agents/ and settings/ rather than a member of either — see
// priorDefaultStateFilename's doc for why it cannot live inside agents/.
func priorDefaultStatePath() (string, error) {
	home, err := kiroHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, priorDefaultStateFilename), nil
}

// atomicWriteFile writes data to path via a temp file + rename so a reader
// (or kiro-cli itself) never observes a half-written file. Creates the
// parent dir. Matches every sibling hook-installing adapter's own atomic
// writer.
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
