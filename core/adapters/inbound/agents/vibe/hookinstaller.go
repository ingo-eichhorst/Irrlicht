// hookinstaller.go manages Mistral Vibe's hook entry in the user's own
// $VIBE_HOME/hooks.toml, and the experimental-hooks gate in
// $VIBE_HOME/config.toml, for the irrlicht daemon (issue #1718, epic #1355).
//
// TWO files, not one — the first structural difference from every hook
// adapter that shipped before this one. Vibe 2.19.1 gates its hooks behind
// enable_experimental_hooks (default false, source-read at
// vibe/core/config/_settings.py in the installed 2.19.1 package) in
// config.toml; hooks.toml holds the entries themselves and is never read at
// all unless that flag is true. Both are TOML, so both go through
// hooktoml (core/adapters/inbound/agents/hooktoml), the comment-preserving
// sibling of hookjson built for exactly this ticket — never hookjson
// itself, which is JSON-specific.
//
// TWO artifacts, chosen by the installed Vibe version (#2171). Vibe 2.21.0
// renamed the hook type users write from post_agent_turn to post_agent and
// stopped reading the flag. Source: the hook reference each wheel bundles at
// vibe/core/skills/builtins/vibe.py, "Required: post_agent_turn | ..." in
// 2.20.0 and "Required: post_agent | ..." in 2.21.0, read during triage. Both
// TUIs were started on the old entry in that ticket's manual check: 2.21.0 and
// 3.1.4 each reported "type: Input should be 'post_agent', ..." and skipped
// it. So the legacy variant below writes the 2.19.1 artifact unchanged, and
// the current one writes `type = "post_agent"` and deletes the flag line:
// 3.1.4 reported "`enable_experimental_hooks` is not a Vibe setting" for it,
// while 2.21.0 said nothing (its settings model ignores unknown keys). Which
// release between those two started warning was not read.
//
// Delivery is the beacon (core/pkg/hookbeacon), not a native http hook:
// vibe's HookExecutor spawns hook.command via asyncio.create_subprocess_shell
// and nothing else (source-read, vibe/core/hooks/executor.py) — there is no
// HTTP hook type to ask for, so a shell command is the only delivery this
// CLI can be given, exactly gemini-cli's position.
package vibe

import (
	"bytes"
	"path/filepath"

	"irrlicht/core/adapters/inbound/agents/hooktoml"
	"irrlicht/core/domain/agent"
	"irrlicht/core/pkg/atomicfile"
	"irrlicht/core/pkg/hookbeacon"
)

// hooksFilename is Vibe's own hook-definitions file, read only when
// enable_experimental_hooks is true. Source-read: harness_files/
// _harness_manager.py's hook_files property resolves the user-level entry as
// VIBE_HOME/"hooks.toml" — live-fired against the installed 2.19.1 CLI
// during this issue's audit, which confirmed both the path and that
// hooks.toml is never written by Vibe itself (grep of the whole installed
// package finds no write path), so this file is purely ours to manage.
const hooksFilename = "hooks.toml"

// experimentalHooksFlag is the config.toml key that gates Vibe's hook system
// entirely. Source-read: vibe/core/config/_settings.py declares
// `enable_experimental_hooks: bool = Field(default=False, exclude=True)`, and
// vibe/core/hooks/config.py's load_hooks_from_fs returns no hooks at all when
// it is false — live-fired: with it unset, an installed hooks.toml entry
// never runs.
const experimentalHooksFlag = "enable_experimental_hooks"

// hookEventPostAgentTurn is the one Vibe hook event the legacy artifact
// installs, for Vibe 2.19.1 up to 2.21.0. See hooks.go's package doc for why
// before_tool/after_tool are not.
const hookEventPostAgentTurn = "post_agent_turn"

// hookEventPostAgent is the same turn-end event under the name Vibe 2.21.0
// and newer accept (#2171) — the current artifact's one entry.
const hookEventPostAgent = "post_agent"

// installedHookEvents is the legacy artifact's event list — a single-element
// slice, but still a variable rather than a literal so the disclosure and
// version-gate contracts read the SAME list the installer builds from,
// instead of a copy that could drift.
var installedHookEvents = []string{hookEventPostAgentTurn}

// installedHookEventsCurrent is the current artifact's event list, for the
// same reason.
var installedHookEventsCurrent = []string{hookEventPostAgent}

// hookEventSince and hookEventSinceCurrent pin each artifact's one event to
// the version that artifact was read against, its own floor: post_agent_turn
// was live-fired on 2.19.1 (#1718), and post_agent is named by 2.21.0's
// bundled hook reference (source-read, #2171). An earlier number would be a
// claim nothing here supports.
var (
	hookEventSince        = map[string]string{hookEventPostAgentTurn: minVibeVersion}
	hookEventSinceCurrent = map[string]string{hookEventPostAgent: minVibePostAgentVersion}
)

// hookEntryName is the fixed `name` field of the [[hooks]] table this
// adapter writes. Vibe requires hook names to be unique within hooks.toml
// (source-read, config.py's _load_hooks_file: a duplicate name becomes a
// HookConfigIssue and the second entry is dropped) — a fixed, distinctive
// name is how a stray hand-written entry sharing it surfaces as Vibe's own
// config-load warning rather than as a silent collision.
const hookEntryName = "irrlicht-turn-done"

// minVibeVersion is the lowest Mistral Vibe version this adapter's install
// requires — the ONLY version directly verified (source-read AND live-fired
// against the installed CLI during this issue's audit). #1355 recorded that
// Vibe's hook schema had "breaking changes at 2.15", but that claim is
// UNVERIFIED here: an attempted upstream changelog fetch produced content
// contradicted by this adapter's own live-fired 2.19.1 event names and was
// discarded as a likely fabrication rather than cited.
//
// The cost of pinning to the verified version rather than the unverified
// 2.15 claim: core/pkg/cliversion fails open only on an UNPARSEABLE version,
// so a real 2.15-2.18 install is refused by this floor, not waved through —
// that is a real, accepted cost, not a free choice. Lowering the floor later
// is cheap once someone verifies an earlier version against source; pinning
// too low first is not (a hooks.toml written in a schema Vibe silently
// ignores is indistinguishable from a working install).
const minVibeVersion = "2.19.1"

// minVibePostAgentVersion is the first Vibe version that accepts
// `type = "post_agent"`, and so the legacy artifact's exclusive ceiling and
// the current artifact's floor (#2171). 2.20.0 and 2.21.0 are adjacent
// releases whose bundled hook references name post_agent_turn and post_agent
// respectively (source-read during triage). Started without a prompt in
// #2171's manual check, the 2.21.0 TUI's banner counted "1 hook" for the
// post_agent entry and none for the post_agent_turn one, so 2.21.0 loads it.
// Not live-fired: no Vibe model credential was available on the machine this
// ticket was executed on, so whether that hook actually runs at turn end is
// unverified.
const minVibePostAgentVersion = "2.21.0"

// HookEndpointPath is the daemon's Mistral Vibe hook route — unused for
// actual HTTP routing (delivery is address-free, see below) but still
// registered so startup.go's route table and beaconroute_test.go's pinned
// map derive from the same segment this installer's Sentinel is scoped to,
// matching every other adapter's convention.
var HookEndpointPath = hookbeacon.EndpointPath(AdapterName)

func hooksTomlPath() (string, error) {
	home, err := vibeHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, hooksFilename), nil
}

// vibeConfigTomlPath is the SAME resolver configuredSaveDir() (config.go)
// already reads from — one function, so the file the hooks flag is written
// into and the file the transcripts permission reads [session_logging] from
// cannot resolve to two different paths.
func vibeConfigTomlPath() (string, error) {
	home, err := vibeHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, configFilename), nil
}

// vibeHookEntryBlock renders the canonical [[hooks]] table of the given hook
// type for command, already resolved by the caller (see
// hookbeacon.InstalledCommand's own doc for why: Uninstall must keep working
// once the binary it names has been removed, and never needs to resolve it at
// all).
func vibeHookEntryBlock(hookType, command string) []byte {
	return []byte("[[hooks]]\n" +
		"name = " + hooktoml.Quote(hookEntryName) + "\n" +
		"type = " + hooktoml.Quote(hookType) + "\n" +
		"command = " + hooktoml.Quote(command) + "\n")
}

// vibeHookConfig describes this adapter's install to hooktoml, for a command
// already resolved by the caller — the identical split ourHookEntry/
// beaconEntry draws in geminicli/hookinstaller.go, for the identical reason.
//
// IsCanonical compares whole-block bytes rather than parsing the command
// field back out: hooktoml's own sentinel search (Sentinel is a substring of
// the rendered command, hookbeacon.Sentinel) already found this block by its
// command; whether it is CURRENT is then simply "are these exactly the bytes
// we would write today", which is also what makes a stray leading comment on
// an old install (from a version of this adapter that wrote one) trigger a
// clean rewrite rather than being treated as canonical-with-decoration.
//
// The same whole-block comparison is what tells the two artifacts apart
// (#2171): both blocks carry the same sentinel, so each variant finds the
// other's block and reads it as not canonical — Stale — which is what lets
// the #1372 re-verification rewrite it in place after an upgrade or a
// downgrade.
func vibeHookConfig(path, hookType, command string) hooktoml.HookConfig {
	canonical := bytes.TrimSpace(vibeHookEntryBlock(hookType, command))
	return hooktoml.HookConfig{
		Path:     path,
		Sentinel: hookbeacon.Sentinel(AdapterName),
		Entry:    func() []byte { return vibeHookEntryBlock(hookType, command) },
		IsCanonical: func(section []byte) bool {
			return bytes.Equal(bytes.TrimSpace(section), canonical)
		},
		WriteFile: atomicfile.WriteFile,
	}
}

// installPaths resolves both files and the beacon command every installer
// entry point needs.
func installPaths() (hooksPath, configPath, command string, err error) {
	if hooksPath, err = hooksTomlPath(); err != nil {
		return "", "", "", err
	}
	if configPath, err = vibeConfigTomlPath(); err != nil {
		return "", "", "", err
	}
	if command, err = hookbeacon.InstalledCommand(AdapterName); err != nil {
		return "", "", "", err
	}
	return hooksPath, configPath, command, nil
}

// EnsureHooksInstalled writes the legacy artifact, for Vibe 2.19.1 up to
// 2.21.0: it adds irrlicht's post_agent_turn entry to hooks.toml and turns on
// enable_experimental_hooks in config.toml, creating either file if absent.
// Every other key in config.toml, and every other [[hooks]] block in
// hooks.toml, is left byte-for-byte alone. Returns true if either file was
// modified.
func EnsureHooksInstalled() (bool, error) {
	hooksPath, configPath, command, err := installPaths()
	if err != nil {
		return false, err
	}

	flagModified, err := hooktoml.EnsureBoolTrue(configPath, experimentalHooksFlag, atomicfile.WriteFile)
	if err != nil {
		return false, err
	}
	entryModified, err := hooktoml.EnsureInstalled(vibeHookConfig(hooksPath, hookEventPostAgentTurn, command))
	if err != nil {
		return flagModified, err
	}
	return flagModified || entryModified, nil
}

// EnsureHooksInstalledCurrent writes the current artifact, for Vibe 2.21.0
// and newer (#2171): irrlicht's post_agent entry in hooks.toml, rewriting a
// legacy post_agent_turn entry in place, and no enable_experimental_hooks key
// in config.toml — a line carrying it is deleted whoever wrote it, because
// Vibe no longer reads it and 3.1.4 warns about it at every start whatever
// its value. config.toml is never created. Returns true if either file was
// modified.
//
// The entry is written first: if config.toml holds a construct hooktoml
// refuses to scan, the delete fails AFTER a working hook is already in place,
// and the warning is all that is left of the damage.
func EnsureHooksInstalledCurrent() (bool, error) {
	hooksPath, configPath, command, err := installPaths()
	if err != nil {
		return false, err
	}

	entryModified, err := hooktoml.EnsureInstalled(vibeHookConfig(hooksPath, hookEventPostAgent, command))
	if err != nil {
		return false, err
	}
	flagModified, err := hooktoml.DeleteTopLevelKeyIfPresent(configPath, experimentalHooksFlag, atomicfile.WriteFile)
	if err != nil {
		return entryModified, err
	}
	return entryModified || flagModified, nil
}

// UninstallHooks removes irrlicht's entry from hooks.toml, whichever
// artifact wrote it: both carry the same sentinel, and hooktoml finds the
// block by that alone. It also removes enable_experimental_hooks from
// config.toml, but ONLY when hooks.toml is left with no [[hooks]] block at
// all afterward — on a Vibe older than 2.21.0 a user's OWN hand-written hooks
// in that same file still need the flag held true, and turning it off out
// from under them would silently disable content this permission never
// touched. Returns true if either file was modified.
//
// The key is deleted rather than set false (#2171): the two read the same on
// 2.19.1 (`Field(default=False)`), and 3.1.4 warns about the key whatever its
// value. Uninstall is one rule for both artifacts, run without knowing the
// installed version, so it takes the form that is right for every version.
func UninstallHooks() (bool, error) {
	hooksPath, configPath, command, err := installPaths()
	if err != nil {
		return false, err
	}

	// The hook type does not matter here: Uninstall matches by sentinel and
	// removes the block whatever its bytes.
	entryModified, err := hooktoml.Uninstall(vibeHookConfig(hooksPath, hookEventPostAgentTurn, command))
	if err != nil {
		return false, err
	}

	hasOther, err := hooktoml.HasAnyHooksBlock(hooksPath)
	if err != nil {
		return entryModified, err
	}
	if hasOther {
		return entryModified, nil
	}

	flagModified, err := hooktoml.DeleteTopLevelKeyIfPresent(configPath, experimentalHooksFlag, atomicfile.WriteFile)
	if err != nil {
		return entryModified, err
	}
	return entryModified || flagModified, nil
}

// VerifyHooksInstalled reports what the legacy artifact looks like RIGHT NOW,
// without writing anything (issue #1372). Vibe's own settings UI can rewrite
// config.toml the same way gemini-cli's does (VibeConfig.save_updates merges
// into the RAW dict off disk rather than a filtered model_dump, so our hand-
// written flag survives an ordinary settings change — but nothing stops a
// future in-app hooks editor, or a hand edit, from dropping either file's
// content), so this is what lets services.HookEntryVerifier notice and
// repair a dead install instead of silently trusting a "granted" permission.
//
// A missing OR canonical-but-ungated entry both read as Missing: the
// practical effect on the daemon is identical either way — the hook cannot
// fire — and HookEntryStatus has no third state to spend on distinguishing
// "the TOML block is there but inert" from "the TOML block is gone". A block
// of ours that is not canonical reads Stale first, gated or not, which is how
// the current artifact (a post_agent block, no flag) reads after a downgrade:
// agent.HookVariant.Verify's rule that another variant's file reads Stale.
func VerifyHooksInstalled() (agent.HookEntryStatus, error) {
	hooksPath, configPath, command, err := installPaths()
	if err != nil {
		return agent.HookEntryStatus{}, err
	}

	present, canonical, err := hooktoml.Inspect(vibeHookConfig(hooksPath, hookEventPostAgentTurn, command))
	if err != nil {
		return agent.HookEntryStatus{}, err
	}
	flagOn, _, err := hooktoml.TopLevelBool(configPath, experimentalHooksFlag)
	if err != nil {
		return agent.HookEntryStatus{}, err
	}

	switch {
	case !present:
		return agent.HookEntryStatus{Missing: []string{hookEventPostAgentTurn}}, nil
	case !canonical:
		return agent.HookEntryStatus{Stale: []string{hookEventPostAgentTurn}}, nil
	case !flagOn:
		return agent.HookEntryStatus{Missing: []string{hookEventPostAgentTurn}}, nil
	default:
		return agent.HookEntryStatus{}, nil
	}
}

// VerifyHooksInstalledCurrent reports, read-only, whether the current
// artifact is in place (#2171): no entry of ours reads Missing; a block of
// ours that is not the canonical post_agent block — a legacy post_agent_turn
// block above all — reads Stale; and so does an enable_experimental_hooks key
// still in config.toml at any value, because the current Apply would delete
// it and the file is therefore not as that Apply leaves it.
func VerifyHooksInstalledCurrent() (agent.HookEntryStatus, error) {
	hooksPath, configPath, command, err := installPaths()
	if err != nil {
		return agent.HookEntryStatus{}, err
	}

	present, canonical, err := hooktoml.Inspect(vibeHookConfig(hooksPath, hookEventPostAgent, command))
	if err != nil {
		return agent.HookEntryStatus{}, err
	}
	_, flagFound, err := hooktoml.TopLevelBool(configPath, experimentalHooksFlag)
	if err != nil {
		return agent.HookEntryStatus{}, err
	}

	switch {
	case !present:
		return agent.HookEntryStatus{Missing: []string{hookEventPostAgent}}, nil
	case !canonical || flagFound:
		return agent.HookEntryStatus{Stale: []string{hookEventPostAgent}}, nil
	default:
		return agent.HookEntryStatus{}, nil
	}
}
