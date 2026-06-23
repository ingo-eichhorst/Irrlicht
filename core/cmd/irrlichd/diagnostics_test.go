package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestLocalhostOnlyGuardsDebugBundle pins the security boundary the diagnostics
// bundle (#736) relies on: a request from a non-loopback address is rejected
// before the wrapped handler runs, while a loopback request passes through. A
// real TCP connection to 127.0.0.1 can never exercise the off-loopback branch,
// so this forges RemoteAddr directly.
func TestLocalhostOnlyGuardsDebugBundle(t *testing.T) {
	for _, tc := range []struct {
		name       string
		remoteAddr string
		wantStatus int
		wantCalled bool
	}{
		{"off-loopback rejected", "203.0.113.5:54321", http.StatusForbidden, false},
		{"loopback allowed", "127.0.0.1:54321", http.StatusOK, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			h := localhostOnly(func(w http.ResponseWriter, r *http.Request) {
				called = true
				w.WriteHeader(http.StatusOK)
			})
			req := httptest.NewRequest(http.MethodGet, "/debug/bundle", nil)
			req.RemoteAddr = tc.remoteAddr
			rec := httptest.NewRecorder()
			h(rec, req)
			if rec.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			if called != tc.wantCalled {
				t.Errorf("handler called = %v, want %v", called, tc.wantCalled)
			}
		})
	}
}
