// Package pi provides an inbound adapter that watches Pi coding agent
// transcript files under ~/.pi/agent/sessions/--<cwd>--/*.jsonl.
package pi

import (
	"log"
	"os"
	"path/filepath"
)

// AdapterName identifies sessions originating from Pi coding agent.
const AdapterName = "pi"

// ProcessName is the OS-level executable name for Pi CLI, used by
// the process lifecycle scanner to detect running instances via pgrep -x.
const ProcessName = "pi"

// defaultRootDir is the path relative to $HOME where Pi stores session
// transcripts by default. Sessions live under
// --<cwd-with-dashes>--/<timestamp>_<uuid>.jsonl.
const defaultRootDir = ".pi/agent/sessions"

// sessionDirEnvVar is the upstream Pi env var (introduced in Pi v0.71.0,
// 2026-04-30) that relocates the session-transcript root. When set, it must
// be the absolute path of the session directory itself (not a parent).
const sessionDirEnvVar = "PI_CODING_AGENT_SESSION_DIR"

// sessionsDir returns the directory the Pi adapter should watch. It honors
// PI_CODING_AGENT_SESSION_DIR when set to an absolute path; non-absolute
// values (relative paths, unexpanded ~) are logged and ignored to surface
// the misconfiguration instead of silently watching the wrong place. The
// env var is read once at Agent() construction time, so a daemon restart
// is required after changing it.
func sessionsDir() string {
	if v := os.Getenv(sessionDirEnvVar); v != "" {
		cleaned := filepath.Clean(v)
		if filepath.IsAbs(cleaned) {
			return cleaned
		}
		log.Printf("pi: ignoring %s=%q — must be an absolute path (no shell expansion)", sessionDirEnvVar, v)
	}
	return defaultRootDir
}
