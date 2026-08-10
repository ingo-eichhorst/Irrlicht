package permissions

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	services "irrlicht/core/application/services"
)

type fakeTarget struct {
	snapshot  services.PermissionsSnapshot
	answered  [][]services.PermissionAnswer
	err       error
	reloads   int
	reloadErr error
}

func (t *fakeTarget) Snapshot() services.PermissionsSnapshot { return t.snapshot }
func (t *fakeTarget) Answer(a []services.PermissionAnswer) error {
	t.answered = append(t.answered, a)
	return t.err
}
func (t *fakeTarget) ReloadFromStore() (int, error) {
	t.reloads++
	return 0, t.reloadErr
}

type silentLogger struct{}

func (silentLogger) LogInfo(_, _, _ string)                                  {}
func (silentLogger) LogError(_, _, _ string)                                 {}
func (silentLogger) LogProcessingTime(_, _ string, _ int64, _ int, _ string) {}
func (silentLogger) Close() error                                            { return nil }

func testSnapshot() services.PermissionsSnapshot {
	return services.PermissionsSnapshot{
		Mode: "ask",
		Agents: []services.AgentPermissions{{
			Name:        "claude-code",
			DisplayName: "Claude Code",
			Detected:    true,
			Permissions: []services.PermissionView{{
				Key: "hooks", Kind: "modify", State: "pending",
				Title: "Install status hooks",
			}},
		}},
	}
}

func TestGetHandlerServesSnapshot(t *testing.T) {
	target := &fakeTarget{snapshot: testSnapshot()}
	rec := httptest.NewRecorder()
	NewGetHandler(target, silentLogger{})(rec, httptest.NewRequest(http.MethodGet, "/api/v1/permissions", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content-type = %q", ct)
	}
	var got services.PermissionsSnapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Mode != "ask" || len(got.Agents) != 1 || got.Agents[0].Name != "claude-code" ||
		!got.Agents[0].Detected || got.Agents[0].Permissions[0].State != "pending" {
		t.Fatalf("unexpected snapshot: %+v", got)
	}
}

func TestAnswerHandlerAppliesAndReturnsSnapshot(t *testing.T) {
	target := &fakeTarget{snapshot: testSnapshot()}
	body := `{"answers":[{"agent":"claude-code","permission":"hooks","grant":true}]}`
	rec := httptest.NewRecorder()
	NewAnswerHandler(target, silentLogger{})(rec, httptest.NewRequest(http.MethodPost, "/api/v1/permissions/answer", strings.NewReader(body)))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if len(target.answered) != 1 || len(target.answered[0]) != 1 {
		t.Fatalf("answered = %+v", target.answered)
	}
	ans := target.answered[0][0]
	if ans.Agent != "claude-code" || ans.Permission != "hooks" || !ans.Grant {
		t.Fatalf("answer = %+v", ans)
	}
	var got services.PermissionsSnapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Mode != "ask" {
		t.Fatalf("response is not the snapshot: %+v", got)
	}
}

func TestAnswerHandlerMalformedJSON(t *testing.T) {
	rec := httptest.NewRecorder()
	NewAnswerHandler(&fakeTarget{}, silentLogger{})(rec, httptest.NewRequest(http.MethodPost, "/api/v1/permissions/answer", strings.NewReader("{nope")))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestAnswerHandlerUnknownPermissionIs400(t *testing.T) {
	target := &fakeTarget{err: services.ErrUnknownPermission}
	body := `{"answers":[{"agent":"nope","permission":"nope","grant":true}]}`
	rec := httptest.NewRecorder()
	NewAnswerHandler(target, silentLogger{})(rec, httptest.NewRequest(http.MethodPost, "/api/v1/permissions/answer", strings.NewReader(body)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestAnswerHandlerRejectsCrossSiteRequests(t *testing.T) {
	body := `{"answers":[{"agent":"claude-code","permission":"hooks","grant":true}]}`
	assertCrossOriginGuard(t, "/api/v1/permissions/answer",
		func() io.Reader { return strings.NewReader(body) },
		func(target *fakeTarget) http.HandlerFunc { return NewAnswerHandler(target, silentLogger{}) },
		func(t *testing.T, site string, target *fakeTarget) {
			t.Helper()
			if len(target.answered) != 0 {
				t.Errorf("Sec-Fetch-Site=%s: cross-origin answer must not be applied, got %+v", site, target.answered)
			}
		})
}

// assertCrossOriginGuard is the shared shape of both mutating routes' guard:
// "cross-site" and "same-site" are refused 403 with no side effect, while
// same-origin (the dashboard) and a missing header (native clients) pass.
//
// Shared rather than copied because the two routes must agree — the reload
// route can run the same Apply closures the answer route can, so a divergence
// between these two tests would mean a divergence in what a webpage the user
// visits is able to reach. notApplied asserts the route-specific "and nothing
// happened" half.
func assertCrossOriginGuard(
	t *testing.T,
	path string,
	body func() io.Reader,
	handler func(*fakeTarget) http.HandlerFunc,
	notApplied func(*testing.T, string, *fakeTarget),
) {
	t.Helper()
	for _, site := range []string{"cross-site", "same-site"} {
		target := &fakeTarget{snapshot: testSnapshot()}
		req := httptest.NewRequest(http.MethodPost, path, body())
		req.Header.Set("Sec-Fetch-Site", site)
		rec := httptest.NewRecorder()
		handler(target)(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("Sec-Fetch-Site=%s: status = %d, want 403", site, rec.Code)
		}
		notApplied(t, site, target)
	}
	for _, site := range []string{"same-origin", "none", ""} {
		target := &fakeTarget{snapshot: testSnapshot()}
		req := httptest.NewRequest(http.MethodPost, path, body())
		if site != "" {
			req.Header.Set("Sec-Fetch-Site", site)
		}
		rec := httptest.NewRecorder()
		handler(target)(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("Sec-Fetch-Site=%q: status = %d, want 200", site, rec.Code)
		}
	}
}

func TestReloadHandlerReloadsAndReturnsSnapshot(t *testing.T) {
	target := &fakeTarget{snapshot: testSnapshot()}
	rec := httptest.NewRecorder()
	NewReloadHandler(target, silentLogger{})(rec, httptest.NewRequest(http.MethodPost, "/api/v1/permissions/reload", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if target.reloads != 1 {
		t.Fatalf("reloads = %d, want 1", target.reloads)
	}
	var got services.PermissionsSnapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Mode != "ask" {
		t.Fatalf("response is not the snapshot: %+v", got)
	}
}

func TestReloadHandlerStoreFailureIs500(t *testing.T) {
	target := &fakeTarget{snapshot: testSnapshot(), reloadErr: errors.New("permissions.json is not valid JSON")}
	rec := httptest.NewRecorder()
	NewReloadHandler(target, silentLogger{})(rec, httptest.NewRequest(http.MethodPost, "/api/v1/permissions/reload", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}

// TestReloadHandlerRejectsCrossSiteRequests: adopting a stored grant can run an
// Apply closure that rewrites ~/.claude/settings.json, so the reload nudge
// needs the same cross-origin guard the answer handler has. A bodyless POST is
// exactly the shape a drive-by form can produce.
func TestReloadHandlerRejectsCrossSiteRequests(t *testing.T) {
	assertCrossOriginGuard(t, "/api/v1/permissions/reload",
		func() io.Reader { return nil },
		func(target *fakeTarget) http.HandlerFunc { return NewReloadHandler(target, silentLogger{}) },
		func(t *testing.T, site string, target *fakeTarget) {
			t.Helper()
			if target.reloads != 0 {
				t.Errorf("Sec-Fetch-Site=%s: cross-origin reload must not run, reloads = %d", site, target.reloads)
			}
		})
}
