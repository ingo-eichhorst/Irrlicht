package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"irrlicht/core/adapters/outbound/relay"
	"irrlicht/core/application/services"
	"irrlicht/core/domain/session"
)

// publishStatusPayload mirrors the GET/PUT /api/v1/relay/publish response so
// tests assert its shape independently of the handler's private struct.
type publishStatusPayload struct {
	Enabled     bool   `json:"enabled"`
	URL         string `json:"url"`
	State       string `json:"state"`
	LastError   string `json:"lastError"`
	DaemonID    string `json:"daemonId"`
	DaemonLabel string `json:"daemonLabel"`
}

// newTestController builds a controller bound to the test's context so any
// forwarder it starts is torn down at cleanup (no lingering dial goroutines).
func newTestController(t *testing.T) *relay.PublishController {
	t.Helper()
	return relay.NewPublishController(
		t.Context(),
		relay.Identity{DaemonID: "d1", DaemonLabel: "lap"},
		services.NewPushService(),
		func() ([]*session.SessionState, []relay.AgentInfo) { return nil, nil },
		nil,
	)
}

// TestPublishStatusEndpoint_Disabled: when publishing is off the controller has
// no forwarder and the endpoint reports enabled=false with no leaked fields.
func TestPublishStatusEndpoint_Disabled(t *testing.T) {
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/relay/publish", nil)
	handleGetPublishStatus(newTestController(t))(rr, req)

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
		t.Fatalf("no forwarder must report enabled=false, got %+v", payload)
	}
	if payload.State != "" || payload.URL != "" {
		t.Fatalf("disabled response must omit link fields, got %+v", payload)
	}
}

// TestPublishStatusEndpoint_Enabled: once publishing is enabled the endpoint
// surfaces the forwarder's URL and identity. State is some live link state — we
// don't assert which, since the forwarder is dialing concurrently.
func TestPublishStatusEndpoint_Enabled(t *testing.T) {
	c := newTestController(t)
	// 127.0.0.1:1 refuses fast, so no real relay is needed and the dial loop
	// stays local; the test context cancels it at cleanup.
	c.Apply(true, "ws://127.0.0.1:1", "")

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/relay/publish", nil)
	handleGetPublishStatus(c)(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", rr.Code)
	}
	var payload publishStatusPayload
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal %q: %v", rr.Body.Bytes(), err)
	}
	if !payload.Enabled {
		t.Fatalf("enabled publishing must report enabled=true, got %+v", payload)
	}
	if payload.DaemonID != "d1" || payload.DaemonLabel != "lap" {
		t.Fatalf("identity not surfaced: %+v", payload)
	}
	if payload.URL == "" {
		t.Fatalf("url must be populated, got %+v", payload)
	}
}

// TestPublishStatusEndpoint_PutTogglesPublishing: a PUT enabling then disabling
// publishing flips the reported status, proving the hot-reload path (issue #722)
// works end-to-end through the HTTP layer with no daemon relaunch.
func TestPublishStatusEndpoint_PutTogglesPublishing(t *testing.T) {
	c := newTestController(t)
	handler := handlePutPublishStatus(c)

	put := func(body string) publishStatusPayload {
		t.Helper()
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPut, "/api/v1/relay/publish", strings.NewReader(body))
		handler(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("PUT %s: status got %d, want 200", body, rr.Code)
		}
		var p publishStatusPayload
		if err := json.Unmarshal(rr.Body.Bytes(), &p); err != nil {
			t.Fatalf("unmarshal %q: %v", rr.Body.Bytes(), err)
		}
		return p
	}

	on := put(`{"enabled":true,"url":"ws://127.0.0.1:1","token":""}`)
	if !on.Enabled || on.URL == "" {
		t.Fatalf("PUT enabled=true must turn publishing on, got %+v", on)
	}

	off := put(`{"enabled":false,"url":"ws://127.0.0.1:1","token":""}`)
	if off.Enabled || off.URL != "" {
		t.Fatalf("PUT enabled=false must turn publishing off, got %+v", off)
	}
}

// TestPublishStatusEndpoint_PutRejectsBadBody: a malformed body is a 400, not a
// silent no-op that the caller mistakes for success.
func TestPublishStatusEndpoint_PutRejectsBadBody(t *testing.T) {
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/relay/publish", strings.NewReader("not json"))
	handlePutPublishStatus(newTestController(t))(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("malformed body: status got %d, want 400", rr.Code)
	}
}
