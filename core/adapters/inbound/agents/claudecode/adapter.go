// Package claudecode provides an inbound adapter that watches Claude Code
// transcript files under ~/.claude/projects/*/*.jsonl.
package claudecode

// AdapterName identifies sessions originating from Claude Code.
const AdapterName = "claude-code"

// ProcessName is the OS-level executable name for Claude Code, used by
// PID-discovery lookups (pgrep, etc.). Distinct from AdapterName.
const ProcessName = "claude"

// projectsDir is the path relative to $HOME where Claude Code stores transcripts.
const projectsDir = ".claude/projects"
