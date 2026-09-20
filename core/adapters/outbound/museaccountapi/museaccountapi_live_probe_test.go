//go:build live_probe

// museaccountapi_live_probe_test.go is issue #2007's ONE authorized live
// request to POST https://api.meta.ai/muse-code/key, using the local Muse
// credential on this machine. It never builds or runs as part of the
// ordinary suite (the live_probe build tag — `go test
// -tags live_probe ./core/adapters/outbound/museaccountapi/... -run
// TestLiveProbe -v`), never loops or retries, and goes through #2003's real
// accountquota.HTTPTransport (accountquota.NewHTTPTransport — the
// production constructor, no loopback dial guard, unlike this package's
// other tests). Every byte written to disk goes through
// RedactSubscriptionResponse FIRST — the raw response body is held only
// in-process memory, for the seconds between the HTTP response and the
// redaction call, and is never itself serialized.
//
// Output: a JSON record at the path named by MUSE_LIVE_PROBE_OUT (or, if
// unset, this package's own directory as museaccountapi_live_probe_result.json
// — gitignored by pattern, never committed directly; the operator reads it,
// hand-verifies it carries no secret, and copies what belongs into the
// committed fixture and the PR body themselves).
package museaccountapi

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"testing"
	"time"

	"irrlicht/core/adapters/outbound/accountquota"
	outbound "irrlicht/core/ports/outbound"
)

// liveProbeResult is everything this probe records — deliberately narrow:
// no raw body field exists on this type at all, so there is no field a
// forgetful edit could accidentally populate with unredacted bytes.
type liveProbeResult struct {
	Date               string `json:"date"`
	MuseVersion        string `json:"muse_version"`
	Command            string `json:"command"`
	CredentialResolved bool   `json:"credential_resolved"`
	CredentialError    string `json:"credential_error,omitempty"`
	RequestMade        bool   `json:"request_made"`
	StatusCode         int    `json:"status_code,omitempty"`
	LatencyMS          int64  `json:"latency_ms,omitempty"`
	FetchError         string `json:"fetch_error,omitempty"`
	RedactedBody       string `json:"redacted_body,omitempty"`
	// RawTopLevelKeys names every top-level JSON key the raw (unredacted)
	// response carried — NAMES only, never values — so the PR body can
	// state which fields existed outside the approved allowlist without
	// this file (or the PR body) ever holding a value from one of them.
	RawTopLevelKeys []string `json:"raw_top_level_keys,omitempty"`
}

func TestLiveProbe(t *testing.T) {
	result := liveProbeResult{Date: time.Now().UTC().Format(time.RFC3339)}

	// Capture the build string ONCE, in this same process, immediately
	// before anything else — issue #2007's probe comment found `muse
	// --version` can change mid-session (a self-update), so the build
	// string must come from the SAME moment as the fixture it labels, never
	// a separate earlier or later read.
	versionCtx, versionCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer versionCancel()
	if out, err := exec.CommandContext(versionCtx, "muse", "--version").Output(); err == nil {
		result.MuseVersion = string(out)
	} else {
		result.MuseVersion = "unavailable: " + err.Error()
	}

	resolver := NewCredentialResolver(AuthPath)
	resolveCtx, resolveCancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer resolveCancel()
	cred, err := resolver.Resolve(resolveCtx)
	if err != nil {
		result.CredentialResolved = false
		result.CredentialError = err.Error()
		result.Command = "museaccountapi.NewCredentialResolver(museaccountapi.AuthPath).Resolve(ctx) — blocked, no HTTP request made"
		writeProbeResult(t, result)
		t.Logf("credential not resolvable: %v — this is #2007 §6 row 1 BLOCKED, not evidence Muse has no account API", err)
		return
	}
	result.CredentialResolved = true

	transport, err := accountquota.NewHTTPTransport([]outbound.FixedDestination{Destination()})
	if err != nil {
		t.Fatalf("accountquota.NewHTTPTransport: %v", err)
	}

	result.Command = "POST " + SubscriptionURL + " via accountquota.HTTPTransport.Fetch (one call)"
	result.RequestMade = true

	fetchCtx, fetchCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer fetchCancel()
	start := time.Now()
	resp, fetchErr := transport.Fetch(fetchCtx, outbound.AccountQuotaRequest{
		DestinationKey: DestinationKey,
		Credential:     cred,
		Auth:           Auth(),
		SessionID:      "live-probe-2007",
	})
	result.LatencyMS = time.Since(start).Milliseconds()

	if fetchErr != nil {
		result.FetchError = fetchErr.Error()
		writeProbeResult(t, result)
		t.Logf("Fetch failed: %v (latency %dms)", fetchErr, result.LatencyMS)
		return
	}

	result.StatusCode = resp.StatusCode

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(resp.Body, &raw); err == nil {
		for k := range raw {
			result.RawTopLevelKeys = append(result.RawTopLevelKeys, k)
		}
	}

	redacted, err := RedactSubscriptionResponse(resp.Body)
	if err != nil {
		t.Fatalf("RedactSubscriptionResponse: %v (a non-JSON or unreadable response must still be recorded, not silently dropped)", err)
	}
	result.RedactedBody = string(redacted)

	writeProbeResult(t, result)
	t.Logf("live probe complete: status=%d latency=%dms", result.StatusCode, result.LatencyMS)
}

func writeProbeResult(t *testing.T, result liveProbeResult) {
	t.Helper()
	out := os.Getenv("MUSE_LIVE_PROBE_OUT")
	if out == "" {
		out = "museaccountapi_live_probe_result.json"
	}
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		t.Fatalf("marshaling probe result: %v", err)
	}
	if err := os.WriteFile(out, data, 0o600); err != nil {
		t.Fatalf("writing probe result to %s: %v", out, err)
	}
	t.Logf("probe result written to %s", out)
}
