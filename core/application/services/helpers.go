package services

import (
	"bufio"
	"encoding/json"
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

// deriveParentSession tries all known methods to extract a parent session ID.
// 1. Claude Code path pattern: .../<parent-session-id>/subagents/<agent-id>.jsonl
// 2. Pi transcript header: {"type": "session", "parentSession": "..."}
func deriveParentSession(transcriptPath string) string {
	if id := deriveParentSessionID(transcriptPath); id != "" {
		return id
	}
	return deriveParentSessionFromTranscript(transcriptPath)
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

// deriveParentSessionFromTranscript reads the first line of a Pi transcript
// and extracts the parentSession field from the session header.
// Returns "" if the transcript is not Pi format or has no parent session.
func deriveParentSessionFromTranscript(transcriptPath string) string {
	if transcriptPath == "" {
		return ""
	}
	f, err := os.Open(transcriptPath)
	if err != nil {
		return ""
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	if !scanner.Scan() {
		return ""
	}
	var header map[string]interface{}
	if err := json.Unmarshal(scanner.Bytes(), &header); err != nil {
		return ""
	}
	if header["type"] != "session" {
		return ""
	}
	if parent, ok := header["parentSession"].(string); ok && parent != "" {
		return parent
	}
	return ""
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
