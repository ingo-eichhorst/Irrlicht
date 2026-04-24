package gastown

import "irrlicht/core/domain/orchestrator"

// RoleInfo is an alias for the domain-level role info.
type RoleInfo = orchestrator.RoleInfo

// deriveRole parses a session's CWD path to extract the Gas Town role.
// Delegates to the domain-level DeriveGasTownRole.
func deriveRole(cwd, gtRoot string) *RoleInfo {
	return orchestrator.DeriveGasTownRole(cwd, gtRoot)
}
