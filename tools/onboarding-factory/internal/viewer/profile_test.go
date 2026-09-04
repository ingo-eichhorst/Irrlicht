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

// viewerScenarioDetail fetches the mixed fixture's detail page. query is
// appended verbatim ("" for the profile-less, backward-compatible URL).
func viewerScenarioDetail(t *testing.T, root, query string) ScenarioDetail {
	t.Helper()
	recorder := httptest.NewRecorder()
	server := &Server{RepoRoot: root}
	server.Handler().ServeHTTP(recorder, httptest.NewRequest("GET", "/api/scenarios/claudecode/scenarios/mixed"+query, nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body)
	}
	var detail ScenarioDetail
	if err := json.Unmarshal(recorder.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	return detail
}

// TestViewerValidatesTheSameLatestRecordingItDisplays is the invariant #1884
// introduced, now carried per execution profile (#1889): whichever recording
// the detail page names as latest is the one its expected-state report — and
// the catalog matrix's measurement — is computed from. The fixture is
// deliberately mixed AND ordered so the newest recording across both profiles
// (r2, desktop-local) is NOT the newest within the default profile (r1,
// cli-local): a viewer that reverted to an all-profile "newest" would grade a
// Desktop recording under a CLI view and this test would catch it.
func TestViewerValidatesTheSameLatestRecordingItDisplays(t *testing.T) {
	root := mixedProfileViewerFixture(t)
	cases := []struct {
		name     string
		query    string
		profile  string
		wantRec  string
		wantPass bool
		wantStat string
	}{
		// No ?profile= — the pre-#1889 URL shape. It stays on CLI Local.
		{"default URL is CLI Local", "", "cli-local", "r1", false, "fail"},
		{"explicit cli-local", "?profile=cli-local", "cli-local", "r1", false, "fail"},
		{"explicit desktop-local", "?profile=desktop-local", "desktop-local", "r2", true, "pass"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			detail := viewerScenarioDetail(t, root, tc.query)
			assertDisplayedRecording(t, detail, tc.profile, tc.wantRec, tc.wantPass)
			measurement := measureScenario(root, "claudecode", "mixed", profileOrFail(t, tc.profile))
			if measurement["status"] != tc.wantStat {
				t.Fatalf("catalog measurement graded a different recording: %+v", measurement)
			}
		})
	}
}

// assertDisplayedRecording pins that the detail page names the expected
// profile and recording, and that its expected-state report describes THAT
// recording rather than the other profile's.
func assertDisplayedRecording(t *testing.T, detail ScenarioDetail, profile, wantRec string, wantPass bool) {
	t.Helper()
	if detail.ExecutionProfile != profile {
		t.Fatalf("execution_profile=%q, want %q", detail.ExecutionProfile, profile)
	}
	if detail.LatestRecording != wantRec {
		t.Fatalf("latest recording=%q, want %q", detail.LatestRecording, wantRec)
	}
	if detail.Expected == nil {
		t.Fatal("latest recording has no expected-state report")
	}
	if detail.Expected.Pass != wantPass {
		t.Fatalf("expected.pass=%t, want %t: %+v", detail.Expected.Pass, wantPass, detail.Expected)
	}
}
