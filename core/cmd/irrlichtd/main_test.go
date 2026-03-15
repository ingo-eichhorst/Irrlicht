package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	inboundhttp "irrlicht/core/adapters/inbound/http"
	"irrlicht/core/adapters/outbound/memory"
	"irrlicht/core/adapters/outbound/security"
	wshub "irrlicht/core/adapters/outbound/websocket"
	"irrlicht/core/application/services"
	"irrlicht/core/adapters/outbound/git"
	"irrlicht/core/adapters/outbound/logging"
	"irrlicht/core/adapters/outbound/metrics"
	"irrlicht/core/domain/session"
)

// nullRepo is a no-op SessionRepository for tests.
type nullRepo struct{}

func (r *nullRepo) Load(id string) (*session.SessionState, error) { return nil, nil }
func (r *nullRepo) Save(s *session.SessionState) error           { return nil }
func (r *nullRepo) Delete(id string) error                        { return nil }
func (r *nullRepo) ListAll() ([]*session.SessionState, error)     { return nil, nil }

func newTestStack(t *testing.T) *httptest.Server {
	t.Helper()

	logger, _ := logging.New()
	t.Cleanup(func() { logger.Close() })

	fsRepo := &nullRepo{}
	memRepo := memory.New(fsRepo)
	push := services.NewPushService()
	pathValidator, _ := security.New()
	svc := services.NewEventService(memRepo, logger, git.New(), metrics.New(), pathValidator)
	svc.SetBroadcaster(push)

	mux := http.NewServeMux()
	handler := inboundhttp.NewHandler(svc, memRepo)
	handler.RegisterRoutes(mux)
	hub := wshub.NewHub(push)
	mux.HandleFunc("GET /api/v1/sessions/stream", hub.ServeWS)

	return httptest.NewServer(mux)
}

// TestGate_PostEventAndGetSessions verifies that POST /api/v1/events followed
// by GET /api/v1/sessions returns the processed session.
func TestGate_PostEventAndGetSessions(t *testing.T) {
	srv := newTestStack(t)
	defer srv.Close()

	body := `{"hook_event_name":"Stop","session_id":"gate-1"}`
	resp, err := http.Post(srv.URL+"/api/v1/events", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("POST status: got %d, want 204", resp.StatusCode)
	}

	resp, err = http.Get(srv.URL + "/api/v1/sessions")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET status: got %d, want 200", resp.StatusCode)
	}

	var sessions []*session.SessionState
	if err := json.NewDecoder(resp.Body).Decode(&sessions); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(sessions) == 0 {
		t.Fatal("expected at least one session")
	}
	found := false
	for _, s := range sessions {
		if s.SessionID == "gate-1" {
			found = true
			break
		}
	}
	if !found {
		t.Error("gate-1 session not found in GET /api/v1/sessions")
	}
}

// TestGate_WebSocketReceivesPush verifies that a WebSocket client receives a
// broadcast after POST /api/v1/events.
func TestGate_WebSocketReceivesPush(t *testing.T) {
	srv := newTestStack(t)
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/v1/sessions/stream"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("ws dial: %v", err)
	}
	defer conn.Close()

	received := make(chan []byte, 1)
	go func() {
		conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, msg, err := conn.ReadMessage()
		if err == nil {
			received <- msg
		} else {
			received <- nil
		}
	}()

	// Small delay to ensure the WS goroutine is subscribed before we POST.
	time.Sleep(20 * time.Millisecond)

	body := `{"hook_event_name":"Stop","session_id":"ws-gate-1"}`
	resp, err := http.Post(srv.URL+"/api/v1/events", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()

	select {
	case msg := <-received:
		if msg == nil {
			t.Fatal("ws: read timed out — no push received")
		}
		var envelope struct {
			Type    string              `json:"type"`
			Session session.SessionState `json:"session"`
		}
		if err := json.Unmarshal(msg, &envelope); err != nil {
			t.Fatalf("ws: decode: %v", err)
		}
		if envelope.Session.SessionID != "ws-gate-1" {
			t.Errorf("ws: session_id: got %q, want ws-gate-1", envelope.Session.SessionID)
		}
		if envelope.Type == "" {
			t.Error("ws: message type should not be empty")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ws: timed out waiting for push")
	}
}
