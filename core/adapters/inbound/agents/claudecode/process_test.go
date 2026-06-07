package claudecode

import "testing"

func TestIsInfraArgv(t *testing.T) {
	tests := []struct {
		name string
		argv []string
		want bool
	}{
		// --- cc-daemon infrastructure (issue #644) → excluded ---
		{
			name: "cc-daemon run",
			argv: []string{"claude", "daemon", "run", "--origin", "transient",
				`--spawned-by`, `{"label":"claude","cwd":".../besenkammer","pid":16298}`},
			want: true,
		},
		{
			name: "bg-pty-host wrapper",
			argv: []string{
				"/Applications/ClaudeCode.app/Contents/MacOS/claude",
				"--bg-pty-host", "/tmp/cc-daemon-501/abc/pty/ab5f01f8.sock", "86", "34",
				"--", "/Users/x/.claude/versions/2.1.168",
				"--resume", "bb9f6ebf-1234", "--permission-mode", "auto",
			},
			want: true,
		},
		{
			name: "bg-spare claim host",
			argv: []string{
				"/Users/x/.claude/versions/2.1.168",
				"--bg-spare", "/tmp/cc-daemon-501/spare/x.sock",
			},
			want: true,
		},

		// --- real interactive sessions → matched (not excluded) ---
		{name: "plain claude", argv: []string{"claude"}, want: false},
		{name: "claude resume", argv: []string{"claude", "--resume", "bb9f6ebf-1234"}, want: false},
		{
			name: "claude with model + dangerous skip",
			argv: []string{"claude", "--model", "opus", "--dangerously-skip-permissions"},
			want: false,
		},
		{
			name: "prompt that merely mentions the infra flags is NOT excluded",
			argv: []string{"claude", "-p", "explain --bg-pty-host and --bg-spare"},
			want: false,
		},
		{
			name: "prompt that mentions daemon run is NOT excluded",
			argv: []string{"claude", "-p", "how do I use daemon run?"},
			want: false,
		},
		{
			name: "daemon as a prompt word (not the subcommand) is NOT excluded",
			argv: []string{"claude", "--resume", "daemon", "run"},
			want: false,
		},

		// --- defensive: unreadable / degenerate argv → treated as a session ---
		{name: "nil argv", argv: nil, want: false},
		{name: "argv0 only", argv: []string{"claude"}, want: false},
		{name: "empty", argv: []string{}, want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsInfraArgv(tc.argv); got != tc.want {
				t.Errorf("IsInfraArgv(%v) = %v, want %v", tc.argv, got, tc.want)
			}
		})
	}
}
