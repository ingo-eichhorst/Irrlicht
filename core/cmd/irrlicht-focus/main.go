// irrlicht-focus brings the terminal/IDE window for a session to the foreground.
//
// Usage: irrlicht-focus <sessionID>
//
// POSTs to the irrlicht daemon's POST /api/v1/sessions/{id}/focus endpoint. The
// daemon broadcasts a focus_requested WebSocket message; the Swift menu-bar app
// receives it and calls SessionLauncher.jump to activate the window.
//
// The daemon's port is resolved rather than assumed (issue #1215): an explicit
// IRRLICHT_BIND_ADDR wins, else the running daemon's published addr file, else
// the default — so focusing a session monitored by a dev daemon on :7838 does
// not send the request to whatever owns :7837.
//
// Exit codes: 0 = success, 1 = usage error or daemon unreachable, 2 = session
// not found or has no launcher data.
package main

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"

	"irrlicht/core/pkg/daemonaddr"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stderr))
}

// run is main's testable body: it takes the arguments after the program name
// and returns the exit code rather than calling os.Exit.
func run(args []string, stderr io.Writer) int {
	if len(args) < 1 || args[0] == "" {
		fmt.Fprintf(stderr, "usage: irrlicht-focus <sessionID>\n")
		return 1
	}

	// Say so when the daemon we resolved is not the one the environment asked
	// for: dialing a stranger silently is the failure mode this CLI had.
	if w := daemonaddr.ClientConfigWarning(); w != "" {
		fmt.Fprintf(stderr, "irrlicht-focus: %s\n", w)
	}

	client := &http.Client{Timeout: 3 * time.Second}

	resp, err := client.Post(focusURL(args[0]), "application/json", nil) //nolint:noctx
	if err != nil {
		fmt.Fprintf(stderr, "irrlicht-focus: daemon unreachable: %v\n", err)
		return 1
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		return 0
	}
	fmt.Fprintf(stderr, "irrlicht-focus: daemon returned %d\n", resp.StatusCode)
	return 2
}

// focusURL builds the daemon endpoint this CLI POSTs to for sessionID, which
// is escaped so an unexpected argument can only ever be a session ID the
// daemon does not know, never a different endpoint.
func focusURL(sessionID string) string {
	return daemonaddr.ClientURL("/api/v1/sessions/" + url.PathEscape(sessionID) + "/focus")
}
