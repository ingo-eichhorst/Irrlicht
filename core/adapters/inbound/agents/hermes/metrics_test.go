package hermes

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	// Registers the "sqlite" driver for the database/sql handles these tests
	// open directly to seed fixtures.
	_ "modernc.org/sqlite"

	"irrlicht/core/domain/session"
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

// sessionRow / messageRow keep the fixture helpers to two parameters each.
// Named fields also make a call site readable — a positional
// insertSession(t, db, sessionRow{id: "s1", source: "cli", model: "m", cwd: "", started: 1000, ended: 0, msgs: 1}) said nothing about
// which number was which.
type sessionRow struct {
	id, source, model, cwd string
	parent                 string // parent_session_id; set for delegated children
	started, ended         float64
	msgs                   int
}

type messageRow struct {
	sessionID, role, content string
	toolCalls, toolCallID    string
	finish                   string
	ts                       float64
}

func insertSession(t *testing.T, db *sql.DB, r sessionRow) {
	t.Helper()
	var endedVal interface{}
	if r.ended > 0 {
		endedVal = r.ended
	}
	var parentVal interface{}
	if r.parent != "" {
		parentVal = r.parent
	}
	if _, err := db.Exec(`INSERT INTO sessions
		(id, source, model, cwd, parent_session_id, started_at, ended_at, message_count,
		 input_tokens, output_tokens, cache_read_tokens, cache_write_tokens)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		r.id, r.source, r.model, r.cwd, parentVal, r.started, endedVal, r.msgs,
		16272, 5, 0, 0); err != nil {
		t.Fatalf("insert session: %v", err)
	}
}

func insertMessage(t *testing.T, db *sql.DB, r messageRow) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO messages
		(session_id, role, content, tool_calls, tool_call_id, finish_reason, timestamp)
		VALUES (?,?,?,?,?,?,?)`,
		r.sessionID, r.role, r.content, r.toolCalls, r.toolCallID,
		r.finish, r.ts); err != nil {
		t.Fatalf("insert message: %v", err)
	}
}

// completedTurnMetrics seeds one finished session — user prompt, a tool call,
// its result, then a closing assistant reply — and returns its metrics. The
// two tests below split the assertions by what they are about: the aggregate
// the `sessions` row carries, and the state signal only replaying the
// messages can produce.
func completedTurnMetrics(t *testing.T) *session.SessionMetrics {
	t.Helper()
	path, db := newTestStore(t)
	insertSession(t, db, sessionRow{id: "s1", source: "tui", model: "gpt-5.6-luna", cwd: "/work/proj", started: 1000, ended: 1042, msgs: 4})
	insertMessage(t, db, messageRow{sessionID: "s1", role: "user", content: "go", toolCalls: "", toolCallID: "", finish: "", ts: 1001})
	insertMessage(t, db, messageRow{sessionID: "s1", role: "assistant", content: "", toolCalls: realToolCallsJSON, toolCallID: "", finish: "tool_calls", ts: 1002})
	insertMessage(t, db, messageRow{sessionID: "s1", role: "tool", content: `{"output":"ok"}`, toolCalls: "", toolCallID: "888148639", finish: "", ts: 1003})
	insertMessage(t, db, messageRow{sessionID: "s1", role: "assistant", content: "done", toolCalls: "", toolCallID: "", finish: "stop", ts: 1004})

	m, err := ComputeMetrics(path, "s1")
	if err != nil {
		t.Fatalf("ComputeMetrics: %v", err)
	}
	if m == nil {
		t.Fatal("expected metrics, got nil")
	}
	return m
}

func TestComputeMetrics_AggregatesFromSessionRow(t *testing.T) {
	m := completedTurnMetrics(t)

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
}

// The state signal the aggregate cannot express: turn boundary, last prose,
// and whether a tool call is still open.
func TestComputeMetrics_FoldsMessageStateSignal(t *testing.T) {
	m := completedTurnMetrics(t)

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
	insertSession(t, db, sessionRow{id: "s1", source: "cli", model: "gpt-5.6-luna", cwd: "", started: 1000, ended: 0, msgs: 2})
	insertMessage(t, db, messageRow{sessionID: "s1", role: "user", content: "go", toolCalls: "", toolCallID: "", finish: "", ts: 1001})
	insertMessage(t, db, messageRow{sessionID: "s1", role: "assistant", content: "", toolCalls: realToolCallsJSON, toolCallID: "", finish: "tool_calls", ts: 1002})

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
		insertSession(t, db, sessionRow{id: id, source: src, model: "gpt-5.6-luna", cwd: "/work/proj", started: 1000, msgs: 2})
		insertMessage(t, db, messageRow{sessionID: id, role: "user", content: "hi", toolCalls: "", toolCallID: "", finish: "", ts: 1001})

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
	insertSession(t, db, sessionRow{id: "empty", source: "cli", model: "m", cwd: "", started: 1000, ended: 0, msgs: 0})
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
	insertSession(t, db, sessionRow{id: "s1", source: "tui", model: "gpt-5.6-luna", cwd: "/work/proj", started: 1000, ended: 0, msgs: 1})
	insertMessage(t, db, messageRow{sessionID: "s1", role: "user", content: "go", toolCalls: "", toolCallID: "", finish: "", ts: 1001})

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

// The store permission is observe-kind and its consent copy promises
// "No row is ever written". modernc.org/sqlite only honors DSN query
// params for a file: URI — given a bare path it silently opens
// READWRITE|CREATE — so this pins that openReadOnly actually enforces the
// mode rather than merely asking for it.
//
// Seen red against the pre-fix DSN (dbPath+"?mode=ro"): the missing store
// was created on disk and the CREATE TABLE succeeded.
func TestOpenReadOnly_CannotWriteOrCreate(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "state.db")

	db, err := openReadOnly(missing)
	if err != nil {
		t.Fatalf("openReadOnly: %v", err)
	}
	defer db.Close()

	if _, err := db.Exec("CREATE TABLE probe(x)"); err == nil {
		t.Error("openReadOnly permitted a write — mode=ro is not being enforced")
	}
	if _, err := os.Stat(missing); err == nil {
		t.Error("openReadOnly CREATED the store file; an observe-kind permission must never write")
	}

	// A real store must still be readable through the same helper.
	path, seed := newTestStore(t)
	insertSession(t, seed, sessionRow{id: "s1", source: "tui", model: "m", cwd: "/work/proj", started: 1000, ended: 0, msgs: 1})
	ro, err := openReadOnly(path)
	if err != nil {
		t.Fatalf("openReadOnly(existing): %v", err)
	}
	defer ro.Close()
	var got string
	if err := ro.QueryRow(`SELECT id FROM sessions WHERE id='s1'`).Scan(&got); err != nil {
		t.Fatalf("read through read-only handle: %v", err)
	}
	if got != "s1" {
		t.Errorf("read back %q, want s1", got)
	}
}

// A source='cli' session has no stored cwd, and the daemon cannot recover
// one later from a "<store>?session=<id>" path the way it would from a real
// transcript file. Resolving it here is what lets the binding BACKFILL on a
// later activity event instead of being frozen at discovery — the case where
// two concurrent sessions made the discovery-time probe ambiguous.
func TestComputeMetrics_UnboundCLISessionBackfillsCWD(t *testing.T) {
	path, db := newTestStore(t)
	insertSession(t, db, sessionRow{id: "s1", source: "cli", model: "m", cwd: "", started: 1000, ended: 0, msgs: 1})
	insertMessage(t, db, messageRow{sessionID: "s1", role: "user", content: "go", toolCalls: "", toolCallID: "", finish: "", ts: 1001})

	prev := liveCWDForSession
	liveCWDForSession = func() string { return "/live/cwd" }
	defer func() { liveCWDForSession = prev }()

	m, err := ComputeMetrics(path, "s1")
	if err != nil || m == nil {
		t.Fatalf("ComputeMetrics: %v, %v", m, err)
	}
	if m.LastCWD != "/live/cwd" {
		t.Errorf("LastCWD = %q, want the live-process cwd so the binding can backfill", m.LastCWD)
	}
}

// A stored cwd always wins — the probe must not override a TUI session's own
// recorded directory.
func TestComputeMetrics_StoredCWDWinsOverProbe(t *testing.T) {
	path, db := newTestStore(t)
	insertSession(t, db, sessionRow{id: "s1", source: "tui", model: "m", cwd: "/work/proj", started: 1000, ended: 0, msgs: 1})
	insertMessage(t, db, messageRow{sessionID: "s1", role: "user", content: "go", toolCalls: "", toolCallID: "", finish: "", ts: 1001})

	prev := liveCWDForSession
	liveCWDForSession = func() string { t.Error("probe must not run when cwd is stored"); return "/wrong" }
	defer func() { liveCWDForSession = prev }()

	m, _ := ComputeMetrics(path, "s1")
	if m == nil || m.LastCWD != "/work/proj" {
		t.Errorf("LastCWD = %v, want /work/proj", m)
	}
}

// The ProcessOwnedStore path bypasses the tailer, so the waiting cue the
// parser computes reaches the daemon only if ComputeMetrics carries it.
// Without this it is calculated and silently dropped, and a session that
// ended on a question settles to `ready` instead of `waiting`.
func TestComputeMetrics_CarriesPendingWaitingCue(t *testing.T) {
	path, db := newTestStore(t)
	insertSession(t, db, sessionRow{id: "s1", source: "tui", model: "m", cwd: "/work/proj", started: 1000, ended: 0, msgs: 2})
	insertMessage(t, db, messageRow{sessionID: "s1", role: "user", content: "go", toolCalls: "", toolCallID: "", finish: "", ts: 1001})
	insertMessage(t, db, messageRow{sessionID: "s1", role: "assistant", content: "Which approach would you prefer?", toolCalls: "", toolCallID: "", finish: "stop", ts: 1002})

	m, err := ComputeMetrics(path, "s1")
	if err != nil || m == nil {
		t.Fatalf("ComputeMetrics: %v, %v", m, err)
	}
	if !m.PendingWaitingCue {
		t.Error("a turn ending on a question must surface PendingWaitingCue")
	}

	// A later user message answers it — the cue must not persist.
	insertMessage(t, db, messageRow{sessionID: "s1", role: "user", content: "the second one", toolCalls: "", toolCallID: "", finish: "", ts: 1003})
	m2, _ := ComputeMetrics(path, "s1")
	if m2.PendingWaitingCue {
		t.Error("a following user message must clear PendingWaitingCue")
	}
}

// The end-to-end proof for the todo gap: a store carrying two successive
// `todo` calls must surface as a task list on SessionMetrics. The parser test
// covers the delta extraction; this covers the wiring through foldMessages,
// which is what was actually missing — Tasks was never assigned at all.
func TestComputeMetrics_TodoToolCallsPopulateTasks(t *testing.T) {
	path, db := newTestStore(t)
	insertSession(t, db, sessionRow{id: "s1", source: "cli", model: "gpt-5.6-luna", cwd: "/work/proj", started: 1000, ended: 0, msgs: 3})
	insertMessage(t, db, messageRow{sessionID: "s1", role: "user", content: "plan it", ts: 1001})
	insertMessage(t, db, messageRow{sessionID: "s1", role: "assistant", toolCalls: realTodoToolCallsJSON, finish: "tool_calls", ts: 1002})
	// Second snapshot advances t2 to completed — the status must follow.
	advanced := strings.Replace(realTodoToolCallsJSON,
		`\"content\":\"Fix the parser\",\"status\":\"in_progress\"`,
		`\"content\":\"Fix the parser\",\"status\":\"completed\"`, 1)
	insertMessage(t, db, messageRow{sessionID: "s1", role: "assistant", toolCalls: advanced, finish: "tool_calls", ts: 1003})

	m, err := ComputeMetrics(path, "s1")
	if err != nil || m == nil {
		t.Fatalf("ComputeMetrics = %v, %v", m, err)
	}
	if len(m.Tasks) != 3 {
		t.Fatalf("len(Tasks) = %d, want 3 — the todo list must reach SessionMetrics", len(m.Tasks))
	}
	bySubject := map[string]string{}
	for _, task := range m.Tasks {
		bySubject[task.Subject] = task.Status
	}
	if got := bySubject["Fix the parser"]; got != "completed" {
		t.Errorf("Fix the parser status = %q, want completed (second snapshot advanced it)", got)
	}
	if got := bySubject["Run the suite"]; got != "pending" {
		t.Errorf("Run the suite status = %q, want pending", got)
	}
}
