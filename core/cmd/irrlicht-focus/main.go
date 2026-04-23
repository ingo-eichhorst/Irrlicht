// irrlicht-focus brings the terminal/IDE window for a session to the foreground.
//
// Usage: irrlicht-focus <sessionID>
//
// POSTs to the irrlicht daemon's POST /api/v1/sessions/{id}/focus endpoint. The
// daemon broadcasts a focus_requested WebSocket message; the Swift menu-bar app
// receives it and calls SessionLauncher.jump to activate the window.
//
// Exit codes: 0 = success, 1 = usage error or daemon unreachable, 2 = session
// not found or has no launcher data.
package main

import (
	"fmt"
	"net/http"
	"os"
	"time"
)

const daemonURL = "http://127.0.0.1:7837"

func main() {
	if len(os.Args) < 2 || os.Args[1] == "" {
		fmt.Fprintf(os.Stderr, "usage: irrlicht-focus <sessionID>\n")
		os.Exit(1)
	}
	sessionID := os.Args[1]

	client := &http.Client{Timeout: 3 * time.Second}
	url := daemonURL + "/api/v1/sessions/" + sessionID + "/focus"

	resp, err := client.Post(url, "application/json", nil) //nolint:noctx
	if err != nil {
		fmt.Fprintf(os.Stderr, "irrlicht-focus: daemon unreachable: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		os.Exit(0)
	}
	fmt.Fprintf(os.Stderr, "irrlicht-focus: daemon returned %d\n", resp.StatusCode)
	os.Exit(2)
}
