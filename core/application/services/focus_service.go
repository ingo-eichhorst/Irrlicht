package services

import (
	"errors"

	"irrlicht/core/ports/outbound"
)

// Sentinel errors returned by FocusService.RequestFocus. Callers (e.g. the
// HTTP handler) use errors.Is to map them to appropriate status codes.
var (
	ErrSessionNotFound = errors.New("session not found")
	ErrNoLauncher      = errors.New("session has no launcher information")
)

// FocusService handles requests to bring a session's host terminal/IDE window
// to the foreground. It resolves the session from the repository, validates
// that host-identity information is present, then broadcasts a
// PushTypeFocusRequested message over the WebSocket hub. The Swift app listens
// on that hub and calls SessionLauncher.jump when it receives the message.
type FocusService struct {
	repo   outbound.SessionRepository
	push   outbound.PushBroadcaster
	logger outbound.Logger
}

// NewFocusService constructs a FocusService.
func NewFocusService(repo outbound.SessionRepository, push outbound.PushBroadcaster, logger outbound.Logger) *FocusService {
	return &FocusService{repo: repo, push: push, logger: logger}
}

// RequestFocus looks up sessionID, checks that the session has launcher data,
// and broadcasts a focus_requested push message. Returns an error that callers
// can map to an HTTP status code.
func (s *FocusService) RequestFocus(sessionID string) error {
	state, err := s.repo.Load(sessionID)
	if err != nil {
		return ErrSessionNotFound
	}
	if state.Launcher == nil {
		return ErrNoLauncher
	}
	s.push.Broadcast(outbound.PushMessage{
		Type:    outbound.PushTypeFocusRequested,
		Session: state,
	})
	s.logger.LogInfo("focus", sessionID, "focus requested")
	return nil
}
