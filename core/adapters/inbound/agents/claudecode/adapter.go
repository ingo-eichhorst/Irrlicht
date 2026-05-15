// Package claudecode provides an inbound adapter that watches Claude Code
// transcript files under ~/.claude/projects/*/*.jsonl.
package claudecode

import (
	"log"
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
// agent's config root. Officially undocumented as of this writing; third-
// party tooling (ccusage) treats projects/ as relocating under it, which
// matches the common config-root convention. The PID-metadata directory
// (~/.claude/sessions/) is NOT moved here — its behavior under the env var
// is unverified, so pid.go intentionally keeps the hardcoded path.
const configDirEnvVar = "CLAUDE_CONFIG_DIR"

// transcriptsDir returns the directory the Claude Code adapter should watch.
// When CLAUDE_CONFIG_DIR is set to an absolute path, transcripts live under
// $CLAUDE_CONFIG_DIR/projects; non-absolute values (relative paths,
// unexpanded ~) are logged and ignored to surface misconfiguration. The env
// var is read once at Agent() construction; a daemon restart is required
// after changing it.
func transcriptsDir() string {
	if v := os.Getenv(configDirEnvVar); v != "" {
		cleaned := filepath.Clean(v)
		if filepath.IsAbs(cleaned) {
			return filepath.Join(cleaned, "projects")
		}
		log.Printf("claudecode: ignoring %s=%q — must be an absolute path (no shell expansion)", configDirEnvVar, v)
	}
	return defaultProjectsDir
}
