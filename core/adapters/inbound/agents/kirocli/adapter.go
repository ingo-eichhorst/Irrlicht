// Package kirocli provides an inbound adapter that watches Kiro CLI
// transcript files under ~/.kiro/sessions/cli/<uuid>.jsonl.
package kirocli

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

// sessionsDir returns the directory the Kiro CLI adapter should watch.
// Kiro documents no env var that relocates the session root, so this is
// constant — kept as a function to mirror the sibling adapters.
func sessionsDir() string {
	return defaultRootDir
}
