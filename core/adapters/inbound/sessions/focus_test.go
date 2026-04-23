package sessions_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	sessions "irrlicht/core/adapters/inbound/sessions"
	services "irrlicht/core/application/services"
	"irrlicht/core/ports/outbound"
)

// stubLogger satisfies outbound.Logger without writing anything.
type stubLogger struct{}

func (stubLogger) LogInfo(_, _, _ string)                          {}
func (stubLogger) LogError(_, _, _ string)                         {}
func (stubLogger) LogProcessingTime(_, _ string, _ int64, _ int, _ string) {}
func (stubLogger) Close() error                                    { return nil }

// stubTarget implements FocusTarget.
type stubTarget struct {
	err error
}

func (s *stubTarget) RequestFocus(_ string) error { return s.err }

func TestFocusHandler_OK(t *testing.T) {
	h := sessions.NewFocusHandler(&stubTarget{}, stubLogger{})
	r := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/abc/focus", nil)
	r.SetPathValue("id", "abc")
	w := httptest.NewRecorder()
	h(w, r)
	if w.Code != http.StatusOK {
		t.Errorf("want 200, got %d", w.Code)
	}
}

func TestFocusHandler_NotFound(t *testing.T) {
	h := sessions.NewFocusHandler(&stubTarget{err: services.ErrSessionNotFound}, stubLogger{})
	r := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/missing/focus", nil)
	r.SetPathValue("id", "missing")
	w := httptest.NewRecorder()
	h(w, r)
	if w.Code != http.StatusNotFound {
		t.Errorf("want 404, got %d", w.Code)
	}
}

func TestFocusHandler_NoLauncher(t *testing.T) {
	h := sessions.NewFocusHandler(&stubTarget{err: services.ErrNoLauncher}, stubLogger{})
	r := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/abc/focus", nil)
	r.SetPathValue("id", "abc")
	w := httptest.NewRecorder()
	h(w, r)
	if w.Code != http.StatusUnprocessableEntity {
		t.Errorf("want 422, got %d", w.Code)
	}
}

func TestFocusHandler_MethodNotAllowed(t *testing.T) {
	h := sessions.NewFocusHandler(&stubTarget{}, stubLogger{})
	r := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/abc/focus", nil)
	r.SetPathValue("id", "abc")
	w := httptest.NewRecorder()
	h(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("want 405, got %d", w.Code)
	}
}

// Compile-time check that stubLogger satisfies the port.
var _ outbound.Logger = stubLogger{}
