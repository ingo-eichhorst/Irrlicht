package orchestrator

import "testing"

func TestDeriveGasTownRole(t *testing.T) {
	const gtRoot = "/Users/test/gt"

	tests := []struct {
		name     string
		cwd      string
		wantRole string
		wantRig  string
		wantName string
		wantIcon string
		wantNil  bool
	}{
		{name: "mayor", cwd: gtRoot + "/mayor", wantRole: "mayor", wantIcon: "\U0001F3A9"},
		{name: "mayor nested", cwd: gtRoot + "/mayor/subdir", wantRole: "mayor", wantIcon: "\U0001F3A9"},
		{name: "deacon", cwd: gtRoot + "/deacon", wantRole: "deacon", wantIcon: "\U0001F4CB"},
		{name: "deacon nested", cwd: gtRoot + "/deacon/work", wantRole: "deacon", wantIcon: "\U0001F4CB"},
		{name: "boot", cwd: gtRoot + "/deacon/dogs/boot", wantRole: "boot", wantIcon: "\U0001F97E"},
		{name: "boot nested", cwd: gtRoot + "/deacon/dogs/boot/work", wantRole: "boot", wantIcon: "\U0001F97E"},
		{name: "dog", cwd: gtRoot + "/deacon/dogs/alpha/myrig", wantRole: "dog", wantName: "alpha", wantRig: "myrig", wantIcon: "\U0001F415"},
		{name: "dog nested", cwd: gtRoot + "/deacon/dogs/bravo/proj/sub", wantRole: "dog", wantName: "bravo", wantRig: "proj", wantIcon: "\U0001F415"},
		{name: "witness", cwd: gtRoot + "/myrig/witness", wantRole: "witness", wantRig: "myrig", wantIcon: "\U0001F989"},
		{name: "refinery", cwd: gtRoot + "/myrig/refinery", wantRole: "refinery", wantRig: "myrig", wantIcon: "\U0001F3ED"},
		{name: "refinery nested", cwd: gtRoot + "/myrig/refinery/rig", wantRole: "refinery", wantRig: "myrig", wantIcon: "\U0001F3ED"},
		{name: "polecat", cwd: gtRoot + "/myrig/polecats/fix-42", wantRole: "polecat", wantRig: "myrig", wantName: "fix-42", wantIcon: "\U0001F477"},
		{name: "polecat nested", cwd: gtRoot + "/myrig/polecats/fix-42/repo", wantRole: "polecat", wantRig: "myrig", wantName: "fix-42", wantIcon: "\U0001F477"},
		{name: "crew", cwd: gtRoot + "/myrig/crew/alice", wantRole: "crew", wantRig: "myrig", wantName: "alice", wantIcon: "\U0001F9D1\u200D\U0001F4BB"},
		{name: "outside gtRoot", cwd: "/other/path", wantNil: true},
		{name: "gtRoot itself", cwd: gtRoot, wantNil: true},
		{name: "empty cwd", cwd: "", wantNil: true},
		{name: "empty gtRoot", cwd: gtRoot + "/mayor", wantNil: true},
		{name: "rig without role", cwd: gtRoot + "/myrig", wantNil: true},
		{name: "polecats without name", cwd: gtRoot + "/myrig/polecats", wantNil: true},
		{name: "crew without name", cwd: gtRoot + "/myrig/crew", wantNil: true},
		{name: "deacon/dogs without name", cwd: gtRoot + "/deacon/dogs", wantRole: "deacon", wantIcon: "\U0001F4CB"},
	}

	icons := IconLookup(func(role string) string {
		m := map[string]string{
			"mayor": "\U0001F3A9", "deacon": "\U0001F4CB", "witness": "\U0001F989",
			"refinery": "\U0001F3ED", "polecat": "\U0001F477",
			"crew": "\U0001F9D1\u200D\U0001F4BB", "boot": "\U0001F97E", "dog": "\U0001F415",
		}
		return m[role]
	})

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := gtRoot
			if tt.name == "empty gtRoot" {
				root = ""
			}
			ri := DeriveGasTownRole(tt.cwd, root, icons)

			if tt.wantNil {
				if ri != nil {
					t.Errorf("got %+v, want nil", ri)
				}
				return
			}

			if ri == nil {
				t.Fatal("got nil, want RoleInfo")
			}
			if ri.Role != tt.wantRole {
				t.Errorf("Role = %q, want %q", ri.Role, tt.wantRole)
			}
			if ri.Rig != tt.wantRig {
				t.Errorf("Rig = %q, want %q", ri.Rig, tt.wantRig)
			}
			if ri.Name != tt.wantName {
				t.Errorf("Name = %q, want %q", ri.Name, tt.wantName)
			}
			if ri.Icon != tt.wantIcon {
				t.Errorf("Icon = %q, want %q", ri.Icon, tt.wantIcon)
			}
		})
	}
}
