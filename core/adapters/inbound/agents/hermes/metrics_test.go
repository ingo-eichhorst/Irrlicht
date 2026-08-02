package hermes

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// newTestStore creates a SQLite store carrying the subset of Hermes'
// schema_version=23 schema the adapter reads. Column names, types and
// nullability mirror the real `.schema sessions` / `.schema messages`
// output so a query that would fail against a real store fails here too.
func newTestStore(t *testing.T) (path string, db *sql.DB) {
	t.Helper()
	path = filepath.Join(t.TempDir(), "state.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	if _, err := db.Exec(`
		CREATE TABLE sessions (
			id TEXT PRIMARY KEY,
			source TEXT NOT NULL,
			model TEXT,
			parent_session_id TEXT,
			started_at REAL NOT NULL,
			ended_at REAL,
			end_reason TEXT,
			message_count INTEGER DEFAULT 0,
			tool_call_count INTEGER DEFAULT 0,
			input_tokens INTEGER DEFAULT 0,
			output_tokens INTEGER DEFAULT 0,
			cache_read_tokens INTEGER DEFAULT 0,
			cache_write_tokens INTEGER DEFAULT 0,
			cwd TEXT,
			git_branch TEXT,
			estimated_cost_usd REAL,
			title TEXT
		);
		CREATE TABLE messages (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			session_id TEXT NOT NULL,
			role TEXT NOT NULL,
			content TEXT,
			tool_call_id TEXT,
			tool_calls TEXT,
			tool_name TEXT,
			timestamp REAL NOT NULL,
			token_count INTEGER,
			finish_reason TEXT,
			display_kind TEXT,
			active INTEGER NOT NULL DEFAULT 1
		);`); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	return path, db
}

func insertSession(t *testing.T, db *sql.DB, id, source, model, cwd string, started, ended float64, msgs int) {
	t.Helper()
	var endedVal interface{}
	if ended > 0 {
		endedVal = ended
	}
	if _, err := db.Exec(`INSERT INTO sessions
		(id, source, model, cwd, started_at, ended_at, message_count,
		 input_tokens, output_tokens, cache_read_tokens, cache_write_tokens)
		VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		id, source, model, cwd, started, endedVal, msgs, 16272, 5, 0, 0); err != nil {
		t.Fatalf("insert session: %v", err)
	}
}

func insertMessage(t *testing.T, db *sql.DB, sessionID, role, content, toolCalls, toolCallID, finish string, ts float64) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO messages
		(session_id, role, content, tool_calls, tool_call_id, finish_reason, timestamp)
		VALUES (?,?,?,?,?,?,?)`,
		sessionID, role, content, toolCalls, toolCallID, finish, ts); err != nil {
		t.Fatalf("insert message: %v", err)
	}
}

func TestComputeMetrics_AggregatesFromSessionRow(t *testing.T) {
	path, db := newTestStore(t)
	insertSession(t, db, "s1", "tui", "gpt-5.6-luna", "/work/proj", 1000, 1042, 4)
	insertMessage(t, db, "s1", "user", "go", "", "", "", 1001)
	insertMessage(t, db, "s1", "assistant", "", realToolCallsJSON, "", "tool_calls", 1002)
	insertMessage(t, db, "s1", "tool", `{"output":"ok"}`, "", "888148639", "", 1003)
	insertMessage(t, db, "s1", "assistant", "done", "", "", "stop", 1004)

	m, err := ComputeMetrics(path, "s1")
	if err != nil {
		t.Fatalf("ComputeMetrics: %v", err)
	}
	if m == nil {
		t.Fatal("expected metrics, got nil")
	}

	if m.ModelName != "gpt-5.6-luna" {
		t.Errorf("ModelName = %q", m.ModelName)
	}
	if m.LastCWD != "/work/proj" {
		t.Errorf("LastCWD = %q", m.LastCWD)
	}
	// Token totals come from the sessions row, not from summing messages —
	// Hermes leaves messages.token_count NULL in practice.
	if m.CumInputTokens != 16272 || m.CumOutputTokens != 5 {
		t.Errorf("tokens = in %d / out %d, want 16272 / 5", m.CumInputTokens, m.CumOutputTokens)
	}
	if m.TotalTokens != 16277 {
		t.Errorf("TotalTokens = %d, want 16277", m.TotalTokens)
	}
	if m.ElapsedSeconds != 42 {
		t.Errorf("ElapsedSeconds = %d, want 42", m.ElapsedSeconds)
	}
	if m.LastEventType != "turn_done" {
		t.Errorf("LastEventType = %q, want turn_done", m.LastEventType)
	}
	if m.LastAssistantText != "done" {
		t.Errorf("LastAssistantText = %q", m.LastAssistantText)
	}
	// The tool call opened at 1002 was closed at 1003.
	if m.HasOpenToolCall {
		t.Errorf("HasOpenToolCall = true, want false (call was closed): open=%v", m.LastOpenToolNames)
	}
}

// An unmatched tool call must stay open — that is what keeps a session in
// `working` while a tool is still running.
func TestComputeMetrics_UnclosedToolCallStaysOpen(t *testing.T) {
	path, db := newTestStore(t)
	insertSession(t, db, "s1", "cli", "gpt-5.6-luna", "", 1000, 0, 2)
	insertMessage(t, db, "s1", "user", "go", "", "", "", 1001)
	insertMessage(t, db, "s1", "assistant", "", realToolCallsJSON, "", "tool_calls", 1002)

	m, err := ComputeMetrics(path, "s1")
	if err != nil || m == nil {
		t.Fatalf("ComputeMetrics: %v, %v", m, err)
	}
	if !m.HasOpenToolCall || m.OpenToolCallCount != 1 {
		t.Errorf("expected 1 open tool call, got open=%v count=%d", m.HasOpenToolCall, m.OpenToolCallCount)
	}
	if len(m.LastOpenToolNames) != 1 || m.LastOpenToolNames[0] != "terminal" {
		t.Errorf("LastOpenToolNames = %v, want [terminal]", m.LastOpenToolNames)
	}
}

// Hermes' messaging gateway writes into the SAME sessions table. Without the
// source filter a WhatsApp conversation would surface in irrlicht as a
// coding session.
func TestComputeMetrics_GatewaySessionsAreNotCodingSessions(t *testing.T) {
	path, db := newTestStore(t)
	for _, src := range []string{"whatsapp", "slack", "discord", "cron"} {
		id := "gw-" + src
		insertSession(t, db, id, src, "gpt-5.6-luna", "/work/proj", 1000, 0, 2)
		insertMessage(t, db, id, "user", "hi", "", "", "", 1001)

		m, err := ComputeMetrics(path, id)
		if err != nil {
			t.Fatalf("ComputeMetrics(%s): %v", src, err)
		}
		if m != nil {
			t.Errorf("source=%q produced metrics %+v — gateway sessions must be skipped", src, m)
		}
	}
}

func TestComputeMetrics_MissingOrEmpty(t *testing.T) {
	path, db := newTestStore(t)

	if m, _ := ComputeMetrics(path, "nope"); m != nil {
		t.Errorf("unknown session must yield nil, got %+v", m)
	}
	// A session row with no messages yet is not an error — the row is
	// inserted ~2s after launch, before the first assistant reply.
	insertSession(t, db, "empty", "cli", "m", "", 1000, 0, 0)
	if m, _ := ComputeMetrics(path, "empty"); m != nil {
		t.Errorf("session with no messages must yield nil, got %+v", m)
	}
	if m, _ := ComputeMetrics("", "s1"); m != nil {
		t.Error("empty store path must yield nil")
	}
	if m, _ := ComputeMetrics(path, ""); m != nil {
		t.Error("empty session id must yield nil")
	}
}

// The watcher encodes the session id into TranscriptPath; ComputeMetrics
// must recover it. A regression here silently reports every session's
// metrics against the wrong id.
func TestParseStorePath(t *testing.T) {
	tests := []struct {
		in, sid, wantPath, wantSID string
	}{
		{"/h/state.db", "s1", "/h/state.db", "s1"},
		{"/h/state.db?session=s2", "", "/h/state.db", "s2"},
		{"/h/state.db?session=s2", "ignored", "/h/state.db", "s2"},
	}
	for _, tt := range tests {
		gotPath, gotSID := parseStorePath(tt.in, tt.sid)
		if gotPath != tt.wantPath || gotSID != tt.wantSID {
			t.Errorf("parseStorePath(%q,%q) = (%q,%q), want (%q,%q)",
				tt.in, tt.sid, gotPath, gotSID, tt.wantPath, tt.wantSID)
		}
	}
}

// Round-trip: what the watcher builds is what ComputeMetrics can read.
func TestComputeMetrics_ReadsWatcherTranscriptPath(t *testing.T) {
	path, db := newTestStore(t)
	insertSession(t, db, "s1", "tui", "gpt-5.6-luna", "/work/proj", 1000, 0, 1)
	insertMessage(t, db, "s1", "user", "go", "", "", "", 1001)

	w := NewWithStorePath(path, 0)
	w.liveCWD = func() string { return "" }
	ev := agentEventFor(t, w, db)

	m, err := ComputeMetrics(ev.TranscriptPath, "")
	if err != nil || m == nil {
		t.Fatalf("ComputeMetrics(%q) = %v, %v", ev.TranscriptPath, m, err)
	}
	if m.ModelName != "gpt-5.6-luna" {
		t.Errorf("ModelName = %q", m.ModelName)
	}
}
