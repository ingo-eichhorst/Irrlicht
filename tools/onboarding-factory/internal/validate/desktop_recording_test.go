package validate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRecordingCompleteDesktopEvidenceMutations proves the generic recording
// completeness gate cannot accept a Desktop manifest whose raw identity chain
// is incomplete. Each required file is removed once from a complete fixture.
func TestRecordingCompleteDesktopEvidenceMutations(t *testing.T) {
	// This list is deliberately independent of RequiredRecordingFiles. Deriving
	// the mutation corpus from production made removing one required filename
	// remove its test too, so that mutation reported "no tests to run".
	desktopEvidenceFiles := []string{
		"desktop-registry.json",
		"desktop-environment.json",
		"hooks.jsonl",
		"process.json",
		"irrlicht-session.json",
		"transcript.jsonl",
	}
	base := []string{"events.jsonl", "manifest.json", "transcript.jsonl", "transcript.jsonl.replay.json.golden"}
	files := append(base, desktopEvidenceFiles...)
	// TranscriptFile appears in both lists. writeRecordingFiles safely rewrites
	// the same fixture path, so the duplicate keeps the two contracts explicit.
	dir := writeRecordingFiles(t, "complete desktop", files)
	writeDesktopManifest(t, dir)
	if got := RecordingComplete(dir); len(got) != 0 {
		t.Fatalf("complete Desktop recording has findings: %v", got)
	}
	for _, name := range desktopEvidenceFiles {
		if name == "transcript.jsonl" {
			continue // the generic missing-transcript row already mutates this file.
		}
		t.Run(name, func(t *testing.T) {
			mutated := writeRecordingFiles(t, name, files)
			writeDesktopManifest(t, mutated)
			if err := os.Remove(filepath.Join(mutated, name)); err != nil {
				t.Fatal(err)
			}
			findings := strings.Join(RecordingComplete(mutated), " | ")
			if !strings.Contains(findings, "missing "+name) {
				t.Fatalf("missing %s was accepted: %s", name, findings)
			}
		})
	}
}

func writeDesktopManifest(t *testing.T, dir string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(`{"execution_profile":"desktop-local"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}
