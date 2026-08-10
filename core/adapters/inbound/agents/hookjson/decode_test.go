// decode_test.go exercises the chokepoint directly, across all four of its
// exits.
//
// It exists because DecodeConfined was the only file in this package without a
// sibling test, and because one of its branches — the nil-confiner guard — is
// reachable from no production call site at all: every constructor builds
// through NewHandler(transcriptConfiner(), …) and ConfinerForSource never
// returns nil. A guard no test can execute is a guard no mutation can redden,
// so replacing its body with `return true` would have left the whole suite
// green while doing precisely what it was written to prevent — dispatching an
// unconfined caller-supplied path because the confiner was missing. Calling it
// from here is what makes the fail-closed choice a pinned decision rather than
// an untested intention. (Found in review of #1389.)
//
// The success case asserts the property the doc comment calls the point of the
// mandatory `set`: the caller's RAW string is gone from the payload afterwards.
// Nothing else pins that directly — the receivers pin it only by consequence.
package hookjson

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

type decodeTestPayload struct {
	TranscriptPath string `json:"transcript_path"`
	Event          string `json:"hook_event_name"`
}

func getDecodePath(p *decodeTestPayload) string           { return p.TranscriptPath }
func setDecodePath(p *decodeTestPayload, confined string) { p.TranscriptPath = confined }

// decodeConfinerRooted builds a confiner over a real temp root, matching how
// the adapters build theirs (an absolute root, ".jsonl" leaves).
//
// No symlink pre-resolution of the root: containedIn resolves the DECLARED root
// itself and rebuilds the accepted path on the unresolved spelling, so a bare
// t.TempDir() confines correctly on darwin despite /var -> /private/var. The
// first draft carried an EvalSymlinks block here whose comment claimed the
// opposite; confine_test.go has passed a raw t.TempDir() to staticRoots all
// along, which is the evidence it was never needed.
func decodeConfinerRooted(t *testing.T) (*PathConfiner, string) {
	t.Helper()
	root := t.TempDir()
	return staticRoots(root), root
}

func postDecode(t *testing.T, body string) *http.Request {
	t.Helper()
	return httptest.NewRequest(http.MethodPost, "/api/v1/hooks/test", strings.NewReader(body))
}

func TestDecodeConfined_AcceptsInTreePathAndErasesTheRawString(t *testing.T) {
	confiner, root := decodeConfinerRooted(t)
	transcript := filepath.Join(root, "session.jsonl")
	if err := os.WriteFile(transcript, []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	// The raw path is spelled with a redundant "." segment so it is not
	// byte-identical to the confined result. Without that, "the raw string was
	// replaced" and "nothing happened" look the same.
	//
	// Concatenated, NOT filepath.Join'd: Join cleans, so Join(root, ".", x)
	// returns exactly the confined string and the assertion below discriminates
	// nothing. The first draft did that, and a deliberate mutation removing the
	// set() call left this test GREEN — the case passed for the wrong reason.
	raw := root + "/./session.jsonl"
	rec := httptest.NewRecorder()
	var p decodeTestPayload
	log := &countingLogger{}

	ok := DecodeConfined(rec, postDecode(t, `{"transcript_path":`+strconv.Quote(raw)+`,"hook_event_name":"Stop"}`),
		log, "test-receiver", confiner, &p, getDecodePath, setDecodePath)

	if !ok {
		t.Fatalf("in-tree transcript refused; status=%d, logged %d line(s)", rec.Code, len(log.lines))
	}
	if p.Event != "Stop" {
		t.Errorf("hook_event_name = %q, want %q — the payload was not decoded", p.Event, "Stop")
	}
	if p.TranscriptPath != transcript {
		t.Errorf("payload path = %q, want the CONFINED path %q — set must overwrite the caller's raw string, "+
			"so a downstream line reaching for payload.TranscriptPath cannot pick the unconfined one back up",
			p.TranscriptPath, transcript)
	}
	if n := confiner.RejectionCount(); n != 0 {
		t.Errorf("in-tree path counted %d rejection(s), want 0", n)
	}
}

func TestDecodeConfined_UndecodableBodyIs400(t *testing.T) {
	confiner, _ := decodeConfinerRooted(t)
	rec := httptest.NewRecorder()
	var p decodeTestPayload

	ok := DecodeConfined(rec, postDecode(t, `{"transcript_path": NOT JSON`),
		&countingLogger{}, "test-receiver", confiner, &p, getDecodePath, setDecodePath)

	if ok {
		t.Fatal("undecodable body reported as decoded")
	}
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
	if n := confiner.RejectionCount(); n != 0 {
		t.Errorf("a body that never decoded counted %d confinement rejection(s), want 0 — "+
			"the path was never examined", n)
	}
}

func TestDecodeConfined_OutOfTreePathIsRefusedCountedAnd2xx(t *testing.T) {
	confiner, _ := decodeConfinerRooted(t)
	outside := filepath.Join(t.TempDir(), "elsewhere.jsonl")
	if err := os.WriteFile(outside, []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	rec := httptest.NewRecorder()
	var p decodeTestPayload
	log := &countingLogger{}

	ok := DecodeConfined(rec, postDecode(t, `{"transcript_path":`+strconv.Quote(outside)+`}`),
		log, "test-receiver", confiner, &p, getDecodePath, setDecodePath)

	if ok {
		t.Fatal("out-of-tree path reported as accepted")
	}
	if rec.Code < 200 || rec.Code > 299 {
		t.Errorf("status = %d, want 2xx — a confinement refusal is reported by the log and the counter, "+
			"never by a status code on the user's critical path (#1361, #1364)", rec.Code)
	}
	if n := confiner.RejectionCount(); n != 1 {
		t.Errorf("counted %d rejection(s), want 1", n)
	}
	// Which message, not merely that one exists: the log is the only record of
	// what a local process tried, so a refusal reported under some other line
	// would leave the counter as the sole evidence.
	if n := log.mentioning("rejected hook transcript_path"); n != 1 {
		t.Errorf("refusal logged %d time(s) naming the path, want 1", n)
	}
	// The decode ran before the confinement, so the payload still holds the
	// CALLER'S RAW string here — set deliberately did not run. That is safe
	// only because ok==false obliges the caller to return without dispatching,
	// and it is worth pinning explicitly: a future refactor that moved set
	// above the confinement check would leave a confined-looking payload on a
	// refused path, and a future one that cleared the field would hide the raw
	// value from RejectPath's log, which is the only record of what was tried.
	if p.TranscriptPath != outside {
		t.Errorf("payload path = %q, want the untouched raw %q — on a refusal set must NOT run, "+
			"and the refused value must stay legible", p.TranscriptPath, outside)
	}
}

// TestDecodeConfined_NilConfinerFailsClosed is the reason this file exists: the
// branch has no production caller, so without an explicit test it can never be
// seen red.
func TestDecodeConfined_NilConfinerFailsClosed(t *testing.T) {
	rec := httptest.NewRecorder()
	var p decodeTestPayload
	log := &countingLogger{}

	ok := DecodeConfined(rec, postDecode(t, `{"transcript_path":"/anywhere/at/all.jsonl"}`),
		log, "test-receiver", nil, &p, getDecodePath, setDecodePath)

	if ok {
		t.Fatal("a receiver with NO confiner reported its payload as usable — that dispatches an " +
			"unconfined caller-supplied path because the guard itself was missing, which is the #1361 defect")
	}
	if rec.Code < 200 || rec.Code > 299 {
		t.Errorf("status = %d, want 2xx — same rule as any other refusal", rec.Code)
	}
	if p.TranscriptPath != "" {
		t.Errorf("payload path = %q, want empty — nothing may be decoded without a confiner", p.TranscriptPath)
	}
	if n := log.mentioning("no path confiner"); n != 1 {
		t.Errorf("the drop was reported %d time(s) naming the missing confiner, want 1 — "+
			"a silent drop is indistinguishable from health", n)
	}
}

// TestDecodeConfined_DisagreeingGetSetFailsClosed pins the postcondition.
//
// A receiver whose set writes a field its get never reads — or a no-op set,
// which is a one-character edit — leaves the caller's raw string in the payload
// while DecodeConfined reports success. Review of #1389 demonstrated exactly
// that against claudecode and the whole suite stayed green, because
// AssertHookPathConfined asserts THAT a receiver dispatched, never WHICH
// spelling. The chokepoint checks it so no call site has to be trusted.
func TestDecodeConfined_DisagreeingGetSetFailsClosed(t *testing.T) {
	confiner, root := decodeConfinerRooted(t)
	transcript := filepath.Join(root, "session.jsonl")
	if err := os.WriteFile(transcript, []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	raw := root + "/./session.jsonl"

	rec := httptest.NewRecorder()
	var p decodeTestPayload
	log := &countingLogger{}

	ok := DecodeConfined(rec, postDecode(t, `{"transcript_path":`+strconv.Quote(raw)+`}`),
		log, "test-receiver", confiner, &p, getDecodePath,
		func(*decodeTestPayload, string) {}) // the no-op set

	if ok {
		t.Fatal("a receiver whose set does not write the field its get reads reported success — " +
			"the payload still holds the caller's unconfined string and the receiver would forward it")
	}
	if rec.Code < 200 || rec.Code > 299 {
		t.Errorf("status = %d, want 2xx — same rule as any other refusal", rec.Code)
	}
	if n := log.mentioning("get/set pair"); n != 1 {
		t.Errorf("mismatch reported %d time(s), want 1 — a silent drop is indistinguishable from health", n)
	}
}

// TestDecodeConfined_OversizedBodyIsRefused pins the MaxBytesReader bound: these
// endpoints are unauthenticated and local, so an unbounded decode lets any local
// process make the daemon allocate without limit.
func TestDecodeConfined_OversizedBodyIsRefused(t *testing.T) {
	confiner, root := decodeConfinerRooted(t)
	transcript := filepath.Join(root, "session.jsonl")
	if err := os.WriteFile(transcript, []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	// Valid JSON naming an in-tree path, padded past the limit with a field the
	// payload ignores — so the ONLY reason it can fail is the size bound.
	body := `{"transcript_path":` + strconv.Quote(transcript) +
		`,"pad":"` + strings.Repeat("x", maxHookBodyBytes+1) + `"}`
	rec := httptest.NewRecorder()
	var p decodeTestPayload

	ok := DecodeConfined(rec, postDecode(t, body), &countingLogger{}, "test-receiver",
		confiner, &p, getDecodePath, setDecodePath)

	if ok {
		t.Fatalf("a %d-byte body was accepted; the %d-byte bound did not apply", len(body), maxHookBodyBytes)
	}
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 — an over-long body is a malformed request", rec.Code)
	}

	// The vacuity guard: the same body under the limit must be ACCEPTED, or
	// this test would pass against a decoder that refuses everything.
	small := `{"transcript_path":` + strconv.Quote(transcript) + `,"pad":"` + strings.Repeat("x", 32) + `"}`
	rec2 := httptest.NewRecorder()
	var p2 decodeTestPayload
	if !DecodeConfined(rec2, postDecode(t, small), &countingLogger{}, "test-receiver",
		confiner, &p2, getDecodePath, setDecodePath) {
		t.Fatalf("an in-tree body well under the bound was refused (status %d)", rec2.Code)
	}
}
