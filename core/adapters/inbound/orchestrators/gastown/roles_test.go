package gastown

import "testing"

func TestDeriveRole(t *testing.T) {
	gtRoot := "/Users/ingo/gt"

	tests := []struct {
		name     string
		cwd      string
		wantNil  bool
		wantRole string
		wantRig  string
		wantName string
	}{
		{
			name:     "mayor root",
			cwd:      "/Users/ingo/gt/mayor",
			wantRole: RoleMayor,
		},
		{
			name:     "mayor subdirectory",
			cwd:      "/Users/ingo/gt/mayor/some/deep/path",
			wantRole: RoleMayor,
		},
		{
			name:     "deacon",
			cwd:      "/Users/ingo/gt/deacon",
			wantRole: RoleDeacon,
		},
		{
			name:     "witness on rig",
			cwd:      "/Users/ingo/gt/irrlicht/witness",
			wantRole: RoleWitness,
			wantRig:  "irrlicht",
		},
		{
			name:     "witness subdirectory",
			cwd:      "/Users/ingo/gt/irrlicht/witness/src",
			wantRole: RoleWitness,
			wantRig:  "irrlicht",
		},
		{
			name:     "refinery on rig",
			cwd:      "/Users/ingo/gt/agent_readyness/refinery",
			wantRole: RoleRefinery,
			wantRig:  "agent_readyness",
		},
		{
			name:     "polecat with name",
			cwd:      "/Users/ingo/gt/irrlicht/polecats/obsidian",
			wantRole: RolePolecat,
			wantRig:  "irrlicht",
			wantName: "obsidian",
		},
		{
			name:     "polecat subdirectory",
			cwd:      "/Users/ingo/gt/irrlicht/polecats/nux/src/main",
			wantRole: RolePolecat,
			wantRig:  "irrlicht",
			wantName: "nux",
		},
		{
			name:     "crew member",
			cwd:      "/Users/ingo/gt/irrlicht/crew/ingo",
			wantRole: RoleCrew,
			wantRig:  "irrlicht",
			wantName: "ingo",
		},
		{
			name:    "outside GT_ROOT",
			cwd:     "/Users/ingo/projects/something",
			wantNil: true,
		},
		{
			name:    "GT_ROOT itself",
			cwd:     "/Users/ingo/gt",
			wantNil: true,
		},
		{
			name:    "empty CWD",
			cwd:     "",
			wantNil: true,
		},
		{
			name:    "rig without role",
			cwd:     "/Users/ingo/gt/irrlicht",
			wantNil: true,
		},
		{
			name:    "polecats dir without name",
			cwd:     "/Users/ingo/gt/irrlicht/polecats",
			wantNil: true,
		},
		{
			name:    "crew dir without name",
			cwd:     "/Users/ingo/gt/irrlicht/crew",
			wantNil: true,
		},
		{
			name:    "unknown role under rig",
			cwd:     "/Users/ingo/gt/irrlicht/unknown",
			wantNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DeriveRole(tt.cwd, gtRoot)

			if tt.wantNil {
				if got != nil {
					t.Fatalf("DeriveRole(%q) = %+v, want nil", tt.cwd, got)
				}
				return
			}

			if got == nil {
				t.Fatalf("DeriveRole(%q) = nil, want role=%q", tt.cwd, tt.wantRole)
			}

			if got.Role != tt.wantRole {
				t.Errorf("Role = %q, want %q", got.Role, tt.wantRole)
			}
			if got.Rig != tt.wantRig {
				t.Errorf("Rig = %q, want %q", got.Rig, tt.wantRig)
			}
			if got.Name != tt.wantName {
				t.Errorf("Name = %q, want %q", got.Name, tt.wantName)
			}
		})
	}
}

func TestDeriveRole_EmptyRoot(t *testing.T) {
	got := DeriveRole("/some/path", "")
	if got != nil {
		t.Fatalf("DeriveRole with empty root = %+v, want nil", got)
	}
}
