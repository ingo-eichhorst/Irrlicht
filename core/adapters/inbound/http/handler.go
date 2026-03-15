package http

import (
	"encoding/json"
	"net/http"

	"irrlicht/core/domain/event"
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

// RegisterRoutes registers POST /api/v1/events and GET /api/v1/sessions on mux.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/events", h.handlePostEvent)
	mux.HandleFunc("GET /api/v1/sessions", h.handleGetSessions)
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
