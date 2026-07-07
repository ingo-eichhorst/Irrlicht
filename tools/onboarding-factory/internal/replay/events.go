// Package replay drives a viewer-side playback of an irrlichd recording.
// LoadEvents reads events.jsonl from a scenario directory and returns
// ordered lifecycle events; StateMachine walks them at a configurable
// speed and broadcasts PushMessages that the viewer's dashboard renders.
package replay

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"

	"irrlicht/core/domain/lifecycle"
)

// LoadEvents reads events.jsonl from path and returns the events ordered
// by their `seq` field (ascending). Malformed lines are skipped silently
// with a one-line warning to stderr — the file's tail may legitimately
// contain a partial write from a crash-killed daemon.
func LoadEvents(path string) ([]lifecycle.Event, error) {
	// Defense-in-depth guard against path traversal — see hasParentTraversal
	// (transcript_events.go) for why this package-local check exists
	// alongside the HTTP-layer validation upstream. Degrades the same way
	// a genuinely-missing file would: a wrapped os.ErrNotExist.
	if hasParentTraversal(path) {
		return nil, fmt.Errorf("open %s: %w", path, os.ErrNotExist)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	var out []lifecycle.Event
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		b := scanner.Bytes()
		if len(b) == 0 {
			continue
		}
		var e lifecycle.Event
		if err := json.Unmarshal(b, &e); err != nil {
			fmt.Fprintf(os.Stderr, "replay.LoadEvents: skip malformed line %d of %s: %v\n", lineNo, path, err)
			continue
		}
		out = append(out, e)
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("scan %s: %w", path, err)
	}
	// Events should already be in seq order on disk; sort defensively in
	// case a future writer interleaves.
	sort.SliceStable(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out, nil
}
