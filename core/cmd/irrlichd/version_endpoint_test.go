package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestVersionEndpoint locks the shape of GET /api/v1/version. The web
// dashboard's app-header reads this to render `Irrlicht v$VERSION` without
// baking the value into its own bundle.
func TestVersionEndpoint(t *testing.T) {
	srv, _ := newTestStack(t)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/version")
	if err != nil {
		t.Fatalf("GET /api/v1/version: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("content-type: got %q, want application/json", ct)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	var payload struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("unmarshal %q: %v", body, err)
	}
	if payload.Version != "test" {
		t.Fatalf("version: got %q, want %q", payload.Version, "test")
	}
}

// TestVersionEndpoint_EmptyVersion confirms an unset build flag still
// produces valid JSON (frontends treat "" as "unknown" rather than crashing
// on a missing field).
func TestVersionEndpoint_EmptyVersion(t *testing.T) {
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/version", nil)
	handleGetVersion("")(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", rr.Code)
	}
	var payload struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal %q: %v", rr.Body.Bytes(), err)
	}
	if payload.Version != "" {
		t.Fatalf("version: got %q, want empty", payload.Version)
	}
}
