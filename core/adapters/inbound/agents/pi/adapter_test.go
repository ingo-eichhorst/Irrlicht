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
			t.Setenv(agentDirEnvVar, "")
			t.Setenv(sessionDirEnvVar, tc.env)
			if got := sessionsDir(); got != tc.want {
				t.Errorf("sessionsDir() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestSessionsDirFollowsPiCodingAgentDir is issue #1721's defect test.
//
// pi composes its sessions directory as getAgentDir() + "/sessions"
// (dist/config.js), and getAgentDir honours $PI_CODING_AGENT_DIR — the
// documented way to relocate an entire pi installation, and the one this
// issue's own live audit used. Before this, irrlicht read only
// $PI_CODING_AGENT_SESSION_DIR, so a user who relocated pi that way had
// their sessions moved out from under a watcher still pointed at
// ~/.pi/agent/sessions, with nothing anywhere reporting it.
//
// It matters more now than it did as a pure observation gap: the hook
// receiver confines caller-supplied transcript paths to THIS root
// (hooks.go's transcriptConfiner), so a wrong root does not merely miss
// transcripts — it refuses every hook the installed extension delivers,
// which reads as "the channel is dead" rather than as a misconfiguration.
func TestSessionsDirFollowsPiCodingAgentDir(t *testing.T) {
	t.Setenv(sessionDirEnvVar, "")
	t.Setenv(agentDirEnvVar, "/tmp/pi-relocated/agent")

	want := "/tmp/pi-relocated/agent/sessions"
	if got := sessionsDir(); got != want {
		t.Errorf("sessionsDir() = %q, want %q", got, want)
	}
}

// TestSessionDirOverrideBeatsAgentDirOverride pins the PRECEDENCE, which is
// pi's own: dist/main.js reads $PI_CODING_AGENT_SESSION_DIR directly and
// uses it in place of the composed path, so the narrower override wins.
// Without this, "honour both" could mean either order and the wrong one
// looks identical until someone sets both.
func TestSessionDirOverrideBeatsAgentDirOverride(t *testing.T) {
	t.Setenv(agentDirEnvVar, "/tmp/pi-relocated/agent")
	t.Setenv(sessionDirEnvVar, "/tmp/pi-explicit-sessions")

	want := "/tmp/pi-explicit-sessions"
	if got := sessionsDir(); got != want {
		t.Errorf("sessionsDir() = %q, want %q", got, want)
	}
}

// TestSessionsDirRejectsARelativeAgentDir pins that the agent-dir leg
// inherits the same absolute-path rule the session-dir leg has always had —
// pi's own expandTildePath does no shell expansion either, so a relative or
// tilde-prefixed value is a misconfiguration to fall back from, not a path
// to join onto the current working directory.
func TestSessionsDirRejectsARelativeAgentDir(t *testing.T) {
	for _, v := range []string{"relative/agent", "~/pi/agent"} {
		t.Setenv(sessionDirEnvVar, "")
		t.Setenv(agentDirEnvVar, v)
		if got := sessionsDir(); got != defaultRootDir {
			t.Errorf("sessionsDir() with %s=%q = %q, want the default %q",
				agentDirEnvVar, v, got, defaultRootDir)
		}
	}
}
