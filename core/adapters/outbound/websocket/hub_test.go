package websocket

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"

	"irrlicht/core/application/services"
)

func TestLoopbackCheckOrigin(t *testing.T) {
	tests := []struct {
		name       string
		origin     string
		remoteAddr string
		want       bool
	}{
		{"no origin from loopback", "", "127.0.0.1:54321", true},
		{"localhost origin", "http://localhost:5173", "127.0.0.1:54321", true},
		{"127.0.0.1 origin", "http://127.0.0.1:5173", "127.0.0.1:54321", true},
		{"127.0.0.2 origin", "http://127.0.0.2", "127.0.0.1:54321", true},
		{"ipv6 loopback origin", "http://[::1]:5173", "127.0.0.1:54321", true},
		{"foreign origin", "https://evil.example.com", "127.0.0.1:54321", false},
		{"foreign origin bare host", "http://example.com", "127.0.0.1:54321", false},
		{"malformed origin", "://bad", "127.0.0.1:54321", false},
		{"origin with no host", "http://", "127.0.0.1:54321", false},
		{"non-loopback remote addr", "http://localhost", "10.0.0.5:54321", false},
		{"unix socket remote addr", "", "@", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/", nil)
			r.RemoteAddr = tt.remoteAddr
			if tt.origin != "" {
				r.Header.Set("Origin", tt.origin)
			}
			if got := loopbackCheckOrigin(r); got != tt.want {
				t.Errorf("loopbackCheckOrigin(origin=%q remote=%q) = %v, want %v",
					tt.origin, tt.remoteAddr, got, tt.want)
			}
		})
	}
}

// TestServeWS_OriginHandshake exercises the full HTTP upgrade with a custom
// Origin header to confirm the upgrader rejects cross-site handshakes.
func TestServeWS_OriginHandshake(t *testing.T) {
	hub := NewHub(services.NewPushService())
	srv := httptest.NewServer(http.HandlerFunc(hub.ServeWS))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")

	// Accepted: loopback Origin.
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, http.Header{
		"Origin": []string{"http://localhost:5173"},
	})
	if err != nil {
		t.Fatalf("loopback origin: dial failed: %v", err)
	}
	conn.Close()

	// Rejected: foreign Origin.
	_, resp, err := websocket.DefaultDialer.Dial(wsURL, http.Header{
		"Origin": []string{"https://evil.example.com"},
	})
	if err == nil {
		t.Fatal("expected dial to fail for foreign origin")
	}
	if resp == nil {
		t.Fatalf("expected HTTP response on handshake rejection, got nil (err=%v)", err)
	}
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("foreign origin: got status %d, want %d", resp.StatusCode, http.StatusForbidden)
	}
}
