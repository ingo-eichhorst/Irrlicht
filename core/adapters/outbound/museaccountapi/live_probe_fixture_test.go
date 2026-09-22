package museaccountapi

import (
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"
)

// liveProbeFixture is testdata/live_probe_2026-09-20.json's shape — the
// REDACTED record of issue #2007's one authorized live request. Loaded here
// (never live) so this package's replay proof runs against the actual
// captured response shape, not only the pinned herdr-agent-quota fixture
// (parser_test.go's powerFixture).
type liveProbeFixture struct {
	Date            string          `json:"date"`
	MuseVersion     string          `json:"muse_version"`
	StatusCode      int             `json:"status_code"`
	RedactedBody    json.RawMessage `json:"redacted_body"`
	RawTopLevelKeys []string        `json:"raw_top_level_keys"`
}

func loadLiveProbeFixture(t *testing.T) liveProbeFixture {
	t.Helper()
	data, err := os.ReadFile("testdata/live_probe_2026-09-20.json")
	if err != nil {
		t.Fatalf("reading testdata/live_probe_2026-09-20.json: %v", err)
	}
	var f liveProbeFixture
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatalf("parsing testdata/live_probe_2026-09-20.json: %v", err)
	}
	return f
}

// TestLiveProbeFixture_NoCredentialOrPersonalDataCommitted is a structural
// safety check on the committed fixture itself, run every time the suite
// runs (never only at capture time): none of the values the real response
// carried outside the approved allowlist (raw_top_level_keys — NAMES only)
// appear anywhere in the fixture file's own bytes as a VALUE, and the
// redacted body decodes to exactly the two approved keys.
func TestLiveProbeFixture_NoCredentialOrPersonalDataCommitted(t *testing.T) {
	f := loadLiveProbeFixture(t)
	if f.StatusCode != 200 {
		t.Fatalf("StatusCode = %d, want 200 — this fixture is expected to be a successful capture", f.StatusCode)
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(f.RedactedBody, &body); err != nil {
		t.Fatalf("redacted_body is not valid JSON: %v", err)
	}
	wantKeys := map[string]bool{"is_subs_active": true, "subs_tier_name": true}
	for k := range body {
		if !wantKeys[k] {
			t.Errorf("committed fixture's redacted_body carries non-approved key %q", k)
		}
	}
	if len(f.RawTopLevelKeys) == 0 {
		t.Fatal("vacuity: raw_top_level_keys is empty — this fixture would prove nothing about what got dropped")
	}
	forbidden := []string{"api_key", "user_email", "user_full_name"}
	for _, k := range forbidden {
		found := false
		for _, got := range f.RawTopLevelKeys {
			if got == k {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("raw_top_level_keys is missing %q — the live response is expected to have carried it (matching the pinned herdr-agent-quota source's own doc comment)", k)
		}
	}
}

// TestBuildSnapshot_ReplaysTheLiveProbeFixture proves BuildSnapshot handles
// the REAL captured response shape, not only the pinned herdr-agent-quota
// fixture: this account's response carries is_subs_active:true and
// subs_tier_name, but NO subs_usage block at all — resolving the open
// question issue #2007's probe comment left ("Currently unavailable" could
// mean zero usage this window, a failed client fetch, or a figure only
// behind the account API): the account API itself also has no usage
// windows for this account, so BuildSnapshot correctly reports
// ErrNoQuotaWindows, never a fabricated zero-percent window.
func TestBuildSnapshot_ReplaysTheLiveProbeFixture(t *testing.T) {
	f := loadLiveProbeFixture(t)
	_, err := BuildSnapshot(f.RedactedBody, time.Unix(1, 0))
	if !errors.Is(err, ErrNoQuotaWindows) {
		t.Fatalf("BuildSnapshot(live probe fixture) err = %v, want ErrNoQuotaWindows", err)
	}
}
