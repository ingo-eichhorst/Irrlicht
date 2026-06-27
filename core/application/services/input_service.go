package services

import (
	"errors"

	"irrlicht/core/domain/session"
	"irrlicht/core/ports/outbound"
)

// Sentinel errors returned by InputService. Callers (the HTTP handler and the
// relay forwarder) use errors.Is to map them to status codes. ErrSessionNotFound
// is shared with FocusService (focus_service.go).
var (
	// ErrControlDisabled means the backchannel master-toggle is off or the
	// per-adapter "control" consent has not been granted. Maps to 403.
	ErrControlDisabled = errors.New("session control is disabled")
	// ErrNotControllable means the session has no usable terminal-backend
	// target (no live tmux pane / kitty window / addressable terminal).
	// Maps to 409.
	ErrNotControllable = errors.New("session is not controllable")
)

// consentGate is the slice of *PermissionService that InputService needs: a
// single hot-path consent check. Declared here (mirroring the focus handler's
// focusTarget pattern) so the service does not import the permission handler.
type consentGate interface {
	Granted(agentName, key string) bool
}

// ControlPermissionKey is the per-adapter permission that gates input
// injection (issue #724). KindModify, default pending⇒denied (#570).
const ControlPermissionKey = "control"

// InputService is the write counterpart to FocusService: it forwards typed
// input and interrupts into a discovered agent session through the session's
// terminal backend (via the AgentController port). Every write passes three
// gates in order — the backchannel master-toggle, the per-adapter "control"
// consent, and backend controllability — so neither a disabled backchannel nor
// a revoked grant can ever reach an agent.
type InputService struct {
	repo       outbound.SessionRepository
	controller outbound.AgentController
	consent    consentGate
	betaOn     func() bool
	logger     outbound.Logger
}

// NewInputService constructs an InputService. betaOn reports whether the
// backchannel master-toggle is currently enabled (default false).
func NewInputService(repo outbound.SessionRepository, controller outbound.AgentController, consent consentGate, betaOn func() bool, logger outbound.Logger) *InputService {
	return &InputService{repo: repo, controller: controller, consent: consent, betaOn: betaOn, logger: logger}
}

// SendInput injects data into the session's terminal. The gate order is the
// whole point: master-toggle → consent → controllability → delegate.
func (s *InputService) SendInput(sessionID string, data []byte) error {
	state, err := s.resolve(sessionID)
	if err != nil {
		return err
	}
	if err := s.controller.SendInput(sessionID, data); err != nil {
		s.logger.LogError("control", sessionID, err.Error())
		return err
	}
	s.logger.LogInfo("control", sessionID, "input forwarded ("+state.Adapter+")")
	return nil
}

// SendCommand forwards an agent-agnostic preset command to the session, passing
// the same gates as SendInput. The command's submit sequence is owned by the
// controller per terminal backend (issue #754).
func (s *InputService) SendCommand(sessionID string, command string) error {
	state, err := s.resolve(sessionID)
	if err != nil {
		return err
	}
	if err := s.controller.SendCommand(sessionID, command); err != nil {
		s.logger.LogError("control", sessionID, err.Error())
		return err
	}
	s.logger.LogInfo("control", sessionID, "command forwarded ("+state.Adapter+")")
	return nil
}

// Interrupt delivers an interrupt to the session, passing the same gates.
func (s *InputService) Interrupt(sessionID string) error {
	state, err := s.resolve(sessionID)
	if err != nil {
		return err
	}
	if err := s.controller.Interrupt(sessionID); err != nil {
		s.logger.LogError("control", sessionID, err.Error())
		return err
	}
	s.logger.LogInfo("control", sessionID, "interrupt forwarded ("+state.Adapter+")")
	return nil
}

// Controllable reports whether an input/interrupt for the session would be
// accepted right now — the same gate order without performing a write. The
// session payload projects this so the UI only offers the affordance when a
// write would actually succeed.
func (s *InputService) Controllable(sessionID string) bool {
	_, err := s.resolve(sessionID)
	return err == nil
}

// resolve runs the shared gate chain and returns the loaded session on success.
func (s *InputService) resolve(sessionID string) (*session.SessionState, error) {
	if !s.betaOn() {
		return nil, ErrControlDisabled
	}
	state, err := s.repo.Load(sessionID)
	if err != nil {
		return nil, ErrSessionNotFound
	}
	if !s.consent.Granted(state.Adapter, ControlPermissionKey) {
		return nil, ErrControlDisabled
	}
	if !s.controller.Controllable(sessionID) {
		return nil, ErrNotControllable
	}
	return state, nil
}
