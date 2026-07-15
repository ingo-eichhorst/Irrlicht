package kirocli

import "testing"

func TestSessionsDir(t *testing.T) {
	tests := []struct {
		name string
		env  string
		want string
	}{
		{"empty falls back to default", "", defaultRootDir},
		{"absolute override produces $KIRO_HOME/sessions/cli", "/tmp/kiro-home", "/tmp/kiro-home/sessions/cli"},
		{"trailing slash is cleaned", "/tmp/kiro-home/", "/tmp/kiro-home/sessions/cli"},
		{"relative override is rejected (falls back to default)", "relative/home", defaultRootDir},
		{"tilde-prefixed override is rejected (no shell expansion)", "~/custom", defaultRootDir},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(kiroHomeEnvVar, tc.env)
			if got := sessionsDir(); got != tc.want {
				t.Errorf("sessionsDir() = %q, want %q", got, tc.want)
			}
		})
	}
}
