// Package claudecode provides an inbound adapter that watches Claude Code
// transcript files under ~/.claude/projects/*/*.jsonl.
package claudecode

import "irrlicht/core/adapters/inbound/agents/fswatcher"

// AdapterName identifies sessions originating from Claude Code.
const AdapterName = "claude-code"

// projectsDir is the path relative to $HOME where Claude Code stores transcripts.
const projectsDir = ".claude/projects"

// New creates a file-system watcher for Claude Code transcripts.
func New() *fswatcher.Watcher {
	return fswatcher.New(projectsDir, AdapterName)
}
