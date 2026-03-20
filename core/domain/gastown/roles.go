package gastown

import (
	"path/filepath"
	"strings"
)

// RoleInfo holds the derived Gas Town role information from a session's CWD.
type RoleInfo struct {
	Role   string // mayor, deacon, witness, refinery, polecat, crew
	Rig    string // rig name (empty for global agents)
	Name   string // agent name (for polecat, crew)
	BeadID string // bead ID extracted from git branch (for polecats)
}

// DeriveRole parses a session's CWD path to extract the Gas Town role.
// Returns nil if the CWD is not under gtRoot.
//
// Path patterns:
//
//	$GT_ROOT/mayor                  → role=mayor
//	$GT_ROOT/deacon                 → role=deacon
//	$GT_ROOT/<rig>/witness          → role=witness, rig=<rig>
//	$GT_ROOT/<rig>/refinery         → role=refinery, rig=<rig>
//	$GT_ROOT/<rig>/polecats/<name>  → role=polecat, rig=<rig>, name=<name>
//	$GT_ROOT/<rig>/crew/<name>      → role=crew, rig=<rig>, name=<name>
func DeriveRole(cwd, gtRoot string) *RoleInfo {
	if gtRoot == "" || cwd == "" {
		return nil
	}

	// Normalize paths for comparison.
	cwd = filepath.Clean(cwd)
	gtRoot = filepath.Clean(gtRoot)

	// CWD must be under GT_ROOT.
	rel, err := filepath.Rel(gtRoot, cwd)
	if err != nil || strings.HasPrefix(rel, "..") {
		return nil
	}

	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) == 0 || parts[0] == "." {
		return nil
	}

	// Global agents: $GT_ROOT/mayor or $GT_ROOT/deacon (possibly deeper).
	switch parts[0] {
	case RoleMayor:
		return &RoleInfo{Role: RoleMayor}
	case RoleDeacon:
		return &RoleInfo{Role: RoleDeacon}
	}

	// Rig-scoped agents: $GT_ROOT/<rig>/<role>[/<name>][/...]
	if len(parts) < 2 {
		return nil
	}
	rig := parts[0]

	switch parts[1] {
	case RoleWitness:
		return &RoleInfo{Role: RoleWitness, Rig: rig}
	case RoleRefinery:
		return &RoleInfo{Role: RoleRefinery, Rig: rig}
	case "polecats":
		if len(parts) >= 3 {
			return &RoleInfo{Role: RolePolecat, Rig: rig, Name: parts[2]}
		}
		return nil
	case RoleCrew:
		if len(parts) >= 3 {
			return &RoleInfo{Role: RoleCrew, Rig: rig, Name: parts[2]}
		}
		return nil
	}

	return nil
}
