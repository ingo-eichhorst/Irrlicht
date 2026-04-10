// Package pi provides an inbound adapter that watches Pi coding agent
// transcript files under ~/.pi/agent/sessions/--<cwd>--/*.jsonl.
package pi

import (
	"time"

	"irrlicht/core/adapters/inbound/agents/fswatcher"
)

// AdapterName identifies sessions originating from Pi coding agent.
const AdapterName = "pi"

// ProcessName is the OS-level executable name for Pi CLI, used by
// the process lifecycle scanner to detect running instances via pgrep -x.
const ProcessName = "pi"

// rootDir is the path relative to $HOME where Pi stores session transcripts.
// Sessions live under --<cwd-with-dashes>--/<timestamp>_<uuid>.jsonl.
const rootDir = ".pi/agent/sessions"

// New creates a file-system watcher for Pi coding agent transcripts.
// maxAge controls the maximum transcript file age; older files are ignored.
func New(maxAge time.Duration) *fswatcher.Watcher {
	return fswatcher.New(rootDir, AdapterName, maxAge)
}
