package deepseekaccountapi

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"irrlicht/core/domain/session"
)

var testNow = time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)

// fixture reads one of the provider-catalog fixtures this package is the
// code-backed route for (replaydata/providers/deepseek/manifest.json), so the
// manifest's fixture-verified claim is exercised by this suite rather than
// by the JSON alone.
func fixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "replaydata", "providers", "deepseek", "fixtures", name))
	if err != nil {
		t.Fatalf("reading fixture %s: %v", name, err)
	}
	return data
}

// wireCredits marshals the snapshot exactly as the daemon would send it and
// decodes the credits object generically, so assertions see the wire, not
// the Go struct.
func wireCredits(t *testing.T, snap session.RateLimitSnapshot) map[string]any {
	t.Helper()
	data, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var wire map[string]any
	if err := json.Unmarshal(data, &wire); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	credits, ok := wire["credits"].(map[string]any)
	if !ok {
		t.Fatalf("wire snapshot has no credits object: %s", data)
	}
	return credits
}

func TestBuildSnapshot_DocumentedResponse(t *testing.T) {
	snap, err := BuildSnapshot(fixture(t, "balance-response.documented.json"), testNow)
	if err != nil {
		t.Fatalf("BuildSnapshot: %v", err)
	}
	c := snap.Credits
	if c == nil {
		t.Fatal("Credits is nil")
	}
	if !c.HasCredits || c.Unlimited || !c.BalanceObserved || c.Balance != 110 {
		t.Errorf("credits = %+v, want available, limited, observed, balance 110", *c)
	}
	if c.Total == nil || *c.Total != 110 || c.Granted == nil || *c.Granted != 10 || c.ToppedUp == nil || *c.ToppedUp != 100 {
		t.Errorf("components = total %v granted %v topped_up %v, want 110/10/100", c.Total, c.Granted, c.ToppedUp)
	}
	if snap.Provider != session.ProviderDeepSeek || snap.AttributionQuality != session.AttributionQualityConfirmed ||
		snap.ObservationSource != "account_api" || snap.SampledAt != testNow.Unix() {
		t.Errorf("identity stamps = %+v", snap)
	}
	if len(snap.Windows) != 0 {
		t.Errorf("a balance must not carry subscription windows, got %v", snap.Windows)
	}
}

// TestBuildSnapshot_CNYSurvivesToTheWire is the target of
// tools/lib/deepseekaccountapi-currency-mutations_test.sh: with the currency
// dropped, both clients render an unlabeled balance with "$", which is wrong
// for a CNY account.
func TestBuildSnapshot_CNYSurvivesToTheWire(t *testing.T) {
	snap, err := BuildSnapshot(fixture(t, "balance-response.documented.json"), testNow)
	if err != nil {
		t.Fatalf("BuildSnapshot: %v", err)
	}
	if got := wireCredits(t, snap)["currency"]; got != "CNY" {
		t.Fatalf("wire credits.currency = %v, want \"CNY\" — a CNY balance with no currency renders as dollars", got)
	}
}

// TestBuildSnapshot_ZeroIsObservedNotMissing is the target of
// tools/lib/deepseekaccountapi-zero-mutations_test.sh. Balance's own
// omitempty drops a 0 from the wire. total (a non-nil pointer) survives too,
// but balance_observed is the only field the clients consult
// (platforms/web/quotaChips.js usageCreditsLine, SessionListView.swift
// usageCreditsLine) to keep an observed zero apart from "never observed".
func TestBuildSnapshot_ZeroIsObservedNotMissing(t *testing.T) {
	snap, err := BuildSnapshot(fixture(t, "balance-zero.shape.json"), testNow)
	if err != nil {
		t.Fatalf("BuildSnapshot: %v", err)
	}
	w := wireCredits(t, snap)
	if w["balance_observed"] != true {
		t.Fatalf("wire credits.balance_observed = %v, want true — an observed zero balance reads as missing", w["balance_observed"])
	}
	if w["total"] != float64(0) {
		t.Errorf("wire credits.total = %v, want 0", w["total"])
	}
	if w["has_credits"] != false || w["currency"] != "USD" {
		t.Errorf("wire credits = %v, want has_credits false, currency USD", w)
	}
	if _, unlimited := w["unlimited"]; unlimited {
		t.Errorf("an observed zero must never read as unlimited: %v", w)
	}
}

func TestBuildSnapshot_RejectsWhatItCannotRead(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantErr error
		wantMsg string
	}{
		{"no balance entry is not a zero", `{"is_available":true,"balance_infos":[]}`, ErrNoBalance, ""},
		{"absent balance_infos", `{"is_available":true}`, ErrNoBalance, ""},
		{"two currencies are never summed or picked", `{"is_available":true,"balance_infos":[` +
			`{"currency":"CNY","total_balance":"1","granted_balance":"0","topped_up_balance":"1"},` +
			`{"currency":"USD","total_balance":"2","granted_balance":"0","topped_up_balance":"2"}]}`, ErrMultipleCurrencies, ""},
		{"no currency", `{"is_available":true,"balance_infos":[{"total_balance":"1","granted_balance":"0","topped_up_balance":"1"}]}`, nil, "no currency"},
		{"missing amount is not zero", `{"is_available":true,"balance_infos":[{"currency":"CNY","granted_balance":"0","topped_up_balance":"1"}]}`, nil, "no total_balance"},
		{"non-numeric amount", `{"is_available":true,"balance_infos":[{"currency":"CNY","total_balance":"lots","granted_balance":"0","topped_up_balance":"1"}]}`, nil, "not a finite number"},
		{"NaN amount", `{"is_available":true,"balance_infos":[{"currency":"CNY","total_balance":"NaN","granted_balance":"0","topped_up_balance":"1"}]}`, nil, "not a finite number"},
		{"no is_available", `{"balance_infos":[{"currency":"CNY","total_balance":"1","granted_balance":"0","topped_up_balance":"1"}]}`, nil, "no is_available"},
		{"not JSON", `<html>`, nil, "not valid JSON"},
	}
	for _, c := range cases {
		snap, err := BuildSnapshot([]byte(c.body), testNow)
		if err == nil {
			t.Errorf("%s: BuildSnapshot succeeded with %+v, want an error", c.name, snap)
			continue
		}
		if c.wantErr != nil && !errors.Is(err, c.wantErr) {
			t.Errorf("%s: err = %v, want %v", c.name, err, c.wantErr)
		}
		if c.wantMsg != "" && !strings.Contains(err.Error(), c.wantMsg) {
			t.Errorf("%s: err = %v, want it to mention %q", c.name, err, c.wantMsg)
		}
		if snap.Credits != nil {
			t.Errorf("%s: a rejected response still produced credits %+v", c.name, *snap.Credits)
		}
	}
}

// TestBuildSnapshot_BindsToNoConfirmedAccount: the response carries no
// account id, so the snapshot must not claim one, and BalanceCovers must
// then refuse to present it beside any session.
func TestBuildSnapshot_BindsToNoConfirmedAccount(t *testing.T) {
	snap, err := BuildSnapshot(fixture(t, "balance-response.documented.json"), testNow)
	if err != nil {
		t.Fatalf("BuildSnapshot: %v", err)
	}
	if snap.ConfirmedAccountRef != "" || snap.QuotaID != "" {
		t.Fatalf("snapshot claims a scope reference (%q/%q) the response never carried", snap.ConfirmedAccountRef, snap.QuotaID)
	}
	use := session.BalanceUse{Provider: session.ProviderDeepSeek, Arrangement: session.BillingArrangementProviderCredits, Scope: session.BalanceScopeAccount}
	if session.BalanceCovers(&snap, use) {
		t.Fatal("a balance bound to no confirmed account covered a session")
	}
}

// TestRedactBalanceResponse_KeepsAbsenceAbsent: a capture must show a missing
// field as missing, not as "" or null.
func TestRedactBalanceResponse_KeepsAbsenceAbsent(t *testing.T) {
	out, err := RedactBalanceResponse([]byte(`{"is_available":true,"balance_infos":[{"total_balance":"1.00"}]}`))
	if err != nil {
		t.Fatalf("RedactBalanceResponse: %v", err)
	}
	for _, absent := range []string{"currency", "granted_balance", "topped_up_balance", "null"} {
		if strings.Contains(string(out), absent) {
			t.Errorf("redacted output invents %q for a field the response never carried:\n%s", absent, out)
		}
	}
}

func TestRedactBalanceResponse_DropsNonApprovedFields(t *testing.T) {
	raw := `{"is_available":true,"api_key":"sk-secret","user_email":"a@b.c","balance_infos":[` +
		`{"currency":"CNY","total_balance":"1.00","granted_balance":"0.00","topped_up_balance":"1.00","account_id":"acct-9"}]}`
	out, err := RedactBalanceResponse([]byte(raw))
	if err != nil {
		t.Fatalf("RedactBalanceResponse: %v", err)
	}
	for _, leaked := range []string{"sk-secret", "api_key", "user_email", "a@b.c", "account_id", "acct-9"} {
		if strings.Contains(string(out), leaked) {
			t.Errorf("redacted output still carries %q:\n%s", leaked, out)
		}
	}
	if _, err := BuildSnapshot(out, testNow); err != nil {
		t.Errorf("redacted output no longer parses: %v", err)
	}
}

func TestDestination_HasTheReviewedRequestShape(t *testing.T) {
	d := Destination()
	if d.Key != DestinationKey || d.URL != "https://api.deepseek.com/user/balance" || d.Method != "GET" || d.Body != nil {
		t.Errorf("Destination = %+v", d)
	}
	if a := Auth(); a.Header != "Authorization" || a.Prefix != "Bearer " {
		t.Errorf("Auth = %+v", a)
	}
}

// TestDeepSeekDestination_NeverDialsInTheOrdinarySuite: no production file in
// this package constructs a transport, and the one file that does carries the
// live_probe build tag on its first line, so a tagless `go test` never
// compiles a call that could reach api.deepseek.com.
func TestDeepSeekDestination_NeverDialsInTheOrdinarySuite(t *testing.T) {
	const liveProbeFile = "deepseekaccountapi_live_probe_test.go"
	data, err := os.ReadFile(liveProbeFile)
	if err != nil {
		t.Fatalf("reading %s: %v", liveProbeFile, err)
	}
	if first := strings.SplitN(string(data), "\n", 2)[0]; first != "//go:build live_probe" {
		t.Fatalf("%s's first line is %q, want \"//go:build live_probe\"", liveProbeFile, first)
	}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	scanned := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		scanned++
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("ReadFile %s: %v", name, err)
		}
		if strings.Contains(string(src), "NewHTTPTransport(") || strings.Contains(string(src), "net/http") {
			t.Errorf("%s builds or imports a transport — only the live_probe file may", name)
		}
	}
	if scanned == 0 {
		t.Fatal("vacuity: scanned zero production .go files")
	}
}
