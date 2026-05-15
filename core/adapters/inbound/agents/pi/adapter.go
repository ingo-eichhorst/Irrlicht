// Package pi provides an inbound adapter that watches Pi coding agent
// transcript files under ~/.pi/agent/sessions/--<cwd>--/*.jsonl.
package pi

import "os"

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
// 2026-04-30) that relocates the session-transcript root. When set, it is
// the absolute path of the session directory itself (not a parent).
const sessionDirEnvVar = "PI_CODING_AGENT_SESSION_DIR"

// sessionsDir returns the directory the Pi adapter should watch. It honors
// PI_CODING_AGENT_SESSION_DIR (absolute path) when set; otherwise it returns
// the default $HOME-relative path. Daemon restart is required to pick up
// changes — the env var is read once at Agent() construction time.
func sessionsDir() string {
	if v := os.Getenv(sessionDirEnvVar); v != "" {
		return v
	}
	return defaultRootDir
}
