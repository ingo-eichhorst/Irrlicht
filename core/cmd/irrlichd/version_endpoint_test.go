package main

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// TestVersionEndpoint locks the shape of GET /api/v1/version. The web
// dashboard's app-header reads this to render `Irrlicht v$VERSION` without
// baking the value into its own bundle, so the contract is:
//
//   - 200 OK
//   - Content-Type: application/json
//   - body: {"version": "..."} with the value passed to handleGetVersion
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

// TestVersionEndpoint_EmptyVersion ensures the handler still returns valid
// JSON when the build flag wasn't set (Version stays as the zero value).
// Frontends should treat an empty string the same as "unknown" rather than
// crashing on a missing `version` field.
func TestVersionEndpoint_EmptyVersion(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/version", handleGetVersion(""))

	rr := newRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/api/v1/version", nil)
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", rr.Code)
	}
	var payload struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(rr.Body, &payload); err != nil {
		t.Fatalf("unmarshal %q: %v", rr.Body, err)
	}
	if payload.Version != "" {
		t.Fatalf("version: got %q, want empty", payload.Version)
	}
}

// tiny in-memory response recorder so the empty-version test doesn't have
// to spin up the full newTestStack.
type responseRecorder struct {
	Code   int
	Hdr    http.Header
	Body   []byte
	wroteH bool
}

func newRecorder() *responseRecorder { return &responseRecorder{Hdr: http.Header{}, Code: http.StatusOK} }
func (r *responseRecorder) Header() http.Header { return r.Hdr }
func (r *responseRecorder) WriteHeader(c int)   { r.Code = c; r.wroteH = true }
func (r *responseRecorder) Write(b []byte) (int, error) {
	if !r.wroteH {
		r.wroteH = true
	}
	r.Body = append(r.Body, b...)
	return len(b), nil
}
