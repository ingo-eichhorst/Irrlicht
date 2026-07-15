// Package claudecode provides an inbound adapter that watches Claude Code
// transcript files under ~/.claude/projects/*/*.jsonl.
package claudecode

import "irrlicht/core/adapters/inbound/agents/agentpaths"

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
// Non-absolute env values are rejected so a misconfigured path surfaces in
// logs instead of silently watching the wrong place.
func transcriptsDir() string {
	return agentpaths.FromEnv("claudecode", configDirEnvVar, defaultProjectsDir, "projects")
}
