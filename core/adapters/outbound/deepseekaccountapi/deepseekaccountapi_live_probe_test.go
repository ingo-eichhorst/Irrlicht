//go:build live_probe

// deepseekaccountapi_live_probe_test.go is issue #2009 §6's probe, row 1:
// one DeepSeek balance request. It has NEVER been run — the run that wrote
// it had no DeepSeek account access (see the #2009 PR body) — so treat it as
// the recorded command, not as evidence.
//
// It never builds in the ordinary suite. To run it, by hand, once:
//
//	DEEPSEEK_API_KEY=... go test -tags live_probe \
//	  ./core/adapters/outbound/deepseekaccountapi/... -run TestLiveProbe -v
//
// The key comes from this test process's own environment only; nothing here
// reads a file or a keychain. It makes one request through #2003's real
// accountquota.HTTPTransport and writes a record whose only body field is
// the RedactBalanceResponse output, to DEEPSEEK_LIVE_PROBE_OUT or
// deepseekaccountapi_live_probe_result.json (gitignored).
package deepseekaccountapi

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"irrlicht/core/adapters/outbound/accountquota"
	outbound "irrlicht/core/ports/outbound"
)

type liveProbeResult struct {
	Date            string   `json:"date"`
	Command         string   `json:"command"`
	StatusCode      int      `json:"status_code,omitempty"`
	FetchError      string   `json:"fetch_error,omitempty"`
	RedactedBody    string   `json:"redacted_body,omitempty"`
	RawTopLevelKeys []string `json:"raw_top_level_keys,omitempty"`
	// RawEntryKeys names the keys of each balance_infos entry — names only,
	// never values — so an undocumented per-entry field (an account id, say)
	// is visible even though the redacted body drops it.
	RawEntryKeys [][]string `json:"raw_entry_keys,omitempty"`
}

func TestLiveProbe(t *testing.T) {
	result := liveProbeResult{
		Date:    time.Now().UTC().Format(time.RFC3339),
		Command: "GET " + BalanceURL + " via accountquota.HTTPTransport.Fetch (one call)",
	}
	key := os.Getenv("DEEPSEEK_API_KEY")
	if key == "" {
		t.Skip("DEEPSEEK_API_KEY is not set — no request made")
	}
	transport, err := accountquota.NewHTTPTransport([]outbound.FixedDestination{Destination()})
	if err != nil {
		t.Fatalf("NewHTTPTransport: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	resp, err := transport.Fetch(ctx, outbound.AccountQuotaRequest{
		DestinationKey: DestinationKey,
		Credential:     outbound.NewCredential(key),
		Auth:           Auth(),
		SessionID:      "live-probe-2009",
	})
	if err != nil {
		result.FetchError = err.Error()
		writeProbeResult(t, result)
		return
	}
	result.StatusCode = resp.StatusCode
	var raw map[string]json.RawMessage
	if json.Unmarshal(resp.Body, &raw) == nil {
		for k := range raw {
			result.RawTopLevelKeys = append(result.RawTopLevelKeys, k)
		}
		var entries []map[string]json.RawMessage
		if json.Unmarshal(raw["balance_infos"], &entries) == nil {
			for _, e := range entries {
				var keys []string
				for k := range e {
					keys = append(keys, k)
				}
				result.RawEntryKeys = append(result.RawEntryKeys, keys)
			}
		}
	}
	redacted, err := RedactBalanceResponse(resp.Body)
	if err != nil {
		t.Fatalf("RedactBalanceResponse: %v", err)
	}
	result.RedactedBody = string(redacted)
	writeProbeResult(t, result)
}

func writeProbeResult(t *testing.T, result liveProbeResult) {
	t.Helper()
	out := os.Getenv("DEEPSEEK_LIVE_PROBE_OUT")
	if out == "" {
		out = "deepseekaccountapi_live_probe_result.json"
	}
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(out, data, 0o600); err != nil {
		t.Fatalf("write %s: %v", out, err)
	}
	t.Logf("probe result written to %s", out)
}
