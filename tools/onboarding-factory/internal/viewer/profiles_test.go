package viewer

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"irrlicht/tools/onboarding-factory/internal/matrix"
)

// This file proves the execution-profile split the viewer serves (#1889): CLI
// Local and Desktop Local evidence never share a status, a recording history,
// a set of versions, or a link.
//
// Three of the tests below are MUTATION FIXTURES. The guards they cover are
// new, so there is no "before the fix" state to observe failing; instead each
// one mutates the thing the guard protects — a result artifact repointed at
// the other profile's recording, a recording relabelled into the other
// profile — and asserts the guard fires. Each mutation is read back off disk
// before it is exercised, so a mutation that silently failed to apply fails
// the test instead of producing a green that proves nothing.

const (
	cliRecordingName     = "a-cli"
	desktopRecordingName = "b-desktop"
	desktopSessionID     = "transcript-1"
	desktopHookRow       = `{"seq":2,"ts":"2026-05-01T00:00:01Z","kind":"hook_received","session_id":"transcript-1","hook_name":"Stop"}`
)

type desktopCellFixture struct {
	root    string
	cellDir string
}

// profileOrFail parses a profile string in a test, failing loudly rather than
// letting a typo silently fall back to the default.
func profileOrFail(t *testing.T, value string) matrix.ExecutionProfile {
	t.Helper()
	profile, err := matrix.ParseExecutionProfile(value)
	if err != nil {
		t.Fatalf("test names an unknown profile %q: %v", value, err)
	}
	return profile
}

// desktopViewerFixture builds one Claude Code cell holding BOTH profiles'
// evidence: a cli-local recording and a complete desktop-local recording with
// all six raw identity-evidence files, plus an execution-results.json naming
// the desktop recording. The desktop recording sorts LAST, so it is the newest
// recording overall — any accidental all-profile "newest" selection shows up
// as a CLI view rendering Desktop bytes.
func desktopViewerFixture(t *testing.T) desktopCellFixture {
	t.Helper()
	root := t.TempDir()
	cellDir := filepath.Join(root, "replaydata", "agents", "claudecode", "scenarios", "desktop-cell")
	mkdirAllOrFail(t, cellDir)
	writeViewerProfileFile(t, filepath.Join(cellDir, "expected.jsonl"),
		`{"schema_version":1}`+"\n"+
			`{"phase":"birth","kind":"transcript_new","relative_to":"start","max_delay_ms":5000}`+"\n")

	writeViewerProfileRecording(t, cellDir, viewerProfileRecording{
		name: cliRecordingName, profile: "cli-local",
		events: `{"ts":"2026-01-01T00:00:00Z","kind":"transcript_new","session_id":"cli","adapter":"claudecode"}` + "\n",
	})
	writeDesktopRecording(t, cellDir)
	writeDesktopResults(t, cellDir, observedDesktopResult(desktopRecordingName))
	return desktopCellFixture{root: root, cellDir: cellDir}
}

func mkdirAllOrFail(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}

// writeDesktopRecording lays down a desktop-local recording carrying the six
// raw identity-evidence files the Desktop contract names.
func writeDesktopRecording(t *testing.T, cellDir string) {
	t.Helper()
	recDir := filepath.Join(cellDir, "recordings", desktopRecordingName)
	mkdirAllOrFail(t, recDir)
	files := map[string]string{
		"manifest.json": `{"execution_profile":"desktop-local","entrypoint":"claude-desktop",` +
			`"daemon_version":"0.6.2+fixture","agent_cli_version":"2.1.258","desktop_app_version":"1.44121.4"}` + "\n",
		"events.jsonl": `{"ts":"2026-05-01T00:00:00Z","kind":"transcript_new","session_id":"` + desktopSessionID + `","adapter":"claudecode"}` + "\n" +
			desktopHookRow + "\n",
		"transcript.jsonl":         `{"type":"user","sessionId":"` + desktopSessionID + `","cwd":"/workspace","entrypoint":"claude-desktop"}` + "\n",
		"desktop-registry.json":    `{"sessionId":"local_desktop-1","cliSessionId":"` + desktopSessionID + `","cwd":"/workspace"}` + "\n",
		"desktop-environment.json": `{"selected_environment":"Local","requested_workspace":"/workspace"}` + "\n",
		"irrlicht-session.json":    `{"session_id":"` + desktopSessionID + `","cwd":"/workspace","pid":4242,"launcher":{"host_bundle_id":"com.anthropic.claudefordesktop"}}` + "\n",
		"process.json":             `{"pid":4242,"command":"claude"}` + "\n",
		"hooks.jsonl":              desktopHookRow + "\n",
	}
	for name, body := range files {
		writeViewerProfileFile(t, filepath.Join(recDir, name), body)
	}
}

// observedDesktopResult is the positive desktop-local result: it names a
// recording and the six canonical evidence files inside it.
func observedDesktopResult(recording string) map[string]any {
	return map[string]any{
		"scenario_id":       "desktop-cell",
		"execution_profile": "desktop-local",
		"outcome":           "observed-passing",
		"recording":         recording,
		"evidence": map[string]string{
			"desktop_registry": "desktop-registry.json",
			"transcript":       "transcript.jsonl",
			"hooks":            "hooks.jsonl",
			"process":          "process.json",
			"irrlicht_session": "irrlicht-session.json",
			"environment":      "desktop-environment.json",
		},
	}
}

func writeDesktopResults(t *testing.T, cellDir string, results ...map[string]any) {
	t.Helper()
	b, err := json.MarshalIndent(map[string]any{"schema_version": 1, "results": results}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	writeViewerProfileFile(t, filepath.Join(cellDir, "execution-results.json"), string(b)+"\n")
}

// get issues one viewer request against the fixture root and returns the
// recorder, so a test can assert on both the status and the body.
func get(t *testing.T, root, path string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	(&Server{RepoRoot: root}).Handler().ServeHTTP(recorder, httptest.NewRequest("GET", path, nil))
	return recorder
}

func getJSON(t *testing.T, root, path string, out any) {
	t.Helper()
	recorder := get(t, root, path)
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET %s: status=%d body=%s", path, recorder.Code, recorder.Body)
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), out); err != nil {
		t.Fatalf("GET %s: %v (body=%s)", path, err, recorder.Body)
	}
}

const desktopCellPath = "/api/scenarios/claudecode/scenarios/desktop-cell"

// TestRecordingHistoriesStaySeparatePerProfile is the "never merge" rule at
// the history level: each profile lists only its own recordings, and the
// legacy profile-less URL keeps listing exactly what it listed before.
func TestRecordingHistoriesStaySeparatePerProfile(t *testing.T) {
	fixture := desktopViewerFixture(t)
	for _, tc := range []struct {
		query   string
		want    string
		profile string
	}{
		{"", cliRecordingName, "cli-local"},
		{"?profile=cli-local", cliRecordingName, "cli-local"},
		{"?profile=desktop-local", desktopRecordingName, "desktop-local"},
	} {
		var archives []RecordingArchive
		getJSON(t, fixture.root, desktopCellPath+"/recordings"+tc.query, &archives)
		if len(archives) != 1 {
			t.Fatalf("%q: got %d recordings %+v, want exactly the one in this profile", tc.query, len(archives), archives)
		}
		if archives[0].Name != tc.want {
			t.Fatalf("%q: listed %q, want %q", tc.query, archives[0].Name, tc.want)
		}
		if archives[0].ExecutionProfile != tc.profile {
			t.Fatalf("%q: row claims profile %q, want %q", tc.query, archives[0].ExecutionProfile, tc.profile)
		}
	}
}

// TestArchiveEndpointRefusesTheOtherProfilesRecording closes the last door a
// CLI recording could walk through into a Desktop page: a direct archive URL.
// The refusal names the archive's real profile, so it can never be read as
// "this cell has no evidence".
func TestArchiveEndpointRefusesTheOtherProfilesRecording(t *testing.T) {
	fixture := desktopViewerFixture(t)
	for _, tc := range []struct{ url, mentions string }{
		{desktopCellPath + "/recordings/" + desktopRecordingName, "desktop-local"},
		{desktopCellPath + "/recordings/" + cliRecordingName + "?profile=desktop-local", "cli-local"},
	} {
		recorder := get(t, fixture.root, tc.url)
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("GET %s: status=%d, want 404", tc.url, recorder.Code)
		}
		if !strings.Contains(recorder.Body.String(), tc.mentions) {
			t.Fatalf("GET %s: body %q does not name the archive's real profile %q", tc.url, recorder.Body, tc.mentions)
		}
	}
	// Same-profile requests still work, so the 404s above are the guard
	// firing and not the endpoint being broken.
	for _, url := range []string{
		desktopCellPath + "/recordings/" + cliRecordingName,
		desktopCellPath + "/recordings/" + desktopRecordingName + "?profile=desktop-local",
	} {
		if code := get(t, fixture.root, url).Code; code != http.StatusOK {
			t.Fatalf("GET %s: status=%d, want 200", url, code)
		}
	}
}

// TestUnknownProfileIsRejected — a mistyped profile is a 400 everywhere, never
// a silent fallback that would serve the other profile's evidence.
func TestUnknownProfileIsRejected(t *testing.T) {
	fixture := desktopViewerFixture(t)
	for _, url := range []string{
		desktopCellPath + "?profile=desktop",
		desktopCellPath + "/recordings?profile=desktop",
		desktopCellPath + "/recordings/" + desktopRecordingName + "?profile=desktop",
		"/api/catalog?profile=desktop",
	} {
		if code := get(t, fixture.root, url).Code; code != http.StatusBadRequest {
			t.Fatalf("GET %s: status=%d, want 400", url, code)
		}
	}
}

// TestDesktopResultLinksItsRecordingAndRawEvidence covers the positive
// contract: an observed Desktop result resolves to its recording, the three
// versions come from that recording's own manifest, and each of the six raw
// identity-evidence files is both listed as present and actually servable.
func TestDesktopResultLinksItsRecordingAndRawEvidence(t *testing.T) {
	fixture := desktopViewerFixture(t)
	var detail ScenarioDetail
	getJSON(t, fixture.root, desktopCellPath+"?profile=desktop-local", &detail)

	result := detail.DesktopResult
	if result == nil {
		t.Fatal("desktop-local view carries no desktop_result")
	}
	if result.Error != "" {
		t.Fatalf("unexpected error on a valid result: %s", result.Error)
	}
	if result.Outcome != "observed-passing" || result.Recording != desktopRecordingName {
		t.Fatalf("result = %+v", result)
	}
	if result.Versions == nil {
		t.Fatal("observed result carries no versions")
	}
	want := DesktopVersions{DesktopApp: "1.44121.4", AgentCLI: "2.1.258", Irrlicht: "0.6.2+fixture"}
	if *result.Versions != want {
		t.Fatalf("versions = %+v, want %+v", *result.Versions, want)
	}
	if len(result.Evidence) != 6 {
		t.Fatalf("got %d evidence links, want the six canonical files: %+v", len(result.Evidence), result.Evidence)
	}
	for _, link := range result.Evidence {
		if !link.Present {
			t.Fatalf("evidence %q is not present in the linked recording", link.Field)
		}
		url := desktopCellPath + "/recordings/" + desktopRecordingName + "/evidence/" + link.Field + "?profile=desktop-local"
		recorder := get(t, fixture.root, url)
		if recorder.Code != http.StatusOK {
			t.Fatalf("GET %s: status=%d body=%s", url, recorder.Code, recorder.Body)
		}
		if recorder.Body.Len() == 0 {
			t.Fatalf("GET %s served an empty body", url)
		}
	}
}

// TestDesktopResultRefusesACLIRecording — MUTATION FIXTURE.
//
// The guard is new, so it has no red-before state of its own. Instead the
// fixture is mutated in the one way that would defeat it: the Desktop result
// is repointed at the cli-local recording. Both halves of the mutation are
// read back off disk first, so a mutation that failed to apply fails here
// rather than producing a green that proves nothing.
func TestDesktopResultRefusesACLIRecording(t *testing.T) {
	fixture := desktopViewerFixture(t)
	writeDesktopResults(t, fixture.cellDir, observedDesktopResult(cliRecordingName))
	assertResultNamesRecording(t, fixture.cellDir, cliRecordingName)
	assertRecordingProfile(t, fixture.cellDir, cliRecordingName, matrix.ProfileCLILocal)

	var detail ScenarioDetail
	getJSON(t, fixture.root, desktopCellPath+"?profile=desktop-local", &detail)
	result := detail.DesktopResult
	if result == nil {
		t.Fatal("desktop-local view carries no desktop_result")
	}
	if result.Recording != "" {
		t.Fatalf("a Desktop result linked the CLI recording %q", result.Recording)
	}
	if result.Versions != nil {
		t.Fatalf("a Desktop result took versions from a CLI recording: %+v", *result.Versions)
	}
	if len(result.Evidence) != 0 {
		t.Fatalf("a Desktop result linked CLI evidence files: %+v", result.Evidence)
	}
	if result.RecordingProfile != string(matrix.ProfileCLILocal) {
		t.Fatalf("recording_profile=%q, want the recording's real profile %q", result.RecordingProfile, matrix.ProfileCLILocal)
	}
	if !strings.Contains(result.Error, string(matrix.ProfileCLILocal)) {
		t.Fatalf("error %q does not say why the recording was refused", result.Error)
	}
}

// TestRelabelledDesktopRecordingLeavesTheDesktopView — MUTATION FIXTURE.
//
// The other direction: the recording itself is relabelled into the CLI
// profile. Its manifest is the only authority on which profile it belongs to,
// so the Desktop history must lose it, the Desktop detail must degrade to "no
// recording", and the result must refuse to link it — rather than the viewer
// keeping it because the result artifact still names it.
func TestRelabelledDesktopRecordingLeavesTheDesktopView(t *testing.T) {
	fixture := desktopViewerFixture(t)
	relabelRecordingProfile(t, fixture.cellDir, desktopRecordingName, matrix.ProfileCLILocal)
	assertRecordingProfile(t, fixture.cellDir, desktopRecordingName, matrix.ProfileCLILocal)

	var archives []RecordingArchive
	getJSON(t, fixture.root, desktopCellPath+"/recordings?profile=desktop-local", &archives)
	if len(archives) != 0 {
		t.Fatalf("desktop history still lists a relabelled recording: %+v", archives)
	}
	var detail ScenarioDetail
	getJSON(t, fixture.root, desktopCellPath+"?profile=desktop-local", &detail)
	if detail.LatestRecording != "" {
		t.Fatalf("desktop detail still displays %q", detail.LatestRecording)
	}
	if !detail.Degraded {
		t.Fatal("a profile with no recording must report degraded, not an empty-but-fine page")
	}
	if detail.DesktopResult == nil || detail.DesktopResult.Recording != "" {
		t.Fatalf("desktop result still links the relabelled recording: %+v", detail.DesktopResult)
	}
	if code := get(t, fixture.root, desktopCellPath+"/recordings/"+desktopRecordingName+"?profile=desktop-local").Code; code != http.StatusNotFound {
		t.Fatalf("archive endpoint still serves the relabelled recording: status=%d", code)
	}
}

// TestNonObservedDesktopResultCarriesItsEvidenceBasedReason — not-applicable,
// unobservable and not-runnable name no recording, so their reason (and, for
// not-runnable, the missing Desktop control) plus their repository evidence
// references are the whole answer and must all reach the viewer.
func TestNonObservedDesktopResultCarriesItsEvidenceBasedReason(t *testing.T) {
	for _, tc := range []struct {
		outcome, reason, missingControl string
	}{
		{"not-applicable", "The scenario is outside Local Desktop.", ""},
		{"unobservable", "The local runtime leaves no observable trace.", ""},
		{"not-runnable", "The Desktop driver cannot perform this recipe.", "model selector"},
	} {
		t.Run(tc.outcome, func(t *testing.T) {
			fixture := desktopViewerFixture(t)
			evidenceRef := "replaydata/agents/claudecode/scenarios/desktop-cell/desktop-evidence/" + tc.outcome + ".md"
			mkdirAllOrFail(t, filepath.Join(fixture.cellDir, "desktop-evidence"))
			writeViewerProfileFile(t, filepath.Join(fixture.root, filepath.FromSlash(evidenceRef)),
				"Desktop campaign evidence for "+tc.outcome+".\n")
			result := map[string]any{
				"scenario_id":       "desktop-cell",
				"execution_profile": "desktop-local",
				"outcome":           tc.outcome,
				"reason":            tc.reason,
				"evidence_refs":     []string{evidenceRef},
			}
			if tc.missingControl != "" {
				result["missing_control"] = tc.missingControl
			}
			writeDesktopResults(t, fixture.cellDir, result)

			var detail ScenarioDetail
			getJSON(t, fixture.root, desktopCellPath+"?profile=desktop-local", &detail)
			got := detail.DesktopResult
			if got == nil {
				t.Fatal("no desktop_result")
			}
			if got.Outcome != tc.outcome || got.Reason != tc.reason || got.MissingControl != tc.missingControl {
				t.Fatalf("result = %+v", got)
			}
			if len(got.EvidenceRefs) != 1 || got.EvidenceRefs[0] != evidenceRef {
				t.Fatalf("evidence_refs = %+v, want %q", got.EvidenceRefs, evidenceRef)
			}
			if got.Recording != "" || got.Versions != nil {
				t.Fatalf("a non-observed result must name no recording: %+v", got)
			}
		})
	}
}

// TestProfileOptionsOfferDesktopWheneverThereIsSomethingToShow — the selector
// offers Desktop Local when the cell has a Desktop result even with no
// recording yet, and always offers the CLI Local default.
func TestProfileOptionsOfferDesktopWheneverThereIsSomethingToShow(t *testing.T) {
	fixture := desktopViewerFixture(t)
	var detail ScenarioDetail
	getJSON(t, fixture.root, desktopCellPath, &detail)
	byID := map[string]ProfileOption{}
	for _, option := range detail.Profiles {
		byID[option.ID] = option
	}
	if len(byID) != 2 {
		t.Fatalf("profiles = %+v, want both execution profiles", detail.Profiles)
	}
	cli := byID[string(matrix.ProfileCLILocal)]
	if !cli.Selectable || cli.Recordings != 1 || cli.HasResult {
		t.Fatalf("cli-local option = %+v", cli)
	}
	desktop := byID[string(matrix.ProfileDesktopLocal)]
	if !desktop.Selectable || desktop.Recordings != 1 || !desktop.HasResult {
		t.Fatalf("desktop-local option = %+v", desktop)
	}

	// A result with no recording keeps Desktop selectable — the reason IS the
	// evidence, and hiding the profile would hide it.
	writeDesktopResults(t, fixture.cellDir, map[string]any{
		"scenario_id":       "desktop-cell",
		"execution_profile": "desktop-local",
		"outcome":           "not-applicable",
		"reason":            "outside Local Desktop",
		"evidence_refs":     []string{"replaydata/agents/claudecode/scenarios/desktop-cell/execution-results.json"},
	})
	relabelRecordingProfile(t, fixture.cellDir, desktopRecordingName, matrix.ProfileCLILocal)
	getJSON(t, fixture.root, desktopCellPath, &detail)
	for _, option := range detail.Profiles {
		if option.ID != string(matrix.ProfileDesktopLocal) {
			continue
		}
		if !option.Selectable || option.Recordings != 0 || !option.HasResult {
			t.Fatalf("desktop-local option with a recording-less result = %+v", option)
		}
	}
}

// TestRawEvidenceEndpointIsAllowlistedAndProfileScoped — the route only ever
// serves the six canonical Desktop identity files, and only out of
// desktop-local recordings.
func TestRawEvidenceEndpointIsAllowlistedAndProfileScoped(t *testing.T) {
	fixture := desktopViewerFixture(t)
	base := desktopCellPath + "/recordings/" + desktopRecordingName + "/evidence/"
	for _, tc := range []struct {
		url  string
		want int
	}{
		{base + "transcript.jsonl?profile=desktop-local", http.StatusOK},
		// events.jsonl is a real file in the recording but not one of the six.
		{base + "events.jsonl?profile=desktop-local", http.StatusBadRequest},
		{base + "manifest.json?profile=desktop-local", http.StatusBadRequest},
		// Raw identity evidence is a Desktop-only concept.
		{base + "transcript.jsonl", http.StatusNotFound},
		{base + "transcript.jsonl?profile=cli-local", http.StatusNotFound},
		// A cli-local recording can never serve Desktop evidence.
		{desktopCellPath + "/recordings/" + cliRecordingName + "/evidence/transcript.jsonl?profile=desktop-local", http.StatusNotFound},
	} {
		if code := get(t, fixture.root, tc.url).Code; code != tc.want {
			t.Fatalf("GET %s: status=%d, want %d", tc.url, code, tc.want)
		}
	}
}

// TestLegacyRecordingWithoutManifestStaysCLILocal pins the compatibility half
// of the split: every recording in replaydata today carries no
// execution_profile (and 45 carry no manifest.json at all), and those stay
// visible under the profile-less default URL.
func TestLegacyRecordingWithoutManifestStaysCLILocal(t *testing.T) {
	root := t.TempDir()
	cellDir := filepath.Join(root, "replaydata", "agents", "claudecode", "scenarios", "legacy")
	recDir := filepath.Join(cellDir, "recordings", "2026-01-01_legacy")
	mkdirAllOrFail(t, recDir)
	writeViewerProfileFile(t, filepath.Join(cellDir, "expected.jsonl"),
		`{"schema_version":1}`+"\n"+`{"phase":"birth","kind":"transcript_new","relative_to":"start","max_delay_ms":5000}`+"\n")
	writeViewerProfileFile(t, filepath.Join(recDir, "events.jsonl"),
		`{"ts":"2026-01-01T00:00:00Z","kind":"transcript_new","session_id":"legacy","adapter":"claudecode"}`+"\n")

	var detail ScenarioDetail
	getJSON(t, root, "/api/scenarios/claudecode/scenarios/legacy", &detail)
	if detail.LatestRecording != "2026-01-01_legacy" {
		t.Fatalf("legacy recording vanished from the default view: %+v", detail)
	}
	if detail.ExecutionProfile != string(matrix.ProfileCLILocal) {
		t.Fatalf("default profile = %q, want %q", detail.ExecutionProfile, matrix.ProfileCLILocal)
	}
	if detail.DesktopResult != nil {
		t.Fatalf("a cell with no execution-results.json must carry no desktop_result: %+v", detail.DesktopResult)
	}
	var archives []RecordingArchive
	getJSON(t, root, "/api/scenarios/claudecode/scenarios/legacy/recordings", &archives)
	if len(archives) != 1 || archives[0].ExecutionProfile != string(matrix.ProfileCLILocal) {
		t.Fatalf("legacy recordings list = %+v", archives)
	}
}

// --- mutation helpers -----------------------------------------------------
// Each one reads its effect back off disk, so a mutation that did not apply
// fails the test instead of leaving a check that silently had nothing to find.

func relabelRecordingProfile(t *testing.T, cellDir, recording string, profile matrix.ExecutionProfile) {
	t.Helper()
	path := filepath.Join(cellDir, "recordings", recording, "manifest.json")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("mutation cannot read %s: %v", path, err)
	}
	var manifest map[string]any
	if err := json.Unmarshal(b, &manifest); err != nil {
		t.Fatalf("mutation cannot parse %s: %v", path, err)
	}
	manifest["execution_profile"] = string(profile)
	out, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	writeViewerProfileFile(t, path, string(out)+"\n")
}

func assertRecordingProfile(t *testing.T, cellDir, recording string, want matrix.ExecutionProfile) {
	t.Helper()
	manifest, err := matrix.LoadRecordingManifest(filepath.Join(cellDir, "recordings", recording, "manifest.json"))
	if err != nil {
		t.Fatalf("cannot verify the mutated manifest for %s: %v", recording, err)
	}
	if manifest.ExecutionProfile != want {
		t.Fatalf("mutation did not apply: recording %s is %q, want %q", recording, manifest.ExecutionProfile, want)
	}
}

func assertResultNamesRecording(t *testing.T, cellDir, want string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(cellDir, "execution-results.json"))
	if err != nil {
		t.Fatalf("cannot verify the mutated result artifact: %v", err)
	}
	var doc struct {
		Results []struct {
			Recording string `json:"recording"`
		} `json:"results"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("cannot parse the mutated result artifact: %v", err)
	}
	if len(doc.Results) != 1 || doc.Results[0].Recording != want {
		t.Fatalf("mutation did not apply: result artifact = %+v, want recording %q", doc.Results, want)
	}
}
