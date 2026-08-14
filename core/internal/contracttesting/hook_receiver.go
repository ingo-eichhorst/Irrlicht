// hook_receiver.go holds the primitives every RECEIVING-side hook contract
// shares, so contract number four is a set of obligations rather than another
// copy of the request plumbing.
//
// The 2xx assertion in particular is here rather than in either contract: "a
// hook receiver reports a refusal through the log and the counter, never
// through a status code on the user's critical path" is one rule (#1361,
// #1364), and a rule stated in three places is a rule that can disagree with
// itself.
//
// Note which of the three take a reporter and which keeps a *testing.T. The
// first two are graded — assertHookStatus2xx decides a verdict and postHookBody
// only marks itself a helper — so both go through the seam and can be driven by
// a negative self-test (#1479, #1497). mkSubdir cannot: a directory that will
// not be created is a fixture that could not be BUILT, and recording that as
// "the obligation fired" is the failure reporter.go's fixtureT split exists to
// prevent. It is named in deferredToTheSeam for exactly that reason.
package contracttesting

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// postHookBody POSTs body to handler at endpoint and returns the response.
func postHookBody(t reporter, handler http.Handler, endpoint, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, endpoint, strings.NewReader(body))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

// assertHookStatus2xx checks the one status rule every hook receiver owes.
// what names the input, so the failure says which case broke it.
func assertHookStatus2xx(t reporter, rec *httptest.ResponseRecorder, what string) {
	t.Helper()
	if rec.Code < 200 || rec.Code > 299 {
		t.Errorf("%s: status = %d, want 2xx — a hook receiver reports a refusal through its log and its counters, never through a status code on the user's critical path", what, rec.Code)
	}
}

// mkSubdir creates and returns root/name. Transcripts sit some levels below the
// root in every real layout, so the fixtures do too.
func mkSubdir(t *testing.T, root, name string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("create %s: %v", dir, err)
	}
	return dir
}
