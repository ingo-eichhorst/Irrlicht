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
	"strings"

	"irrlicht/tools/agent-onboarding/internal/viewer"
)

func main() {
	repoRoot := flag.String("repo-root", ".", "repository root containing replaydata/")
	addr := flag.String("addr", "127.0.0.1:8765", "bind address")
	autoPlay := flag.String("auto-play", "", "auto-start playback at boot: <agent>/<subtree>/<scenario> (e.g. claudecode/scenarios/multi-turn-conversation)")
	speed := flag.Float64("speed", 1.0, "playback speed for --auto-play")
	flag.Parse()

	if _, err := os.Stat(*repoRoot + "/replaydata/agents"); err != nil {
		log.Fatalf("repo-root has no replaydata/agents subdir: %v", err)
	}
	s := &viewer.Server{RepoRoot: *repoRoot}
	handler := s.Handler()

	if *autoPlay != "" {
		parts := strings.SplitN(*autoPlay, "/", 3)
		if len(parts) != 3 {
			log.Fatalf("--auto-play must be <agent>/<subtree>/<scenario>, got %q", *autoPlay)
		}
		if _, err := s.PlaybackManager().StartViewerInternal(parts[0], parts[1], parts[2], *speed); err != nil {
			log.Fatalf("auto-play: %v", err)
		}
		fmt.Fprintf(os.Stderr, "auto-playing %s at %g× → open http://%s/dashboard\n", *autoPlay, *speed, *addr)
	}

	fmt.Fprintf(os.Stderr, "agent-onboard viewer listening on http://%s/\n", *addr)
	if err := http.ListenAndServe(*addr, handler); err != nil {
		log.Fatal(err)
	}
}
