// Command agent-onboard-viewer serves the replay viewer on localhost.
// Reads recordings from replaydata/agents/ and renders a single-page web
// UI with a scrubbable timeline.
//
// This is a separate binary from `agent-onboard` to keep the main tool's
// binary size down and let the viewer evolve independently.
//
// Exit codes (matching the cmd/expected-validate convention so callers can
// branch on outcome rather than just "nonzero"):
//
//	0  — clean shutdown
//	1  — runtime failure (e.g. the listener could not bind / exited with error)
//	2  — usage / configuration error (bad flags, repo-root has no replaydata/agents)
package main

import (
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"

	"irrlicht/tools/agent-onboarding/internal/viewer"
)

const (
	exitOK         = 0
	exitRuntimeErr = 1
	exitUsageErr   = 2
)

func main() {
	os.Exit(run(os.Args[1:]))
}

// run is the testable entry point: a local FlagSet (rather than the global
// flag.CommandLine) keeps it re-callable from tests.
func run(args []string) int {
	fs := flag.NewFlagSet("agent-onboard-viewer", flag.ContinueOnError)
	repoRoot := fs.String("repo-root", ".", "repository root containing replaydata/")
	addr := fs.String("addr", "127.0.0.1:8765", "bind address")
	if err := fs.Parse(args); err != nil {
		// -h/-help is a successful, user-requested action: ContinueOnError
		// already printed usage to stderr, so exit 0 (not the usage-error
		// code) to match the conventional `--help` contract.
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsageErr
	}

	if _, err := os.Stat(*repoRoot + "/replaydata/agents"); err != nil {
		fmt.Fprintf(os.Stderr, "error: repo-root %q has no replaydata/agents subdir: %v\n", *repoRoot, err)
		return exitUsageErr
	}
	s := &viewer.Server{RepoRoot: *repoRoot}
	fmt.Fprintf(os.Stderr, "agent-onboard viewer listening on http://%s/\n", *addr)
	if err := http.ListenAndServe(*addr, s.Handler()); err != nil {
		fmt.Fprintf(os.Stderr, "error: server exited: %v\n", err)
		return exitRuntimeErr
	}
	return exitOK
}
