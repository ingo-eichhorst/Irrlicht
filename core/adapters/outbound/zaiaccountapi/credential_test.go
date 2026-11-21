package zaiaccountapi

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"irrlicht/core/adapters/inbound/agents/claudecode/settingsenv"
	"irrlicht/core/domain/session"
)

func newTestReader(t *testing.T) *CredentialReader {
	t.Helper()
	fp, err := NewFingerprinter()
	if err != nil {
		t.Fatal(err)
	}
	return NewCredentialReader(settingsenv.ReadEnv, fp)
}

func settingsFile(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), ".claude", "settings.local.json")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func zaiRoute(file string) *session.RouteObservation {
	return &session.RouteObservation{Status: session.RouteObserved, Endpoint: "https://api.z.ai/api/anthropic",
		Source: "settings:local", SettingsFile: file}
}

func reasonOf(err error) string {
	var cu *CredentialUnavailableError
	if errors.As(err, &cu) {
		return cu.Reason
	}
	return ""
}

func TestCredentialReader_ReadsTheRouteFilesToken(t *testing.T) {
	r := newTestReader(t)
	file := settingsFile(t, `{"env":{"ANTHROPIC_BASE_URL":"https://api.z.ai/api/anthropic","ANTHROPIC_AUTH_TOKEN":"tok-auth","ANTHROPIC_API_KEY":"tok-api"}}`)

	c, err := r.Read(zaiRoute(file))
	if err != nil {
		t.Fatal(err)
	}
	if c.Key.IsZero() || c.Fingerprint == "" {
		t.Fatalf("got %+v, want a key and a fingerprint", c)
	}
	if strings.Contains(c.Fingerprint, "tok-auth") {
		t.Fatal("the fingerprint must not carry the key")
	}
	same, _ := r.Read(zaiRoute(file))
	if same.Fingerprint != c.Fingerprint {
		t.Error("one key must fingerprint identically within one reader")
	}
	apiOnly := settingsFile(t, `{"env":{"ANTHROPIC_BASE_URL":"https://api.z.ai/api/anthropic","ANTHROPIC_API_KEY":"tok-auth"}}`)
	viaAPIKey, err := r.Read(zaiRoute(apiOnly))
	if err != nil || viaAPIKey.Fingerprint != c.Fingerprint {
		t.Errorf("ANTHROPIC_API_KEY fallback: got %+v, %v; want the same key's fingerprint", viaAPIKey, err)
	}
}

// Two daemons (two salts) never fingerprint one key alike: the fingerprint
// identifies a key only inside the process that drew the salt.
func TestFingerprinter_SaltIsPerInstance(t *testing.T) {
	a, _ := NewFingerprinter()
	b, _ := NewFingerprinter()
	if a.fingerprint("k") == b.fingerprint("k") {
		t.Fatal("two salts produced one fingerprint")
	}
}

// The key is used only while the route's own file still names api.z.ai:
// once it points elsewhere, its key may be another provider's.
func TestCredentialReader_RequiresTheFileToStillRouteToZai(t *testing.T) {
	r := newTestReader(t)
	for name, body := range map[string]string{
		"moved to kimi":    `{"env":{"ANTHROPIC_BASE_URL":"https://api.moonshot.ai/anthropic","ANTHROPIC_AUTH_TOKEN":"kimi-key"}}`,
		"lookalike host":   `{"env":{"ANTHROPIC_BASE_URL":"https://api.z.ai.example.com/anthropic","ANTHROPIC_AUTH_TOKEN":"k"}}`,
		"no base url":      `{"env":{"ANTHROPIC_AUTH_TOKEN":"k"}}`,
		"host in the path": `{"env":{"ANTHROPIC_BASE_URL":"https://example.com/api.z.ai","ANTHROPIC_AUTH_TOKEN":"k"}}`,
	} {
		if _, err := r.Read(zaiRoute(settingsFile(t, body))); reasonOf(err) != ReasonSettingsNotZai {
			t.Errorf("%s: err = %v, want %s", name, err, ReasonSettingsNotZai)
		}
	}
}

func TestCredentialReader_ClassifiesUnavailable(t *testing.T) {
	r := newTestReader(t)
	envRoute := &session.RouteObservation{Status: session.RouteObserved, Endpoint: "https://api.z.ai/api/anthropic", Source: "env:ANTHROPIC_BASE_URL"}
	if _, err := r.Read(envRoute); reasonOf(err) != ReasonRouteNotFromSettings {
		t.Errorf("env-sourced route: err = %v, want %s (a key only in the process env is never read)", err, ReasonRouteNotFromSettings)
	}
	if _, err := r.Read(zaiRoute(filepath.Join(t.TempDir(), "gone.json"))); reasonOf(err) != ReasonSettingsUnreadable {
		t.Errorf("missing file: err = %v, want %s", err, ReasonSettingsUnreadable)
	}
	noKey := settingsFile(t, `{"env":{"ANTHROPIC_BASE_URL":"https://api.z.ai/api/anthropic"}}`)
	if _, err := r.Read(zaiRoute(noKey)); reasonOf(err) != ReasonNoKey {
		t.Errorf("no key: err = %v, want %s", err, ReasonNoKey)
	}
}
