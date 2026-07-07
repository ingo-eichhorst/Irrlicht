package services_test

import (
	"errors"
	"testing"

	"irrlicht/core/application/services"
	"irrlicht/core/domain/permission"
	"irrlicht/core/domain/session"
	"irrlicht/core/internal/contracttesting"
)

// fakeController records what InputService delegates to the AgentController.
type fakeController struct {
	controllable bool
	sentData     []byte
	sentCommand  string
	interrupted  bool
	sendErr      error
}

func (f *fakeController) SendInput(_ string, data []byte) error {
	f.sentData = data
	return f.sendErr
}
func (f *fakeController) SendCommand(_, command string) error {
	f.sentCommand = command
	return f.sendErr
}
func (f *fakeController) Interrupt(_ string) error   { f.interrupted = true; return nil }
func (f *fakeController) Controllable(_ string) bool { return f.controllable }

type fakeConsent struct{ granted bool }

func (f fakeConsent) Granted(_, _ string) bool { return f.granted }

// mutableConsent is a consentGranter whose answer can change between calls —
// needed to drive a single InputService instance through all three
// consent states for contracttesting.AssertPermissionGated.
type mutableConsent struct{ granted bool }

func (c *mutableConsent) Granted(_, _ string) bool { return c.granted }

func controllableSession() *session.SessionState {
	return &session.SessionState{SessionID: "abc", Adapter: "claude-code"}
}

// newInput builds an InputService with the given gate inputs. All "open"
// unless overridden by the individual test.
func newInput(betaOn, consent, controllable bool, ctrl *fakeController) *services.InputService {
	if ctrl == nil {
		ctrl = &fakeController{controllable: controllable}
	}
	return services.NewInputService(
		&stubRepo{state: controllableSession()},
		ctrl,
		fakeConsent{granted: consent},
		func() bool { return betaOn },
		stubLog{},
	)
}

func TestSendInput_BetaOff(t *testing.T) {
	svc := newInput(false, true, true, nil)
	if err := svc.SendInput("abc", []byte("x")); !errors.Is(err, services.ErrControlDisabled) {
		t.Errorf("beta off: want ErrControlDisabled, got %v", err)
	}
}

func TestSendInput_ConsentDenied(t *testing.T) {
	svc := newInput(true, false, true, nil)
	if err := svc.SendInput("abc", []byte("x")); !errors.Is(err, services.ErrControlDisabled) {
		t.Errorf("consent denied: want ErrControlDisabled, got %v", err)
	}
}

func TestSendInput_SessionNotFound(t *testing.T) {
	svc := services.NewInputService(
		&stubRepo{err: errors.New("nope")},
		&fakeController{controllable: true},
		fakeConsent{granted: true},
		func() bool { return true },
		stubLog{},
	)
	if err := svc.SendInput("abc", []byte("x")); !errors.Is(err, services.ErrSessionNotFound) {
		t.Errorf("missing session: want ErrSessionNotFound, got %v", err)
	}
}

func TestSendInput_NotControllable(t *testing.T) {
	svc := newInput(true, true, false, nil)
	if err := svc.SendInput("abc", []byte("x")); !errors.Is(err, services.ErrNotControllable) {
		t.Errorf("no backend target: want ErrNotControllable, got %v", err)
	}
}

func TestSendInput_Delegates(t *testing.T) {
	ctrl := &fakeController{controllable: true}
	svc := newInput(true, true, true, ctrl)
	if err := svc.SendInput("abc", []byte("hello\r")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(ctrl.sentData) != "hello\r" {
		t.Errorf("controller got %q, want %q", ctrl.sentData, "hello\r")
	}
}

func TestInterrupt_Delegates(t *testing.T) {
	ctrl := &fakeController{controllable: true}
	svc := newInput(true, true, true, ctrl)
	if err := svc.Interrupt("abc"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ctrl.interrupted {
		t.Error("expected Interrupt to be delegated to the controller")
	}
}

func TestControllable_ReflectsGate(t *testing.T) {
	if newInput(false, true, true, nil).Controllable("abc") {
		t.Error("beta off: want not controllable")
	}
	if newInput(true, false, true, nil).Controllable("abc") {
		t.Error("consent denied: want not controllable")
	}
	if newInput(true, true, false, nil).Controllable("abc") {
		t.Error("no backend target: want not controllable")
	}
	if !newInput(true, true, true, nil).Controllable("abc") {
		t.Error("all gates open: want controllable")
	}
}

// TestSendInput_PermissionGateContract wires the backchannel's live
// per-call ConsentGranter check (input_service.go's resolve, the exact
// "backchannel write path" issue #797 was filed to guard) into the shared
// three-state contract: forwarding must be a no-op while the "control"
// permission is pending or denied, must delegate to the controller once
// granted, and must stop again once revoked.
func TestSendInput_PermissionGateContract(t *testing.T) {
	consent := &mutableConsent{}
	ctrl := &fakeController{controllable: true}
	svc := services.NewInputService(&stubRepo{state: controllableSession()}, ctrl, consent, func() bool { return true }, stubLog{})

	contracttesting.AssertPermissionGated(t, contracttesting.PermissionGate{
		SetState: func(state permission.State) { consent.granted = state == permission.StateGranted },
		Exercise: func() {
			ctrl.sentData = nil
			_ = svc.SendInput("abc", []byte("x"))
		},
		Observe: func() bool { return ctrl.sentData != nil },
	})
}
