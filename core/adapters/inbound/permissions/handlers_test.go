package permissions

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	services "irrlicht/core/application/services"
)

type fakeTarget struct {
	snapshot services.PermissionsSnapshot
	answered [][]services.PermissionAnswer
	err      error
}

func (t *fakeTarget) Snapshot() services.PermissionsSnapshot { return t.snapshot }
func (t *fakeTarget) Answer(a []services.PermissionAnswer) error {
	t.answered = append(t.answered, a)
	return t.err
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
