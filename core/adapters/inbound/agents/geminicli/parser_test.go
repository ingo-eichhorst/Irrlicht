package geminicli

import (
	"bufio"
	"encoding/json"
	"os"
	"testing"
)

// decode is a tiny helper turning a JSON object literal into the raw map the
// tailer hands ParseLine.
func decode(t *testing.T, s string) map[string]interface{} {
	t.Helper()
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("decode %q: %v", s, err)
	}
	return m
}

func TestParseLine_SkipsHeaderAndHeartbeat(t *testing.T) {
	p := &Parser{}

	header := decode(t, `{"sessionId":"s","projectHash":"h","startTime":"2026-06-08T21:40:47.487Z","kind":"main"}`)
	if ev := p.ParseLine(header); !ev.Skip {
		t.Errorf("session header: want Skip, got EventType=%q", ev.EventType)
	}

	heartbeat := decode(t, `{"$set":{"lastUpdated":"2026-06-08T21:41:47.353Z"}}`)
	if ev := p.ParseLine(heartbeat); !ev.Skip {
		t.Errorf("lastUpdated heartbeat: want Skip, got EventType=%q", ev.EventType)
	}
}

func TestParseLine_HarvestsCWDFromBootstrap(t *testing.T) {
	p := &Parser{}
	boot := decode(t, `{"$set":{"messages":[{"id":"d0","type":"user","content":[{"text":"<session_context>\nWorkspace stuff.\n- **Workspace Directories:**\n  - /private/tmp/gem-probe\n- **Directory Structure:**\n</session_context>"}]}]}}`)

	ev := p.ParseLine(boot)
	if !ev.Skip {
		t.Fatalf("bootstrap $set: want Skip, got EventType=%q", ev.EventType)
	}
	if p.cwd != "/private/tmp/gem-probe" {
		t.Fatalf("cwd: want /private/tmp/gem-probe, got %q", p.cwd)
	}

	// The next real event must carry the harvested cwd so it lands in state.CWD.
	prompt := decode(t, `{"id":"u1","type":"user","content":[{"text":"hello"}]}`)
	ev = p.ParseLine(prompt)
	if ev.EventType != "user_message" {
		t.Fatalf("prompt: want user_message, got %q (skip=%v)", ev.EventType, ev.Skip)
	}
	if ev.CWD != "/private/tmp/gem-probe" {
		t.Errorf("prompt CWD: want /private/tmp/gem-probe, got %q", ev.CWD)
	}
	if !ev.ClearToolNames {
		t.Errorf("user prompt should ClearToolNames")
	}
}

func TestParseLine_UserFunctionResponseIsToolResult(t *testing.T) {
	p := &Parser{}
	line := decode(t, `{"id":"u2","type":"user","content":[
		{"functionResponse":{"id":"call_a","name":"update_topic","response":{"output":"ok"}}},
		{"functionResponse":{"id":"call_b","name":"list_directory","response":{"output":"empty"}}}
	]}`)

	ev := p.ParseLine(line)
	if ev.Skip {
		t.Fatal("tool-result user message should not be skipped")
	}
	if ev.EventType != "function_call_output" {
		t.Errorf("EventType: want function_call_output, got %q", ev.EventType)
	}
	if ev.ClearToolNames {
		t.Error("tool result must NOT ClearToolNames (it is not a new user turn)")
	}
	if got := ev.ToolResultIDs; len(got) != 2 || got[0] != "call_a" || got[1] != "call_b" {
		t.Errorf("ToolResultIDs: want [call_a call_b], got %v", got)
	}
}

func TestParseLine_AssistantPlaceholderStaysWorking(t *testing.T) {
	p := &Parser{}
	// Empty streaming placeholder, no tool calls: an assistant_message, NOT a
	// turn_done — the session must stay working.
	line := decode(t, `{"id":"g1","type":"gemini","content":"","tokens":{"input":100,"output":5,"cached":0,"total":105},"model":"gemini-3-flash-preview"}`)
	ev := p.ParseLine(line)
	if ev.EventType != "assistant_message" {
		t.Fatalf("placeholder: want assistant_message, got %q", ev.EventType)
	}
	if ev.Contribution == nil || ev.Contribution.Usage.Input != 100 || ev.Contribution.Usage.Output != 5 {
		t.Errorf("placeholder contribution: want input=100 output=5, got %+v", ev.Contribution)
	}
}

func TestParseLine_CancelNoticeSettlesTurn(t *testing.T) {
	p := &Parser{}
	// User pressed ESC: Gemini writes only this info notice and never flushes a
	// terminal "gemini" message. It must settle the open turn (#659), otherwise
	// the session sticks in working.
	cancel := decode(t, `{"id":"i1","type":"info","content":"Request cancelled."}`)
	ev := p.ParseLine(cancel)
	if ev.Skip {
		t.Fatal("cancel notice must not be skipped")
	}
	if ev.EventType != "turn_done" {
		t.Errorf("cancel notice: want turn_done, got %q", ev.EventType)
	}

	// Any other info notice carries no signal and must stay skipped, so it can't
	// prematurely settle a still-working session.
	other := decode(t, `{"id":"i2","type":"info","content":"Compressing conversation history."}`)
	if ev := p.ParseLine(other); !ev.Skip {
		t.Errorf("unrelated info notice: want Skip, got EventType=%q", ev.EventType)
	}
}

func TestParseLine_AssistantToolCallOpensTool(t *testing.T) {
	p := &Parser{}
	line := decode(t, `{"id":"g2","type":"gemini","content":"","model":"gemini-3-flash-preview","toolCalls":[
		{"id":"call_a","name":"update_topic","status":"success"}
	]}`)
	ev := p.ParseLine(line)
	if ev.EventType != "assistant_message" {
		t.Fatalf("tool-calling message: want assistant_message (not turn_done), got %q", ev.EventType)
	}
	if len(ev.ToolUses) != 1 || ev.ToolUses[0].Name != "update_topic" || ev.ToolUses[0].ID != "call_a" {
		t.Errorf("ToolUses: want [{call_a update_topic}], got %v", ev.ToolUses)
	}
	if len(ev.ToolResultIDs) != 1 || ev.ToolResultIDs[0] != "call_a" {
		t.Errorf("success toolCall should self-close: want ToolResultIDs=[call_a], got %v", ev.ToolResultIDs)
	}
}

func TestParseLine_FinalTextSettlesTurn(t *testing.T) {
	p := &Parser{}
	line := decode(t, `{"id":"g3","type":"gemini","content":"The current directory is empty.\n\nDONE","tokens":{"input":10081,"output":8,"cached":7443,"total":10089},"model":"gemini-3-flash-preview"}`)
	ev := p.ParseLine(line)
	if ev.EventType != "turn_done" {
		t.Fatalf("final text message: want turn_done, got %q", ev.EventType)
	}
	if ev.AssistantText == "" {
		t.Error("turn_done should still carry AssistantText for waiting display")
	}
	// input is inclusive of cached: bill (input-cached) as Input, cached as CacheRead.
	if c := ev.Contribution; c == nil || c.Usage.Input != 2638 || c.Usage.CacheRead != 7443 || c.Usage.Output != 8 {
		t.Errorf("contribution: want input=2638 cacheRead=7443 output=8, got %+v", ev.Contribution)
	}
}

func TestParseLine_ErrorLineSettlesTurn(t *testing.T) {
	p := &Parser{}
	// When a turn aborts on an API error, gemini-cli records the aborted turn
	// as a type:"error" message (upstream PR #13300). With no inactivity sweep
	// on `working`, the parser must treat it as the turn's end and settle to
	// ready — otherwise the session sticks in `working` forever (#665).
	line := decode(t, `{"id":"e1","type":"error","content":"API Error: 429 RESOURCE_EXHAUSTED"}`)
	ev := p.ParseLine(line)
	if ev.Skip {
		t.Fatal("error line should not be skipped")
	}
	if ev.EventType != "turn_done" {
		t.Fatalf("error line: want turn_done, got %q", ev.EventType)
	}
	if !ev.IsError {
		t.Error("error line should set IsError")
	}
	if ev.AssistantText == "" {
		t.Error("error line should carry the error text as AssistantText for waiting display")
	}
}

func TestParseLine_TerminalInfoSettlesTurn(t *testing.T) {
	p := &Parser{}
	// A turn aborted by user Esc / quota records a TERMINAL type:"info" notice
	// with no following gemini message. With no inactivity sweep on `working`,
	// the parser must settle to ready — otherwise the session sticks (#676).
	line := decode(t, `{"id":"i1","type":"info","content":"Request cancelled."}`)
	ev := p.ParseLine(line)
	if ev.Skip {
		t.Fatal("terminal info line should not be skipped")
	}
	if ev.EventType != "turn_done" {
		t.Fatalf("terminal info: want turn_done, got %q", ev.EventType)
	}
	if !ev.IsError {
		t.Error("terminal info should set IsError")
	}
	if ev.AssistantText == "" {
		t.Error("terminal info should carry the notice as AssistantText for waiting display")
	}
}

func TestParseLine_BenignInfoSkipped(t *testing.T) {
	p := &Parser{}
	// A benign type:"info" notice (a model switch) does NOT end the turn — it
	// must be skipped, never settle. A false-settle mid-turn is worse than the
	// false-stick this guards against (#676).
	line := decode(t, `{"id":"i2","type":"info","content":"Model set to gemini-2.5-flash"}`)
	ev := p.ParseLine(line)
	if !ev.Skip {
		t.Fatalf("benign info line must be skipped, got EventType=%q", ev.EventType)
	}
	if ev.EventType == "turn_done" {
		t.Error("benign info must not settle the turn (false-settle guard)")
	}
}

func TestParseLine_StreamingReemissionNotDoubleBilled(t *testing.T) {
	p := &Parser{}
	first := decode(t, `{"id":"g4","type":"gemini","content":"","tokens":{"input":9925,"output":91,"cached":0,"total":10016},"model":"gemini-3-flash-preview"}`)
	if ev := p.ParseLine(first); ev.Contribution == nil {
		t.Fatal("first sight of a message should contribute")
	}
	// Same id, identical tokens (Gemini rewrites the message in place once it
	// gains toolCalls) — must NOT bill again.
	second := decode(t, `{"id":"g4","type":"gemini","content":"","tokens":{"input":9925,"output":91,"cached":0,"total":10016},"model":"gemini-3-flash-preview","toolCalls":[{"id":"c","name":"x","status":"success"}]}`)
	if ev := p.ParseLine(second); ev.Contribution != nil {
		t.Errorf("re-emission under same id must not double-bill, got %+v", ev.Contribution)
	}
}

func TestParseLine_WriteTodosEmitsTaskDeltas(t *testing.T) {
	p := &Parser{}

	// First write_todos call: a full three-item snapshot, all pending. Each
	// item must be Created (the tailer starts created tasks at pending).
	first := decode(t, `{"id":"g1","type":"gemini","content":"","model":"gemini-2.5-pro","toolCalls":[
		{"id":"wt_1","name":"write_todos","status":"success","args":{"todos":[
			{"status":"pending","description":"read README"},
			{"status":"pending","description":"summarize README"},
			{"status":"pending","description":"reply done"}
		]}}
	]}`)
	ev := p.ParseLine(first)
	if len(ev.TaskDeltas) != 3 {
		t.Fatalf("first write_todos: want 3 create deltas, got %d (%+v)", len(ev.TaskDeltas), ev.TaskDeltas)
	}
	for i, d := range ev.TaskDeltas {
		if d.Op != "create" {
			t.Errorf("delta[%d]: want create, got %q", i, d.Op)
		}
	}
	if ev.TaskDeltas[0].Subject != "read README" {
		t.Errorf("delta[0] subject: want 'read README', got %q", ev.TaskDeltas[0].Subject)
	}
	if ev.TaskSnapshot == nil || len(*ev.TaskSnapshot) != 3 {
		t.Fatalf("first write_todos: want 3-entry snapshot, got %v", ev.TaskSnapshot)
	}

	// Second write_todos call: same list, but item 1 now in_progress. Only an
	// Update for the changed item — no new Creates.
	second := decode(t, `{"id":"g2","type":"gemini","content":"","model":"gemini-2.5-pro","toolCalls":[
		{"id":"wt_2","name":"write_todos","status":"success","args":{"todos":[
			{"status":"in_progress","description":"read README"},
			{"status":"pending","description":"summarize README"},
			{"status":"pending","description":"reply done"}
		]}}
	]}`)
	ev = p.ParseLine(second)
	if len(ev.TaskDeltas) != 1 {
		t.Fatalf("second write_todos: want 1 update delta, got %d (%+v)", len(ev.TaskDeltas), ev.TaskDeltas)
	}
	if d := ev.TaskDeltas[0]; d.Op != "update" || d.Status != "in_progress" {
		t.Errorf("second delta: want update/in_progress, got op=%q status=%q", d.Op, d.Status)
	}
	if ev.TaskSnapshot == nil || len(*ev.TaskSnapshot) != 3 {
		t.Fatalf("second write_todos: want 3-entry snapshot, got %v", ev.TaskSnapshot)
	}

	// Third write_todos call: item 1 completed. The snapshot must carry its
	// completed status so the tailer's reconcile stamps it done.
	third := decode(t, `{"id":"g3","type":"gemini","content":"","model":"gemini-2.5-pro","toolCalls":[
		{"id":"wt_3","name":"write_todos","status":"success","args":{"todos":[
			{"status":"completed","description":"read README"},
			{"status":"pending","description":"summarize README"},
			{"status":"pending","description":"reply done"}
		]}}
	]}`)
	ev = p.ParseLine(third)
	if len(ev.TaskDeltas) != 1 {
		t.Fatalf("third write_todos: want 1 update delta, got %d (%+v)", len(ev.TaskDeltas), ev.TaskDeltas)
	}
	if d := ev.TaskDeltas[0]; d.Op != "update" || d.Status != "completed" {
		t.Errorf("third delta: want update/completed, got op=%q status=%q", d.Op, d.Status)
	}
}

// TestParseLine_WriteTodosPendingOnCreateSkipsUpdate ports opencode's
// PendingOnCreateSkipsUpdate: a freshly-created item whose first sighting is
// already in_progress must emit BOTH a Create (the tailer starts created tasks
// at pending) AND an Update to reach in_progress — never a bare Create that
// would leave the status wrong.
func TestParseLine_WriteTodosPendingOnCreateSkipsUpdate(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(decode(t, `{"id":"g1","type":"gemini","content":"","model":"gemini-2.5-pro","toolCalls":[
		{"id":"wt_1","name":"write_todos","status":"success","args":{"todos":[
			{"status":"in_progress","description":"already in progress"}
		]}}
	]}`))
	if len(ev.TaskDeltas) != 2 {
		t.Fatalf("want 2 deltas (create + update), got %d (%+v)", len(ev.TaskDeltas), ev.TaskDeltas)
	}
	if d := ev.TaskDeltas[0]; d.Op != "create" || d.Subject != "already in progress" {
		t.Errorf("deltas[0] = %+v, want create subject='already in progress'", d)
	}
	if d := ev.TaskDeltas[1]; d.Op != "update" || d.ID != "1" || d.Status != "in_progress" {
		t.Errorf("deltas[1] = %+v, want update id=1 status=in_progress", d)
	}
}

// TestParseLine_WriteTodosSnapshotPrunesDeletedTodos ports opencode's
// SnapshotPrunesDeletedTodos: write_todos is a full-list replace, so a todo
// missing from a later snapshot has been removed. The emitted TaskSnapshot
// must carry only the surviving entries so the tailer can prune the stale one.
func TestParseLine_WriteTodosSnapshotPrunesDeletedTodos(t *testing.T) {
	p := &Parser{}
	p.ParseLine(decode(t, `{"id":"g1","type":"gemini","content":"","model":"gemini-2.5-pro","toolCalls":[
		{"id":"wt_1","name":"write_todos","status":"success","args":{"todos":[
			{"status":"pending","description":"Task A"},
			{"status":"pending","description":"Task B"},
			{"status":"pending","description":"Task C"}
		]}}
	]}`))
	ev := p.ParseLine(decode(t, `{"id":"g2","type":"gemini","content":"","model":"gemini-2.5-pro","toolCalls":[
		{"id":"wt_2","name":"write_todos","status":"success","args":{"todos":[
			{"status":"pending","description":"Task A"},
			{"status":"pending","description":"Task B"}
		]}}
	]}`))
	if ev.TaskSnapshot == nil {
		t.Fatal("expected TaskSnapshot to be set")
	}
	if got, want := len(*ev.TaskSnapshot), 2; got != want {
		t.Fatalf("snapshot len = %d, want %d", got, want)
	}
	ids := map[string]bool{}
	for _, e := range *ev.TaskSnapshot {
		ids[e.ID] = true
	}
	if !ids["1"] || !ids["2"] {
		t.Errorf("snapshot IDs = %v, want {1, 2}", ids)
	}
	if ids["3"] {
		t.Errorf("snapshot still contains pruned id=3: %v", ids)
	}
}

// TestParseLine_WriteTodosSnapshotCarriesStatusReversion ports opencode's
// SnapshotCarriesStatusReversion: a todo flipping in_progress → pending in a
// later snapshot emits no Update delta (the Update path suppresses pending
// writes), so the reversion must surface only via TaskSnapshot — without it
// the tailer would freeze the stale in_progress.
func TestParseLine_WriteTodosSnapshotCarriesStatusReversion(t *testing.T) {
	p := &Parser{}
	p.ParseLine(decode(t, `{"id":"g1","type":"gemini","content":"","model":"gemini-2.5-pro","toolCalls":[
		{"id":"wt_1","name":"write_todos","status":"success","args":{"todos":[
			{"status":"in_progress","description":"Task A"}
		]}}
	]}`))
	ev := p.ParseLine(decode(t, `{"id":"g2","type":"gemini","content":"","model":"gemini-2.5-pro","toolCalls":[
		{"id":"wt_2","name":"write_todos","status":"success","args":{"todos":[
			{"status":"pending","description":"Task A"}
		]}}
	]}`))
	// No Update delta because the new status is pending.
	if len(ev.TaskDeltas) != 0 {
		t.Errorf("TaskDeltas len = %d, want 0 (reversion via snapshot only)", len(ev.TaskDeltas))
	}
	if ev.TaskSnapshot == nil || len(*ev.TaskSnapshot) != 1 {
		t.Fatalf("TaskSnapshot = %v, want one entry", ev.TaskSnapshot)
	}
	if got := (*ev.TaskSnapshot)[0]; got.ID != "1" || got.Status != "pending" {
		t.Errorf("snapshot entry = %+v, want {ID:1 Status:pending}", got)
	}
}

// TestParse_RealSession replays the real captured transcript end-to-end and
// asserts the session-level signals: exactly one user turn, exactly one
// settle-to-ready, the workspace cwd, tool open/close, and deduped billing.
func TestParse_RealSession(t *testing.T) {
	f, err := os.Open("testdata/real-toolcall-session.jsonl")
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer f.Close()

	p := &Parser{}
	var (
		userMsgs, turnDones               int
		lastCWD, finalText                string
		toolNames                         = map[string]bool{}
		toolResults                       = map[string]bool{}
		sumInput, sumOutput, sumCacheRead int64
	)

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1024*1024), 1024*1024)
	for sc.Scan() {
		var raw map[string]interface{}
		if err := json.Unmarshal(sc.Bytes(), &raw); err != nil {
			t.Fatalf("decode line: %v", err)
		}
		ev := p.ParseLine(raw)
		if ev.Skip {
			continue
		}
		if ev.CWD != "" {
			lastCWD = ev.CWD
		}
		switch ev.EventType {
		case "user_message":
			userMsgs++
		case "turn_done":
			turnDones++
			finalText = ev.AssistantText
		}
		for _, tu := range ev.ToolUses {
			toolNames[tu.Name] = true
		}
		for _, id := range ev.ToolResultIDs {
			toolResults[id] = true
		}
		if ev.Contribution != nil {
			sumInput += ev.Contribution.Usage.Input
			sumOutput += ev.Contribution.Usage.Output
			sumCacheRead += ev.Contribution.Usage.CacheRead
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan: %v", err)
	}

	if lastCWD != "/private/tmp/gem-probe" {
		t.Errorf("cwd: want /private/tmp/gem-probe, got %q", lastCWD)
	}
	if userMsgs != 1 {
		t.Errorf("user_message count: want 1, got %d", userMsgs)
	}
	if turnDones != 1 {
		t.Errorf("turn_done count: want 1, got %d", turnDones)
	}
	if finalText == "" {
		t.Error("final turn_done carried no AssistantText")
	}
	if !toolNames["update_topic"] {
		t.Errorf("expected update_topic in ToolUses, got %v", toolNames)
	}
	// Both tool results close: update_topic + list_directory.
	if len(toolResults) < 2 {
		t.Errorf("expected >=2 tool results closed, got %v", toolResults)
	}
	// 91bc4bb9 (input 9925, contributed once across its two emissions) +
	// 8fcff41a (input 10081 - cached 7443 = 2638).
	if sumInput != 12563 {
		t.Errorf("billed Input: want 12563 (dedup across re-emission), got %d", sumInput)
	}
	if sumOutput != 99 {
		t.Errorf("billed Output: want 99, got %d", sumOutput)
	}
	if sumCacheRead != 7443 {
		t.Errorf("billed CacheRead: want 7443, got %d", sumCacheRead)
	}
}
