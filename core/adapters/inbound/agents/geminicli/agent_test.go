package geminicli

import "testing"

func TestIsHeapBumpWorker(t *testing.T) {
	node := "/Users/x/.nvm/versions/node/v22.18.0/bin/node"
	script := "/Users/x/.nvm/versions/node/v22.18.0/bin/gemini"

	cases := []struct {
		name string
		argv []string
		want bool
	}{
		{
			name: "launcher (node script -y) is kept",
			argv: []string{"node", script, "-y"},
			want: false,
		},
		{
			name: "heap-bump worker is excluded",
			argv: []string{node, "--max-old-space-size=16384", script, "-y"},
			want: true,
		},
		{
			name: "single-process gemini (no re-exec) is kept",
			argv: []string{node, script},
			want: false,
		},
		{
			name: "nil argv is not excluded (unreadable — contract default)",
			argv: nil,
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isHeapBumpWorker(tc.argv); got != tc.want {
				t.Errorf("isHeapBumpWorker(%v) = %v, want %v", tc.argv, got, tc.want)
			}
		})
	}
}

func TestLowestPID(t *testing.T) {
	// PID discovery binds the launcher: the lowest-PID ancestor.
	if got := lowestPID([]int{7015, 6185}); got != 6185 {
		t.Errorf("lowestPID([7015 6185]) = %d, want 6185 (launcher ancestor)", got)
	}
	if got := lowestPID(nil); got != 0 {
		t.Errorf("lowestPID(nil) = %d, want 0", got)
	}
}

func TestAgentRegistration(t *testing.T) {
	a := Agent()
	if a.Identity.Name != "gemini-cli" {
		t.Errorf("adapter name: want gemini-cli, got %q", a.Identity.Name)
	}
	if a.Process.Match == nil || a.Process.PIDForSession == nil {
		t.Error("Process.Match and PIDForSession must be set")
	}
	if a.Process.ExcludeArgv == nil {
		t.Error("ExcludeArgv must be set to drop the heap-bump worker")
	}
}
