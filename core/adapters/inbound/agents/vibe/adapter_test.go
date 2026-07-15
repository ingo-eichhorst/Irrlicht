package vibe

import (
	"path/filepath"
	"runtime"
	"testing"

	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/backchannel"
)

// TestSessionsDir pins the $VIBE_HOME seam. Upstream honors the override in
// source (v2.19.1, vibe/core/paths/_vibe_home.py) though not in its docs, so
// a hardcoded root would make every session of a $VIBE_HOME user invisible.
// Absolute-only, matching the sibling adapters: irrlicht performs no shell
// expansion, so a relative or "~"-prefixed value is logged and ignored.
//
// $HOME is redirected to an empty temp dir throughout: sessionsDir now also
// reads $VIBE_HOME/config.toml, and a developer running this on a machine
// with a real ~/.vibe/config.toml would otherwise get that file's save_dir
// (an absolute path) where the test expects the $HOME-relative default —
// green in CI, red locally.
func TestSessionsDir(t *testing.T) {
	tests := []struct {
		name string
		env  string
		want string
	}{
		{"empty falls back to default", "", defaultRootDir},
		{"absolute override produces $VIBE_HOME/logs/session", "/tmp/vibe-home", "/tmp/vibe-home/logs/session"},
		{"trailing slash is cleaned", "/tmp/vibe-home/", "/tmp/vibe-home/logs/session"},
		{"relative override is rejected (falls back to default)", "relative/home", defaultRootDir},
		{"tilde-prefixed override is rejected (no shell expansion)", "~/custom", defaultRootDir},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			t.Setenv(vibeHomeEnvVar, tc.env)
			if got := sessionsDir(); got != tc.want {
				t.Errorf("sessionsDir() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestSessionsDir_SaveDirOverridesRoot pins upstream's precedence: a set
// [session_logging].save_dir REPLACES the session root rather than layering
// under $VIBE_HOME (v2.19.1, vibe/core/config/models.py:58-63), so it must
// outrank both the env override and the default. Issue #1115: without this,
// a user who edits the key — which Vibe writes into every config on first run
// — has every session silently invisible.
func TestSessionsDir_SaveDirOverridesRoot(t *testing.T) {
	t.Run("save_dir beats $VIBE_HOME", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		home := writeConfig(t, t.TempDir(), "[session_logging]\nsave_dir = \"/srv/vibe-logs\"\n")
		t.Setenv(vibeHomeEnvVar, home)

		if got := sessionsDir(); got != "/srv/vibe-logs" {
			t.Errorf("sessionsDir() = %q, want %q", got, "/srv/vibe-logs")
		}
	})

	t.Run("unset save_dir leaves $VIBE_HOME/logs/session", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		home := writeConfig(t, t.TempDir(), "[session_logging]\nenabled = true\n")
		t.Setenv(vibeHomeEnvVar, home)

		want := filepath.Join(home, "logs", "session")
		if got := sessionsDir(); got != want {
			t.Errorf("sessionsDir() = %q, want %q", got, want)
		}
	})

	t.Run("unresolvable save_dir leaves $VIBE_HOME/logs/session", func(t *testing.T) {
		captureLog(t)
		t.Setenv("HOME", t.TempDir())
		home := writeConfig(t, t.TempDir(), "[session_logging]\nsave_dir = \"relative/logs\"\n")
		t.Setenv(vibeHomeEnvVar, home)

		want := filepath.Join(home, "logs", "session")
		if got := sessionsDir(); got != want {
			t.Errorf("sessionsDir() = %q, want %q", got, want)
		}
	})
}

func TestAgent_Identity(t *testing.T) {
	a := Agent()
	if a.Identity.Name != AdapterName {
		t.Errorf("Name = %q, want %q", a.Identity.Name, AdapterName)
	}
	if a.Identity.DisplayName != "Mistral Vibe" {
		t.Errorf("DisplayName = %q", a.Identity.DisplayName)
	}
	if a.Identity.IconSVGLight == "" || a.Identity.IconSVGDark == "" {
		t.Error("expected both light and dark icons")
	}
}

func TestAgent_Source_FilesUnderRoot(t *testing.T) {
	// Pin the default root explicitly: the resolver reads both $VIBE_HOME and
	// $VIBE_HOME/config.toml, so the developer's own env and config would
	// otherwise leak into this assertion.
	t.Setenv("HOME", t.TempDir())
	t.Setenv(vibeHomeEnvVar, "")

	src, ok := Agent().Source.(agent.FilesUnderRoot)
	if !ok {
		t.Fatalf("Source is %T, want FilesUnderRoot", Agent().Source)
	}
	// The root is resolved lazily, at watcher-build time (post-consent), so it
	// arrives via DirFunc rather than the static Dir — Agent() must not read
	// config.toml while the transcripts permission is still pending.
	if src.Dir != "" {
		t.Errorf("Dir = %q, want \"\" (root resolves through DirFunc)", src.Dir)
	}
	if src.DirFunc == nil {
		t.Fatal("expected DirFunc (root depends on config.toml, which may only be read post-consent)")
	}
	if got := src.RootDirFor(runtime.GOOS); got != defaultRootDir {
		t.Errorf("RootDirFor() = %q, want %q", got, defaultRootDir)
	}
	if src.SessionIDFromPath == nil {
		t.Error("expected SessionIDFromPath (filename is the constant messages.jsonl)")
	}
	if _, ok := src.Parser.(agent.JSONLineParser); !ok {
		t.Errorf("Parser is %T, want JSONLineParser", src.Parser)
	}
}

func TestAgent_Process_CommandPattern(t *testing.T) {
	// Vibe is a Python console-script, so it must match on the command line,
	// not an exact process name.
	if _, ok := Agent().Process.Match.(agent.CommandPattern); !ok {
		t.Errorf("Match is %T, want CommandPattern", Agent().Process.Match)
	}
	if Agent().Process.PIDForSession == nil {
		t.Error("expected PIDForSession")
	}
}

func TestAgent_Control_Backchannel(t *testing.T) {
	a := Agent()
	if !a.Control.SupportsInput {
		t.Error("expected Control.SupportsInput (vibe is an interactive REPL)")
	}
	if a.Control.Interrupt != agent.InterruptCtrlC {
		t.Errorf("Interrupt = %v, want InterruptCtrlC", a.Control.Interrupt)
	}
	if got := a.Control.Presets[backchannel.PresetCompact]; got != "/compact" {
		t.Errorf("compact preset = %q, want /compact", got)
	}
	// An adapter that forwards input MUST declare the shared control consent gate.
	var hasControl bool
	for _, p := range a.Permissions {
		if p.Key == agent.ControlPermissionKey {
			hasControl = true
		}
	}
	if !hasControl {
		t.Error("expected agent.ControlPermission() among Permissions")
	}
}

func TestSessionIDFromPath(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{"/Users/x/.vibe/logs/session/session_20260706_101952_48e134e3/messages.jsonl", "session_20260706_101952_48e134e3"},
		{"/Users/x/.vibe/logs/session/session_abc/meta.json", ""}, // sidecar rejected
		{"/Users/x/.vibe/logs/session/session_abc/other.jsonl", ""},
		{"messages.jsonl", ""}, // no parent dir
	}
	for _, c := range cases {
		if got := sessionIDFromPath(c.path); got != c.want {
			t.Errorf("sessionIDFromPath(%q) = %q, want %q", c.path, got, c.want)
		}
	}
}

func TestProcessCmdRegex(t *testing.T) {
	assertCmdRegexMatches(t, true, []string{
		"/Users/x/.local/share/uv/tools/mistral-vibe/bin/python3 /Users/x/.local/bin/vibe --yolo",
		"/Users/x/.local/bin/vibe",
		"/usr/bin/vibe --model devstral",
	})
	assertCmdRegexMatches(t, false, []string{
		"irrlichd --watch /Users/x/.vibe/logs/session", // the watcher must not self-trip
		"vim /Users/x/notes-about-vibe.md",
		"grep vibe README.md",
	})
}

func assertCmdRegexMatches(t *testing.T, want bool, cmds []string) {
	t.Helper()
	for _, cmd := range cmds {
		if got := processCmdRegex.MatchString(cmd); got != want {
			t.Errorf("processCmdRegex.MatchString(%q) = %v, want %v", cmd, got, want)
		}
	}
}
