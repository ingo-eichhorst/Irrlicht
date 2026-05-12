package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"testing"

	"irrlicht/core/internal/contracttesting"
)

// TestContract_AgentsEndpoint locks the shape of GET /api/v1/agents.
// The dashboard and macOS Swift app both consume this endpoint to map an
// adapter name (e.g. "claude-code") to its display name and icons. Any drift
// in field names, ordering of keys inside each entry, or the JSON envelope
// shape is a regression.
//
// Refresh the golden with:
//
//	UPDATE_CONTRACT_GOLDENS=1 go test ./core/cmd/irrlichd/...
//
// The golden captures the *content* of every adapter entry (name +
// display_name + icons), which lets the test catch silent changes to any
// adapter's branding. testAgentCfgs() at main_test.go:35 already mirrors the
// production agentCfgs slice, so adding a new adapter requires regenerating
// this golden as a deliberate, reviewed change.
func TestContract_AgentsEndpoint(t *testing.T) {
	srv, _ := newTestStack(t)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/agents")
	if err != nil {
		t.Fatalf("GET /api/v1/agents: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d, want 200", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}

	// Re-indent the wire bytes for golden readability. The handler uses
	// json.NewEncoder which emits compact JSON; the contract being locked
	// is the shape, not the formatting.
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, body, "", "  "); err != nil {
		t.Fatalf("indent response: %v\nraw: %s", err, body)
	}

	contracttesting.CompareGolden(t, pretty.Bytes(), filepath.Join("testdata", "agents_endpoint.golden.json"))
}
