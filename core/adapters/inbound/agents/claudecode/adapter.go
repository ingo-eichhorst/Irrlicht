// Package claudecode provides an inbound adapter that watches Claude Code
// transcript files under ~/.claude/projects/*/*.jsonl.
package claudecode

import (
	"os"
	"path/filepath"
)

// AdapterName identifies sessions originating from Claude Code.
const AdapterName = "claude-code"

// ProcessName is the OS-level executable name for Claude Code, used by
// PID-discovery lookups (pgrep, etc.). Distinct from AdapterName.
const ProcessName = "claude"

// defaultProjectsDir is the path relative to $HOME where Claude Code stores
// transcripts by default.
const defaultProjectsDir = ".claude/projects"

// configDirEnvVar is the upstream Claude Code env var that relocates the
// agent's home directory (default: $HOME/.claude). When set, projects and
// PID metadata both move under it.
const configDirEnvVar = "CLAUDE_CONFIG_DIR"

// transcriptsDir returns the directory the Claude Code adapter should watch.
// When CLAUDE_CONFIG_DIR is set, transcripts live under $CLAUDE_CONFIG_DIR/projects;
// otherwise the default $HOME-relative path is returned.
func transcriptsDir() string {
	if v := os.Getenv(configDirEnvVar); v != "" {
		return filepath.Join(v, "projects")
	}
	return defaultProjectsDir
}
