package services

import (
	"os"
	"path/filepath"
	"time"
)

// isStaleTranscript reports whether the transcript file at path has not been
// modified within orphanTranscriptAge. Returns false for empty paths or
// stat errors (file missing → not stale, will be caught elsewhere).
func isStaleTranscript(path string) bool {
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return time.Since(info.ModTime()) > orphanTranscriptAge
}

// deriveParentSessionID extracts a parent session ID from a subagent transcript path.
// Claude Code subagent transcripts live at .../<parent-session-id>/subagents/<agent-id>.jsonl.
// Returns "" if the path doesn't match the subagent pattern.
func deriveParentSessionID(transcriptPath string) string {
	dir := filepath.Dir(transcriptPath) // .../subagents
	if filepath.Base(dir) != "subagents" {
		return ""
	}
	return filepath.Base(filepath.Dir(dir)) // parent session ID
}

// extractProjectDir extracts the project directory name from a transcript path.
// Expected format: .../<project-dir>/<session-id>.jsonl
func extractProjectDir(transcriptPath string) string {
	if transcriptPath == "" {
		return ""
	}
	// filepath.Dir gives us the directory containing the file,
	// filepath.Base of that gives us the project directory name.
	dir := filepath.Dir(transcriptPath)
	if dir == "." || dir == "/" {
		return ""
	}
	return filepath.Base(dir)
}
