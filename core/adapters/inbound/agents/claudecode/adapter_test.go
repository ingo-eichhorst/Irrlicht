package claudecode

import "testing"

func TestTranscriptsDir(t *testing.T) {
	tests := []struct {
		name string
		env  string
		want string
	}{
		{"empty falls back to default", "", defaultProjectsDir},
		{"absolute override produces $CLAUDE_CONFIG_DIR/projects", "/tmp/claude-home", "/tmp/claude-home/projects"},
		{"trailing slash is cleaned", "/tmp/claude-home/", "/tmp/claude-home/projects"},
		{"relative override is rejected (falls back to default)", "relative/home", defaultProjectsDir},
		{"tilde-prefixed override is rejected (no shell expansion)", "~/custom", defaultProjectsDir},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(configDirEnvVar, tc.env)
			if got := transcriptsDir(); got != tc.want {
				t.Errorf("transcriptsDir() = %q, want %q", got, tc.want)
			}
		})
	}
}
