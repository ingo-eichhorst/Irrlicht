package main

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"irrlicht/core/adapters/outbound/memory"
	wshub "irrlicht/core/adapters/outbound/websocket"
	"irrlicht/core/application/services"
	"irrlicht/core/domain/session"
)

// nullRepo is a no-op SessionRepository for tests.
type nullRepo struct{}

func (r *nullRepo) Load(id string) (*session.SessionState, error) { return nil, nil }
func (r *nullRepo) Save(s *session.SessionState) error           { return nil }
func (r *nullRepo) Delete(id string) error                        { return nil }
func (r *nullRepo) ListAll() ([]*session.SessionState, error)     { return nil, nil }

func newTestStack(t *testing.T) (*httptest.Server, *memory.Store) {
	t.Helper()

	fsRepo := &nullRepo{}
	memRepo := memory.New(fsRepo)
	push := services.NewPushService()

	mux := http.NewServeMux()
	registerReadRoutes(mux, memRepo)
	hub := wshub.NewHub(push)
	mux.HandleFunc("GET /api/v1/sessions/stream", hub.ServeWS)

	uiSub, _ := fs.Sub(uiFS, "ui")
	mux.Handle("/", http.FileServer(http.FS(uiSub)))

	return httptest.NewServer(mux), memRepo
}

// seedSession saves a test session into the memory store.
func seedSession(t *testing.T, repo *memory.Store, id, state string) {
	t.Helper()
	s := &session.SessionState{
		SessionID: id,
		State:     state,
		UpdatedAt: time.Now().Unix(),
	}
	if err := repo.Save(s); err != nil {
		t.Fatalf("seed session %s: %v", id, err)
	}
}

// TestGate_GetSessions verifies that GET /api/v1/sessions returns seeded sessions.
func TestGate_GetSessions(t *testing.T) {
	srv, memRepo := newTestStack(t)
	defer srv.Close()

	seedSession(t, memRepo, "gate-1", session.StateReady)

	resp, err := http.Get(srv.URL + "/api/v1/sessions")
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

// TestGate_WebSocketConnect verifies that a WebSocket client can connect to the stream endpoint.
func TestGate_WebSocketConnect(t *testing.T) {
	srv, _ := newTestStack(t)
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/v1/sessions/stream"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("ws dial: %v", err)
	}
	defer conn.Close()
}

// TestGate_GetState verifies that GET /state returns the compact debug-state format.
func TestGate_GetState(t *testing.T) {
	srv, memRepo := newTestStack(t)
	defer srv.Close()

	seedSession(t, memRepo, "state-gate-1", session.StateWorking)

	resp, err := http.Get(srv.URL + "/state")
	if err != nil {
		t.Fatalf("GET /state: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /state status: got %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("Content-Type: got %q, want application/json", ct)
	}

	var state struct {
		Sessions []struct {
			ID    string `json:"id"`
			State string `json:"state"`
		} `json:"sessions"`
		SessionCount int    `json:"sessionCount"`
		LastUpdated  string `json:"lastUpdated"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&state); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if state.LastUpdated == "" {
		t.Error("lastUpdated must not be empty")
	}
	if state.SessionCount != len(state.Sessions) {
		t.Errorf("sessionCount %d != len(sessions) %d", state.SessionCount, len(state.Sessions))
	}
	found := false
	for _, s := range state.Sessions {
		if s.ID == "state-gate-1" {
			found = true
			if s.State == "" {
				t.Error("sessions[].state must not be empty")
			}
		}
	}
	if !found {
		t.Error("state-gate-1 not found in GET /state response")
	}
}

// TestGate_UIServed verifies that GET / returns 200 with HTML content.
func TestGate_UIServed(t *testing.T) {
	srv, _ := newTestStack(t)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET / status: got %d, want 200", resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "text/html") {
		t.Errorf("Content-Type: got %q, want text/html", ct)
	}
}
