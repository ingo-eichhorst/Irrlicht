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

// sessionsDir returns the directory the Pi adapter should watch, resolving
// the same two overrides pi itself does and in the same order.
//
// pi composes the sessions directory as getAgentDir() + "/sessions"
// (dist/config.js's getSessionsDir), where getAgentDir honours
// $PI_CODING_AGENT_DIR; $PI_CODING_AGENT_SESSION_DIR then overrides the
// result outright (dist/main.js reads it directly, and pi's own --help
// describes --session-dir as overriding it).
//
// The $PI_CODING_AGENT_DIR leg is issue #1721's: before it, setting only
// that variable — the documented way to relocate a whole pi installation —
// moved pi's sessions while irrlicht kept watching ~/.pi/agent/sessions and
// saw nothing. It matters more now than it did: the hook receiver confines
// caller-supplied transcript paths to THIS root (hooks.go's
// transcriptConfiner), so a wrong root does not merely miss transcripts, it
// rejects every hook the extension delivers.
func sessionsDir() string {
	// FromEnv with an empty default is how "was this override set to an
	// absolute path?" is asked without restating the absolute-path check and
	// its warn-once logging.
	if dir := agentpaths.FromEnv("pi", sessionDirEnvVar, ""); dir != "" {
		return dir
	}
	return agentpaths.FromEnv("pi", agentDirEnvVar, defaultRootDir, sessionsSubdir)
}

// sessionsSubdir is the agent-dir-relative directory pi writes transcripts
// into — the "sessions" in getSessionsDir's join(getAgentDir(), "sessions").
const sessionsSubdir = "sessions"
