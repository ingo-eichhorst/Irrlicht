package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"irrlicht/core/adapters/outbound/relay"
)

// publishStatusPayload mirrors the GET /api/v1/relay/publish response so tests
// assert its shape independently of the handler's private struct.
type publishStatusPayload struct {
	Enabled     bool   `json:"enabled"`
	URL         string `json:"url"`
	State       string `json:"state"`
	LastError   string `json:"lastError"`
	DaemonID    string `json:"daemonId"`
	DaemonLabel string `json:"daemonLabel"`
}

// TestPublishStatusEndpoint_Disabled: when publishing is off the forwarder is
// nil and the endpoint reports enabled=false with no leaked fields.
func TestPublishStatusEndpoint_Disabled(t *testing.T) {
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/relay/publish", nil)
	handleGetPublishStatus(nil)(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("content-type: got %q, want application/json", ct)
	}
	var payload publishStatusPayload
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal %q: %v", rr.Body.Bytes(), err)
	}
	if payload.Enabled {
		t.Fatalf("nil forwarder must report enabled=false, got %+v", payload)
	}
	if payload.State != "" || payload.URL != "" {
		t.Fatalf("disabled response must omit link fields, got %+v", payload)
	}
}

// TestPublishStatusEndpoint_Enabled: with a forwarder present the endpoint
// surfaces its live state and identity. A freshly-built forwarder reports
// PublishConnecting before it has dialed.
func TestPublishStatusEndpoint_Enabled(t *testing.T) {
	fwd := relay.NewForwarder("wss://funken.io", relay.Identity{DaemonID: "d1", DaemonLabel: "lap"}, "", nil, nil, nil)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/relay/publish", nil)
	handleGetPublishStatus(fwd)(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", rr.Code)
	}
	var payload publishStatusPayload
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal %q: %v", rr.Body.Bytes(), err)
	}
	if !payload.Enabled {
		t.Fatalf("forwarder present must report enabled=true, got %+v", payload)
	}
	if payload.State != relay.PublishConnecting {
		t.Fatalf("state: got %q, want %q", payload.State, relay.PublishConnecting)
	}
	if payload.DaemonID != "d1" || payload.DaemonLabel != "lap" {
		t.Fatalf("identity not surfaced: %+v", payload)
	}
	if payload.URL == "" {
		t.Fatalf("url must be populated, got %+v", payload)
	}
}
