// Command agent-onboard-viewer serves the Phase 7 replay viewer on
// localhost. Reads recordings from replaydata/agents/ and renders a
// single-page web UI scrubbable timeline.
//
// This is a separate binary from `agent-onboard` to keep the main tool's
// binary size down and to allow the viewer to evolve independently.
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"

	"irrlicht/tools/agent-onboarding/internal/viewer"
)

func main() {
	repoRoot := flag.String("repo-root", ".", "repository root containing replaydata/")
	addr := flag.String("addr", "127.0.0.1:8765", "bind address")
	flag.Parse()

	if _, err := os.Stat(*repoRoot + "/replaydata/agents"); err != nil {
		log.Fatalf("repo-root has no replaydata/agents subdir: %v", err)
	}
	s := &viewer.Server{RepoRoot: *repoRoot}
	fmt.Fprintf(os.Stderr, "agent-onboard viewer listening on http://%s/\n", *addr)
	if err := http.ListenAndServe(*addr, s.Handler()); err != nil {
		log.Fatal(err)
	}
}
