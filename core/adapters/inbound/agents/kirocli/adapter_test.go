package kirocli

import "testing"

func TestSessionsDir(t *testing.T) {
	if got := sessionsDir(); got != defaultRootDir {
		t.Errorf("sessionsDir() = %q, want %q", got, defaultRootDir)
	}
}
