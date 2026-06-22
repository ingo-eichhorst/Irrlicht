package sessions

import (
	"encoding/json"
	"errors"
	"net/http"

	services "irrlicht/core/application/services"
	"irrlicht/core/ports/outbound"
)

// inputTarget is the interface the input/interrupt handlers call into.
// Satisfied by *services.InputService without importing the services package
// from the route table.
type inputTarget interface {
	SendInput(sessionID string, data []byte) error
	Interrupt(sessionID string) error
}

// inputRequest is the POST body for the input endpoint. data is a plain
// (JSON-escaped) string of bytes to inject — control characters travel as
// JSON \u escapes, e.g. {"data":"hello\r"} or {"data":""}.
type inputRequest struct {
	Data string `json:"data"`
}

// NewInputHandler returns an http.HandlerFunc that accepts
// POST /api/v1/sessions/{id}/input and forwards the body's data into the
// session's controlling terminal (issue #724, the "backchannel").
//
// Responses:
//   - 200: input forwarded
//   - 400: malformed request (no session id / bad body)
//   - 403: backchannel disabled or "control" consent not granted
//   - 404: session not found
//   - 405: method not allowed
//   - 409: session has no controllable terminal backend
func NewInputHandler(target inputTarget, log outbound.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if rejectCrossOrigin(w, r) {
			return
		}
		sessionID := r.PathValue("id")
		if sessionID == "" {
			http.Error(w, "missing session id", http.StatusBadRequest)
			return
		}
		var req inputRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if err := target.SendInput(sessionID, []byte(req.Data)); err != nil {
			writeControlError(w, log, sessionID, err)
			return
		}
		w.WriteHeader(http.StatusOK)
	}
}

// NewInterruptHandler returns an http.HandlerFunc that accepts
// POST /api/v1/sessions/{id}/interrupt and delivers an interrupt to the
// session. Same response codes as the input handler (no request body).
func NewInterruptHandler(target inputTarget, log outbound.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if rejectCrossOrigin(w, r) {
			return
		}
		sessionID := r.PathValue("id")
		if sessionID == "" {
			http.Error(w, "missing session id", http.StatusBadRequest)
			return
		}
		if err := target.Interrupt(sessionID); err != nil {
			writeControlError(w, log, sessionID, err)
			return
		}
		w.WriteHeader(http.StatusOK)
	}
}

// rejectCrossOrigin blocks cross-origin browser requests on these mutating,
// loopback-only routes. localhostOnly is not enough: a safelisted cross-origin
// POST from any webpage the user visits reaches loopback without a CORS
// preflight and could drive an agent. Browsers stamp Sec-Fetch-Site; reject
// the cross values. Non-browser clients (the macOS URLSession client, curl)
// omit the header → allowed. Mirrors activation/handler.go. Returns true when
// the request was rejected (handler should stop).
func rejectCrossOrigin(w http.ResponseWriter, r *http.Request) bool {
	if site := r.Header.Get("Sec-Fetch-Site"); site == "cross-site" || site == "same-site" {
		http.Error(w, "forbidden", http.StatusForbidden)
		return true
	}
	return false
}

// writeControlError maps InputService sentinels to status codes.
func writeControlError(w http.ResponseWriter, log outbound.Logger, sessionID string, err error) {
	log.LogError("control", sessionID, err.Error())
	switch {
	case errors.Is(err, services.ErrSessionNotFound):
		http.Error(w, err.Error(), http.StatusNotFound)
	case errors.Is(err, services.ErrControlDisabled):
		http.Error(w, err.Error(), http.StatusForbidden)
	case errors.Is(err, services.ErrNotControllable):
		http.Error(w, err.Error(), http.StatusConflict)
	default:
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}
