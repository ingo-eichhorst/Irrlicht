package vibe

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// captureLog redirects the standard logger into a buffer for the duration of
// the test, restoring stderr and the default flags afterwards. Mirrors the
// helper in agentpaths, which pins the same kind of rejection line.
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

// writeConfig writes a config.toml into dir and returns dir.
func writeConfig(t *testing.T, dir, content string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, configFilename), []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return dir
}

// setVibeHome points $VIBE_HOME at a fresh temp dir holding the given
// config.toml, and returns it. $HOME is pinned to a separate empty temp dir so
// the developer's own ~/.vibe/config.toml can never reach the code under test.
func setVibeHome(t *testing.T, content string) string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	home := writeConfig(t, t.TempDir(), content)
	t.Setenv(vibeHomeEnvVar, home)
	return home
}

// setDefaultVibeHome pins $HOME at a temp dir and writes the given config.toml
// into its .vibe/, exercising the $VIBE_HOME-unset path.
func setDefaultVibeHome(t *testing.T, content string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	vibeDir := filepath.Join(home, defaultHomeDirName)
	if err := os.MkdirAll(vibeDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeConfig(t, vibeDir, content)
}

// TestParseSaveDir pins the table-aware scan. save_dir is meaningless without
// its [session_logging] table — the same bare key lives under other tables in
// Vibe's own default config — so a table-blind scan (the shape of the codex
// reader in core/pkg/tailer) would be wrong here rather than merely loose.
// Every unparseable shape must yield "" so the caller falls back to the
// default root instead of watching a garbage path.
func TestParseSaveDir(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{
			name:    "value under the session_logging table",
			content: "[session_logging]\nsave_dir = \"/logs\"\n",
			want:    "/logs",
		},
		{
			name: "shape Vibe writes on first run",
			content: "[project_context]\ndefault_commit_count = 5\n\n" +
				"[session_logging]\nsave_dir = \"/Users/ingo/.vibe/logs/session\"\n" +
				"session_prefix = \"session\"\nenabled = true\n",
			want: "/Users/ingo/.vibe/logs/session",
		},
		{
			name:    "same key under a different table is ignored",
			content: "[other]\nsave_dir = \"/wrong\"\n",
			want:    "",
		},
		{
			name:    "table scoping ends at the next header",
			content: "[session_logging]\nenabled = true\n\n[other]\nsave_dir = \"/wrong\"\n",
			want:    "",
		},
		{
			name:    "key before any table header is ignored",
			content: "save_dir = \"/wrong\"\n[session_logging]\nenabled = true\n",
			want:    "",
		},
		{
			name:    "absent key",
			content: "[session_logging]\nenabled = true\n",
			want:    "",
		},
		{
			name:    "commented-out key",
			content: "[session_logging]\n# save_dir = \"/logs\"\n",
			want:    "",
		},
		{
			name:    "trailing comment is stripped",
			content: "[session_logging]\nsave_dir = \"/logs\" # where sessions go\n",
			want:    "/logs",
		},
		{
			name:    "hash inside the value is kept",
			content: "[session_logging]\nsave_dir = \"/logs/#1\"\n",
			want:    "/logs/#1",
		},
		{
			name:    "literal string is verbatim",
			content: "[session_logging]\nsave_dir = 'C:\\logs\\vibe'\n",
			want:    `C:\logs\vibe`,
		},
		{
			name:    "escapes in a basic string are decoded",
			content: "[session_logging]\nsave_dir = \"C:\\\\logs\\\\vibe\"\n",
			want:    `C:\logs\vibe`,
		},
		{
			name:    "surrounding whitespace and spaced table header",
			content: "  [ session_logging ]  \n   save_dir   =   \"/logs\"   \n",
			want:    "/logs",
		},
		{
			name:    "array-of-tables header never matches",
			content: "[[session_logging]]\nsave_dir = \"/wrong\"\n",
			want:    "",
		},
		{
			name:    "empty value",
			content: "[session_logging]\nsave_dir = \"\"\n",
			want:    "",
		},
		{
			name:    "bare unquoted value is not a string",
			content: "[session_logging]\nsave_dir = /logs\n",
			want:    "",
		},
		{
			name:    "unterminated quote",
			content: "[session_logging]\nsave_dir = \"/logs\n",
			want:    "",
		},
		{
			name:    "empty content",
			content: "",
			want:    "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseSaveDir(tc.content); got != tc.want {
				t.Errorf("parseSaveDir() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestResolveSaveDir pins the normalization. Upstream applies
// expanduser().resolve() (v2.19.1, vibe/core/config/models.py:65-68), so "~"
// is a working value in a config file — nothing shell-expands it on Vibe's
// behalf — while a relative value anchors to the directory vibe was launched
// from and is therefore unresolvable here.
func TestResolveSaveDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	tests := []struct {
		name  string
		value string
		want  string
	}{
		{"absolute is used as-is", "/srv/vibe/logs", "/srv/vibe/logs"},
		{"trailing slash is cleaned", "/srv/vibe/logs/", "/srv/vibe/logs"},
		{"tilde-slash expands to $HOME", "~/vibe-logs", filepath.Join(home, "vibe-logs")},
		{"bare tilde expands to $HOME", "~", home},
		{"relative is rejected", "logs", ""},
		{"dot-relative is rejected", "./logs", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveSaveDir(tc.value, "/cfg/config.toml"); got != tc.want {
				t.Errorf("resolveSaveDir(%q) = %q, want %q", tc.value, got, tc.want)
			}
		})
	}
}

// TestResolveSaveDir_RejectionIsLogged pins the rejection surfacing: a
// relative save_dir means the daemon is about to watch the wrong place, which
// is indistinguishable from "no sessions exist" unless it says so.
func TestResolveSaveDir_RejectionIsLogged(t *testing.T) {
	buf := captureLog(t)

	if got := resolveSaveDir("logs", "/cfg/config.toml"); got != "" {
		t.Fatalf("resolveSaveDir() = %q, want \"\"", got)
	}
	out := buf.String()
	for _, want := range []string{"save_dir", `"logs"`, "/cfg/config.toml"} {
		if !strings.Contains(out, want) {
			t.Errorf("log %q missing %q", out, want)
		}
	}
}

// TestConfiguredSaveDir covers the read end to end, including the seam
// interaction issue #1115 flags: config.toml itself lives under $VIBE_HOME
// (v2.19.1, _harness_manager.py:55), so relocating the home relocates the file
// that can relocate the session root.
func TestConfiguredSaveDir(t *testing.T) {
	const saveDirConfig = "[session_logging]\nsave_dir = \"/srv/logs\"\n"

	tests := []struct {
		name  string
		setup func(t *testing.T)
		want  string
	}{
		{
			name:  "save_dir in $VIBE_HOME/config.toml wins",
			setup: func(t *testing.T) { setVibeHome(t, saveDirConfig) },
			want:  "/srv/logs",
		},
		{
			name: "save_dir in ~/.vibe/config.toml when VIBE_HOME is unset",
			setup: func(t *testing.T) {
				setDefaultVibeHome(t, saveDirConfig)
				t.Setenv(vibeHomeEnvVar, "")
			},
			want: "/srv/logs",
		},
		{
			name: "relative VIBE_HOME falls back to ~/.vibe for the config too",
			setup: func(t *testing.T) {
				setDefaultVibeHome(t, saveDirConfig)
				t.Setenv(vibeHomeEnvVar, "relative/home")
			},
			want: "/srv/logs",
		},
		{
			name: "absent config yields empty",
			setup: func(t *testing.T) {
				t.Setenv("HOME", t.TempDir())
				t.Setenv(vibeHomeEnvVar, t.TempDir())
			},
			want: "",
		},
		{
			name:  "unset save_dir yields empty",
			setup: func(t *testing.T) { setVibeHome(t, "[session_logging]\nenabled = true\n") },
			want:  "",
		},
		{
			name: "unresolvable save_dir yields empty",
			setup: func(t *testing.T) {
				captureLog(t)
				setVibeHome(t, "[session_logging]\nsave_dir = \"relative/logs\"\n")
			},
			want: "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tc.setup(t)
			if got := configuredSaveDir(); got != tc.want {
				t.Errorf("configuredSaveDir() = %q, want %q", got, tc.want)
			}
		})
	}
}
