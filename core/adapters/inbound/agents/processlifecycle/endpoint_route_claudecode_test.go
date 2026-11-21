package processlifecycle

import (
	"os"
	"path/filepath"
	"testing"

	"irrlicht/core/domain/session"
)

const zaiLocal = `{"env":{"ANTHROPIC_BASE_URL":"` + zaiAnthropicEndpoint + `"}}`

func requireRoute(t *testing.T, got *session.RouteObservation, status, endpoint, source string) {
	t.Helper()
	if got == nil {
		t.Fatalf("got nil observation, want status %s", status)
	}
	if got.Status != status || got.Endpoint != endpoint || got.Source != source {
		t.Fatalf("got {status:%s endpoint:%q source:%q}, want {status:%s endpoint:%q source:%q}",
			got.Status, got.Endpoint, got.Source, status, endpoint, source)
	}
}

func TestObserveClaudeCodeRoute_DeniedReadsNothing(t *testing.T) {
	withClaudeSettingsHost(t)
	cwd := t.TempDir()
	writeSettings(t, cwd, "settings.local.json", zaiLocal)
	spy := claudeRouteSpy(map[string]string{})
	withRouteObserver(t, spy)

	got := ObserveClaudeCodeRoute(123, cwd, false)

	if spy.calledEnvOf {
		t.Error("a denied permission must not read the environment")
	}
	requireRoute(t, got, session.RouteDenied, "", "")
}

// Managed settings outrank every other file, and inside the managed tier a
// later drop-in replaces an earlier one, which replaces managed-settings.json.
func TestObserveClaudeCodeRoute_ManagedTierOrder(t *testing.T) {
	h := withClaudeSettingsHost(t)
	cwd := t.TempDir()
	writeSettings(t, cwd, "settings.local.json", zaiLocal)
	if err := os.WriteFile(filepath.Join(h.ManagedDir, "managed-settings.json"),
		[]byte(`{"env":{"ANTHROPIC_BASE_URL":"https://base.example/v1"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	dropins := filepath.Join(h.ManagedDir, "managed-settings.d")
	if err := os.MkdirAll(dropins, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"10-gateway.json":   `{"env":{"ANTHROPIC_BASE_URL":"https://ten.example/v1"}}`,
		"20-gateway.json":   `{"env":{"ANTHROPIC_BASE_URL":"https://twenty.example/v1"}}`,
		".99-hidden.json":   `{"env":{"ANTHROPIC_BASE_URL":"https://hidden.example/v1"}}`,
		"30-notjson.txt":    `{"env":{"ANTHROPIC_BASE_URL":"https://txt.example/v1"}}`,
		"25-telemetry.json": `{"env":{"DISABLE_TELEMETRY":"1"}}`,
	} {
		if err := os.WriteFile(filepath.Join(dropins, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	withRouteObserver(t, claudeRouteSpy(map[string]string{}))

	got := ObserveClaudeCodeRoute(123, cwd, true)

	requireRoute(t, got, session.RouteObserved, "https://twenty.example/v1", "settings:managed")
	if got.SettingsFile != filepath.Join(dropins, "20-gateway.json") {
		t.Errorf("SettingsFile = %q, want the 20- drop-in", got.SettingsFile)
	}
}

// A --settings flag sits between managed and the project files, and its
// value is not reconstructed: the route is unreadable rather than the local
// file's value — unless a managed file decides it first.
func TestObserveClaudeCodeRoute_SettingsFlagIsNamedNotSkipped(t *testing.T) {
	for _, argv := range [][]string{
		{"claude", "--settings", `{"env":{"ANTHROPIC_BASE_URL":"http://elsewhere"}}`, "-p", "hi"},
		{"claude", "--settings=/tmp/s.json"},
		{"claude", "--setting-sources", "user"},
		nil, // ArgvOf could not read the command line
	} {
		t.Run("", func(t *testing.T) {
			h := withClaudeSettingsHost(t)
			cwd := t.TempDir()
			writeSettings(t, cwd, "settings.local.json", zaiLocal)
			spy := claudeRouteSpy(map[string]string{})
			spy.argv = map[int][]string{123: argv}
			withRouteObserver(t, spy)

			got := ObserveClaudeCodeRoute(123, cwd, true)
			requireRoute(t, got, session.RouteUnreadable, "", "settings:cli")

			if err := os.WriteFile(filepath.Join(h.ManagedDir, "managed-settings.json"),
				[]byte(`{"env":{"ANTHROPIC_BASE_URL":"https://managed.example"}}`), 0o600); err != nil {
				t.Fatal(err)
			}
			got = ObserveClaudeCodeRoute(123, cwd, true)
			requireRoute(t, got, session.RouteObserved, "https://managed.example", "settings:managed")
		})
	}
}

// A layer that exists but cannot be parsed is unreadable, never absent —
// and never skipped in favour of the process env below it.
func TestObserveClaudeCodeRoute_UnparseableLayerIsUnreadable(t *testing.T) {
	for name, body := range map[string]string{
		"comment":        "{\n  // Claude Code settings are strict JSON\n  \"env\": {}\n}",
		"trailing comma": `{"env":{"ANTHROPIC_BASE_URL":"https://x.example",}}`,
		"non-string":     `{"env":{"ANTHROPIC_BASE_URL":42}}`,
		"not an object":  `["env"]`,
	} {
		t.Run(name, func(t *testing.T) {
			withClaudeSettingsHost(t)
			cwd := t.TempDir()
			file := writeSettings(t, cwd, "settings.local.json", body)
			withRouteObserver(t, claudeRouteSpy(map[string]string{"ANTHROPIC_BASE_URL": "http://127.0.0.1:9"}))

			got := ObserveClaudeCodeRoute(123, cwd, true)

			requireRoute(t, got, session.RouteUnreadable, "", "settings:local")
			if got.SettingsFile != file {
				t.Errorf("SettingsFile = %q, want %s", got.SettingsFile, file)
			}
		})
	}
}

// The project file, the user file and its CLAUDE_CONFIG_DIR relocation, in
// that order below the local file.
func TestObserveClaudeCodeRoute_ProjectThenUserLayers(t *testing.T) {
	h := withClaudeSettingsHost(t)
	cwd := t.TempDir()
	userFile := writeSettings(t, h.Home, "settings.json", `{"env":{"ANTHROPIC_BASE_URL":"https://user.example"}}`)
	withRouteObserver(t, claudeRouteSpy(map[string]string{}))

	got := ObserveClaudeCodeRoute(123, cwd, true)
	requireRoute(t, got, session.RouteObserved, "https://user.example", "settings:user")
	if got.SettingsFile != userFile {
		t.Errorf("SettingsFile = %q, want %s", got.SettingsFile, userFile)
	}

	writeSettings(t, cwd, "settings.json", `{"env":{"ANTHROPIC_BASE_URL":"https://project.example"}}`)
	got = ObserveClaudeCodeRoute(123, cwd, true)
	requireRoute(t, got, session.RouteObserved, "https://project.example", "settings:project")

	// CLAUDE_CONFIG_DIR from the SESSION's environment moves the user file.
	cfg := t.TempDir()
	if err := os.WriteFile(filepath.Join(cfg, "settings.json"),
		[]byte(`{"env":{"ANTHROPIC_BASE_URL":"https://relocated.example"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cwd2 := t.TempDir()
	withRouteObserver(t, claudeRouteSpy(map[string]string{"CLAUDE_CONFIG_DIR": cfg}))
	got = ObserveClaudeCodeRoute(123, cwd2, true)
	requireRoute(t, got, session.RouteObserved, "https://relocated.example", "settings:user")

	withRouteObserver(t, claudeRouteSpy(map[string]string{"CLAUDE_CONFIG_DIR": "relative/dir"}))
	got = ObserveClaudeCodeRoute(123, cwd2, true)
	requireRoute(t, got, session.RouteUnreadable, "", "settings:user")
}

// A hardened-runtime binary hides its environment (errEnvHidden). A settings
// hit above the user layer still decides; with no such hit the user file's
// location is unknown, so the result is unreadable, naming that layer.
func TestObserveClaudeCodeRoute_HiddenEnvStillUsesSettings(t *testing.T) {
	withClaudeSettingsHost(t)
	cwd := t.TempDir()
	spy := claudeRouteSpy(nil)
	spy.err = errEnvOfFailedForTest
	withRouteObserver(t, spy)

	got := ObserveClaudeCodeRoute(123, cwd, true)
	requireRoute(t, got, session.RouteUnreadable, "", "settings:user")

	writeSettings(t, cwd, "settings.local.json", zaiLocal)
	got = ObserveClaudeCodeRoute(123, cwd, true)
	requireRoute(t, got, session.RouteObserved, zaiAnthropicEndpoint, "settings:local")
}

// An empty value in a settings file cancels a shell export (Claude Code's
// env documentation), so the env value below it is not the route.
func TestObserveClaudeCodeRoute_EmptySettingsValueCancelsEnv(t *testing.T) {
	withClaudeSettingsHost(t)
	cwd := t.TempDir()
	writeSettings(t, cwd, "settings.local.json", `{"env":{"ANTHROPIC_BASE_URL":""}}`)
	withRouteObserver(t, claudeRouteSpy(map[string]string{"ANTHROPIC_BASE_URL": "http://127.0.0.1:9"}))

	got := ObserveClaudeCodeRoute(123, cwd, true)

	requireRoute(t, got, session.RouteAbsent, "", "settings:local")
}

// No settings layer sets the URL: the process env decides, exactly as
// ObserveRoute would.
func TestObserveClaudeCodeRoute_FallsBackToEnv(t *testing.T) {
	withClaudeSettingsHost(t)
	cwd := t.TempDir()
	writeSettings(t, cwd, "settings.local.json", `{"env":{"ANTHROPIC_MODEL":"glm-4.6"},"permissions":{"allow":[]}}`)
	withRouteObserver(t, claudeRouteSpy(map[string]string{"ANTHROPIC_BASE_URL": "https://user:pw@gw.example/v1?k=s"}))

	got := ObserveClaudeCodeRoute(123, cwd, true)
	requireRoute(t, got, session.RouteObserved, "https://gw.example/v1", "env:ANTHROPIC_BASE_URL")
	if got.SettingsFile != "" {
		t.Errorf("SettingsFile = %q, want empty for an env-sourced route", got.SettingsFile)
	}

	withRouteObserver(t, claudeRouteSpy(map[string]string{}))
	got = ObserveClaudeCodeRoute(123, cwd, true)
	requireRoute(t, got, session.RouteAbsent, "", "")
}

// A settings value is redacted exactly like an env value.
func TestObserveClaudeCodeRoute_SettingsValueIsRedacted(t *testing.T) {
	withClaudeSettingsHost(t)
	cwd := t.TempDir()
	writeSettings(t, cwd, "settings.local.json",
		`{"env":{"ANTHROPIC_BASE_URL":"https://u:secret@api.z.ai/api/anthropic?key=s#f"}}`)
	withRouteObserver(t, claudeRouteSpy(map[string]string{}))

	got := ObserveClaudeCodeRoute(123, cwd, true)

	requireRoute(t, got, session.RouteObserved, zaiAnthropicEndpoint, "settings:local")
}

// Inside a git repository the local file lives at the repository root, and
// a file left in the starting directory by an older Claude Code is read
// below it (the root's value wins).
func TestObserveClaudeCodeRoute_LocalFileAtRepoRoot(t *testing.T) {
	withClaudeSettingsHost(t)
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(repo, "pkg", "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	writeSettings(t, sub, "settings.local.json", `{"env":{"ANTHROPIC_BASE_URL":"https://legacy.example"}}`)
	withRouteObserver(t, claudeRouteSpy(map[string]string{}))

	got := ObserveClaudeCodeRoute(123, sub, true)
	requireRoute(t, got, session.RouteObserved, "https://legacy.example", "settings:local")

	rootFile := writeSettings(t, repo, "settings.local.json", zaiLocal)
	got = ObserveClaudeCodeRoute(123, sub, true)
	requireRoute(t, got, session.RouteObserved, zaiAnthropicEndpoint, "settings:local")
	if got.SettingsFile != rootFile {
		t.Errorf("SettingsFile = %q, want the repository root's %s", got.SettingsFile, rootFile)
	}
}

// Review finding 1 (#2161): the daemon's route reader — the dispatch that
// makes the settings-layer fix live — sends a claude-code session through
// the settings layers and every other adapter through the env-only read.
func TestSessionRouteReader_DispatchesClaudeCodeToSettings(t *testing.T) {
	withClaudeSettingsHost(t)
	cwd := t.TempDir()
	writeSettings(t, cwd, "settings.local.json", zaiLocal)
	withRouteObserver(t, claudeRouteSpy(map[string]string{"ANTHROPIC_BASE_URL": "http://127.0.0.1:9"}))
	granted := true
	read := SessionRouteReader("claude-code", func() bool { return granted })

	requireRoute(t, read(123, "claude-code", cwd), session.RouteObserved, zaiAnthropicEndpoint, "settings:local")
	requireRoute(t, read(123, "codex", cwd), session.RouteObserved, "http://127.0.0.1:9", "env:ANTHROPIC_BASE_URL")

	granted = false
	requireRoute(t, read(123, "claude-code", cwd), session.RouteDenied, "", "")
}

// Review finding 4 (#2161): a null value and a case-variant "Env" block are
// not Claude Code's env, so neither may decide the route.
func TestObserveClaudeCodeRoute_NullAndCaseVariantEnvAreNotTheRoute(t *testing.T) {
	t.Run("null value is unreadable, not an empty cancel", func(t *testing.T) {
		withClaudeSettingsHost(t)
		cwd := t.TempDir()
		writeSettings(t, cwd, "settings.local.json", `{"env":{"ANTHROPIC_BASE_URL":null}}`)
		withRouteObserver(t, claudeRouteSpy(map[string]string{"ANTHROPIC_BASE_URL": "http://127.0.0.1:9"}))
		requireRoute(t, ObserveClaudeCodeRoute(123, cwd, true), session.RouteUnreadable, "", "settings:local")
	})
	t.Run("null env block is unreadable", func(t *testing.T) {
		withClaudeSettingsHost(t)
		cwd := t.TempDir()
		writeSettings(t, cwd, "settings.local.json", `{"env":null}`)
		withRouteObserver(t, claudeRouteSpy(map[string]string{}))
		requireRoute(t, ObserveClaudeCodeRoute(123, cwd, true), session.RouteUnreadable, "", "settings:local")
	})
	t.Run("an Env block is not env", func(t *testing.T) {
		withClaudeSettingsHost(t)
		cwd := t.TempDir()
		writeSettings(t, cwd, "settings.local.json", `{"Env":{"ANTHROPIC_BASE_URL":"`+zaiAnthropicEndpoint+`"}}`)
		withRouteObserver(t, claudeRouteSpy(map[string]string{}))
		requireRoute(t, ObserveClaudeCodeRoute(123, cwd, true), session.RouteAbsent, "", "")
	})
}
