package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image/png"
	"net/http"
	"strings"
	"testing"
)

func TestResolvePairingHandoffAcceptsOnlyHTTPSOrigin(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{name: "https origin", raw: "https://relay.example.com"},
		{name: "case-insensitive scheme", raw: "HTTPS://relay.example.com"},
		{name: "trim and slash", raw: "  https://relay.example.com/  "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolvePairingHandoff(tt.raw)
			if got.publicURL != "https://relay.example.com" {
				t.Fatalf("publicURL = %q", got.publicURL)
			}
			if got.unavailableReason != "" {
				t.Fatalf("valid public URL has reason %q", got.unavailableReason)
			}
		})
	}
}

func TestResolvePairingHandoffRejectsAnythingButHTTPSOrigin(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{name: "missing"},
		{name: "http", raw: "http://relay.example.com"},
		{name: "path", raw: "https://relay.example.com/elfdans"},
		{name: "query", raw: "https://relay.example.com?workspace=secret"},
		{name: "fragment", raw: "https://relay.example.com/#secret"},
		{name: "credentials", raw: "https://token@relay.example.com"},
		{name: "relative", raw: "relay.example.com"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolvePairingHandoff(tt.raw)
			if got.publicURL != "" {
				t.Fatalf("publicURL = %q, want empty", got.publicURL)
			}
			if got.unavailableReason == "" {
				t.Fatal("invalid public URL has no user-facing reason")
			}
		})
	}
}

type pairingMintResponse struct {
	Code             string `json:"code"`
	PairingURL       string `json:"pairing_url"`
	PairingQR        string `json:"pairing_qr"`
	PairingURLReason string `json:"pairing_url_reason"`
}

func mintPairing(t *testing.T, env *pushEnv) pairingMintResponse {
	t.Helper()
	status, body := doPush(t, http.MethodPost, env.srv.URL+"/api/v1/push/pairings", env.tokens["dashboard"], nil)
	if status != http.StatusCreated {
		t.Fatalf("mint = %d: %s", status, body)
	}
	var got pairingMintResponse
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	return got
}

func decodePairingPNG(t *testing.T, dataURL string) []byte {
	t.Helper()
	const prefix = "data:image/png;base64,"
	if !strings.HasPrefix(dataURL, prefix) {
		t.Fatalf("pairing_qr does not carry a PNG data URL: %.40q", dataURL)
	}
	data, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(dataURL, prefix))
	if err != nil {
		t.Fatalf("decode QR: %v", err)
	}
	return data
}

func TestPairingMintCarriesURLAndDecodablePNG(t *testing.T) {
	handoff := resolvePairingHandoff("https://relay.example.com")
	env := newPushEnvWithHandoff(t, handoff, tokenSeed{label: "dashboard", workspace: "acme"})
	got := mintPairing(t, env)
	wantURL := "https://relay.example.com/pair/" + got.Code + "/"
	if got.PairingURL != wantURL {
		t.Fatalf("pairing_url = %q, want %q", got.PairingURL, wantURL)
	}
	if got.PairingURLReason != "" {
		t.Fatalf("configured mint has reason %q", got.PairingURLReason)
	}
	data := decodePairingPNG(t, got.PairingQR)
	cfg, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decode QR PNG: %v", err)
	}
	if cfg.Width != 256 || cfg.Height != 256 {
		t.Fatalf("QR size = %dx%d, want 256x256", cfg.Width, cfg.Height)
	}
	if strings.Contains(got.PairingURL, env.tokens["dashboard"]) || strings.Contains(string(data), env.tokens["dashboard"]) {
		t.Fatal("pairing handoff contains the client bearer token")
	}
}

func TestPairingMintWithoutPublicURLKeepsManualCode(t *testing.T) {
	env := newPushEnv(t, tokenSeed{label: "dashboard", workspace: "acme"})
	status, body := doPush(t, http.MethodPost, env.srv.URL+"/api/v1/push/pairings", env.tokens["dashboard"], nil)
	if status != http.StatusCreated {
		t.Fatalf("mint = %d: %s", status, body)
	}
	var got struct {
		Code             string `json:"code"`
		PairingURL       string `json:"pairing_url"`
		PairingQR        string `json:"pairing_qr"`
		PairingURLReason string `json:"pairing_url_reason"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got.Code == "" || got.PairingURL != "" || got.PairingQR != "" {
		t.Fatalf("manual fallback = %+v", got)
	}
	if !strings.Contains(got.PairingURLReason, "--public-url") {
		t.Fatalf("fallback reason %q does not name --public-url", got.PairingURLReason)
	}
}

func TestPairingHandoffPageAndManifest(t *testing.T) {
	handoff := resolvePairingHandoff("https://relay.example.com")
	env := newPushEnvWithHandoff(t, handoff, tokenSeed{label: "dashboard", workspace: "acme"})
	status, body := doPush(t, http.MethodGet, env.srv.URL+"/pair/ABCD-EFGH/", "", nil)
	if status != http.StatusOK {
		t.Fatalf("handoff = %d: %s", status, body)
	}
	page := string(body)
	for _, want := range []string{`href="manifest.webmanifest"`, `src="/pair-handoff.js"`, "ABCD-EFGH"} {
		if !strings.Contains(page, want) {
			t.Fatalf("handoff page does not contain %q", want)
		}
	}

	status, body = doPush(t, http.MethodGet, env.srv.URL+"/pair/ABCD-EFGH/manifest.webmanifest", "", nil)
	if status != http.StatusOK {
		t.Fatalf("manifest = %d: %s", status, body)
	}
	var manifest struct {
		ID       string `json:"id"`
		StartURL string `json:"start_url"`
		Scope    string `json:"scope"`
	}
	if err := json.Unmarshal(body, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.ID != "/" || manifest.Scope != "/" || manifest.StartURL != "/pair/ABCD-EFGH/" {
		t.Fatalf("manifest identity = %+v", manifest)
	}

	status, _ = doPush(t, http.MethodGet, env.srv.URL+"/pair/not-a-code/", "", nil)
	if status != http.StatusNotFound {
		t.Fatalf("invalid handoff = %d, want 404", status)
	}
}

func TestPushInfoReportsPairingAvailability(t *testing.T) {
	env := newPushEnvWithHandoff(t, resolvePairingHandoff("https://relay.example.com"), tokenSeed{label: "client"})
	status, body := doPush(t, http.MethodGet, env.srv.URL+"/api/v1/push/info", "", nil)
	if status != http.StatusOK {
		t.Fatalf("info = %d: %s", status, body)
	}
	var got struct {
		PublicURL        string `json:"public_url"`
		PairingURLReason string `json:"pairing_url_reason"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got.PublicURL != "https://relay.example.com" || got.PairingURLReason != "" {
		t.Fatalf("pairing info = %+v", got)
	}
}

func TestParseServeFlagsCarriesPublicURL(t *testing.T) {
	cfg := parseServeFlags([]string{"--public-url", "https://relay.example.com"})
	if cfg.publicURL != "https://relay.example.com" {
		t.Fatalf("publicURL = %q", cfg.publicURL)
	}
}
