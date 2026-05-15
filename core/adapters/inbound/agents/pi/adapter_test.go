package pi

import "testing"

func TestSessionsDir(t *testing.T) {
	tests := []struct {
		name string
		env  string
		want string
	}{
		{"empty falls back to default", "", defaultRootDir},
		{"absolute override is used as-is", "/tmp/pi-sessions", "/tmp/pi-sessions"},
		{"trailing slash is cleaned", "/tmp/pi-sessions/", "/tmp/pi-sessions"},
		{"relative override is rejected (falls back to default)", "relative/sessions", defaultRootDir},
		{"tilde-prefixed override is rejected (no shell expansion)", "~/custom", defaultRootDir},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(sessionDirEnvVar, tc.env)
			if got := sessionsDir(); got != tc.want {
				t.Errorf("sessionsDir() = %q, want %q", got, tc.want)
			}
		})
	}
}
