package validate

import (
	"path/filepath"
	"testing"
)

func TestValidateExpected_parentSessionSameAsRejectsWrongParent(t *testing.T) {
	dir := t.TempDir()
	writeRec(t, dir, "events.jsonl",
		`{"ts":"2026-01-01T00:00:00Z","kind":"transcript_new","session_id":"parent"}`+"\n"+
			`{"ts":"2026-01-01T00:00:01Z","kind":"transcript_new","session_id":"child"}`+"\n"+
			`{"ts":"2026-01-01T00:00:02Z","kind":"parent_linked","session_id":"child","parent_session_id":"wrong-parent"}`+"\n")
	mustWrite(t, filepath.Join(dir, "expected.jsonl"),
		`{"schema_version":1,"scenario_id":"test","source":"unit test"}`+"\n"+
			`{"phase":"parent","kind":"transcript_new","relative_to":"start","text":"parent appears"}`+"\n"+
			`{"phase":"child","kind":"transcript_new","relative_to":"parent","new_session":true,"text":"child appears"}`+"\n"+
			`{"phase":"linked","kind":"parent_linked","relative_to":"child","same_session_as":"child","parent_session_same_as":"parent","text":"child links to parent"}`+"\n")
	report, err := ValidateExpected(dir)
	if err != nil {
		t.Fatalf("ValidateExpected: %v", err)
	}
	if report.Pass {
		t.Fatal("expected wrong parent_session_id to fail validation")
	}
}
