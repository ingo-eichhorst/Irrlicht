package codex

import "testing"

func TestSessionsDir(t *testing.T) {
	tests := []struct {
		name string
		env  string
		want string
	}{
		{"empty falls back to default", "", defaultRootDir},
		{"override produces $CODEX_HOME/sessions", "/tmp/codex-home", "/tmp/codex-home/sessions"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(codexHomeEnvVar, tc.env)
			if got := sessionsDir(); got != tc.want {
				t.Errorf("sessionsDir() = %q, want %q", got, tc.want)
			}
		})
	}
}
