package orchestrator

import (
	"path/filepath"
	"strings"
)

// RoleInfo holds the derived orchestrator role information from a session's CWD.
type RoleInfo struct {
	Role   string // mayor, deacon, witness, refinery, polecat, crew, boot, dog
	Rig    string // rig name (empty for global agents)
	Name   string // agent name (for polecat, crew, dog)
	Icon   string // display emoji
	BeadID string // bead ID extracted from git branch (for polecats)
}

// gasTownIcons maps role names to display emojis.
var gasTownIcons = map[string]string{
	"mayor":    "\U0001F3A9", // 🎩
	"deacon":   "\U0001F4CB", // 📋
	"witness":  "\U0001F989", // 🦉
	"refinery": "\U0001F3ED", // 🏭
	"polecat":  "\U0001F477", // 👷
	"crew":     "\U0001F9D1\u200D\U0001F4BB", // 🧑‍💻
	"boot":     "\U0001F97E", // 🥾
	"dog":      "\U0001F415", // 🐕
}

// DeriveGasTownRole parses a session's CWD path to extract the Gas Town role.
// Returns nil if the CWD is not under gtRoot. RoleMeta must be provided by
// the caller (adapter) to populate Icon.
//
// Path patterns:
//
//	$GT_ROOT/mayor                           → role=mayor
//	$GT_ROOT/deacon                          → role=deacon
//	$GT_ROOT/deacon/dogs/boot                → role=boot
//	$GT_ROOT/deacon/dogs/<name>/<rig>        → role=dog, name=<name>, rig=<rig>
//	$GT_ROOT/<rig>/witness                   → role=witness, rig=<rig>
//	$GT_ROOT/<rig>/refinery                  → role=refinery, rig=<rig>
//	$GT_ROOT/<rig>/polecats/<name>           → role=polecat, rig=<rig>, name=<name>
//	$GT_ROOT/<rig>/crew/<name>               → role=crew, rig=<rig>, name=<name>
func DeriveGasTownRole(cwd, gtRoot string) *RoleInfo {
	if gtRoot == "" || cwd == "" {
		return nil
	}

	cwd = filepath.Clean(cwd)
	gtRoot = filepath.Clean(gtRoot)

	rel, err := filepath.Rel(gtRoot, cwd)
	if err != nil || strings.HasPrefix(rel, "..") {
		return nil
	}

	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) == 0 || parts[0] == "." {
		return nil
	}

	var ri *RoleInfo

	switch {
	case parts[0] == "mayor":
		ri = &RoleInfo{Role: "mayor"}
	case parts[0] == "deacon":
		if len(parts) >= 3 && parts[1] == "dogs" {
			if parts[2] == "boot" {
				ri = &RoleInfo{Role: "boot"}
			} else if len(parts) >= 4 {
				ri = &RoleInfo{Role: "dog", Name: parts[2], Rig: parts[3]}
			}
		}
		if ri == nil {
			ri = &RoleInfo{Role: "deacon"}
		}
	case len(parts) >= 2:
		rig := parts[0]
		switch parts[1] {
		case "witness":
			ri = &RoleInfo{Role: "witness", Rig: rig}
		case "refinery":
			ri = &RoleInfo{Role: "refinery", Rig: rig}
		case "polecats":
			if len(parts) >= 3 {
				ri = &RoleInfo{Role: "polecat", Rig: rig, Name: parts[2]}
			}
		case "crew":
			if len(parts) >= 3 {
				ri = &RoleInfo{Role: "crew", Rig: rig, Name: parts[2]}
			}
		}
	}

	if ri != nil {
		ri.Icon = gasTownIcons[ri.Role]
	}
	return ri
}
