package groundtruth

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestFromSidecar_basic(t *testing.T) {
	start := time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC)
	startMs := start.UnixMilli()
	sidecar := strings.NewReader(strings.Join([]string{
		"# comment line is ignored",
		"random chatter without gt: prefix",
		(itoa64(startMs)) + " gt:driver_send_prompt_1 working",
		(itoa64(startMs+1500)) + " gt:turn_done_1 ready 500 transcript_field_value end_turn observed",
		(itoa64(startMs+2500)) + " gt:driver_send_prompt_2 working",
	}, "\n"))

	meta, labels, err := FromSidecar(sidecar, "claudecode", "multi-turn-conversation", start)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Agent != "claudecode" || meta.Scenario != "multi-turn-conversation" {
		t.Errorf("meta wrong: %+v", meta)
	}
	if len(labels) != 3 {
		t.Fatalf("want 3 labels, got %d", len(labels))
	}
	if labels[0].Marker != "driver_send_prompt_1" || labels[0].ExpectedState != "working" || labels[0].TsOffsetMs != 0 {
		t.Errorf("label 0 wrong: %+v", labels[0])
	}
	if labels[1].ToleranceMs != 500 || labels[1].EvidenceKind != "transcript_field_value" || labels[1].Notes != "end_turn observed" {
		t.Errorf("label 1 wrong: %+v", labels[1])
	}
	if labels[2].TsOffsetMs != 2500 {
		t.Errorf("label 2 ts wrong: %d", labels[2].TsOffsetMs)
	}
}

func TestFromSidecar_rejectsBadState(t *testing.T) {
	start := time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC)
	sidecar := strings.NewReader(itoa64(start.UnixMilli()) + " gt:x bogus")
	_, _, err := FromSidecar(sidecar, "a", "s", start)
	if err == nil {
		t.Fatal("expected error on bad state")
	}
}

func TestWriteRead_roundtrip(t *testing.T) {
	meta := Meta{
		SchemaVersion:      1,
		Agent:              "claudecode",
		Scenario:           "test",
		RecordingStartedAt: time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC),
	}
	labels := []Label{
		{TsOffsetMs: 0, Marker: "send", ExpectedState: "working", EvidenceKind: "driver_emitted"},
		{TsOffsetMs: 1500, Marker: "done", ExpectedState: "ready", ToleranceMs: 500, EvidenceKind: "transcript_field_value"},
	}
	var buf bytes.Buffer
	if err := Write(&buf, meta, labels); err != nil {
		t.Fatal(err)
	}
	gotMeta, gotLabels, err := Read(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if gotMeta.Agent != meta.Agent || gotMeta.Scenario != meta.Scenario {
		t.Errorf("meta drift: %+v vs %+v", gotMeta, meta)
	}
	if len(gotLabels) != 2 {
		t.Fatalf("want 2 labels, got %d", len(gotLabels))
	}
	if gotLabels[1].ToleranceMs != 500 || gotLabels[1].ExpectedState != "ready" {
		t.Errorf("label 1 drift: %+v", gotLabels[1])
	}
}

func TestRead_noMetaHeaderIsTolerated(t *testing.T) {
	// File with only labels, no meta header.
	content := `{"ts_offset_ms":0,"marker":"x","expected_state":"working"}` + "\n" +
		`{"ts_offset_ms":1000,"marker":"y","expected_state":"ready"}` + "\n"
	_, labels, err := Read(strings.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	if len(labels) != 2 {
		t.Errorf("want 2 labels, got %d", len(labels))
	}
}

// itoa64 — small helper to keep test setup terse.
func itoa64(n int64) string {
	return strings.TrimSpace(stringInt64(n))
}

func stringInt64(n int64) string {
	if n == 0 {
		return "0"
	}
	negative := n < 0
	if negative {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if negative {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
