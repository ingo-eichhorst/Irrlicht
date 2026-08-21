// hookinstaller.go manages Mistral Vibe's hook entry in the user's own
// $VIBE_HOME/hooks.toml, and the experimental-hooks gate in
// $VIBE_HOME/config.toml, for the irrlicht daemon (issue #1718, epic #1355).
//
// TWO files, not one — the first structural difference from every hook
// adapter that shipped before this one. Vibe's hooks are gated behind
// enable_experimental_hooks (default false, source-read at
// vibe/core/config/_settings.py in the installed 2.19.1 package) in
// config.toml; hooks.toml holds the entries themselves and is never read at
// all unless that flag is true. Both are TOML, so both go through
// hooktoml (core/adapters/inbound/agents/hooktoml), the comment-preserving
// sibling of hookjson built for exactly this ticket — never hookjson
// itself, which is JSON-specific.
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

// hookEventPostAgentTurn is the one Vibe hook event this adapter installs.
// See hooks.go's package doc for why before_tool/after_tool are not.
const hookEventPostAgentTurn = "post_agent_turn"

// installedHookEvents is the installer's event list — a single-element
// slice, but still a variable rather than a literal so the disclosure and
// version-gate contracts read the SAME list the installer would extend if a
// second Vibe event were ever added, instead of a copy that could drift.
var installedHookEvents = []string{hookEventPostAgentTurn}

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

// vibeHookEntryBlock renders the canonical [[hooks]] table for command,
// already resolved by the caller (see hookbeacon.InstalledCommand's own doc
// for why: Uninstall must keep working once the binary it names has been
// removed, and never needs to resolve it at all).
func vibeHookEntryBlock(command string) []byte {
	return []byte("[[hooks]]\n" +
		"name = " + hooktoml.Quote(hookEntryName) + "\n" +
		"type = " + hooktoml.Quote(hookEventPostAgentTurn) + "\n" +
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
func vibeHookConfig(path, command string) hooktoml.HookConfig {
	canonical := bytes.TrimSpace(vibeHookEntryBlock(command))
	return hooktoml.HookConfig{
		Path:     path,
		Sentinel: hookbeacon.Sentinel(AdapterName),
		Entry:    func() []byte { return vibeHookEntryBlock(command) },
		IsCanonical: func(section []byte) bool {
			return bytes.Equal(bytes.TrimSpace(section), canonical)
		},
		WriteFile: hooktoml.AtomicWriteFile,
	}
}

// EnsureHooksInstalled adds irrlicht's hook entry to hooks.toml and turns on
// enable_experimental_hooks in config.toml, creating either file if absent.
// Every other key in config.toml, and every other [[hooks]] block in
// hooks.toml, is left byte-for-byte alone. Returns true if either file was
// modified.
func EnsureHooksInstalled() (bool, error) {
	hooksPath, err := hooksTomlPath()
	if err != nil {
		return false, err
	}
	configPath, err := vibeConfigTomlPath()
	if err != nil {
		return false, err
	}
	command, err := hookbeacon.InstalledCommand(AdapterName)
	if err != nil {
		return false, err
	}

	flagModified, err := hooktoml.EnsureBoolTrue(configPath, experimentalHooksFlag, hooktoml.AtomicWriteFile)
	if err != nil {
		return false, err
	}
	entryModified, err := hooktoml.EnsureInstalled(vibeHookConfig(hooksPath, command))
	if err != nil {
		return flagModified, err
	}
	return flagModified || entryModified, nil
}

// UninstallHooks removes irrlicht's entry from hooks.toml. It also clears
// enable_experimental_hooks in config.toml, but ONLY when hooks.toml is left
// with no [[hooks]] block at all afterward — a user's OWN hand-written hooks
// in that same file still need the flag held true, and turning it off out
// from under them would silently disable content this permission never
// touched. Returns true if either file was modified.
func UninstallHooks() (bool, error) {
	hooksPath, err := hooksTomlPath()
	if err != nil {
		return false, err
	}
	configPath, err := vibeConfigTomlPath()
	if err != nil {
		return false, err
	}
	command, err := hookbeacon.InstalledCommand(AdapterName)
	if err != nil {
		return false, err
	}

	entryModified, err := hooktoml.Uninstall(vibeHookConfig(hooksPath, command))
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

	flagModified, err := hooktoml.ClearBoolIfPresent(configPath, experimentalHooksFlag, hooktoml.AtomicWriteFile)
	if err != nil {
		return entryModified, err
	}
	return entryModified || flagModified, nil
}

// VerifyHooksInstalled reports what our install looks like RIGHT NOW,
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
// "the TOML block is there but inert" from "the TOML block is gone".
func VerifyHooksInstalled() (agent.HookEntryStatus, error) {
	hooksPath, err := hooksTomlPath()
	if err != nil {
		return agent.HookEntryStatus{}, err
	}
	configPath, err := vibeConfigTomlPath()
	if err != nil {
		return agent.HookEntryStatus{}, err
	}
	command, err := hookbeacon.InstalledCommand(AdapterName)
	if err != nil {
		return agent.HookEntryStatus{}, err
	}

	present, canonical, err := hooktoml.Inspect(vibeHookConfig(hooksPath, command))
	if err != nil {
		return agent.HookEntryStatus{}, err
	}
	flagOn, _, err := hooktoml.TopLevelBool(configPath, experimentalHooksFlag)
	if err != nil {
		return agent.HookEntryStatus{}, err
	}

	switch {
	case !present || !flagOn:
		return agent.HookEntryStatus{Missing: []string{hookEventPostAgentTurn}}, nil
	case !canonical:
		return agent.HookEntryStatus{Stale: []string{hookEventPostAgentTurn}}, nil
	default:
		return agent.HookEntryStatus{}, nil
	}
}
