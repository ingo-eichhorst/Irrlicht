package viewer

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

type viewerProfileRecording struct {
	name, profile, events string
}

func writeViewerProfileRecording(t *testing.T, cellDir string, fixture viewerProfileRecording) {
	t.Helper()
	recordingDir := filepath.Join(cellDir, "recordings", fixture.name)
	if err := os.MkdirAll(recordingDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeViewerProfileFile(t, filepath.Join(recordingDir, "manifest.json"),
		`{"execution_profile":"`+fixture.profile+`"}`+"\n")
	writeViewerProfileFile(t, filepath.Join(recordingDir, "events.jsonl"), fixture.events)
}

func writeViewerProfileFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mixedProfileViewerFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	cellDir := filepath.Join(root, "replaydata", "agents", "claudecode", "scenarios", "mixed")
	if err := os.MkdirAll(cellDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeViewerProfileFile(t, filepath.Join(cellDir, "expected.jsonl"),
		`{"schema_version":1}`+"\n"+`{"phase":"ready","expected_state":"ready","relative_to":"start","max_delay_ms":5000}`+"\n")
	writeViewerProfileRecording(t, cellDir, viewerProfileRecording{
		name: "r1", profile: "cli-local",
		events: `{"ts":"2026-01-01T00:00:00Z","kind":"transcript_new","session_id":"cli"}` + "\n",
	})
	writeViewerProfileRecording(t, cellDir, viewerProfileRecording{
		name: "r2", profile: "desktop-local",
		events: `{"ts":"2026-01-01T00:00:00Z","kind":"transcript_new","session_id":"desktop"}` + "\n" +
			`{"ts":"2026-01-01T00:00:01Z","kind":"state_transition","session_id":"desktop","new_state":"ready"}` + "\n",
	})
	return root
}

func viewerScenarioDetail(t *testing.T, root string) ScenarioDetail {
	t.Helper()
	recorder := httptest.NewRecorder()
	server := &Server{RepoRoot: root}
	server.Handler().ServeHTTP(recorder, httptest.NewRequest("GET", "/api/scenarios/claudecode/scenarios/mixed", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body)
	}
	var detail ScenarioDetail
	if err := json.Unmarshal(recorder.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	return detail
}

func TestViewerValidatesTheSameLatestRecordingItDisplays(t *testing.T) {
	root := mixedProfileViewerFixture(t)
	detail := viewerScenarioDetail(t, root)
	if detail.LatestRecording != "r2" {
		t.Fatalf("latest recording=%q, want r2", detail.LatestRecording)
	}
	if detail.Expected == nil {
		t.Fatal("latest recording has no expected-state report")
	}
	if !detail.Expected.Pass {
		t.Fatalf("latest Desktop recording must pass validation: %+v", detail.Expected)
	}
	measurement := measureScenario(root, "claudecode", "mixed")
	if measurement["status"] != "pass" {
		t.Fatalf("catalog measurement validated a different recording: %+v", measurement)
	}
}
