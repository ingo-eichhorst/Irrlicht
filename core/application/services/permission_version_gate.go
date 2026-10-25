package services

import (
	"errors"
	"fmt"

	"irrlicht/core/domain/agent"
	"irrlicht/core/pkg/cliprobe"
	"irrlicht/core/pkg/cliversion"
)

// The upstream-CLI version gate (#1365, ceiling #2105), in its own file for the
// reason permission_shared_config_gate.go gives for its own: it is refused from
// the same call site (runClosureEffect) but answers an unrelated question, and
// permission_service.go is the package's largest unit. Its tests are
// permission_version_gate_test.go and permission_version_ceiling_test.go.

// hookVersionRefusal resolves the installed upstream CLI version for a
// permission that declares a version bound — a floor, a ceiling (#2105), or
// both — and returns a non-nil error when the install must not proceed, with
// the bound that refused it: runClosureEffect removes an earlier install on a
// ceiling refusal and never on a floor refusal. It returns nil for every
// permission that declares neither bound, which is every non-hook permission
// and any hook adapter that has not opted in.
//
// Resolution prefers the adapter's own passive signal over running anything:
// Codex reads cli_version out of the transcript it is already tailing, which
// costs nothing and works when the binary is not on the daemon's PATH. The
// probe is the fallback for adapters with no such signal.
func (s *PermissionService) hookVersionRefusal(p agent.Permission) (agent.VersionBound, error) {
	if p.Writes == nil || !p.Writes.Version.Bounded() {
		return agent.WithinBounds, nil
	}
	gate := p.Writes.Version

	installed := ""
	if gate.Observed != nil {
		installed = gate.Observed()
	}
	if s.shouldConfirmByProbe(gate, installed) {
		probed, err := cliprobe.Probe(s.effectContext(), gate.Probe)
		if err != nil {
			// Unknown version: fail open, but say so. A probe that could not
			// run is the state most likely to be misread later as "the gate
			// checked and approved".
			s.log.LogInfo("permissions", "", fmt.Sprintf(
				"%s: version probe failed (%v); installing anyway — an unreadable version is not an old one", p.Key, err))
		} else {
			installed = probed
		}
	}

	bound, why := gate.Check(installed)
	if bound == agent.WithinBounds {
		return bound, nil
	}
	return bound, errors.New(why)
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
// signal that parses and already clears every declared bound.
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
//     process to be sure. At or above a ceiling (#2105) the same probe is what
//     catches a user who downgraded back below it.
func (s *PermissionService) shouldConfirmByProbe(gate *agent.VersionGate, installed string) bool {
	if len(gate.Probe) == 0 {
		return false
	}
	if _, parsed := cliversion.Parse(installed); !parsed {
		return true
	}
	allowed, _ := gate.Permits(installed)
	return !allowed
}
