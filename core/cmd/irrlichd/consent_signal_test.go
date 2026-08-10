package main

import (
	"bytes"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"irrlicht/core/domain/config"
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
	postConsentReload(&out, srv.URL+consentReloadPath, consentSignalTimeout, hooksNoun)

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
	postConsentReload(&out, url, 500*time.Millisecond, hooksNoun)

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
	postConsentReload(&out, srv.URL+consentReloadPath, consentSignalTimeout, hooksNoun)

	got := out.String()
	if !strings.Contains(got, "403") || !strings.Contains(got, "restart it") {
		t.Errorf("output = %q, want the status and the remedy", got)
	}
}

// TestConsentSignalNamesTheContentTheCallerRemoved is #1437's copy regression.
// Both uninstall commands reach this one endpoint, and its two advisory lines
// tell the user what to go and check — so a --uninstall-task-eta run that says
// "hooks" points them at ~/.claude/settings.json when the content at risk is
// the managed blocks in their own CLAUDE.md.
//
// Red before the noun was threaded through: the grant-all arm printed "it will
// re-install these hooks" verbatim on the instructions path, reproduced against
// a live grant-all daemon.
//
// Table-driven over both arms, because they are two separate hardcoded strings
// and fixing one is the easy way to miss the other.
func TestConsentSignalNamesTheContentTheCallerRemoved(t *testing.T) {
	grantAll := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"mode":"` + config.PermissionModeGrantAll + `"}`))
	})
	refuse := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	})

	for _, arm := range []struct {
		name    string
		handler http.Handler
	}{
		{"grant-all warning", grantAll},
		{"non-success status", refuse},
	} {
		t.Run(arm.name, func(t *testing.T) {
			srv := httptest.NewServer(arm.handler)
			defer srv.Close()

			var out bytes.Buffer
			postConsentReload(&out, srv.URL+consentReloadPath, consentSignalTimeout, instructionsNoun)

			got := out.String()
			if !strings.Contains(got, instructionsNoun.content) {
				t.Errorf("output = %q, want it to name %q", got, instructionsNoun.content)
			}
			// The specific wrong answer, named: the shared endpoint's copy used
			// to be hooks-only, and "does not say hooks" is the assertion that
			// fails if somebody hardcodes it back.
			if strings.Contains(got, "hooks") {
				t.Errorf("output = %q — --uninstall-task-eta must not send the user to inspect their hook config", got)
			}
		})
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
	var conns []net.Conn
	var connMu sync.Mutex
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			// Held, not closed per-iteration: the point is a daemon that
			// accepts and never answers. Closed in the cleanup below rather
			// than by a defer inside the loop, which would only run at
			// goroutine exit anyway.
			connMu.Lock()
			conns = append(conns, conn)
			connMu.Unlock()
		}
	}()
	t.Cleanup(func() {
		connMu.Lock()
		defer connMu.Unlock()
		for _, c := range conns {
			_ = c.Close()
		}
	})

	done := make(chan struct{})
	go func() {
		var out bytes.Buffer
		postConsentReload(&out, "http://"+ln.Addr().String()+consentReloadPath, 300*time.Millisecond, hooksNoun)
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

// TestConsentSignalWarnsWhenTheDaemonIsInGrantAllMode: grant-all is the one
// mode where ReloadFromStore is a deliberate no-op, so the daemon will
// re-install the entries the user just removed. The CLI must say so instead of
// printing its success line — this is the mode in which the mechanism cannot
// work, and it is also the mode the onboarding recording rig runs in against
// the user's real home.
func TestConsentSignalWarnsWhenTheDaemonIsInGrantAllMode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"mode":"grant-all","agents":[]}`))
	}))
	defer srv.Close()

	var out bytes.Buffer
	postConsentReload(&out, srv.URL+consentReloadPath, consentSignalTimeout, hooksNoun)

	got := out.String()
	if strings.Contains(got, "Notified the running daemon to reload consent") {
		t.Errorf("grant-all reported as a successful reload: %q", got)
	}
	if !strings.Contains(got, "IRRLICHT_PERMISSION_MODE") || !strings.Contains(got, "re-install") {
		t.Errorf("output = %q, want a warning naming the mode and the consequence", got)
	}
}

// TestConsentSignalAskModeStillReportsSuccess is the vacuity guard for the
// test above: a handler that answered anything at all would otherwise satisfy
// "did not print success".
func TestConsentSignalAskModeStillReportsSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"mode":"ask","agents":[]}`))
	}))
	defer srv.Close()

	var out bytes.Buffer
	postConsentReload(&out, srv.URL+consentReloadPath, consentSignalTimeout, hooksNoun)

	if !strings.Contains(out.String(), "Notified the running daemon to reload consent") {
		t.Errorf("ask mode did not report success: %q", out.String())
	}
}

// TestConsentSignalSkipsWhenNothingNamesADaemon pins the gate that keeps an
// uninstall in an isolated tree from reaching into an unrelated daemon.
//
// Without it the address ladder falls through to the default port 7837 — so a
// test, or a user running against a temp IRRLICHT_HOME, POSTed to whatever
// production daemon happened to be listening. The gate is what makes the
// hermeticity claim in uninstall_hooks_live_daemon_test.go true.
//
// It asserts the reported outcome rather than "nothing was hit", deliberately:
// the only way to observe the ungated behaviour is to have something listening
// on the default port, and standing a listener up there is the very thing this
// test exists to stop happening. The gate's own three-rung logic is covered in
// daemonaddr (TestClientTargetsANamedDaemon); the two tests below are the
// vacuity guards that it does not simply always skip.
func TestConsentSignalSkipsWhenNothingNamesADaemon(t *testing.T) {
	t.Setenv("IRRLICHT_HOME", t.TempDir()) // no addr file under it
	t.Setenv("IRRLICHT_BIND_ADDR", "")

	var out bytes.Buffer
	notifyDaemonConsentChanged(&out, hooksNoun)

	if !strings.Contains(out.String(), "No running daemon to notify") {
		t.Errorf("output = %q, want the no-daemon note", out.String())
	}
}

// TestConsentSignalSendsWhenAnAddressIsPublished — the addr-file rung.
func TestConsentSignalSendsWhenAnAddressIsPublished(t *testing.T) {
	srv, hit := reloadStubDaemon(t)

	home := t.TempDir()
	t.Setenv("IRRLICHT_HOME", home)
	t.Setenv("IRRLICHT_BIND_ADDR", "")
	addr := strings.TrimPrefix(srv.URL, "http://")
	if err := os.WriteFile(filepath.Join(home, "irrlichd.addr"), []byte(addr+"\n"), 0o600); err != nil {
		t.Fatalf("write addr file: %v", err)
	}

	var out bytes.Buffer
	notifyDaemonConsentChanged(&out, hooksNoun)

	select {
	case <-hit:
	default:
		t.Fatalf("the nudge was not delivered to the daemon that published an address; output = %q", out.String())
	}
}

// TestConsentSignalSendsWhenBindAddrNamesADaemon — the explicit-configuration
// rung, which daemonaddr documents as the HIGHEST-trust source.
//
// An earlier version of the gate stat'd the addr file directly and so silently
// discarded IRRLICHT_BIND_ADDR: a developer pointing the CLI at a dev daemon
// got "No running daemon to notify" from a daemon that was listening and would
// have answered. Reading the resolution's own provenance instead of
// re-deriving one rung of it is what fixes that.
func TestConsentSignalSendsWhenBindAddrNamesADaemon(t *testing.T) {
	srv, hit := reloadStubDaemon(t)

	t.Setenv("IRRLICHT_HOME", t.TempDir()) // deliberately no addr file
	t.Setenv("IRRLICHT_BIND_ADDR", strings.TrimPrefix(srv.URL, "http://"))

	var out bytes.Buffer
	notifyDaemonConsentChanged(&out, hooksNoun)

	select {
	case <-hit:
	default:
		t.Fatalf("an explicitly configured IRRLICHT_BIND_ADDR was ignored; output = %q", out.String())
	}
}

// reloadStubDaemon stands up a daemon that answers the reload route with an
// ask-mode snapshot, and a channel that receives once it is hit.
func reloadStubDaemon(t *testing.T) (*httptest.Server, <-chan struct{}) {
	t.Helper()
	hit := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == consentReloadPath {
			select {
			case hit <- struct{}{}:
			default:
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"mode":"ask","agents":[]}`))
	}))
	t.Cleanup(srv.Close)
	return srv, hit
}
