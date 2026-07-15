// Package kirocli provides an inbound adapter that watches Kiro CLI
// v2 transcript files under ~/.kiro/sessions/cli/<uuid>.jsonl. Kiro CLI V3
// stores an incompatible per-workspace session format and is intentionally not
// discovered by this adapter.
package kirocli

import "irrlicht/core/adapters/inbound/agents/agentpaths"

// AdapterName identifies sessions originating from Kiro CLI.
const AdapterName = "kiro-cli"

// ProcessName is the OS-level executable name for Kiro CLI, used by the
// process lifecycle scanner to detect running instances via pgrep -x.
// `kiro-cli chat` keeps comm="kiro-cli" for the parent process; the child
// helpers (kiro-cli-chat, bun tui.js) carry different names and the
// always-running kiro_cli_desktop companion never matches an exact-name
// lookup for "kiro-cli".
const ProcessName = "kiro-cli"

// defaultRootDir is the path relative to $HOME where Kiro CLI v2 stores
// session transcripts. Each session is a <uuid>.jsonl conversation log
// with a <uuid>.json metadata sidecar (cwd, title, per-turn credits)
// next to it.
const defaultRootDir = ".kiro/sessions/cli"

// kiroHomeEnvVar is the upstream Kiro CLI env var (added v2.3.0, 2026-05-12)
// that relocates the agent's home directory (default: ~/.kiro). Kiro's docs
// state it "[o]verrides the ~/.kiro directory used for global agents,
// prompts, skills, steering, settings, and sessions" — so when set, sessions
// move to $KIRO_HOME/sessions/cli.
const kiroHomeEnvVar = "KIRO_HOME"

// sessionsDir returns the directory the Kiro CLI adapter should watch —
// $KIRO_HOME/sessions/cli when that override is set, else defaultRootDir.
func sessionsDir() string {
	return agentpaths.FromEnv("kirocli", kiroHomeEnvVar, defaultRootDir, "sessions", "cli")
}
