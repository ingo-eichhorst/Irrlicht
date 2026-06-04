package activation

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	services "irrlicht/core/application/services"
)

const (
	testAgent = "claude-code"
	testPerm  = "instructions"
)

// fakeConsent implements consentTarget over a single agent/permission pair,
// recording the answers the handler submits.
type fakeConsent struct {
	enabled   bool
	answerErr error
	answers   []services.PermissionAnswer
}

func (f *fakeConsent) Granted(agentName, key string) bool {
	return agentName == testAgent && key == testPerm && f.enabled
}

func (f *fakeConsent) Answer(answers []services.PermissionAnswer) error {
	if f.answerErr != nil {
		return f.answerErr
	}
	f.answers = append(f.answers, answers...)
	for _, a := range answers {
		if a.Agent == testAgent && a.Permission == testPerm {
			f.enabled = a.Grant
		}
	}
	return nil
}

type nopLogger struct{}

func (nopLogger) LogInfo(_, _, _ string)                                  {}
func (nopLogger) LogError(_, _, _ string)                                 {}
func (nopLogger) LogProcessingTime(_, _ string, _ int64, _ int, _ string) {}
func (nopLogger) Close() error                                            { return nil }

func do(t *testing.T, target *fakeConsent, method string) *httptest.ResponseRecorder {
	t.Helper()
	h := NewHandler(target, testAgent, testPerm, nopLogger{})
	req := httptest.NewRequest(method, "/api/v1/activation/task-eta", nil)
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

func decodeState(t *testing.T, rec *httptest.ResponseRecorder) state {
	t.Helper()
	var s state
	if err := json.NewDecoder(rec.Body).Decode(&s); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return s
}

func TestHandler_GetReturnsState(t *testing.T) {
	rec := do(t, &fakeConsent{enabled: true}, http.MethodGet)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
	if !decodeState(t, rec).TaskEtaEnabled {
		t.Error("expected enabled state")
	}
}

func TestHandler_PostGrants(t *testing.T) {
	target := &fakeConsent{}
	rec := do(t, target, http.MethodPost)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
	if !decodeState(t, rec).TaskEtaEnabled || !target.enabled {
		t.Error("POST should grant")
	}
	want := services.PermissionAnswer{Agent: testAgent, Permission: testPerm, Grant: true}
	if len(target.answers) != 1 || target.answers[0] != want {
		t.Errorf("answers = %+v, want [%+v]", target.answers, want)
	}
}

func TestHandler_DeleteRevokes(t *testing.T) {
	target := &fakeConsent{enabled: true}
	rec := do(t, target, http.MethodDelete)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
	if decodeState(t, rec).TaskEtaEnabled || target.enabled {
		t.Error("DELETE should revoke")
	}
	want := services.PermissionAnswer{Agent: testAgent, Permission: testPerm, Grant: false}
	if len(target.answers) != 1 || target.answers[0] != want {
		t.Errorf("answers = %+v, want [%+v]", target.answers, want)
	}
}

func TestHandler_MethodNotAllowed(t *testing.T) {
	for _, m := range []string{http.MethodPut, http.MethodPatch} {
		if rec := do(t, &fakeConsent{}, m); rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s: code = %d, want 405", m, rec.Code)
		}
	}
}

func TestHandler_AnswerErrorIs500(t *testing.T) {
	rec := do(t, &fakeConsent{answerErr: errors.New("boom")}, http.MethodPost)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("code = %d, want 500", rec.Code)
	}
}

func TestHandler_RejectsCrossSiteMutations(t *testing.T) {
	h := NewHandler(&fakeConsent{}, testAgent, testPerm, nopLogger{})
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
