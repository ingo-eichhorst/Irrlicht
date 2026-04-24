// Package pi provides an inbound adapter that watches Pi coding agent
// transcript files under ~/.pi/agent/sessions/--<cwd>--/*.jsonl.
package pi

// AdapterName identifies sessions originating from Pi coding agent.
const AdapterName = "pi"

// ProcessName is the OS-level executable name for Pi CLI, used by
// the process lifecycle scanner to detect running instances via pgrep -x.
const ProcessName = "pi"

// rootDir is the path relative to $HOME where Pi stores session transcripts.
// Sessions live under --<cwd-with-dashes>--/<timestamp>_<uuid>.jsonl.
const rootDir = ".pi/agent/sessions"
