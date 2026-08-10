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
	"runtime"
	"strings"
	"testing"
)

type decodeTestPayload struct {
	TranscriptPath string `json:"transcript_path"`
	Event          string `json:"hook_event_name"`
}

func getDecodePath(p *decodeTestPayload) string           { return p.TranscriptPath }
func setDecodePath(p *decodeTestPayload, confined string) { p.TranscriptPath = confined }

// decodeLogger records LogError calls so a refusal can be asserted to have been
// reported, not merely to have happened.
type decodeLogger struct{ errs []string }

func (l *decodeLogger) LogInfo(eventType, sessionID, msg string)  {}
func (l *decodeLogger) LogError(eventType, sessionID, msg string) { l.errs = append(l.errs, msg) }
func (l *decodeLogger) LogProcessingTime(eventType, sessionID string, ms int64, size int, result string) {
}
func (l *decodeLogger) Close() error { return nil }

// decodeConfinerRooted builds a confiner over a real temp root, matching how
// the adapters build theirs (an absolute root, ".jsonl" leaves).
func decodeConfinerRooted(t *testing.T) (*PathConfiner, string) {
	t.Helper()
	root := t.TempDir()
	if runtime.GOOS == "darwin" {
		// /var is a symlink to /private/var; the confiner resolves symlinks
		// before containment, so the declared root must be the resolved one or
		// every in-tree path reads as an escape.
		if resolved, err := filepath.EvalSymlinks(root); err == nil {
			root = resolved
		}
	}
	return NewPathConfiner(func() []string { return []string{root} }, ".jsonl"), root
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
	log := &decodeLogger{}

	ok := DecodeConfined(rec, postDecode(t, `{"transcript_path":`+quote(raw)+`,"hook_event_name":"Stop"}`),
		log, "test-receiver", confiner, &p, getDecodePath, setDecodePath)

	if !ok {
		t.Fatalf("in-tree transcript refused; status=%d logs=%v", rec.Code, log.errs)
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
		&decodeLogger{}, "test-receiver", confiner, &p, getDecodePath, setDecodePath)

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
	log := &decodeLogger{}

	ok := DecodeConfined(rec, postDecode(t, `{"transcript_path":`+quote(outside)+`}`),
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
	if len(log.errs) == 0 {
		t.Error("refusal was not logged")
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
	log := &decodeLogger{}

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
	if len(log.errs) == 0 {
		t.Error("a dropped body was not reported; a silent drop is indistinguishable from health")
	}
}

// quote renders s as a JSON string literal without pulling encoding/json into
// this file — the architecture rule in core/architecture_hookbody_test.go does
// not read test files, but keeping the import out avoids any suggestion that
// this test is a second decode path.
func quote(s string) string {
	return `"` + strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), `"`, `\"`) + `"`
}
