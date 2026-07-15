package agentpaths

import (
	"bytes"
	"log"
	"os"
	"strings"
	"testing"
)

// captureLog redirects the standard logger into a buffer for the duration of
// the test, restoring stderr and the default flags afterwards.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(os.Stderr)
		log.SetFlags(log.LstdFlags)
	})
	return &buf
}

const testEnvVar = "IRRLICHT_AGENTPATHS_TEST_HOME"

func TestFromEnv(t *testing.T) {
	tests := []struct {
		name   string
		env    string
		subdir []string
		want   string
	}{
		{"empty falls back to default", "", []string{"sessions"}, "default/dir"},
		{"absolute override joins subdir", "/tmp/home", []string{"sessions"}, "/tmp/home/sessions"},
		{"absolute override with no subdir is used as-is", "/tmp/sessions", nil, "/tmp/sessions"},
		{"absolute override joins nested subdirs", "/tmp/home", []string{"sessions", "cli"}, "/tmp/home/sessions/cli"},
		{"trailing slash is cleaned", "/tmp/home/", []string{"sessions"}, "/tmp/home/sessions"},
		{"trailing slash is cleaned with no subdir", "/tmp/sessions/", nil, "/tmp/sessions"},
		{"relative override is rejected (falls back to default)", "relative/home", []string{"sessions"}, "default/dir"},
		{"tilde-prefixed override is rejected (no shell expansion)", "~/custom", []string{"sessions"}, "default/dir"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(testEnvVar, tc.env)
			if got := FromEnv("testagent", testEnvVar, "default/dir", tc.subdir...); got != tc.want {
				t.Errorf("FromEnv() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestFromEnvLogsRejectionUnderAdapterName pins the operator-visible log line:
// a rejected override must name the adapter and the offending value, so the
// misconfiguration is greppable rather than silent.
func TestFromEnvLogsRejectionUnderAdapterName(t *testing.T) {
	buf := captureLog(t)

	t.Setenv(testEnvVar, "~/custom")
	FromEnv("kirocli", testEnvVar, "default/dir", "sessions", "cli")

	got := strings.TrimSpace(buf.String())
	want := `kirocli: ignoring ` + testEnvVar + `="~/custom" — must be an absolute path (no shell expansion)`
	if got != want {
		t.Errorf("log line = %q, want %q", got, want)
	}
}

// TestFromEnvDoesNotLogOnAcceptedOverride guards against noise: a valid
// absolute override is the supported path, not something to warn about.
func TestFromEnvDoesNotLogOnAcceptedOverride(t *testing.T) {
	buf := captureLog(t)

	t.Setenv(testEnvVar, "/tmp/home")
	FromEnv("codex", testEnvVar, "default/dir", "sessions")

	if buf.Len() != 0 {
		t.Errorf("accepted override logged %q, want no output", buf.String())
	}
}
