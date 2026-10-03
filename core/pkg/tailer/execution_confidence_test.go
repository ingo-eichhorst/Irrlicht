package tailer

import (
	"os"
	"slices"
	"strings"
	"testing"

	"irrlicht/core/domain/session"
	"irrlicht/core/pkg/capacity"
)

// hedgeParser is a minimal parser that feeds each assistant line's text
// through session.MeasureHedging — what every adapter parser does at its
// full-text call site — so these tests drive the tailer's real accumulation
// path (applyAssistantTextAndMarkers → surfaceSporadicMetrics → ledger).
type hedgeParser struct{}

func (hedgeParser) ParseLine(raw map[string]interface{}) *ParsedEvent {
	ev := &ParsedEvent{Timestamp: ParseTimestamp(raw)}
	switch raw["type"] {
	case "user":
		ev.EventType = "user"
		ev.ClearToolNames = true
	case "assistant":
		text, _ := raw["text"].(string)
		ev.EventType = "assistant"
		ev.AssistantText = TruncateAssistantText(text)
		ev.Hedge = session.MeasureHedging(text)
		ev.HedgeKey, _ = raw["key"].(string)
	default:
		ev.Skip = true
	}
	return ev
}

const (
	tailerHedgeHeavy = "I'm not sure what this ticket wants. The requirement is ambiguous — " +
		"it could mean the list view or the detail view, and it's unclear which one. " +
		"Maybe the list view? I might be wrong, though. I don't know which approach is right."
	tailerDecisive = "I'll update the parser to read the new field, then add a test that " +
		"feeds it a recorded transcript. The fix goes in parseAssistant. Running the suite next."
)

func hedgeTranscript(t *testing.T, msgs ...string) string {
	t.Helper()
	lines := []map[string]interface{}{{"type": "user", "timestamp": ts(0)}}
	for i, m := range msgs {
		lines = append(lines, map[string]interface{}{"type": "assistant", "timestamp": ts(i + 1), "text": m})
	}
	return writeTranscriptLines(t, lines)
}

func newHedgeTailer(path string) *TranscriptTailer {
	tl := NewTranscriptTailer(path, hedgeParser{}, "claude-code")
	tl.capacityMgr = capacity.NewForTest(testCapacityFixture)
	return tl
}

func tailerScore(t *testing.T, m *SessionMetrics) int {
	t.Helper()
	s, ok := m.ExecutionConfidence.Score()
	if !ok {
		t.Fatal("tailer metrics carry no execution-confidence score")
	}
	return s
}

func repeatMsg(msg string, n int) []string { return slices.Repeat([]string{msg}, n) }

// The tailer decays by assistant MESSAGE: five hedge-heavy messages followed
// by fifteen decisive ones must recover above the low threshold, end to end
// through TailAndProcess.
func TestTailer_ExecutionConfidenceRecoversAfterDecisiveMessages(t *testing.T) {
	early, err := newHedgeTailer(hedgeTranscript(t, repeatMsg(tailerHedgeHeavy, 5)...)).TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if got := tailerScore(t, early); got >= session.ExecutionConfidenceLowThreshold {
		t.Fatalf("precondition: 5 hedge-heavy messages scored %d, want below %d", got, session.ExecutionConfidenceLowThreshold)
	}

	msgs := append(repeatMsg(tailerHedgeHeavy, 5), repeatMsg(tailerDecisive, 15)...)
	m, err := newHedgeTailer(hedgeTranscript(t, msgs...)).TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if got := tailerScore(t, m); got < session.ExecutionConfidenceLowThreshold {
		t.Errorf("5 hedge-heavy then 15 decisive messages scored %d, want >= %d (decay must let a recovered session go high)",
			got, session.ExecutionConfidenceLowThreshold)
	}
	if m.ExecutionConfidence.Messages != 20 {
		t.Errorf("Messages = %d, want 20 (one per assistant line; user lines are not messages)", m.ExecutionConfidence.Messages)
	}
}

// The accumulator survives a daemon restart: a tailer rehydrated from the
// ledger that resumes at EOF (zero new lines) reports the same score, and
// re-persists it so a second restart survives too.
func TestLedger_PersistsExecutionConfidence(t *testing.T) {
	path := hedgeTranscript(t, tailerDecisive, tailerHedgeHeavy, tailerHedgeHeavy)
	tl1 := newHedgeTailer(path)
	m1, err := tl1.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	want := tailerScore(t, m1)

	ledger := tl1.GetLedgerState()
	if ledger.ExecutionConfidence == nil {
		t.Fatal("ledger carries no ExecutionConfidence")
	}

	tl2 := newHedgeTailer(path)
	tl2.SetLedgerState(ledger)
	m2, err := tl2.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if got := tailerScore(t, m2); got != want {
		t.Errorf("post-restart score = %d, want %d (resume-at-EOF must keep the score)", got, want)
	}
	if got := tl2.GetLedgerState().ExecutionConfidence; got == nil || *got != *ledger.ExecutionConfidence {
		t.Errorf("re-persisted accumulator = %+v, want %+v", got, ledger.ExecutionConfidence)
	}

	// The ledger hands out a copy: a later pass must not mutate a snapshot
	// already returned.
	before := *ledger.ExecutionConfidence
	tl1.executionConfidence.Observe(&session.HedgeSample{Weight: 9, Words: 9})
	if *ledger.ExecutionConfidence != before {
		t.Error("GetLedgerState aliased the live accumulator")
	}
}

// A session with no assistant prose has no score and writes none to the ledger.
func TestTailer_NoAssistantProseNoExecutionConfidence(t *testing.T) {
	tl := newHedgeTailer(hedgeTranscript(t))
	m, err := tl.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := m.ExecutionConfidence.Score(); ok {
		t.Error("a transcript with no assistant prose produced a score")
	}
	if tl.GetLedgerState().ExecutionConfidence != nil {
		t.Error("ledger carries an accumulator for a session with no scored message")
	}
}

// A rotation, truncation or in-place rewrite re-reads the transcript from byte
// 0, so the accumulator must start over with it: otherwise every surviving
// message is observed twice and prose a rewind deleted keeps counting
// (#737 review, finding 1). After the rewrite the score must equal what a
// fresh tailer reads from the same file.
func TestTailer_ExecutionConfidenceResetsOnRewrite(t *testing.T) {
	path := hedgeTranscript(t, tailerHedgeHeavy, tailerHedgeHeavy, tailerDecisive)
	tl := newHedgeTailer(path)
	if _, err := tl.TailAndProcess(); err != nil {
		t.Fatal(err)
	}

	// Rewrite in place to a single decisive message, padded so the file does
	// not shrink below the consumed offset (the #1104 rewrite-detection path).
	lines := []map[string]interface{}{
		{"type": "user", "timestamp": ts(0)},
		{"type": "assistant", "timestamp": ts(1), "text": tailerDecisive},
		{"type": "padding", "pad": strings.Repeat("x", 2000)},
	}
	rewritten := writeTranscriptLines(t, lines)
	data, err := os.ReadFile(rewritten)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	m, err := tl.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := newHedgeTailer(path).TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if m.ExecutionConfidence != fresh.ExecutionConfidence {
		t.Errorf("after an in-place rewrite the accumulator is %+v, a fresh tailer reads %+v — "+
			"the rotation reset must clear it", m.ExecutionConfidence, fresh.ExecutionConfidence)
	}
}

// A message re-emitted under the same HedgeKey with an identical sample is
// observed once; a changed sample under the same key, or no key at all, is a
// new observation (#737 review, finding 2).
func TestTailer_ExecutionConfidenceDedupesKeyedReemission(t *testing.T) {
	line := func(i int, key, text string) map[string]interface{} {
		m := map[string]interface{}{"type": "assistant", "timestamp": ts(i), "text": text}
		if key != "" {
			m["key"] = key
		}
		return m
	}
	cases := []struct {
		name  string
		lines []map[string]interface{}
		want  int
	}{
		{"same key, same text", []map[string]interface{}{line(1, "m1", tailerHedgeHeavy), line(2, "m1", tailerHedgeHeavy), line(3, "m1", tailerHedgeHeavy)}, 1},
		// A LOCK on an accepted limitation, not a requirement: a keyed
		// message whose text grew is observed again (see observeHedge).
		{"same key, grown text", []map[string]interface{}{line(1, "m1", "I'm not sure."), line(2, "m1", "I'm not sure. Now I am.")}, 2},
		{"distinct keys", []map[string]interface{}{line(1, "m1", tailerHedgeHeavy), line(2, "m2", tailerHedgeHeavy)}, 2},
		{"no key", []map[string]interface{}{line(1, "", tailerHedgeHeavy), line(2, "", tailerHedgeHeavy)}, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, err := newHedgeTailer(writeTranscriptLines(t, tc.lines)).TailAndProcess()
			if err != nil {
				t.Fatal(err)
			}
			if m.ExecutionConfidence.Messages != tc.want {
				t.Errorf("Messages = %d, want %d", m.ExecutionConfidence.Messages, tc.want)
			}
		})
	}
}
