package services

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// dbBackedPathSentinel marks a TranscriptPath that encodes a DB-backed
// session (e.g. OpenCode's "…/opencode.db-wal?session=ses_xxx"). The query
// string carries the session ID since the WAL itself is shared across all
// sessions in the DB. Used by isDBBackedTranscriptPath.
const dbBackedPathSentinel = '?'

// isDBBackedTranscriptPath reports whether path encodes a DB-backed adapter
// session. Such paths can't be stat'd for mtime-based staleness (the WAL is
// shared), so callers route them through adapter-specific liveness checks
// instead.
func isDBBackedTranscriptPath(path string) bool {
	return strings.IndexByte(path, dbBackedPathSentinel) >= 0
}

// isStaleTranscript reports whether the transcript file at path has not been
// modified within orphanTranscriptAge. Returns false for empty paths, stat
// errors, or DB-backed paths (whose staleness is managed by the adapter).
func isStaleTranscript(path string) bool {
	if path == "" || isDBBackedTranscriptPath(path) {
		return false
	}
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return time.Since(info.ModTime()) > orphanTranscriptAge
}

// isNewestTranscriptInDir reports whether the transcript at path has the
// newest mtime among the sibling .jsonl files in its directory. Used as the
// ghost-guard for the stale-transcript rescue (issue #576): a live agent
// process corresponds to at most one transcript per project directory — the
// most recently written one — so older stale siblings must never be rescued.
// Ties (>=) count as newest so coarse mtime granularity can't exclude the
// event's own file. Fails open (true) on ReadDir or sibling-stat errors —
// the downstream cwd and process-liveness checks still gate the rescue. A
// stat failure on path itself returns false (unreachable in practice: the
// caller only gets here after isStaleTranscript stat'd the same path).
func isNewestTranscriptInDir(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	mtime := info.ModTime()
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		return true
	}
	base := filepath.Base(path)
	for _, e := range entries {
		if e.IsDir() || e.Name() == base || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		sibling, err := e.Info()
		if err != nil {
			continue
		}
		if sibling.ModTime().After(mtime) {
			return false
		}
	}
	return true
}

// cwdMissing reports whether cwd refers to a directory that no longer
// exists. Returns false for empty or relative paths to avoid second-guessing
// callers when the cwd metadata is incomplete.
func cwdMissing(cwd string) bool {
	if cwd == "" || !filepath.IsAbs(cwd) {
		return false
	}
	_, err := os.Stat(cwd)
	return os.IsNotExist(err)
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
// Claude Code subagent transcripts live at .../<parent-session-id>/subagents/<agent-id>.jsonl;
// Workflow-tool agents one level deeper, at
// .../<parent-session-id>/subagents/workflows/<run-id>/agent-<id>.jsonl (issue #565).
// Returns "" if the path doesn't match either pattern.
func deriveParentSessionID(transcriptPath string) string {
	dir := filepath.Dir(transcriptPath) // .../subagents or .../subagents/workflows/<run-id>
	if filepath.Base(dir) == "subagents" {
		return filepath.Base(filepath.Dir(dir)) // parent session ID
	}
	if wfRoot := workflowRunRoot(dir); wfRoot != "" {
		return filepath.Base(wfRoot)
	}
	return ""
}

// workflowRunRoot returns the parent-session directory encoded in a Workflow
// run directory path (.../<parent-session-id>/subagents/workflows/<run-id>),
// or "" when dir doesn't match that layout.
func workflowRunRoot(dir string) string {
	workflows := filepath.Dir(dir) // .../subagents/workflows
	if filepath.Base(workflows) != "workflows" {
		return ""
	}
	subagents := filepath.Dir(workflows) // .../<parent-session-id>/subagents
	if filepath.Base(subagents) != "subagents" {
		return ""
	}
	return filepath.Dir(subagents)
}

// isWorkflowBookkeepingFile reports whether path is a non-transcript .jsonl
// inside a Workflow run directory (.../subagents/workflows/<run-id>/). The
// Workflow tool writes a replay journal (journal.jsonl) next to its
// agent-*.jsonl transcripts; only agent-* files are session transcripts —
// anything else must never surface as a session (issue #565).
func isWorkflowBookkeepingFile(path string) bool {
	if strings.HasPrefix(filepath.Base(path), "agent-") {
		return false
	}
	return workflowRunRoot(filepath.Dir(path)) != ""
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
