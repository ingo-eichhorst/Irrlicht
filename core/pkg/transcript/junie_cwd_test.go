package transcript

import (
	"os"
	"path/filepath"
	"testing"
)

// junieTree builds the ~/.junie layout under a temp root and returns the
// transcript path for sessionID:
//
//	<root>/sessions/<session-id>/events.jsonl
//	<root>/processes/<sidecar files>
func junieTree(t *testing.T, sessionID string, sidecars map[string]string) string {
	t.Helper()
	root := t.TempDir()
	sessionDir := filepath.Join(root, "sessions", sessionID)
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	transcriptPath := filepath.Join(sessionDir, "events.jsonl")
	if err := os.WriteFile(transcriptPath, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	processesDir := filepath.Join(root, "processes")
	if err := os.MkdirAll(processesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range sidecars {
		if err := os.WriteFile(filepath.Join(processesDir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return transcriptPath
}

// A session whose events.jsonl never saw a CurrentDirectoryUpdatedEvent (a
// session that never started a task) still has a project directory — Junie
// names it in the ~/.junie/processes/ sidecar. Without this fallback such a
// session lands in the dashboard's "unknown" group.
func TestExtractCWDFromJunieSidecar(t *testing.T) {
	path := junieTree(t, "session-260826-143957-1hhj", map[string]string{
		"47990-session-260826-143957-1hhj-fb94e3507e28.json": `{"pid":47990,"sessionId":"session-260826-143957-1hhj","projectPath":"/Users/dev/Workspace/demo","startedAt":1787748883685}`,
	})
	if got := ExtractCWDFromJunieSidecar(path); got != "/Users/dev/Workspace/demo" {
		t.Errorf("got %q, want /Users/dev/Workspace/demo", got)
	}
}

// The filename embeds the session ID but the JSON body is authoritative: a
// sidecar whose body names a DIFFERENT session must not answer, and a
// malformed one must be skipped in favor of a later parseable match.
func TestExtractCWDFromJunieSidecar_BodyIsAuthoritative(t *testing.T) {
	path := junieTree(t, "session-260826-143957-1hhj", map[string]string{
		"1-session-260826-143957-1hhj-aaaa.json": `{"pid":1,"sessionId":"session-OTHER","projectPath":"/wrong"}`,
		"2-session-260826-143957-1hhj-bbbb.json": `{not json`,
		"3-session-260826-143957-1hhj-cccc.json": `{"pid":3,"sessionId":"session-260826-143957-1hhj","projectPath":"/right"}`,
	})
	if got := ExtractCWDFromJunieSidecar(path); got != "/right" {
		t.Errorf("got %q, want /right", got)
	}
}

// Non-junie shapes must not answer: a foreign basename, a session directory
// without the session- prefix, and a tree with no processes/ dir at all.
func TestExtractCWDFromJunieSidecar_NonJuniePaths(t *testing.T) {
	if got := ExtractCWDFromJunieSidecar("/tmp/whatever/messages.jsonl"); got != "" {
		t.Errorf("foreign basename: got %q, want empty", got)
	}
	if got := ExtractCWDFromJunieSidecar(filepath.Join(t.TempDir(), "events.jsonl")); got != "" {
		t.Errorf("no session- parent: got %q, want empty", got)
	}
	root := t.TempDir()
	sessionDir := filepath.Join(root, "sessions", "session-x")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(sessionDir, "events.jsonl")
	if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := ExtractCWDFromJunieSidecar(path); got != "" {
		t.Errorf("missing processes dir: got %q, want empty", got)
	}
}
