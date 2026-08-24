package filesystem

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"irrlicht/core/domain/session"
)

// vocabRecorder captures the unrecognized-state warning so the test can read
// the message the daemon would actually log.
type vocabRecorder struct{ msgs []string }

func (r *vocabRecorder) LogInfo(_, _, _ string)                                  {}
func (r *vocabRecorder) LogError(_, _, msg string)                               { r.msgs = append(r.msgs, msg) }
func (r *vocabRecorder) LogProcessingTime(_, _ string, _ int64, _ int, _ string) {}
func (r *vocabRecorder) Close() error                                            { return nil }

// warnOnUnknownState drives ListAll past one session file holding a state this
// build does not know, and returns the single warning it produced.
//
// It asserts the warning was actually emitted rather than returning an empty
// string on failure: absence of a finding and inability to look must not
// produce the same output, and every assertion below is on the message's
// CONTENT — so a silent no-warning path would make them all vacuously pass.
func warnOnUnknownState(t *testing.T) string {
	t.Helper()

	instances := t.TempDir()
	const junk = `{"version":1,"session_id":"s1","state":"zzz-not-a-state"}`
	if err := os.WriteFile(filepath.Join(instances, "s1.json"), []byte(junk), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	rec := &vocabRecorder{}
	repo := NewWithDir(instances)
	repo.SetLogger(rec)

	states, err := repo.ListAll()
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(states) != 0 {
		t.Fatalf("precondition: the unknown-state session must be skipped, got %d", len(states))
	}
	if len(rec.msgs) != 1 {
		t.Fatalf("precondition: expected exactly one warning, got %d (%v) — "+
			"the assertions below read the message, so no warning means they check nothing",
			len(rec.msgs), rec.msgs)
	}
	return rec.msgs[0]
}

// TestWarnUnknownState_NamesEveryCanonicalState is the #1798 carry-forward
// finding from #1797's QA, as a test.
//
// warnUnknownStateOnce used to build its message from a hand-typed
// "%s/%s/%s" with three constants passed positionally. That is a second,
// hand-maintained copy of the state vocabulary: adding a fourth state left the
// message asserting "this build knows only working/waiting/ready" while the
// build in fact knew four — a log line lying about the one thing it exists to
// report, to whoever is debugging a mixed-version install.
//
// The fix derives the list from session.CanonicalStates(), so this test is
// written against the VOCABULARY rather than against any particular spelling:
// it passes for three states, four, or five, and fails only when the message
// and the predicate disagree. That is the property, and it is why the
// assertion does not hardcode the expected sentence.
func TestWarnUnknownState_NamesEveryCanonicalState(t *testing.T) {
	msg := warnOnUnknownState(t)

	for _, state := range session.CanonicalStates() {
		if !strings.Contains(msg, state) {
			t.Errorf("warning omits canonical state %q — the message carries a hand-copied\n"+
				"vocabulary that has drifted from IsCanonicalState.\nmessage: %s", state, msg)
		}
	}
}

// TestWarnUnknownState_ClaimsNoStateItDoesNotKnow is the other half, and the
// half a "does it contain every state" check cannot catch on its own: a
// message built by concatenating a stale hardcoded list would still contain
// every canonical state if the list were merely a SUPERSET.
//
// It pins the count of listed states rather than the prose, so the two
// assertions together say "the message lists exactly the vocabulary" without
// freezing a sentence that a future edit may legitimately reword.
func TestWarnUnknownState_ClaimsNoStateItDoesNotKnow(t *testing.T) {
	msg := warnOnUnknownState(t)

	// The vocabulary is rendered as a single slash-joined run; find it and
	// compare it element-wise against the domain's own list.
	want := strings.Join(session.CanonicalStates(), "/")
	if !strings.Contains(msg, want) {
		t.Errorf("warning does not carry the vocabulary verbatim as %q — it was built from a\n"+
			"hand-maintained list rather than session.CanonicalStates().\nmessage: %s", want, msg)
	}
}
