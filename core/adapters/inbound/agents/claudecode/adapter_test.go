package claudecode

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTranscriptsDir(t *testing.T) {
	tests := []struct {
		name string
		env  string
		want string
	}{
		{"empty falls back to default", "", defaultProjectsDir},
		{"override produces $CLAUDE_CONFIG_DIR/projects", "/tmp/claude-home", "/tmp/claude-home/projects"},
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

func TestDefaultSessionsDirRespectsConfigDir(t *testing.T) {
	t.Setenv(configDirEnvVar, "/tmp/claude-home")
	want := "/tmp/claude-home/sessions"
	if got := defaultSessionsDir(); got != want {
		t.Errorf("defaultSessionsDir() = %q, want %q", got, want)
	}
}

func TestDefaultSessionsDirFallsBackToHome(t *testing.T) {
	t.Setenv(configDirEnvVar, "")
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	want := filepath.Join(home, ".claude", "sessions")
	if got := defaultSessionsDir(); got != want {
		t.Errorf("defaultSessionsDir() = %q, want %q", got, want)
	}
}
