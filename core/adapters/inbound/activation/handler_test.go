package activation

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	services "irrlicht/core/application/services"
)

type fakeTarget struct {
	state      services.ActivationState
	enableErr  error
	disableErr error
}

func (f *fakeTarget) Status() services.ActivationState { return f.state }
func (f *fakeTarget) Enable() (services.ActivationState, error) {
	if f.enableErr != nil {
		return f.state, f.enableErr
	}
	f.state.TaskEtaEnabled = true
	return f.state, nil
}
func (f *fakeTarget) Disable() (services.ActivationState, error) {
	if f.disableErr != nil {
		return f.state, f.disableErr
	}
	f.state.TaskEtaEnabled = false
	return f.state, nil
}

type nopLogger struct{}

func (nopLogger) LogInfo(_, _, _ string)                                  {}
func (nopLogger) LogError(_, _, _ string)                                 {}
func (nopLogger) LogProcessingTime(_, _ string, _ int64, _ int, _ string) {}
func (nopLogger) Close() error                                            { return nil }

func do(t *testing.T, target *fakeTarget, method string) *httptest.ResponseRecorder {
	t.Helper()
	h := NewHandler(target, nopLogger{})
	req := httptest.NewRequest(method, "/api/v1/activation/task-eta", nil)
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

func decodeState(t *testing.T, rec *httptest.ResponseRecorder) services.ActivationState {
	t.Helper()
	var state services.ActivationState
	if err := json.NewDecoder(rec.Body).Decode(&state); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return state
}

func TestHandler_GetReturnsState(t *testing.T) {
	rec := do(t, &fakeTarget{state: services.ActivationState{TaskEtaEnabled: true}}, http.MethodGet)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
	if !decodeState(t, rec).TaskEtaEnabled {
		t.Error("expected enabled state")
	}
}

func TestHandler_PostEnables(t *testing.T) {
	target := &fakeTarget{}
	rec := do(t, target, http.MethodPost)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
	if !decodeState(t, rec).TaskEtaEnabled || !target.state.TaskEtaEnabled {
		t.Error("POST should enable")
	}
}

func TestHandler_DeleteDisables(t *testing.T) {
	target := &fakeTarget{state: services.ActivationState{TaskEtaEnabled: true}}
	rec := do(t, target, http.MethodDelete)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
	if decodeState(t, rec).TaskEtaEnabled || target.state.TaskEtaEnabled {
		t.Error("DELETE should disable")
	}
}

func TestHandler_MethodNotAllowed(t *testing.T) {
	for _, m := range []string{http.MethodPut, http.MethodPatch} {
		if rec := do(t, &fakeTarget{}, m); rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s: code = %d, want 405", m, rec.Code)
		}
	}
}

func TestHandler_EnableErrorIs500(t *testing.T) {
	rec := do(t, &fakeTarget{enableErr: errors.New("boom")}, http.MethodPost)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("code = %d, want 500", rec.Code)
	}
}

func TestHandler_RejectsCrossSiteMutations(t *testing.T) {
	h := NewHandler(&fakeTarget{}, nopLogger{})
	for _, m := range []string{http.MethodPost, http.MethodDelete} {
		for _, site := range []string{"cross-site", "same-site"} {
			req := httptest.NewRequest(m, "/api/v1/activation/task-eta", nil)
			req.Header.Set("Sec-Fetch-Site", site)
			rec := httptest.NewRecorder()
			h(rec, req)
			if rec.Code != http.StatusForbidden {
				t.Errorf("%s with Sec-Fetch-Site=%s: code = %d, want 403", m, site, rec.Code)
			}
		}
	}
	// same-origin (the dashboard) and a missing header (native client) pass.
	for _, site := range []string{"same-origin", "none", ""} {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/activation/task-eta", nil)
		if site != "" {
			req.Header.Set("Sec-Fetch-Site", site)
		}
		rec := httptest.NewRecorder()
		h(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("POST with Sec-Fetch-Site=%q: code = %d, want 200", site, rec.Code)
		}
	}
}
