// Package pi provides an inbound adapter that watches Pi coding agent
// transcript files under ~/.pi/agent/sessions/--<cwd>--/*.jsonl.
package pi

import "irrlicht/core/adapters/inbound/agents/agentpaths"

// AdapterName identifies sessions originating from Pi coding agent.
const AdapterName = "pi"

// ProcessName is the OS-level executable name for Pi CLI, used by
// the process lifecycle scanner to detect running instances via pgrep -x.
const ProcessName = "pi"

// defaultRootDir is the path relative to $HOME where Pi stores session
// transcripts by default. Sessions live under
// --<cwd-with-dashes>--/<timestamp>_<uuid>.jsonl.
const defaultRootDir = ".pi/agent/sessions"

// sessionDirEnvVar is the upstream Pi env var that relocates the session-
// transcript root. When set, it must be the absolute path of the session
// directory itself (not a parent).
const sessionDirEnvVar = "PI_CODING_AGENT_SESSION_DIR"

// sessionsDir returns the directory the Pi adapter should watch —
// $PI_CODING_AGENT_SESSION_DIR itself when that override is set (it names the
// session directory, not a root above it), else defaultRootDir.
func sessionsDir() string {
	return agentpaths.FromEnv("pi", sessionDirEnvVar, defaultRootDir)
}
