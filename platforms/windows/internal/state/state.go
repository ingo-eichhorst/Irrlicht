//go:build windows

// Package state subscribes to the daemon's session stream and computes
// the aggregate tray state (working / waiting / ready). Mirrors the
// macOS MenuBarController precedence: any session working → working;
// otherwise any waiting → waiting; otherwise → ready.
package state

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"irrlicht/platforms/windows/internal/daemon"
)

// Tray represents the aggregate icon state. Names match the daemon's
// session.SessionState string values so we never need a translation
// layer.
type Tray string

const (
	TrayReady   Tray = "ready"
	TrayWaiting Tray = "waiting"
	TrayWorking Tray = "working"
	TrayOffline Tray = "offline" // daemon unreachable
)

// Snapshot is the externally observable state at a point in time.
type Snapshot struct {
	Tray     Tray
	Counts   map[Tray]int
	Total    int
	UpdatedAt time.Time
}

// Subscriber connects to the daemon's WebSocket stream and broadcasts
// debounced Snapshot updates to listeners.
type Subscriber struct {
	mu        sync.Mutex
	sessions  map[string]Tray // sessionID → state
	listeners []func(Snapshot)
	last      Snapshot
}

// New returns a Subscriber with no listeners.
func New() *Subscriber {
	return &Subscriber{
		sessions: make(map[string]Tray),
		last: Snapshot{
			Tray:   TrayOffline,
			Counts: map[Tray]int{},
		},
	}
}

// Subscribe registers a listener that will be called (on any goroutine)
// whenever the aggregate state changes.
func (s *Subscriber) Subscribe(fn func(Snapshot)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.listeners = append(s.listeners, fn)
	// Fire once with the current snapshot so listeners can render
	// immediately rather than waiting for the next event.
	fn(s.last)
}

// Run connects to the daemon's session stream and processes events until
// ctx is cancelled. Reconnects on disconnect with a short backoff. Also
// periodically fetches /api/v1/sessions for full hydration in case any
// deltas were missed.
func (s *Subscriber) Run(ctx context.Context) {
	go s.hydrateLoop(ctx)
	for {
		if err := ctx.Err(); err != nil {
			return
		}
		s.runOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}

// runOnce establishes a single WebSocket connection and pumps messages
// until the connection drops or ctx is cancelled.
func (s *Subscriber) runOnce(ctx context.Context) {
	url := "ws://" + daemon.DaemonAddr + "/api/v1/sessions/stream"
	dialer := websocket.Dialer{HandshakeTimeout: 2 * time.Second}
	conn, _, err := dialer.DialContext(ctx, url, nil)
	if err != nil {
		s.markOffline()
		return
	}
	defer conn.Close()

	// Hydrate once on connect so we don't miss live sessions whose
	// updates arrived before we subscribed.
	s.hydrate(ctx)

	for {
		if ctx.Err() != nil {
			return
		}
		_ = conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		_, msg, err := conn.ReadMessage()
		if err != nil {
			s.markOffline()
			return
		}
		s.applyDelta(msg)
	}
}

// hydrateLoop runs a 30s tick that pulls /api/v1/sessions for full state.
// Mirrors macOS SessionManager.swift:33 — covers any fields not carried
// by the WebSocket deltas.
func (s *Subscriber) hydrateLoop(ctx context.Context) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.hydrate(ctx)
		}
	}
}

// sessionDTO captures the subset of /api/v1/sessions fields we care about.
type sessionDTO struct {
	SessionID string `json:"sessionId"`
	State     Tray   `json:"state"`
}

// hydrate replaces the session map from the daemon's REST endpoint.
func (s *Subscriber) hydrate(ctx context.Context) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		daemon.DaemonURL("/api/v1/sessions"), nil)
	if err != nil {
		return
	}
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		s.markOffline()
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return
	}
	var rows []sessionDTO
	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
		log.Printf("state: decode /api/v1/sessions: %v", err)
		return
	}
	s.mu.Lock()
	s.sessions = make(map[string]Tray, len(rows))
	for _, r := range rows {
		s.sessions[r.SessionID] = r.State
	}
	s.mu.Unlock()
	s.recompute()
}

// applyDelta processes one WebSocket frame. The daemon sends both
// session-level updates (with sessionId and state fields) and orchestrator
// envelopes; we ignore the latter for tray purposes.
func (s *Subscriber) applyDelta(msg []byte) {
	var env struct {
		Type      string `json:"type"`
		SessionID string `json:"sessionId"`
		State     Tray   `json:"state"`
	}
	if err := json.Unmarshal(msg, &env); err != nil {
		return
	}
	if env.SessionID == "" {
		return
	}
	s.mu.Lock()
	switch env.Type {
	case "session_deleted":
		delete(s.sessions, env.SessionID)
	default:
		if env.State != "" {
			s.sessions[env.SessionID] = env.State
		}
	}
	s.mu.Unlock()
	s.recompute()
}

// recompute regenerates the snapshot and notifies listeners if it changed.
func (s *Subscriber) recompute() {
	s.mu.Lock()
	counts := map[Tray]int{TrayReady: 0, TrayWaiting: 0, TrayWorking: 0}
	for _, st := range s.sessions {
		counts[st]++
	}
	tray := TrayReady
	switch {
	case counts[TrayWorking] > 0:
		tray = TrayWorking
	case counts[TrayWaiting] > 0:
		tray = TrayWaiting
	}
	snap := Snapshot{
		Tray:      tray,
		Counts:    counts,
		Total:     len(s.sessions),
		UpdatedAt: time.Now(),
	}
	if snap.Tray == s.last.Tray && snap.Total == s.last.Total &&
		counts[TrayWorking] == s.last.Counts[TrayWorking] &&
		counts[TrayWaiting] == s.last.Counts[TrayWaiting] &&
		counts[TrayReady] == s.last.Counts[TrayReady] {
		s.mu.Unlock()
		return
	}
	s.last = snap
	listeners := append([]func(Snapshot){}, s.listeners...)
	s.mu.Unlock()
	for _, fn := range listeners {
		fn(snap)
	}
}

// markOffline transitions to the offline state when the daemon goes away.
func (s *Subscriber) markOffline() {
	s.mu.Lock()
	if s.last.Tray == TrayOffline {
		s.mu.Unlock()
		return
	}
	snap := Snapshot{
		Tray:      TrayOffline,
		Counts:    map[Tray]int{},
		Total:     0,
		UpdatedAt: time.Now(),
	}
	s.last = snap
	listeners := append([]func(Snapshot){}, s.listeners...)
	s.mu.Unlock()
	for _, fn := range listeners {
		fn(snap)
	}
}
