// Package kirocli provides an inbound adapter that watches Kiro CLI
// transcript files under ~/.kiro/sessions/cli/<uuid>.jsonl.
package kirocli

import (
	"log"
	"os"
	"path/filepath"
)

// AdapterName identifies sessions originating from Kiro CLI.
const AdapterName = "kiro-cli"

// ProcessName is the OS-level executable name for Kiro CLI, used by the
// process lifecycle scanner to detect running instances via pgrep -x.
// `kiro-cli chat` keeps comm="kiro-cli" for the parent process; the child
// helpers (kiro-cli-chat, bun tui.js) carry different names and the
// always-running kiro_cli_desktop companion never matches an exact-name
// lookup for "kiro-cli".
const ProcessName = "kiro-cli"

// defaultRootDir is the path relative to $HOME where Kiro CLI stores
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

// sessionsDir returns the directory the Kiro CLI adapter should watch. Non-
// absolute env values are rejected so a misconfigured path surfaces in logs
// instead of silently watching the wrong place.
func sessionsDir() string {
	if v := os.Getenv(kiroHomeEnvVar); v != "" {
		cleaned := filepath.Clean(v)
		if filepath.IsAbs(cleaned) {
			return filepath.Join(cleaned, "sessions", "cli")
		}
		log.Printf("kirocli: ignoring %s=%q — must be an absolute path (no shell expansion)", kiroHomeEnvVar, v)
	}
	return defaultRootDir
}
