package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"irrlicht/core/domain/config"
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

// noDaemonNote is what the CLI says when there is nothing to notify. Phrased
// so it does not read as a failure of the command that just succeeded — it is
// the normal case, not a fault.
// maxReloadResponseBytes bounds every read of the reload response. The body is
// the permissions snapshot, which is small; the bound is here because a
// misresolved port could answer with anything at all.
const maxReloadResponseBytes = 1 << 20

const noDaemonNote = "No running daemon to notify (the opt-out is recorded and takes effect on the next start)"

// notifyDaemonConsentChanged tells a running daemon that permissions.json has
// changed underneath it, so it re-reads the store instead of acting on the
// copy it loaded at startup (#1425).
//
// Why this exists at all: an uninstall command runs in its own process. It
// removes the content and records the opt-out, but a daemon that is already
// running loaded the consent store once, at startup, and never looks again —
// so it keeps acting on "granted". For `--uninstall-hooks` that means #1372's
// re-verification loop re-installs the entries within one interval; for
// `--uninstall-task-eta` it means the managed blocks return to the user's own
// CLAUDE.md at the next start, with nothing in between having reported a
// disagreement (#1437).
//
// noun is what THIS caller removed, so the advisory lines below name the
// content the user should actually go and check. Both callers reach the same
// endpoint, and telling a --uninstall-task-eta user to watch for "hooks"
// pointing them at the wrong file is the whole reason it is a parameter.
//
// **A signal that cannot be delivered is not an error.** Both flags worked
// with no daemon running before this existed and have to keep working: the
// store on disk already says "denied", so the next daemon start reads the
// correct value with or without the nudge. Every failure path here is one
// informational line and no change to the exit code — the caller's success is
// decided by the uninstall, never by whether something was listening.
func notifyDaemonConsentChanged(w io.Writer, noun managedFileNoun) {
	if warning := daemonaddr.ClientConfigWarning(); warning != "" {
		fmt.Fprintf(w, "note: %s\n", warning)
	}
	// Only notify a daemon somebody actually named. Without this the address
	// ladder falls through to the default port whenever this tree has no
	// daemon, so an uninstall run against an isolated IRRLICHT_HOME would POST
	// to whatever is listening on 7837 — a different daemon, with a different
	// consent store, that this command never touched.
	if !daemonaddr.ClientTargetsANamedDaemon() {
		fmt.Fprintln(w, noDaemonNote)
		return
	}
	postConsentReload(w, daemonaddr.ClientURL(consentReloadPath), consentSignalTimeout, noun)
}

// postConsentReload performs the nudge against an explicit URL. Split out from
// notifyDaemonConsentChanged so a test can point it at a stub daemon and at a
// dead port without touching address resolution.
func postConsentReload(w io.Writer, url string, timeout time.Duration, noun managedFileNoun) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		fmt.Fprintf(w, "note: could not build the consent-reload request (%v); a running daemon will pick this up on its next restart\n", err)
		return
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		// Two different situations, and calling both "no daemon" would be a
		// lie in the second. A refused dial is a stale addr file or a daemon
		// that went away. A deadline means a daemon that IS there and is
		// busy — plausibly busy holding the very lock this request needs,
		// since Answer holds it across a CLI version probe and a config
		// rewrite — and telling the user nothing is running would send them
		// looking for the wrong problem.
		if errors.Is(err, context.DeadlineExceeded) {
			fmt.Fprintf(w, "note: the running daemon did not answer within %s; the opt-out is recorded and takes effect on the next start\n", timeout)
			return
		}
		fmt.Fprintln(w, noDaemonNote)
		return
	}
	defer func() {
		// Drained before closing so a receiver that streams its response does
		// not see a client vanish mid-write — the same reason hookbeacon.send
		// drains. Bounded, because this body is caller-controlled only in the
		// sense that a wrong port could answer with anything.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxReloadResponseBytes))
		_ = resp.Body.Close()
	}()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		fmt.Fprintf(w, "note: the running daemon answered %d to the consent-reload request; restart it if the %s come back\n", resp.StatusCode, noun.content)
		return
	}
	reportReloadOutcome(w, resp.Body, noun)
}

// reportReloadOutcome reads the snapshot the reload route answers with and
// says what actually happened.
//
// The interesting case is grant-all. IRRLICHT_PERMISSION_MODE=grant-all grants
// every permission in memory and never persists it, so ReloadFromStore is a
// deliberate no-op there — adopting the store would revoke the lot. That means
// the ONE mode in which this whole mechanism cannot work is the mode in which
// the CLI would otherwise print its most confident success message, while the
// re-verification loop puts the entries straight back. Grant-all is not
// hypothetical: the onboarding recording rig runs such daemons against the
// user's real home, and these uninstall flags are the documented way out —
// which is exactly why the warning has to name the right content: a rig daemon
// rewrites BOTH the agent configs and the user's CLAUDE.md.
//
// The mode is already on the wire — the route answers with the full
// permissions snapshot — so this costs a decode, not a round trip. An
// undecodable body is not an error: the reload was accepted either way.
func reportReloadOutcome(w io.Writer, body io.Reader, noun managedFileNoun) {
	var snap struct {
		Mode string `json:"mode"`
	}
	if err := json.NewDecoder(io.LimitReader(body, maxReloadResponseBytes)).Decode(&snap); err == nil &&
		snap.Mode == config.PermissionModeGrantAll {
		fmt.Fprintf(w, "warning: the running daemon is in IRRLICHT_PERMISSION_MODE=%s and ignores the recorded opt-out; it will re-install these %s. Stop it, or restart it without that setting.\n",
			config.PermissionModeGrantAll, noun.content)
		return
	}
	fmt.Fprintln(w, "Notified the running daemon to reload consent")
}
