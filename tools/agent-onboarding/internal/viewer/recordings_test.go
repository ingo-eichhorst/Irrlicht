package viewer

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// archiveFixture builds a scenario with one archived recording under
// recordings/<name>/ (manifest + events), returning the repo root.
func archiveFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	scenarioDir := filepath.Join(root, "replaydata", "agents", "claudecode", "scenarios", "test")
	archiveDir := filepath.Join(scenarioDir, "recordings", "2026-05-01_run")
	if err := os.MkdirAll(archiveDir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{"name":"ignored","promoted_at":"2026-05-01T10:00:00Z","daemon_version":"0.3.0"}`
	if err := os.WriteFile(filepath.Join(archiveDir, "manifest.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	events := `{"seq":1,"ts":"2026-05-01T10:00:00Z","kind":"state_transition","session_id":"s1","new_state":"working"}` + "\n"
	if err := os.WriteFile(filepath.Join(archiveDir, "events.jsonl"), []byte(events), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// TestHandleRecordingsList covers the archive listing handler (#461
// finding #6 — previously untested): the manifest is read and the internal
// `name` is forced to the dir name regardless of file content.
func TestHandleRecordingsList(t *testing.T) {
	s := &Server{RepoRoot: archiveFixture(t)}
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest("GET", "/api/scenarios/claudecode/scenarios/test/recordings", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body)
	}
	var got []RecordingArchive
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 archive, got %d", len(got))
	}
	if got[0].Name != "2026-05-01_run" {
		t.Errorf("archive name = %q; want the dir name (manifest's internal name is ignored)", got[0].Name)
	}
	if got[0].DaemonVersion != "0.3.0" {
		t.Errorf("daemon_version = %q; want 0.3.0", got[0].DaemonVersion)
	}
}

// TestHandleArchivedRecording covers fetching one archive's detail and the
// path-traversal guard on the archive name.
func TestHandleArchivedRecording(t *testing.T) {
	s := &Server{RepoRoot: archiveFixture(t)}
	h := s.Handler()

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/api/scenarios/claudecode/scenarios/test/recordings/2026-05-01_run", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body)
	}
	var got ArchivedRecordingDetail
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Name != "2026-05-01_run" || got.Manifest.DaemonVersion != "0.3.0" {
		t.Errorf("unexpected detail: %+v", got)
	}
	if len(got.Transitions) != 1 {
		t.Errorf("expected 1 transition row, got %d", len(got.Transitions))
	}

	// Missing archive → 404.
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/api/scenarios/claudecode/scenarios/test/recordings/does-not-exist", nil))
	if rr.Code != http.StatusNotFound {
		t.Errorf("missing archive: status=%d; want 404", rr.Code)
	}
}
