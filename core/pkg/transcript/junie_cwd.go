package transcript

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// ExtractCWDFromJunieSidecar resolves the working directory for a JetBrains
// Junie transcript whose events.jsonl carries no CurrentDirectoryUpdatedEvent
// (a session that never started a task emits none). Junie names the project
// directory in a process sidecar two levels up:
//
//	~/.junie/sessions/<session-id>/events.jsonl
//	~/.junie/processes/<pid>-<session-id>-<hash>.json  → {"sessionId":"...","projectPath":"/abs/cwd"}
//
// The sidecar filename embeds the session ID but the JSON body is
// authoritative: a file is accepted only when its sessionId field matches
// (mirroring the junie adapter's own sidecar reader). Malformed or unreadable
// files are skipped. No liveness check is needed — the project directory
// outlives any one process. Returns "" for any non-junie transcript path
// (base name isn't events.jsonl under a session-* directory inside sessions/)
// or when no sidecar answers — the caller falls through to its other
// resolution paths.
func ExtractCWDFromJunieSidecar(transcriptPath string) string {
	if filepath.Base(transcriptPath) != "events.jsonl" {
		return ""
	}
	sessionDir := filepath.Dir(transcriptPath)
	sessionID := filepath.Base(sessionDir)
	if !strings.HasPrefix(sessionID, "session-") {
		return ""
	}
	sessionsDir := filepath.Dir(sessionDir)
	if filepath.Base(sessionsDir) != "sessions" {
		return ""
	}
	processesDir := filepath.Join(filepath.Dir(sessionsDir), "processes")
	entries, err := os.ReadDir(processesDir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".json") || !strings.Contains(name, sessionID) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(processesDir, name))
		if err != nil {
			continue
		}
		var sc struct {
			SessionID   string `json:"sessionId"`
			ProjectPath string `json:"projectPath"`
		}
		if json.Unmarshal(data, &sc) != nil || sc.SessionID != sessionID || sc.ProjectPath == "" {
			continue
		}
		return sc.ProjectPath
	}
	return ""
}
