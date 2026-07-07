package opencode

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite" // registers the "sqlite" driver for database/sql
)

func TestParseTranscriptPath_RawDBPath(t *testing.T) {
	dbPath, sid := parseTranscriptPath("/home/user/.local/share/opencode/opencode.db", "ses_abc")
	if dbPath != "/home/user/.local/share/opencode/opencode.db" {
		t.Errorf("dbPath = %q", dbPath)
	}
	if sid != "ses_abc" {
		t.Errorf("sid = %q, want ses_abc", sid)
	}
}

func TestParseTranscriptPath_WALSuffix(t *testing.T) {
	dbPath, sid := parseTranscriptPath("/home/.local/share/opencode/opencode.db-wal", "ses_xyz")
	if dbPath != "/home/.local/share/opencode/opencode.db" {
		t.Errorf("dbPath = %q, want .../opencode.db", dbPath)
	}
	if sid != "ses_xyz" {
		t.Errorf("sid = %q, want ses_xyz", sid)
	}
}

func TestParseTranscriptPath_WALWithSession(t *testing.T) {
	dbPath, sid := parseTranscriptPath("/home/.local/share/opencode/opencode.db-wal?session=ses_123", "")
	if dbPath != "/home/.local/share/opencode/opencode.db" {
		t.Errorf("dbPath = %q, want .../opencode.db", dbPath)
	}
	if sid != "ses_123" {
		t.Errorf("sid = %q, want ses_123", sid)
	}
}

func TestParseTranscriptPath_DBWithSession(t *testing.T) {
	dbPath, sid := parseTranscriptPath("/home/.local/share/opencode/opencode.db?session=ses_456", "")
	if dbPath != "/home/.local/share/opencode/opencode.db" {
		t.Errorf("dbPath = %q", dbPath)
	}
	if sid != "ses_456" {
		t.Errorf("sid = %q, want ses_456", sid)
	}
}

func TestParseTranscriptPath_EmptyPath(t *testing.T) {
	dbPath, sid := parseTranscriptPath("", "")
	if dbPath != "" {
		t.Errorf("dbPath = %q, want empty", dbPath)
	}
	if sid != "" {
		t.Errorf("sid = %q, want empty", sid)
	}
}

func TestParseTranscriptPath_EmptyPathWithSession(t *testing.T) {
	dbPath, sid := parseTranscriptPath("", "fallback-id")
	if dbPath != "" {
		t.Errorf("dbPath = %q, want empty", dbPath)
	}
	if sid != "fallback-id" {
		t.Errorf("sid = %q, want fallback-id", sid)
	}
}

func TestParseTranscriptPath_DBPathWithoutWAL_WithSession(t *testing.T) {
	dbPath, sid := parseTranscriptPath("/tmp/test.db?session=hello", "")
	if dbPath != "/tmp/test.db" {
		t.Errorf("dbPath = %q, want /tmp/test.db", dbPath)
	}
	if sid != "hello" {
		t.Errorf("sid = %q, want hello", sid)
	}
}

func TestParseTranscriptPath_NoSessionQuery(t *testing.T) {
	dbPath, sid := parseTranscriptPath("/tmp/test.db-wal", "provided-id")
	if dbPath != "/tmp/test.db" {
		t.Errorf("dbPath = %q, want /tmp/test.db", dbPath)
	}
	if sid != "provided-id" {
		t.Errorf("sid = %q, want provided-id", sid)
	}
}

// TestComputeMetrics_TodowriteTasks builds a synthetic OpenCode SQLite DB
// containing three todowrite parts (create three todos → mark first
// in_progress → mark first completed, second in_progress, third pending)
// and asserts that ComputeMetrics folds those parts into metrics.Tasks the
// same way the tailer's TaskDelta accumulator does on the replay path.
// Guards the inline TaskDelta loop in querySessionMetrics from drift now
// that it lives separately from the tailer's reference implementation.
func TestComputeMetrics_TodowriteTasks(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "opencode.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	// Minimal subset of the live opencode schema — only the columns
	// querySessionMetrics actually reads. The full schema has NOT NULL
	// constraints on project_id/slug/title/version we satisfy with empty
	// strings, since the query doesn't touch them.
	schema := []string{
		`CREATE TABLE session (
			id text PRIMARY KEY, project_id text NOT NULL, parent_id text,
			slug text NOT NULL, directory text NOT NULL, title text NOT NULL,
			version text NOT NULL, time_created integer NOT NULL, time_updated integer NOT NULL
		);`,
		`CREATE TABLE message (
			id text PRIMARY KEY, session_id text NOT NULL,
			time_created integer NOT NULL, time_updated integer NOT NULL, data text NOT NULL
		);`,
		`CREATE TABLE part (
			id text PRIMARY KEY, message_id text NOT NULL, session_id text NOT NULL,
			time_created integer NOT NULL, time_updated integer NOT NULL, data text NOT NULL
		);`,
	}
	for _, stmt := range schema {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("schema: %v", err)
		}
	}

	const sid = "ses_test_todowrite"
	const dir = "/tmp/opencode-todowrite-test"
	if _, err := db.Exec(
		`INSERT INTO session(id, project_id, slug, directory, title, version, time_created, time_updated) VALUES (?, '', '', ?, '', '', 0, 0)`,
		sid, dir,
	); err != nil {
		t.Fatalf("insert session: %v", err)
	}

	// One assistant message row carrying the three todowrite tool parts.
	msgData := `{"role":"assistant","time":{"created":1000},"model":{"providerID":"test","modelID":"test-model"}}`
	if _, err := db.Exec(
		`INSERT INTO message(id, session_id, time_created, time_updated, data) VALUES (?, ?, ?, ?, ?)`,
		"msg_1", sid, 1000, 1000, msgData,
	); err != nil {
		t.Fatalf("insert message: %v", err)
	}

	todoPart := func(callID string, todos []map[string]any) string {
		raw := map[string]any{
			"type":   "tool",
			"tool":   "todowrite",
			"callID": callID,
			"state": map[string]any{
				"status": "completed",
				"input":  map[string]any{"todos": todos},
			},
		}
		b, err := json.Marshal(raw)
		if err != nil {
			t.Fatalf("marshal part: %v", err)
		}
		return string(b)
	}

	parts := []struct {
		id      string
		created int64
		data    string
	}{
		{"part_1", 1100, todoPart("call_1", []map[string]any{
			{"content": "Task A", "status": "pending"},
			{"content": "Task B", "status": "pending"},
			{"content": "Task C", "status": "pending"},
		})},
		{"part_2", 1200, todoPart("call_2", []map[string]any{
			{"content": "Task A", "status": "in_progress"},
			{"content": "Task B", "status": "pending"},
			{"content": "Task C", "status": "pending"},
		})},
		{"part_3", 1300, todoPart("call_3", []map[string]any{
			{"content": "Task A", "status": "completed"},
			{"content": "Task B", "status": "in_progress"},
			{"content": "Task C", "status": "pending"},
		})},
	}
	for _, p := range parts {
		if _, err := db.Exec(
			`INSERT INTO part(id, message_id, session_id, time_created, time_updated, data) VALUES (?, ?, ?, ?, ?, ?)`,
			p.id, "msg_1", sid, p.created, p.created, p.data,
		); err != nil {
			t.Fatalf("insert part %s: %v", p.id, err)
		}
	}

	metrics, err := ComputeMetrics(dbPath, sid)
	if err != nil {
		t.Fatalf("ComputeMetrics: %v", err)
	}
	if metrics == nil {
		t.Fatal("ComputeMetrics returned nil — expected populated metrics")
	}
	if len(metrics.Tasks) != 3 {
		t.Fatalf("metrics.Tasks len = %d, want 3", len(metrics.Tasks))
	}
	want := []struct {
		id, subject, status string
	}{
		{"1", "Task A", "completed"},
		{"2", "Task B", "in_progress"},
		{"3", "Task C", "pending"},
	}
	for i, exp := range want {
		got := metrics.Tasks[i]
		if got.ID != exp.id || got.Subject != exp.subject || got.Status != exp.status {
			t.Errorf("Tasks[%d] = {ID:%q Subject:%q Status:%q}, want {ID:%q Subject:%q Status:%q}",
				i, got.ID, got.Subject, got.Status, exp.id, exp.subject, exp.status)
		}
	}
	// The message row's `model.modelID` field must surface as
	// metrics.ModelName — guards the schema fix where the prior code read
	// the top-level msgMap["modelID"] that opencode never populates.
	if metrics.ModelName != "test-model" {
		t.Errorf("metrics.ModelName = %q, want %q", metrics.ModelName, "test-model")
	}
}

// TestComputeMetrics_TodowriteSnapshotPrune drives a session where the
// second todowrite call drops Task C and reverts Task A from in_progress
// back to pending. The snapshot reconcile must (a) remove the dropped
// task from metrics.Tasks and (b) walk the reverted task back to pending.
func TestComputeMetrics_TodowriteSnapshotPrune(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "opencode.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	schema := []string{
		`CREATE TABLE session (id text PRIMARY KEY, project_id text NOT NULL, parent_id text, slug text NOT NULL, directory text NOT NULL, title text NOT NULL, version text NOT NULL, time_created integer NOT NULL, time_updated integer NOT NULL);`,
		`CREATE TABLE message (id text PRIMARY KEY, session_id text NOT NULL, time_created integer NOT NULL, time_updated integer NOT NULL, data text NOT NULL);`,
		`CREATE TABLE part (id text PRIMARY KEY, message_id text NOT NULL, session_id text NOT NULL, time_created integer NOT NULL, time_updated integer NOT NULL, data text NOT NULL);`,
	}
	for _, stmt := range schema {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("schema: %v", err)
		}
	}

	const sid = "ses_test_prune"
	if _, err := db.Exec(
		`INSERT INTO session(id, project_id, slug, directory, title, version, time_created, time_updated) VALUES (?, '', '', ?, '', '', 0, 0)`,
		sid, "/tmp/d",
	); err != nil {
		t.Fatalf("insert session: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO message(id, session_id, time_created, time_updated, data) VALUES (?, ?, ?, ?, ?)`,
		"msg_1", sid, 1000, 1000,
		`{"role":"assistant","model":{"modelID":"test-model"}}`,
	); err != nil {
		t.Fatalf("insert message: %v", err)
	}

	todoPart := func(callID string, todos []map[string]any) string {
		raw := map[string]any{
			"type":   "tool",
			"tool":   "todowrite",
			"callID": callID,
			"state": map[string]any{
				"status": "completed",
				"input":  map[string]any{"todos": todos},
			},
		}
		b, _ := json.Marshal(raw)
		return string(b)
	}

	parts := []struct {
		id      string
		created int64
		data    string
	}{
		{"part_1", 1100, todoPart("c1", []map[string]any{
			{"content": "Task A", "status": "in_progress"},
			{"content": "Task B", "status": "pending"},
			{"content": "Task C", "status": "pending"},
		})},
		{"part_2", 1200, todoPart("c2", []map[string]any{
			{"content": "Task A", "status": "pending"}, // reverted
			{"content": "Task B", "status": "pending"}, // unchanged
			// Task C dropped from the snapshot.
		})},
	}
	for _, p := range parts {
		if _, err := db.Exec(
			`INSERT INTO part(id, message_id, session_id, time_created, time_updated, data) VALUES (?, ?, ?, ?, ?, ?)`,
			p.id, "msg_1", sid, p.created, p.created, p.data,
		); err != nil {
			t.Fatalf("insert part %s: %v", p.id, err)
		}
	}

	metrics, err := ComputeMetrics(dbPath, sid)
	if err != nil {
		t.Fatalf("ComputeMetrics: %v", err)
	}
	if metrics == nil {
		t.Fatal("ComputeMetrics returned nil")
	}
	if len(metrics.Tasks) != 2 {
		t.Fatalf("metrics.Tasks len = %d, want 2 (C pruned)", len(metrics.Tasks))
	}
	if got := metrics.Tasks[0]; got.ID != "1" || got.Subject != "Task A" || got.Status != "pending" {
		t.Errorf("Tasks[0] = %+v, want {ID:1 Subject:Task A Status:pending}", got)
	}
	if got := metrics.Tasks[1]; got.ID != "2" || got.Subject != "Task B" || got.Status != "pending" {
		t.Errorf("Tasks[1] = %+v, want {ID:2 Subject:Task B Status:pending}", got)
	}
}

// TestComputeMetrics_TaskEstimate drives a session whose assistant text parts
// carry task-estimate markers (issue #558) and asserts the custom metrics
// path — which bypasses the tailer — surfaces the latest estimate and a
// projected completion ETA.
func TestComputeMetrics_TaskEstimate(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "opencode.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	schema := []string{
		`CREATE TABLE session (
			id text PRIMARY KEY, project_id text NOT NULL, parent_id text,
			slug text NOT NULL, directory text NOT NULL, title text NOT NULL,
			version text NOT NULL, time_created integer NOT NULL, time_updated integer NOT NULL
		);`,
		`CREATE TABLE message (
			id text PRIMARY KEY, session_id text NOT NULL,
			time_created integer NOT NULL, time_updated integer NOT NULL, data text NOT NULL
		);`,
		`CREATE TABLE part (
			id text PRIMARY KEY, message_id text NOT NULL, session_id text NOT NULL,
			time_created integer NOT NULL, time_updated integer NOT NULL, data text NOT NULL
		);`,
	}
	for _, stmt := range schema {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("schema: %v", err)
		}
	}

	const sid = "ses_test_taskestimate"
	if _, err := db.Exec(
		`INSERT INTO session(id, project_id, slug, directory, title, version, time_created, time_updated) VALUES (?, '', '', ?, '', '', 0, 0)`,
		sid, "/tmp/opencode-eta-test",
	); err != nil {
		t.Fatalf("insert session: %v", err)
	}
	msgData := `{"role":"assistant","time":{"created":1000},"model":{"providerID":"test","modelID":"test-model"}}`
	if _, err := db.Exec(
		`INSERT INTO message(id, session_id, time_created, time_updated, data) VALUES (?, ?, ?, ?, ?)`,
		"msg_1", sid, 1000, 1000, msgData,
	); err != nil {
		t.Fatalf("insert message: %v", err)
	}

	textPart := func(text string) string {
		b, err := json.Marshal(map[string]any{"type": "text", "text": text})
		if err != nil {
			t.Fatalf("marshal part: %v", err)
		}
		return string(b)
	}
	parts := []struct {
		id      string
		created int64
		data    string
	}{
		{"part_1", 1100, textPart(`Step 1 done. <!-- {"marker":"irrlicht-eta","total_rounds":6,"completed_rounds":1} -->`)},
		{"part_2", 240000, textPart(`Step 3 done. <!-- {"marker":"irrlicht-eta","total_rounds":6,"completed_rounds":3} -->`)},
	}
	for _, p := range parts {
		if _, err := db.Exec(
			`INSERT INTO part(id, message_id, session_id, time_created, time_updated, data) VALUES (?, ?, ?, ?, ?, ?)`,
			p.id, "msg_1", sid, p.created, p.created, p.data,
		); err != nil {
			t.Fatalf("insert part %s: %v", p.id, err)
		}
	}

	metrics, err := ComputeMetrics(dbPath, sid)
	if err != nil {
		t.Fatalf("ComputeMetrics: %v", err)
	}
	if metrics == nil || metrics.TaskEstimate == nil {
		t.Fatal("expected TaskEstimate on metrics")
	}
	// Latest marker wins.
	if metrics.TaskEstimate.TotalRounds != 6 || metrics.TaskEstimate.CompletedRounds != 3 {
		t.Errorf("rounds = %d/%d, want 3/6 (latest marker)",
			metrics.TaskEstimate.CompletedRounds, metrics.TaskEstimate.TotalRounds)
	}
	if metrics.TaskEstimate.UpdatedAt != 240 { // part_2 time_updated 240000ms → unix 240s
		t.Errorf("UpdatedAt = %d, want 240", metrics.TaskEstimate.UpdatedAt)
	}
	// ElapsedSeconds ≈ 239s for 3 completed rounds → remaining 3 → eta set.
	if metrics.TaskCompletionEta == nil {
		t.Fatal("expected TaskCompletionEta to be projected")
	}
}

// TestComputeMetrics_TaskEstimateResetOnUserMessage: a user part after the
// markers starts a new task — only markers after the last user message count
// (issue #558 reset semantics, mirroring the tailer).
func TestComputeMetrics_TaskEstimateResetOnUserMessage(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "opencode.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	schema := []string{
		`CREATE TABLE session (
			id text PRIMARY KEY, project_id text NOT NULL, parent_id text,
			slug text NOT NULL, directory text NOT NULL, title text NOT NULL,
			version text NOT NULL, time_created integer NOT NULL, time_updated integer NOT NULL
		);`,
		`CREATE TABLE message (
			id text PRIMARY KEY, session_id text NOT NULL,
			time_created integer NOT NULL, time_updated integer NOT NULL, data text NOT NULL
		);`,
		`CREATE TABLE part (
			id text PRIMARY KEY, message_id text NOT NULL, session_id text NOT NULL,
			time_created integer NOT NULL, time_updated integer NOT NULL, data text NOT NULL
		);`,
	}
	for _, stmt := range schema {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("schema: %v", err)
		}
	}

	const sid = "ses_test_eta_reset"
	if _, err := db.Exec(
		`INSERT INTO session(id, project_id, slug, directory, title, version, time_created, time_updated) VALUES (?, '', '', ?, '', '', 0, 0)`,
		sid, "/tmp/opencode-eta-reset-test",
	); err != nil {
		t.Fatalf("insert session: %v", err)
	}
	msgs := []struct {
		id   string
		data string
	}{
		{"msg_a", `{"role":"assistant","time":{"created":1000},"model":{"providerID":"test","modelID":"test-model"}}`},
		{"msg_u", `{"role":"user","time":{"created":2000}}`},
	}
	for _, m := range msgs {
		if _, err := db.Exec(
			`INSERT INTO message(id, session_id, time_created, time_updated, data) VALUES (?, ?, ?, ?, ?)`,
			m.id, sid, 1000, 1000, m.data,
		); err != nil {
			t.Fatalf("insert message %s: %v", m.id, err)
		}
	}
	parts := []struct {
		id, msgID string
		created   int64
		data      string
	}{
		{"part_1", "msg_a", 1100, `{"type":"text","text":"Done. <!-- {\"marker\":\"irrlicht-eta\",\"total_rounds\":6,\"completed_rounds\":5} -->"}`},
		{"part_2", "msg_u", 2100, `{"type":"text","text":"now do something else instead"}`},
	}
	for _, p := range parts {
		if _, err := db.Exec(
			`INSERT INTO part(id, message_id, session_id, time_created, time_updated, data) VALUES (?, ?, ?, ?, ?, ?)`,
			p.id, p.msgID, sid, p.created, p.created, p.data,
		); err != nil {
			t.Fatalf("insert part %s: %v", p.id, err)
		}
	}

	metrics, err := ComputeMetrics(dbPath, sid)
	if err != nil {
		t.Fatalf("ComputeMetrics: %v", err)
	}
	if metrics == nil {
		t.Fatal("expected metrics")
	}
	if metrics.TaskEstimate != nil {
		t.Errorf("TaskEstimate = %+v, want nil after the user message", metrics.TaskEstimate)
	}
	if metrics.TaskCompletionEta != nil {
		t.Errorf("TaskCompletionEta = %v, want nil after the user message", metrics.TaskCompletionEta)
	}
}
