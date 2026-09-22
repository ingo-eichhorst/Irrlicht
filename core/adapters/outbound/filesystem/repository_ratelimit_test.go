package filesystem_test

import (
	"os"
	"path/filepath"
	"testing"

	"irrlicht/core/adapters/outbound/filesystem"
	"irrlicht/core/domain/session"
)

// legacyRateLimitSession is the instance-file shape a pre-#1994 daemon wrote
// and #2030 found on disk after an upgrade: a rate_limit snapshot with windows
// but none of the attribution fields. Copied from the reporter's
// instances/01d53d1c-….json (windows trimmed to two readings).
const legacyRateLimitSession = `{
  "version": 1,
  "session_id": "legacy",
  "state": "working",
  "adapter": "claude-code",
  "metrics": {
    "rate_limit": {
      "sampled_at": 1790106466,
      "windows": [
        {"used_percent": 12, "window_minutes": 300, "resets_at": 1790110000},
        {"used_percent": 40, "window_minutes": 10080, "resets_at": 1790500000}
      ]
    },
    "rate_limit_forecast_eta": 1790109000
  }
}`

const confirmedRateLimitSession = `{
  "version": 1,
  "session_id": "confirmed",
  "state": "working",
  "adapter": "claude-code",
  "metrics": {
    "rate_limit": {
      "sampled_at": 1790107482,
      "provider": "anthropic",
      "attribution_evidence": "claude_code_statusline",
      "observation_source": "hook",
      "attribution_quality": "confirmed",
      "windows": [
        {"used_percent": 12, "window_minutes": 300, "resets_at": 1790110000}
      ]
    },
    "rate_limit_forecast_eta": 1790109000
  }
}`

func writeInstance(t *testing.T, dir, id, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, id+".json"), []byte(body), 0600); err != nil {
		t.Fatalf("write %s: %v", id, err)
	}
}

// loadBothWays writes one instance file and returns the state as Load and as
// ListAll decode it, so each test asserts both restore paths.
func loadBothWays(t *testing.T, id, body string) map[string]*session.SessionState {
	t.Helper()
	dir := t.TempDir()
	writeInstance(t, dir, id, body)
	repo := filesystem.NewWithDir(dir)

	loaded, err := repo.Load(id)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	listed, err := repo.ListAll()
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("ListAll: got %d states, want 1", len(listed))
	}
	return map[string]*session.SessionState{"Load": loaded, "ListAll": listed[0]}
}

// Red-first for #2030: a restored unattributed snapshot made every client key
// the session as "unknown:claude-code" next to the confirmed "anthropic"
// sessions — one subscription, two quota providers.
func TestRepository_DropsLegacyUnattributedRateLimit(t *testing.T) {
	for name, s := range loadBothWays(t, "legacy", legacyRateLimitSession) {
		if s.Metrics == nil {
			t.Fatalf("%s: metrics dropped entirely; only the rate-limit fields should go", name)
		}
		if s.Metrics.RateLimit != nil {
			t.Errorf("%s: legacy unattributed rate_limit survived the load: %+v", name, s.Metrics.RateLimit)
		}
		if s.Metrics.RateLimitForecastEta != nil {
			t.Errorf("%s: forecast ETA of the dropped snapshot survived: %d", name, *s.Metrics.RateLimitForecastEta)
		}
	}
}

// Lock: a confirmed snapshot is the current shape and must load unchanged.
func TestRepository_KeepsConfirmedRateLimit(t *testing.T) {
	for name, s := range loadBothWays(t, "confirmed", confirmedRateLimitSession) {
		rl := s.Metrics.RateLimit
		if rl == nil {
			t.Fatalf("%s: confirmed rate_limit was dropped on load", name)
		}
		if rl.Provider != session.ProviderAnthropic {
			t.Errorf("%s: provider changed on load: %q", name, rl.Provider)
		}
		if len(rl.Windows) != 1 {
			t.Errorf("%s: windows changed on load: %+v", name, rl.Windows)
		}
		if s.Metrics.RateLimitForecastEta == nil {
			t.Errorf("%s: forecast ETA of a confirmed snapshot was dropped", name)
		}
	}
}
