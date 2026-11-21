package processlifecycle

import (
	"os"
	"path/filepath"
	"testing"

	"irrlicht/core/domain/session"
)

// zaiAnthropicEndpoint is Z.ai's documented Claude Code base URL, the value
// the #2010 probe put in .claude/settings.local.json.
const zaiAnthropicEndpoint = "https://api.z.ai/api/anthropic"

// withClaudeSettingsHost points the user-settings home and the managed
// directory at fresh empty temp dirs, so a test reads only the files it
// writes — never this machine's ~/.claude/settings.json or
// /Library/Application Support/ClaudeCode.
func withClaudeSettingsHost(t *testing.T) settingsHost {
	t.Helper()
	h := settingsHost{Home: t.TempDir(), ManagedDir: t.TempDir()}
	prev := claudeSettingsHost
	claudeSettingsHost = func() settingsHost { return h }
	t.Cleanup(func() { claudeSettingsHost = prev })
	return h
}

// writeSettings writes body to dir/.claude/<name> and returns its path.
func writeSettings(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, ".claude", name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// claudeRouteSpy is a routeSpyObserver whose process has a readable argv
// with no settings flag, so only the env and the files decide the route.
func claudeRouteSpy(env map[string]string) *routeSpyObserver {
	return &routeSpyObserver{
		fakeObserver: fakeObserver{argv: map[int][]string{123: {"claude", "-p", "hi"}}},
		env:          env,
	}
}

// TestObserveClaudeCodeRoute_SettingsOnlyRouteIsObserved is #2010 probe row
// A: Z.ai's documented setup puts ANTHROPIC_BASE_URL only in
// .claude/settings.local.json. The production daemon (v0.6.9) reported
// route {"status":"absent"} for that session, which Z.ai billed.
func TestObserveClaudeCodeRoute_SettingsOnlyRouteIsObserved(t *testing.T) {
	withClaudeSettingsHost(t)
	cwd := t.TempDir()
	file := writeSettings(t, cwd, "settings.local.json",
		`{"env":{"ANTHROPIC_BASE_URL":"`+zaiAnthropicEndpoint+`","ANTHROPIC_AUTH_TOKEN":"sk-test-not-a-key"}}`)
	withRouteObserver(t, claudeRouteSpy(map[string]string{}))

	got := ObserveClaudeCodeRoute(123, cwd, true)

	if got == nil || got.Status != session.RouteObserved || got.Endpoint != zaiAnthropicEndpoint {
		t.Fatalf("a settings-only Z.ai route must be observed at %s; got %+v", zaiAnthropicEndpoint, got)
	}
	if got.Source != "settings:local" || got.SettingsFile != file {
		t.Errorf("Source/SettingsFile = %q/%q, want settings:local/%s", got.Source, got.SettingsFile, file)
	}
}

// TestObserveClaudeCodeRoute_SettingsBeatProcessEnv is #2010 probe row B:
// the process env exported ANTHROPIC_BASE_URL=http://127.0.0.1:9 (a closed
// port) and settings.local.json pointed at Z.ai. The request succeeded, so
// the settings value won — yet v0.6.9 reported observed http://127.0.0.1:9.
func TestObserveClaudeCodeRoute_SettingsBeatProcessEnv(t *testing.T) {
	withClaudeSettingsHost(t)
	cwd := t.TempDir()
	writeSettings(t, cwd, "settings.local.json",
		`{"env":{"ANTHROPIC_BASE_URL":"`+zaiAnthropicEndpoint+`"}}`)
	withRouteObserver(t, claudeRouteSpy(map[string]string{"ANTHROPIC_BASE_URL": "http://127.0.0.1:9"}))

	got := ObserveClaudeCodeRoute(123, cwd, true)

	if got == nil || got.Status != session.RouteObserved || got.Endpoint != zaiAnthropicEndpoint {
		t.Fatalf("settings.local.json must outrank the process env: want observed %s; got %+v", zaiAnthropicEndpoint, got)
	}
	if got.Source != "settings:local" {
		t.Errorf("Source = %q, want settings:local", got.Source)
	}
}
