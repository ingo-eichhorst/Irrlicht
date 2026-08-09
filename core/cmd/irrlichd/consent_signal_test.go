package main

import (
	"bytes"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestConsentSignalReachesARunningDaemon: the happy path — the nudge is a POST
// to the reload route, and the CLI says so.
func TestConsentSignalReachesARunningDaemon(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	var out bytes.Buffer
	postConsentReload(&out, srv.URL+consentReloadPath, consentSignalTimeout)

	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotPath != consentReloadPath {
		t.Errorf("path = %q, want %q", gotPath, consentReloadPath)
	}
	if !strings.Contains(out.String(), "Notified the running daemon") {
		t.Errorf("output did not confirm the nudge: %q", out.String())
	}
}

// TestConsentSignalWithNoDaemonIsNotAnError is the constraint that keeps
// `--uninstall-hooks` usable without a daemon: an undeliverable signal reports
// itself and changes nothing. This function has no error return by design —
// the assertion is that it neither panics nor says anything alarming, and the
// exit code is decided by the uninstall, which is asserted end to end in
// TestUninstallHooksWithNoDaemonRunning.
func TestConsentSignalWithNoDaemonIsNotAnError(t *testing.T) {
	url := "http://" + closedLoopbackAddr(t) + consentReloadPath

	var out bytes.Buffer
	postConsentReload(&out, url, 500*time.Millisecond)

	got := out.String()
	if !strings.Contains(got, "No running daemon to notify") {
		t.Errorf("output = %q, want the no-daemon note", got)
	}
	// It must not read as a failure of the command that just succeeded.
	for _, alarming := range []string{"error", "failed", "Error", "Failed"} {
		if strings.Contains(got, alarming) {
			t.Errorf("no-daemon output reads as a failure (%q): %q", alarming, got)
		}
	}
}

// TestConsentSignalNonSuccessStatusIsReported: a daemon that answers but
// refuses gets a note pointing at the remedy, still without failing the
// command.
func TestConsentSignalNonSuccessStatusIsReported(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	defer srv.Close()

	var out bytes.Buffer
	postConsentReload(&out, srv.URL+consentReloadPath, consentSignalTimeout)

	got := out.String()
	if !strings.Contains(got, "403") || !strings.Contains(got, "restart it") {
		t.Errorf("output = %q, want the status and the remedy", got)
	}
}

// TestConsentSignalDoesNotHangOnAnUnresponsiveDaemon: the nudge is a courtesy
// at the tail of a command that has already done its work, so a daemon that
// accepts the connection and then says nothing must not hold the CLI open.
func TestConsentSignalDoesNotHangOnAnUnresponsiveDaemon(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	// Accept and never answer.
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			defer conn.Close()
		}
	}()

	done := make(chan struct{})
	go func() {
		var out bytes.Buffer
		postConsentReload(&out, "http://"+ln.Addr().String()+consentReloadPath, 300*time.Millisecond)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the consent nudge did not respect its timeout against an unresponsive daemon")
	}
}

// closedLoopbackAddr returns a loopback address nothing is listening on, by
// binding a port and immediately releasing it.
func closedLoopbackAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}
	return addr
}
