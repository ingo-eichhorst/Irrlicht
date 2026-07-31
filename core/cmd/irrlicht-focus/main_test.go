package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// focusRecorder is a stand-in daemon that records the path it was POSTed to.
type focusRecorder struct {
	srv  *httptest.Server
	path string
}

// newFocusRecorder starts a loopback server answering 200 to any focus POST.
func newFocusRecorder(t *testing.T) *focusRecorder {
	t.Helper()
	rec := &focusRecorder{}
	rec.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// EscapedPath, not Path: the server decodes %2F back into Path, which
		// would hide whether the CLI escaped the session ID on the wire.
		rec.path = r.Method + " " + r.URL.EscapedPath()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(rec.srv.Close)
	return rec
}

// addr returns the host:port the recorder is listening on.
func (r *focusRecorder) addr() string {
	return strings.TrimPrefix(r.srv.URL, "http://")
}

// TestFocusTargetsBindAddrFromEnv is the defect test for #1215: a session
// monitored by a daemon on an alternate port must receive the focus request,
// instead of it going to whatever owns the default :7837.
func TestFocusTargetsBindAddrFromEnv(t *testing.T) {
	rec := newFocusRecorder(t)
	t.Setenv("IRRLICHT_BIND_ADDR", rec.addr())
	t.Setenv("IRRLICHT_HOME", t.TempDir())

	if code := run([]string{"sess-1215"}, io.Discard); code != 0 {
		t.Fatalf("run exit code = %d, want 0 (focus went somewhere other than the resolved bind address)", code)
	}
	if want := "POST /api/v1/sessions/sess-1215/focus"; rec.path != want {
		t.Errorf("daemon received %q, want %q", rec.path, want)
	}
}

// TestFocusFallsBackToAddrFile covers the invocation the env alone cannot
// reach: irrlicht-focus is typed in a shell that never exported
// IRRLICHT_BIND_ADDR (the macOS app does not launch it, so there is nothing to
// inherit). The running daemon's published addr file is the fallback.
func TestFocusFallsBackToAddrFile(t *testing.T) {
	rec := newFocusRecorder(t)
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "irrlichd.addr"), []byte(rec.addr()+"\n"), 0o600); err != nil {
		t.Fatalf("write addr file: %v", err)
	}
	t.Setenv("IRRLICHT_BIND_ADDR", "")
	t.Setenv("IRRLICHT_HOME", home)

	if code := run([]string{"sess-addrfile"}, io.Discard); code != 0 {
		t.Fatalf("run exit code = %d, want 0 (focus ignored the daemon's published addr file)", code)
	}
	if want := "POST /api/v1/sessions/sess-addrfile/focus"; rec.path != want {
		t.Errorf("daemon received %q, want %q", rec.path, want)
	}
}

// TestRunWarnsWhenItIgnoresTheEnvironment: a typo'd IRRLICHT_BIND_ADDR must
// not silently route the request to whichever daemon owns the default port.
func TestRunWarnsWhenItIgnoresTheEnvironment(t *testing.T) {
	rec := newFocusRecorder(t)
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "irrlichd.addr"), []byte(rec.addr()+"\n"), 0o600); err != nil {
		t.Fatalf("write addr file: %v", err)
	}
	t.Setenv("IRRLICHT_BIND_ADDR", "7838") // missing host — unusable
	t.Setenv("IRRLICHT_HOME", home)

	var stderr strings.Builder
	if code := run([]string{"sess-warn"}, &stderr); code != 0 {
		t.Fatalf("run exit code = %d, want 0", code)
	}
	if !strings.Contains(stderr.String(), "IRRLICHT_BIND_ADDR") {
		t.Errorf("stderr = %q, want it to name the variable it ignored", stderr.String())
	}
}

// TestFocusEscapesTheSessionID: a stray argument can only ever be a session ID
// the daemon does not know, never a different endpoint.
func TestFocusEscapesTheSessionID(t *testing.T) {
	rec := newFocusRecorder(t)
	t.Setenv("IRRLICHT_BIND_ADDR", rec.addr())
	t.Setenv("IRRLICHT_HOME", t.TempDir())

	if code := run([]string{"../../state"}, io.Discard); code != 0 {
		t.Fatalf("run exit code = %d, want 0", code)
	}
	if want := "POST /api/v1/sessions/..%2F..%2Fstate/focus"; rec.path != want {
		t.Errorf("daemon received %q, want %q", rec.path, want)
	}
}

// TestRunRejectsMissingSessionID locks the usage contract: no session ID is an
// exit-1 usage error, and nothing is sent.
func TestRunRejectsMissingSessionID(t *testing.T) {
	var stderr strings.Builder
	for _, args := range [][]string{{}, {""}} {
		if code := run(args, &stderr); code != 1 {
			t.Errorf("run(%q) exit code = %d, want 1", args, code)
		}
	}
	if !strings.Contains(stderr.String(), "usage: irrlicht-focus") {
		t.Errorf("stderr = %q, want a usage line", stderr.String())
	}
}

// TestRunReportsNotFound locks the exit-2 branch: the daemon answered, but the
// session is unknown to it.
func TestRunReportsNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	t.Setenv("IRRLICHT_BIND_ADDR", strings.TrimPrefix(srv.URL, "http://"))
	t.Setenv("IRRLICHT_HOME", t.TempDir())

	var stderr strings.Builder
	if code := run([]string{"ghost"}, &stderr); code != 2 {
		t.Fatalf("run exit code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "404") {
		t.Errorf("stderr = %q, want it to name the status code", stderr.String())
	}
}
