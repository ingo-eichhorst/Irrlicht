package filesystem_test

import (
	"strings"
	"testing"

	"irrlicht/core/adapters/outbound/filesystem"
	"irrlicht/core/domain/session"
	"irrlicht/core/internal/contracttesting"
)

// warnOnUnknownState drives ListAll past one session file holding a state this
// build does not know, and returns the single warning it produced.
//
// It asserts the warning was actually emitted rather than returning an empty
// string on failure: absence of a finding and inability to look must not
// produce the same output, and every assertion below is on the message's
// CONTENT — so a silent no-warning path would make them all vacuously pass.
func warnOnUnknownState(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	writeRawStateFile(t, dir, "s1.json", "zzz-not-a-state")

	logger := &contracttesting.RecordingLogger{}
	repo := filesystem.NewWithDir(dir)
	repo.SetLogger(logger)

	states, err := repo.ListAll()
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(states) != 0 {
		t.Fatalf("precondition: the unknown-state session must be skipped, got %d", len(states))
	}
	errs := logger.Errors()
	if len(errs) != 1 {
		t.Fatalf("precondition: expected exactly one warning, got %d (%v) — "+
			"the assertions below read the message, so no warning means they check nothing",
			len(errs), errs)
	}
	// The channel matters as much as the text: this is the tag a human greps
	// for, and RecordingLogger exposes it precisely so a test can assert it.
	if got := logger.EventTypes()[0]; got != "session_state_unrecognized" {
		t.Errorf("warning filed under event type %q, want session_state_unrecognized", got)
	}
	return errs[0]
}

// TestWarnUnknownState_ListsExactlyTheCanonicalVocabulary is the #1798
// carry-forward finding from #1797's QA, as a test.
//
// warnUnknownStateOnce used to build its message from a hand-typed "%s/%s/%s"
// with three constants passed positionally. That is a second, hand-maintained
// copy of the state vocabulary: adding a fourth state left the message
// asserting "this build knows only working/waiting/ready" while the build in
// fact knew four — a log line lying about the one thing it exists to report,
// to whoever is debugging a mixed-version install.
//
// The assertion is on the VOCABULARY rather than on any particular spelling,
// so it passes for three states, four, or five, and fails only when the message
// and session.CanonicalStates() disagree. Comparing the joined run in one shot
// is deliberately stronger than checking each state is present somewhere: a
// message built from a stale hardcoded list that happened to be a SUPERSET
// would satisfy a per-element check and fail this one.
func TestWarnUnknownState_ListsExactlyTheCanonicalVocabulary(t *testing.T) {
	msg := warnOnUnknownState(t)

	want := strings.Join(session.CanonicalStates(), "/")
	if !strings.Contains(msg, want) {
		t.Errorf("warning does not carry the vocabulary verbatim as %q — it was built from a\n"+
			"hand-maintained list rather than session.CanonicalStates().\nmessage: %s", want, msg)
	}
}
