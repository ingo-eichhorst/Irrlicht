package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"irrlicht/core/pkg/daemonaddr"
)

// consentReloadPath is the daemon route that re-reads permissions.json into
// the running service (#1425).
const consentReloadPath = "/api/v1/permissions/reload"

// consentSignalTimeout bounds the whole nudge. Short on purpose: this is a
// courtesy at the tail of a command that has already done its work, and the
// call is loopback to a process that either answers immediately or is not
// there. Nothing downstream waits on the answer.
const consentSignalTimeout = 2 * time.Second

// notifyDaemonConsentChanged tells a running daemon that permissions.json has
// changed underneath it, so it re-reads the store instead of acting on the
// copy it loaded at startup (#1425).
//
// Why this exists at all: `--uninstall-hooks` runs in its own process. It
// removes the entries and records the opt-out, but a daemon that is already
// running loaded the consent store once, at startup, and never looks again —
// so #1372's re-verification loop asks a gate that still says "granted" and
// re-installs the entries within one interval.
//
// **A signal that cannot be delivered is not an error.** `--uninstall-hooks`
// worked with no daemon running before this existed and has to keep working:
// the store on disk already says "denied", so the next daemon start reads the
// correct value with or without the nudge. Every failure path here is one
// informational line and no change to the exit code — the caller's success is
// decided by the uninstall, never by whether something was listening.
//
// ClientURL, not LocalURL: this runs in a separate process from the daemon, so
// the port has to come from the addr file the running daemon published, not
// from what this process would itself bind. Same reasoning as the #1373
// beacon.
func notifyDaemonConsentChanged(w io.Writer) {
	if warning := daemonaddr.ClientConfigWarning(); warning != "" {
		fmt.Fprintf(w, "note: %s\n", warning)
	}
	postConsentReload(w, daemonaddr.ClientURL(consentReloadPath), consentSignalTimeout)
}

// postConsentReload performs the nudge against an explicit URL. Split out from
// notifyDaemonConsentChanged so a test can point it at a stub daemon and at a
// dead port without touching address resolution.
func postConsentReload(w io.Writer, url string, timeout time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		fmt.Fprintf(w, "note: could not build the consent-reload request (%v); a running daemon will pick this up on its next restart\n", err)
		return
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		// By far the common case, and it is the NORMAL one: no daemon is
		// running. Phrased so it does not read as a failure of the uninstall,
		// which succeeded.
		fmt.Fprintln(w, "No running daemon to notify (the opt-out is recorded and takes effect on the next start)")
		return
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		_ = resp.Body.Close()
	}()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		fmt.Fprintf(w, "note: the running daemon answered %d to the consent-reload request; restart it if the hooks come back\n", resp.StatusCode)
		return
	}
	fmt.Fprintln(w, "Notified the running daemon to reload consent")
}
