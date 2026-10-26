package services

import (
	"context"
	"errors"
	"fmt"

	"irrlicht/core/domain/agent"
	"irrlicht/core/pkg/cliprobe"
	"irrlicht/core/pkg/cliversion"
)

// The upstream-CLI version gate (#1365, ceiling #2105, per-major variants
// #2108), in its own file for the reason permission_shared_config_gate.go gives
// for its own: it is refused from the same call site (runClosureEffect) but
// answers an unrelated question, and permission_service.go is the package's
// largest unit. Its tests are permission_version_gate_test.go,
// permission_version_ceiling_test.go and permission_version_variant_test.go.

// hookVersionRefusal resolves the installed upstream CLI version for a
// permission that declares a version bound — a floor, a ceiling (#2105), or
// per-major variants (#2108) — and decides what its install does.
//
// It returns the variant to install for a variant declaration, and nil for a
// single-artifact one, whose own Apply is the install. On a refusal it returns
// a non-nil error with the bound that refused: runClosureEffect removes an
// earlier install on a ceiling refusal and never on any other. It returns
// (nil, WithinBounds, nil) for every permission that declares no bound, which
// is every non-hook permission and any hook adapter that has not opted in.
//
// The version is resolved ONCE here, and the variant's Apply receives the
// answer by being the closure that runs: the adapter never asks the CLI again.
func (s *PermissionService) hookVersionRefusal(agentName string, p agent.Permission) (*agent.HookVariant, agent.VersionBound, error) {
	if !versionGated(p.Writes) {
		return nil, agent.WithinBounds, nil
	}
	installed, probeErr := resolveInstalledVersion(s.effectContext(), s.Granted, agentName, p.Writes)
	if probeErr != nil {
		// A probe that could not run is the state most likely to be misread
		// later as "the gate checked and approved", so it is said out loud. What
		// follows from it differs by shape: a single artifact fails open, and a
		// variant declaration cannot pick its artifact without a version.
		consequence := "installing anyway — an unreadable version is not an old one"
		if len(p.Writes.Variants) > 0 {
			consequence = "the artifact is chosen by the version, so an unreadable one installs nothing"
		}
		s.log.LogInfo("permissions", "", fmt.Sprintf(
			"%s: version probe failed (%v); %s", p.Key, probeErr, consequence))
	}

	variant, bound, why := p.Writes.SelectVariant(installed)
	if bound == agent.WithinBounds {
		return variant, bound, nil
	}
	return nil, bound, errors.New(why)
}

// versionGated reports whether a managed file's install depends on the
// upstream CLI version at all: it declares a floor or a ceiling, or per-major
// variants. A variant declaration is gated even with no envelope declared —
// its artifact cannot be chosen without a version, so it must never fall
// through to the permission's own Apply.
func versionGated(w *agent.ManagedUserFile) bool {
	return w != nil && (w.Version.Bounded() || len(w.Variants) > 0)
}

// resolveInstalledVersion is the ONE place the installed upstream CLI version
// is learnt, for the install (hookVersionRefusal) and for the #1372
// re-verification of a variant declaration (HookEntryVerifier) alike, so the
// two cannot disagree about which artifact is current.
//
// It prefers the adapter's own passive signal over running anything: Codex
// reads cli_version out of the transcript it is already tailing, which costs
// nothing and works when the binary is not on the daemon's PATH. That signal is
// consulted only while the permission it reads behind is granted
// (VersionGate.ObservedRequires, #2108): opencode's reads the session database,
// which only its `database` permission discloses. The probe is the fallback,
// for the cases shouldConfirmByProbe names.
//
// probeErr reports a probe that ran and failed; installed is then whatever the
// passive signal said, which is what the gate grades. The caller decides
// whether a failed probe is worth a log line.
func resolveInstalledVersion(ctx context.Context, granted func(agentName, key string) bool, agentName string, w *agent.ManagedUserFile) (installed string, probeErr error) {
	gate := w.Version
	if gate == nil {
		return "", nil
	}
	if gate.Observed != nil && observedAllowed(granted, agentName, gate.ObservedRequires) {
		installed = gate.Observed()
	}
	if !shouldConfirmByProbe(w, installed) {
		return installed, nil
	}
	probed, err := cliprobe.Probe(ctx, gate.Probe)
	if err != nil {
		return installed, err
	}
	return probed, nil
}

// observedAllowed reports whether a passive version source may be read: it
// declares no permission it reads behind, or that permission is granted. A nil
// granted reads as "nothing is granted" — the safe direction for a gate.
func observedAllowed(granted func(agentName, key string) bool, agentName, requires string) bool {
	if requires == "" {
		return true
	}
	return granted != nil && granted(agentName, requires)
}

// removeAfterCeilingRefusal removes the file an earlier install left behind,
// after hookVersionRefusal refused at the ceiling, and returns the error to
// record: the refusal, plus the removal's own failure if it had one.
//
// Why a ceiling removes and a floor does not (#2105): below the floor an
// earlier install can only have come from a newer CLI the user moved off, and
// cliprobe resolves /opt/homebrew/bin before PATH, so a floor verdict can be a
// stale reading of a brew 1.x the user does not run — deleting on it would
// change behaviour for every adapter with a floor. At or above the ceiling the
// earlier install is the 1.x artifact the new major rejects on every load
// (opencode 2.x logs `failed to load plugin` for it), and leaving it is the
// harm.
//
// For a variant declaration (#2108) the ceiling is the envelope's — the
// highest variant's — or a lower variant's when the version sits between two
// variants (agent.ManagedUserFile.SelectVariant). Inside a variant's range
// nothing is refused, so nothing is removed: the variant's own Apply rewrites
// another variant's file in place.
//
// Runs on every path that re-runs a granted effect — PermissionService.Start at
// daemon start, a wizard re-grant, and the #1372 repair through
// RepairGrantedHookInstall — because all three reach runClosureEffect. It goes
// through the adapter's declared Writes.Uninstall, so the adapter's own "is
// this ours" check decides what is removed: hookartifact.Installer.Uninstall
// returns early, removing nothing, for a file without irrlicht's sentinel.
// It logs only an actual removal: the repair loop retries a ceiling refusal on
// its back-off, against a file that is already gone.
func (s *PermissionService) removeAfterCeilingRefusal(e pendingEffect, refused error) error {
	if e.perm.Writes.Uninstall == nil {
		return refused
	}
	removed, err := e.perm.Writes.Uninstall()
	if err != nil {
		return fmt.Errorf("%w; removing the hook file an earlier install left behind also failed: %v", refused, err)
	}
	if removed {
		s.log.LogInfo("permissions", "", fmt.Sprintf(
			"%s/%s: removed the hook file an earlier install left behind, because %s",
			e.agentName, e.perm.Key, refused))
	}
	return refused
}

// shouldConfirmByProbe decides whether to spend a process on confirming the
// version. It returns false only for the case that needs no help: a passive
// signal that parses and already selects an install — clears every declared
// bound, and for a variant declaration lands inside a variant.
//
// The two cases it does probe are the ones where the passive answer cannot be
// relied on:
//
//   - It is unknown or unparseable. An adapter with no Observed source at all
//     (Claude Code) is always here, and so is one whose source returned
//     something it could not read — falling open without asking the binary
//     that could have answered would make the gate decorative.
//   - It parses and the gate refuses it. A passive signal is the LAST-RUN
//     version, not the installed one. Below a floor it can only be stale
//     downward: a user who upgrades and grants before starting a new session
//     would otherwise be refused on a version they no longer run, with nothing
//     installed and the permission still showing green. A false refusal is the
//     one direction this gate must never produce quietly, so it is worth a
//     process to be sure. At or above a ceiling (#2105), or between two
//     variants (#2108), the same probe is what catches a user who downgraded
//     back into a supported range.
//
// So for a variant declaration a passive answer that selects a variant WINS
// over the probe, which is never run: opencode's store reading 2.1.4 selects
// the 2.x artifact even where cliprobe would resolve a brew 1.19.4 first
// (#2105 correction 1). That is the direction #2104 item 3 measured to be
// right — opencode 1.x hangs on a store 2.x has migrated, so a store that
// 2.x wrote is not one a 1.x CLI is running against.
func shouldConfirmByProbe(w *agent.ManagedUserFile, installed string) bool {
	if len(w.Version.Probe) == 0 {
		return false
	}
	if _, parsed := cliversion.Parse(installed); !parsed {
		return true
	}
	_, bound, _ := w.SelectVariant(installed)
	return bound != agent.WithinBounds
}
