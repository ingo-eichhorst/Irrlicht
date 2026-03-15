package http

import (
	"encoding/json"
	"net/http"
	"time"

	"irrlicht/core/domain/event"
	"irrlicht/core/domain/session"
	"irrlicht/core/ports/inbound"
	"irrlicht/core/ports/outbound"
)

// Handler exposes EventHandler and SessionRepository over HTTP.
type Handler struct {
	eventHandler inbound.EventHandler
	repo         outbound.SessionRepository
}

// NewHandler creates a Handler with the provided dependencies.
func NewHandler(eventHandler inbound.EventHandler, repo outbound.SessionRepository) *Handler {
	return &Handler{eventHandler: eventHandler, repo: repo}
}

// RegisterRoutes registers POST /api/v1/events, GET /api/v1/sessions, and GET /state on mux.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/events", h.handlePostEvent)
	mux.HandleFunc("GET /api/v1/sessions", h.handleGetSessions)
	mux.HandleFunc("GET /state", h.handleGetState)
}

func (h *Handler) handlePostEvent(w http.ResponseWriter, r *http.Request) {
	var evt event.HookEvent
	if err := json.NewDecoder(r.Body).Decode(&evt); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := h.eventHandler.HandleEvent(&evt); err != nil {
		http.Error(w, "processing error: "+err.Error(), http.StatusUnprocessableEntity)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) handleGetSessions(w http.ResponseWriter, r *http.Request) {
	sessions, err := h.repo.ListAll()
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if len(sessions) == 0 {
		w.Write([]byte("[]"))
		return
	}
	json.NewEncoder(w).Encode(sessions)
}

// handleGetState returns a compact debug-friendly state dump for agent verification.
// Mirrors the format written to ~/.irrlicht/debug-state.json when IRRLICHT_DEBUG=1.
func (h *Handler) handleGetState(w http.ResponseWriter, r *http.Request) {
	sessions, err := h.repo.ListAll()
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	type sessionEntry struct {
		ID                 string  `json:"id"`
		ProjectName        string  `json:"projectName,omitempty"`
		State              string  `json:"state"`
		Model              string  `json:"model,omitempty"`
		ContextUtilization float64 `json:"contextUtilization"`
		TotalTokens        int64   `json:"totalTokens"`
	}

	type stateResponse struct {
		Sessions     []sessionEntry `json:"sessions"`
		SessionCount int            `json:"sessionCount"`
		WorkingCount int            `json:"workingCount"`
		WaitingCount int            `json:"waitingCount"`
		ReadyCount   int            `json:"readyCount"`
		LastUpdated  string         `json:"lastUpdated"`
	}

	entries := make([]sessionEntry, 0, len(sessions))
	var workingCount, waitingCount, readyCount int
	for _, s := range sessions {
		var ctxUtil float64
		var totalTokens int64
		if s.Metrics != nil {
			ctxUtil = s.Metrics.ContextUtilization
			totalTokens = s.Metrics.TotalTokens
		}
		model := s.Model
		if s.Metrics != nil && s.Metrics.ModelName != "" && s.Metrics.ModelName != "unknown" {
			model = s.Metrics.ModelName
		}
		entries = append(entries, sessionEntry{
			ID:                 s.SessionID,
			ProjectName:        s.ProjectName,
			State:              s.State,
			Model:              model,
			ContextUtilization: ctxUtil,
			TotalTokens:        totalTokens,
		})
		switch s.State {
		case session.StateWorking:
			workingCount++
		case session.StateWaiting:
			waitingCount++
		case session.StateReady:
			readyCount++
		}
	}

	resp := stateResponse{
		Sessions:     entries,
		SessionCount: len(sessions),
		WorkingCount: workingCount,
		WaitingCount: waitingCount,
		ReadyCount:   readyCount,
		LastUpdated:  time.Now().UTC().Format(time.RFC3339),
	}

	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(resp)
}
