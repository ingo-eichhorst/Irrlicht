package services_test

import (
	"errors"
	"testing"

	"irrlicht/core/application/services"
	"irrlicht/core/domain/session"
	"irrlicht/core/ports/outbound"
)

// stubs

type stubRepo struct {
	state *session.SessionState
	err   error
}

func (r *stubRepo) Load(_ string) (*session.SessionState, error) { return r.state, r.err }
func (r *stubRepo) Save(_ *session.SessionState) error           { return nil }
func (r *stubRepo) Delete(_ string) error                        { return nil }
func (r *stubRepo) ListAll() ([]*session.SessionState, error)    { return nil, nil }

type stubPush struct{ got []outbound.PushMessage }

func (p *stubPush) Broadcast(m outbound.PushMessage)        { p.got = append(p.got, m) }
func (p *stubPush) Subscribe() chan outbound.PushMessage    { return make(chan outbound.PushMessage) }
func (p *stubPush) Unsubscribe(_ chan outbound.PushMessage) {}

type stubLog struct{}

func (stubLog) LogInfo(_, _, _ string)                                  {}
func (stubLog) LogError(_, _, _ string)                                 {}
func (stubLog) LogProcessingTime(_, _ string, _ int64, _ int, _ string) {}
func (stubLog) Close() error                                            { return nil }

// tests

func TestRequestFocus_NotFound(t *testing.T) {
	svc := services.NewFocusService(&stubRepo{err: errors.New("not found")}, &stubPush{}, stubLog{})
	err := svc.RequestFocus("missing")
	if !errors.Is(err, services.ErrSessionNotFound) {
		t.Errorf("want ErrSessionNotFound, got %v", err)
	}
}

func TestRequestFocus_NoLauncher(t *testing.T) {
	state := &session.SessionState{SessionID: "abc"}
	svc := services.NewFocusService(&stubRepo{state: state}, &stubPush{}, stubLog{})
	err := svc.RequestFocus("abc")
	if !errors.Is(err, services.ErrNoLauncher) {
		t.Errorf("want ErrNoLauncher, got %v", err)
	}
}

func TestRequestFocus_OK(t *testing.T) {
	launcher := &session.Launcher{TermProgram: "vscode"}
	state := &session.SessionState{SessionID: "abc", Launcher: launcher}
	push := &stubPush{}
	svc := services.NewFocusService(&stubRepo{state: state}, push, stubLog{})
	if err := svc.RequestFocus("abc"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(push.got) != 1 || push.got[0].Type != outbound.PushTypeFocusRequested {
		t.Errorf("expected one focus_requested broadcast, got %v", push.got)
	}
}
