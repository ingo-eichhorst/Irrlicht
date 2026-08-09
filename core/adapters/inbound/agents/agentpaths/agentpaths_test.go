package agentpaths

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
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
	resetWarnOnce()
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
	resetWarnOnce()
	buf := captureLog(t)

	t.Setenv(testEnvVar, "/tmp/home")
	FromEnv("codex", testEnvVar, "default/dir", "sessions")

	if buf.Len() != 0 {
		t.Errorf("accepted override logged %q, want no output", buf.String())
	}
}

// TestAbsRoot pins the absolute-or-$HOME-relative rule. AbsRoot is the single
// shared implementation two independent consumers rely on to agree — the
// fswatcher that watches a tree and the hook receiver that confines paths to it
// (issue #1361) — so the contract belongs at the layer that owns it.
func TestAbsRoot(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	tests := map[string]struct {
		dir     string
		want    string
		wantErr bool
	}{
		"$HOME-relative default": {".claude/projects", filepath.Join(home, ".claude/projects"), false},
		"absolute used as-is":    {"/opt/agent/sessions", "/opt/agent/sessions", false},
		"absolute is cleaned":    {"/opt/agent/./sessions/", "/opt/agent/sessions", false},
		// An empty root is the absence of a root, never $HOME: for the
		// confinement caller, answering "$HOME" would be a fail-open over the
		// user's entire home directory.
		"empty is an error":      {"", "", true},
		"whitespace is an error": {"   ", "", true},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := AbsRoot(tt.dir)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("AbsRoot(%q) = %q, want an error", tt.dir, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("AbsRoot(%q): %v", tt.dir, err)
			}
			if got != tt.want {
				t.Errorf("AbsRoot(%q) = %q, want %q", tt.dir, got, tt.want)
			}
		})
	}
}

// TestFromEnvWarnsOncePerAdapterAndVar pins the dedupe. FromEnv is now on the
// hook receiver's per-request path (issue #1361), and that endpoint is local
// and unauthenticated — without this, any local process could drive unbounded
// log volume by POSTing in a loop while a bad override is set.
func TestFromEnvWarnsOncePerAdapterAndVar(t *testing.T) {
	resetWarnOnce()
	t.Setenv("IRR_TEST_ROOT", "relative/nope")

	buf := captureLog(t)
	for i := 0; i < 5; i++ {
		FromEnv("testadapter", "IRR_TEST_ROOT", ".default")
	}
	if got := strings.Count(buf.String(), "IRR_TEST_ROOT"); got != 1 {
		t.Errorf("logged %d times across 5 calls, want exactly 1:\n%s", got, buf.String())
	}
}
