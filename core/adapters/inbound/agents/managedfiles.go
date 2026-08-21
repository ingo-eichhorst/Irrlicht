package agents

import (
	"fmt"
	"path/filepath"

	"irrlicht/core/domain/agent"
)

// ManagedUserFile is one permission's declared shared user-owned file,
// resolved.
type ManagedUserFile struct {
	// Adapter is the declaration's label (agent.Identity.Name) — the key the
	// consent store records permissions under.
	Adapter string
	// Key is the permission key that writes the file.
	Key string
	// Path is the resolved, absolute file the permission writes.
	Path string
	// Also holds the resolved, absolute paths of every additional file the
	// SAME permission's Apply closure writes (agent.ManagedUserFile.Also,
	// #1718) — empty for the eight declarations that predate that field. It is
	// resolved with the same rigor as Path (non-nil, resolves without error,
	// absolute), because the two consumers that read it need completeness for
	// the identical reason they need it of Path: #1449's grant-all refusal must
	// see every file an Apply can write, not just the first, and the
	// recorder's backup/restore sweep (ManagedUserFiles) would otherwise hand
	// a file that was never snapshotted back to the user unrestored.
	Also []string
	// Uninstall removes irrlicht's content from Path (and, for a permission
	// declaring Also, from every one of those files too — that is the
	// closure's own job, not something this projection orchestrates),
	// reporting whether it modified anything.
	Uninstall func() (bool, error)
}

// ManagedUserFiles projects a consent catalog onto the set of shared,
// user-owned files irrlicht writes — the single source both `irrlichd
// --uninstall-hooks` (via HookConfigs) and the onboarding recorder's file
// snapshot read (#1357, widened by #1383).
//
// Before this existed each of them carried its own two-element literal list.
// Both were correct, and both would have been silently wrong the first time a
// third hook adapter landed: uninstall would leave that agent's entries in the
// user's real config pointing at a dead port forever, and a recording would
// repoint the same file at a dead recorder port and never hand it back.
//
// #1383 widened it from hooks to every managed file. A recording daemon runs
// IRRLICHT_PERMISSION_MODE=grant-all, so it exercises every modify permission's
// Apply closure — not just the hook installers. Projecting only hooks left the
// user's own ~/.claude/CLAUDE.md and ~/.config/kitty/kitty.conf carrying
// irrlicht's managed blocks after the recorder was gone.
//
// A declaration that is present but unusable is an error rather than a skip.
// Dropping it would reproduce exactly the failure this function exists to
// remove — a file-writing permission absent from both lists — only harder to
// notice, because nothing would say so.
//
// It takes the catalog rather than calling All() itself, matching the other
// registry projections in maps.go — and, since #1383, because the catalog it
// must run over is strictly LARGER than All(): the daemon's consent catalog is
// All() plus three daemon-wide declarations composed in core/cmd/irrlichd
// (gastown, launcher, kitty), and package agents cannot import
// processlifecycle, which imports it back. consentCatalog there is the one
// place that composition lives.
func ManagedUserFiles(catalog []agent.Agent) ([]ManagedUserFile, error) {
	return collectManagedFiles(catalog, func(string) bool { return true })
}

// collectManagedFiles is the shared walk behind the full projection and the
// narrowed ones. `want` decides which keys are collected AND which are
// validated, and those being the same set is the point.
//
// A narrowed command must not fail on a declaration outside its narrowing.
// Resolving the whole catalog and filtering afterwards made `--uninstall-task-eta`
// abort — removing nothing from the user's CLAUDE.md — because kitty's
// remote-control path did not resolve, a permission that command never touches.
// It is the one command pointed at a user-authored file, and before #1437 it
// was immune to this because it called the adapter directly.
//
// The full projection keeps the opposite semantics deliberately: for
// `--print-managed-files` and the onboarding recorder, whose job IS
// completeness, a declaration that is present but unusable must be an error
// rather than a skip — an empty or short list there reads as "nothing to
// protect" and the recorder writes over the user's real files unprotected.
//
// Note this does not weaken the existing lock in
// TestManagedUserFilesRejectsUnusableDeclarations: a broken HOOKS declaration
// is still rejected by HookConfigs, because it is inside that filter. What
// changes is only whether one command dies on another command's declaration.
func collectManagedFiles(catalog []agent.Agent, want func(key string) bool) ([]ManagedUserFile, error) {
	var out []ManagedUserFile
	for _, a := range catalog {
		for _, p := range a.Permissions {
			if p.Writes == nil || !want(p.Key) {
				continue
			}
			if p.Writes.Path == nil || p.Writes.Uninstall == nil {
				return nil, fmt.Errorf("adapter %q permission %q: ManagedUserFile needs both Path and Uninstall", a.Identity.Name, p.Key)
			}
			path, err := p.Writes.Path()
			if err != nil {
				return nil, fmt.Errorf("adapter %q permission %q: resolve managed file path: %w", a.Identity.Name, p.Key, err)
			}
			if !filepath.IsAbs(path) {
				return nil, fmt.Errorf("adapter %q permission %q: managed file path %q is not absolute", a.Identity.Name, p.Key, path)
			}
			// Also gets the identical rigor as Path (#1718), over the SAME
			// selected permission — this is not resolving another key's
			// declaration, so it does not weaken the narrowed-projection rule
			// collectManagedFiles otherwise upholds (see configsForKey's doc).
			also := make([]string, 0, len(p.Writes.Also))
			for i, resolveAlso := range p.Writes.Also {
				if resolveAlso == nil {
					return nil, fmt.Errorf("adapter %q permission %q: ManagedUserFile.Also[%d] is a nil resolver", a.Identity.Name, p.Key, i)
				}
				aPath, err := resolveAlso()
				if err != nil {
					return nil, fmt.Errorf("adapter %q permission %q: resolve managed file Also[%d]: %w", a.Identity.Name, p.Key, i, err)
				}
				if !filepath.IsAbs(aPath) {
					return nil, fmt.Errorf("adapter %q permission %q: managed file Also[%d] path %q is not absolute", a.Identity.Name, p.Key, i, aPath)
				}
				also = append(also, aPath)
			}
			out = append(out, ManagedUserFile{
				Adapter:   a.Identity.Name,
				Key:       p.Key,
				Path:      path,
				Also:      also,
				Uninstall: p.Writes.Uninstall,
			})
		}
	}
	return out, nil
}

// HookConfigs is the hooks slice of the managed-file declaration — the agent
// config files irrlicht installs hooks into, and nothing else.
//
// The narrowing is what keeps `irrlichd --uninstall-hooks` meaning what its
// name says after #1383 widened the declaration. That command removes hook
// entries and records the matching consent as denied; running the instructions
// or kitty uninstaller from it would revoke a capability the user never asked
// it to touch. The recorder, whose job is the opposite — protect everything a
// grant-all daemon can write — reads ManagedUserFiles directly.
func HookConfigs(catalog []agent.Agent) ([]ManagedUserFile, error) {
	return configsForKey(catalog, agent.HooksPermissionKey)
}

// InstructionConfigs is the instructions slice of the managed-file declaration
// — the user-level instruction files irrlicht writes managed blocks into, and
// nothing else. It is what `irrlichd --uninstall-task-eta` reads (#1437).
//
// It is a projection rather than a call to claudecode.UninstallInstructionBlocks
// for two reasons, and the second is the one that matters. The obvious one: a
// second adapter that grows an instruction block is covered by existing instead
// of by remembering. The load-bearing one: the projection carries the
// (Adapter, Key) pair, which is what lets the command record the matching
// consent as DENIED. Calling the uninstaller directly is exactly what the
// command did before #1437 — it removed the blocks and left the permission
// reading "granted", so PermissionService.Start's Apply re-run wrote them back
// on the next daemon start.
//
// Narrowed for the same reason HookConfigs is, and symmetrically: running the
// hook uninstallers from a command named --uninstall-task-eta would revoke a
// capability the user never asked it to touch.
func InstructionConfigs(catalog []agent.Agent) ([]ManagedUserFile, error) {
	return configsForKey(catalog, agent.InstructionsPermissionKey)
}

// configsForKey is the shared narrowing behind HookConfigs and
// InstructionConfigs. One implementation, so the two commands cannot come to
// disagree about what "the files this key writes" means — the divergence #1437
// was: --uninstall-hooks projected the catalog while --uninstall-task-eta
// called one adapter function by hand.
//
// It narrows BEFORE resolving, so a command's failure domain is the same set as
// its blast radius. See collectManagedFiles.
func configsForKey(catalog []agent.Agent, key string) ([]ManagedUserFile, error) {
	return collectManagedFiles(catalog, func(k string) bool { return k == key })
}
