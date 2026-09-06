package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"testing"

	"irrlicht/tools/onboarding-factory/internal/desktopresults"
	"irrlicht/tools/onboarding-factory/internal/matrix"
)

// The recording rig writes execution-results.json from a jq template in
// tools/onboarding-factory/scripts/lib/desktop-profile.sh, while #1886's
// validator reads it through a typed, unknown-field-rejecting schema. Nothing
// used to connect the two: a field rename on either side stayed green here and
// only surfaced during a live Desktop run.
//
// desktop-profile_test.sh pins the exact bytes the shell function emits. These
// tests take that same pinned literal and (1) decode it with the production
// strict loader, (2) run its shape through `of validate` inside the positive
// fixture. A rename, an added key, or a dropped key on either side goes red.
var shellWriterPin = regexp.MustCompile(`(?m)^want='(\{.*\})'$`)

func shellWriterPinnedDocument(t *testing.T) []byte {
	t.Helper()
	path := filepath.Join("..", "..", "scripts", "lib", "desktop-profile_test.sh")
	source, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("cannot read the rig's writer test: %v", err)
	}
	match := shellWriterPin.FindSubmatch(source)
	if match == nil {
		t.Fatalf("no pinned execution-results literal found in %s — this check "+
			"cannot run, which is a failure, not a pass", path)
	}
	return match[1]
}

func TestShellWriterEmitsTheTypedResultSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), desktopresults.FileName)
	if err := os.WriteFile(path, shellWriterPinnedDocument(t), 0o600); err != nil {
		t.Fatal(err)
	}
	doc, err := desktopresults.Load(path)
	if err != nil {
		t.Fatalf("the rig's own writer no longer decodes as a typed #1886 document: %v", err)
	}
	if doc.SchemaVersion != desktopresults.SchemaVersion {
		t.Fatalf("schema_version = %d, want %d", doc.SchemaVersion, desktopresults.SchemaVersion)
	}
	if len(doc.Results) != 1 {
		t.Fatalf("results = %d, want 1", len(doc.Results))
	}
	result := doc.Results[0]
	if result.ExecutionProfile != string(matrix.ProfileDesktopLocal) {
		t.Fatalf("execution_profile = %q, want %q", result.ExecutionProfile, matrix.ProfileDesktopLocal)
	}
	if result.Outcome != desktopresults.OutcomeObservedPassing {
		t.Fatalf("outcome = %q, want %q", result.Outcome, desktopresults.OutcomeObservedPassing)
	}
	if result.ScenarioID == "" || result.Recording == "" {
		t.Fatalf("writer must name both the scenario and the recording; got %+v", result)
	}
	if result.Evidence == nil {
		t.Fatal("writer emitted no evidence block")
	}
	// The evidence block must name every raw file the recording layer requires,
	// so a seventh required file cannot ship without the writer learning it.
	got := []string{
		result.Evidence.DesktopRegistry,
		result.Evidence.Environment,
		result.Evidence.Hooks,
		result.Evidence.Process,
		result.Evidence.IrrlichtSession,
		result.Evidence.Transcript,
	}
	want := desktopresults.RequiredRecordingFiles()
	sort.Strings(got)
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("evidence names %d files, the recording layer requires %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("evidence files = %v, want %v", got, want)
		}
	}
}

// TestShellWriterShapeSurvivesValidateRepo runs the pinned shape end to end.
// Only the two identity strings are retargeted onto the positive fixture; every
// key, profile, outcome, and evidence name stays exactly as the rig writes it.
func TestShellWriterShapeSurvivesValidateRepo(t *testing.T) {
	path := filepath.Join(t.TempDir(), desktopresults.FileName)
	if err := os.WriteFile(path, shellWriterPinnedDocument(t), 0o600); err != nil {
		t.Fatal(err)
	}
	doc, err := desktopresults.Load(path)
	if err != nil {
		t.Fatal(err)
	}

	fixture := desktopResultsRepo(t, true)
	cellDir := fixture.cellDirs["observed-pass"]
	doc.Results[0].ScenarioID = "observed-pass"
	doc.Results[0].Recording = fixture.record
	body, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(cellDir, desktopResultsFile), string(body)+"\n")

	if code, _, stderr := runOf("validate", "--repo-root", fixture.root); code != exitOK {
		t.Fatalf("the rig's own result shape failed the merged validator: exit=%d stderr:\n%s", code, stderr)
	}
}
