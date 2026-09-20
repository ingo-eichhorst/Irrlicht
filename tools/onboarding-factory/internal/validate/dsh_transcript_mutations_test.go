package validate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"irrlicht/tools/onboarding-factory/internal/matrix"
	internalreplay "irrlicht/tools/onboarding-factory/internal/replay"
)

// TestDSHBackgroundTranscriptMutations proves the 3.2 contract checks the
// two automatic DSH child-delivery records and their completed parent turns.
func TestDSHBackgroundTranscriptMutations(t *testing.T) {
	source, recording, records := dshBackgroundTranscriptFixture(t)
	for _, tc := range dshBackgroundTranscriptMutations() {
		t.Run(tc.name, func(t *testing.T) {
			cell := writeDSHBackgroundTranscriptMutation(t, source, recording.Dir, records, tc)
			assertTranscriptMutationFails(t, cell, tc.assertion)
		})
	}
}

type dshTranscriptMutation struct {
	name, assertion string
	keep            func(map[string]any) bool
	mutate          func(map[string]any)
}

func dshBackgroundTranscriptFixture(t *testing.T) (string, matrix.Recording, []map[string]any) {
	t.Helper()
	source, err := filepath.Abs(filepath.Join("..", "..", "..", "..", "replaydata", "agents", "deepseek-harness", "scenarios", "3-2_background-subagent"))
	if err != nil {
		t.Fatal(err)
	}
	recording, ok, err := matrix.NewestRecording(source, matrix.ProfileCLILocal)
	if err != nil || !ok {
		t.Fatalf("newest DSH 3.2 recording: ok=%v err=%v", ok, err)
	}
	records, err := readJSONLRecords(internalreplay.TranscriptPath(recording.Dir), "transcript")
	if err != nil {
		t.Fatalf("read DSH 3.2 transcript: %v", err)
	}
	return source, recording, records
}

func dshBackgroundTranscriptMutations() []dshTranscriptMutation {
	return []dshTranscriptMutation{
		{"missing agent-message", "one child result relay", func(r map[string]any) bool {
			value, _ := valueAtPath(r, "data.source.kind")
			return value != "agent-message"
		}, nil},
		{"aborted follow-up", "three completed parent turns", func(map[string]any) bool { return true }, func(r map[string]any) {
			typ, _ := valueAtPath(r, "type")
			turn, _ := valueAtPath(r, "data.turn")
			if typ == "turn/end" && turn == float64(3) {
				r["data"].(map[string]any)["reason"].(map[string]any)["kind"] = "aborted"
			}
		}},
	}
}

func writeDSHBackgroundTranscriptMutation(t *testing.T, source, recordingDir string, records []map[string]any, mutation dshTranscriptMutation) string {
	t.Helper()
	cell := t.TempDir()
	mustCopy(t, filepath.Join(source, "expected.jsonl"), filepath.Join(cell, "expected.jsonl"))
	destination := filepath.Join(cell, "recordings", "run")
	if err := os.MkdirAll(destination, 0755); err != nil {
		t.Fatal(err)
	}
	mustCopy(t, filepath.Join(recordingDir, "events.jsonl"), filepath.Join(destination, "events.jsonl"))
	mustCopy(t, filepath.Join(recordingDir, "manifest.json"), filepath.Join(destination, "manifest.json"))
	writeMutatedTranscript(t, filepath.Join(destination, "transcript.jsonl"), records, mutation)
	return cell
}

func writeMutatedTranscript(t *testing.T, path string, records []map[string]any, mutation dshTranscriptMutation) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}()
	for _, record := range records {
		copy := cloneJSONRecord(t, record)
		if !mutation.keep(copy) {
			continue
		}
		if mutation.mutate != nil {
			mutation.mutate(copy)
		}
		writeJSONRecord(t, f, copy)
	}
}

func cloneJSONRecord(t *testing.T, record map[string]any) map[string]any {
	t.Helper()
	b, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	var copy map[string]any
	if err := json.Unmarshal(b, &copy); err != nil {
		t.Fatal(err)
	}
	return copy
}

func writeJSONRecord(t *testing.T, f *os.File, record map[string]any) {
	t.Helper()
	b, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		t.Fatal(err)
	}
}

func assertTranscriptMutationFails(t *testing.T, cell, assertion string) {
	t.Helper()
	report, err := ValidateTranscriptForProfile(cell, matrix.ProfileCLILocal)
	if err != nil || report == nil || report.Pass {
		t.Fatalf("mutation did not fail: report=%+v err=%v", report, err)
	}
	for _, result := range report.Asserts {
		if result.Name == assertion && !result.OK {
			return
		}
	}
	t.Fatalf("mutation failed without %q: %+v", assertion, report.Asserts)
}

func mustCopy(t *testing.T, source, destination string) {
	t.Helper()
	b, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, b, 0644); err != nil {
		t.Fatal(err)
	}
}
